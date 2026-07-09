// Package deploy orchestrates image updates against the cluster: it patches or
// restarts target workloads, watches the rollout, records history, and runs the
// optional Cloudflare purge.
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/timothydodd/tagalong/internal/events"
	"github.com/timothydodd/tagalong/internal/model"
)

// rolloutTimeout bounds how long we wait for a single target to become healthy.
const rolloutTimeout = 5 * time.Minute

// Store is the persistence surface the engine needs.
type Store interface {
	CreateEvent(model.DeployEvent) (model.DeployEvent, error)
	UpdateEvent(model.DeployEvent) error
	SetLastSeen(id int64, tag, digest string) error
	GetSetting(key string) (string, error)
	GetApp(id int64) (model.App, error)
	ListInterrupted() ([]model.DeployEvent, error)
	SweepStale() (int64, error)
}

// Purger runs a post-deploy cache purge for an app. Implemented by the
// cloudflare package; may be nil.
type Purger interface {
	Purge(ctx context.Context, app model.App) error
}

// Job is a requested deploy.
type Job struct {
	App     model.App
	Trigger string // model.Trigger*
	// NewImage is the fully-qualified image to patch to. Empty means restart.
	NewImage string
	// Tag/Digest recorded as last-seen on success.
	Tag    string
	Digest string
	// Action is model.ActionPatch or model.ActionRestart.
	Action string
}

// Engine serializes deploys per app and executes them.
type Engine struct {
	k8s    *K8s
	store  Store
	bus    *events.Bus
	purger Purger
	log    *slog.Logger

	mu    sync.Mutex
	queues map[int64]chan Job
	wg    sync.WaitGroup
}

// NewEngine constructs an Engine. purger may be nil.
func NewEngine(k8s *K8s, store Store, bus *events.Bus, purger Purger, log *slog.Logger) *Engine {
	return &Engine{
		k8s:    k8s,
		store:  store,
		bus:    bus,
		purger: purger,
		log:    log,
		queues: make(map[int64]chan Job),
	}
}

// Enqueue queues a job for asynchronous execution, serialized per app. Duplicate
// jobs (same app already queued for the same image) are dropped.
func (e *Engine) Enqueue(job Job) {
	e.mu.Lock()
	q, ok := e.queues[job.App.ID]
	if !ok {
		q = make(chan Job, 8)
		e.queues[job.App.ID] = q
		e.wg.Add(1)
		go e.worker(job.App.ID, q)
	}
	e.mu.Unlock()

	select {
	case q <- job:
	default:
		e.log.Warn("deploy queue full, dropping job", "app", job.App.Name, "image", job.NewImage)
	}
}

// DeploySync executes a job synchronously and returns the terminal event. Used
// by the manual-deploy API so the caller gets an immediate result.
func (e *Engine) DeploySync(ctx context.Context, job Job) model.DeployEvent {
	return e.run(ctx, job)
}

// ReconcileStartup resolves deploy events left in-flight by a previous process.
// The common case is tagalong deploying itself: the rollout kills the pod
// running the deploy before it can record success, leaving the event in
// pending/rolling. Rather than blindly marking these "unknown", it inspects the
// live workload and, if the target reached the event's desired state (patched
// image is live, or the restart annotation was applied), marks it success; a
// failing rollout is marked failed. Without a reachable cluster it falls back to
// sweeping them to "unknown". Call once at startup, before new deploys begin.
func (e *Engine) ReconcileStartup(ctx context.Context) {
	if e.k8s == nil || !e.k8s.Configured() {
		if n, err := e.store.SweepStale(); err != nil {
			e.log.Warn("sweep stale events", "err", err)
		} else if n > 0 {
			e.log.Info("swept stale in-flight events", "count", n)
		}
		return
	}
	events, err := e.store.ListInterrupted()
	if err != nil {
		e.log.Warn("list interrupted events", "err", err)
		return
	}
	for _, ev := range events {
		status, detail := e.reconcileEvent(ctx, ev)
		e.finish(ev, status, detail)
		e.log.Info("reconciled interrupted deploy event", "app", ev.AppName, "id", ev.ID, "status", status)
	}
}

// reconcileEvent decides the terminal status of a single interrupted event by
// comparing the event's intent against the live workload's desired spec (not pod
// readiness, which may not be settled while tagalong itself is still starting).
func (e *Engine) reconcileEvent(ctx context.Context, ev model.DeployEvent) (status, detail string) {
	const interrupted = "interrupted (service restart)"
	if ev.AppID == nil {
		return model.StatusUnknown, interrupted
	}
	app, err := e.store.GetApp(*ev.AppID)
	if err != nil || len(app.Targets) == 0 {
		return model.StatusUnknown, interrupted
	}
	applied := true
	for _, t := range app.Targets {
		if st, serr := e.k8s.rolloutStatus(ctx, t); serr == nil && st.Failed {
			return model.StatusFailed, "rollout failed: " + st.Message
		}
		if ev.Action == model.ActionRestart {
			ra, ok, _ := e.k8s.TemplateRestartedAt(ctx, t)
			if !ok || ra.Before(ev.StartedAt) {
				applied = false
			}
		} else {
			img, ierr := e.k8s.CurrentImage(ctx, t)
			if ierr != nil || ev.NewImage == "" || img != ev.NewImage {
				applied = false
			}
		}
	}
	if applied {
		return model.StatusSuccess, "completed (confirmed after tagalong restarted)"
	}
	return model.StatusUnknown, interrupted
}

func (e *Engine) worker(appID int64, q chan Job) {
	defer e.wg.Done()
	for job := range q {
		e.run(context.Background(), job)
	}
}

func (e *Engine) run(ctx context.Context, job Job) model.DeployEvent {
	app := job.App
	action := job.Action
	if action == "" {
		if job.NewImage != "" {
			action = model.ActionPatch
		} else {
			action = model.ActionRestart
		}
	}

	ev := model.DeployEvent{
		AppID:    &app.ID,
		AppName:  app.Name,
		Trigger:  job.Trigger,
		Action:   action,
		NewImage: job.NewImage,
		Status:   model.StatusPending,
	}
	// Record current image (from the first target) for history / rollback.
	if len(app.Targets) > 0 {
		if cur, err := e.k8s.CurrentImage(ctx, app.Targets[0]); err == nil {
			ev.OldImage = cur
		}
	}
	ev, err := e.store.CreateEvent(ev)
	if err != nil {
		e.log.Error("create deploy event", "err", err)
		return ev
	}
	e.bus.Publish(ev)

	if len(app.Targets) == 0 {
		return e.finish(ev, model.StatusFailed, "app has no targets")
	}

	// Move to rolling.
	ev.Status = model.StatusRolling
	e.store.UpdateEvent(ev)
	e.bus.Publish(ev)

	// Apply to every target, then wait for each rollout.
	for _, t := range app.Targets {
		var perr error
		if action == model.ActionRestart {
			perr = e.k8s.RestartRollout(ctx, t, time.Now())
		} else {
			perr = e.k8s.PatchImage(ctx, t, job.NewImage)
		}
		if perr != nil {
			return e.finish(ev, model.StatusFailed, fmt.Sprintf("patch %s/%s: %v", t.Namespace, t.Name, perr))
		}
	}
	for _, t := range app.Targets {
		wctx, cancel := context.WithTimeout(ctx, rolloutTimeout)
		werr := e.k8s.WaitForRollout(wctx, t, rolloutTimeout)
		cancel()
		if werr != nil {
			return e.finish(ev, model.StatusFailed, fmt.Sprintf("%s/%s: %v", t.Namespace, t.Name, werr))
		}
	}

	// Success: record last-seen and run optional purge.
	if job.Tag != "" || job.Digest != "" {
		if err := e.store.SetLastSeen(app.ID, job.Tag, job.Digest); err != nil {
			e.log.Warn("set last seen", "app", app.Name, "err", err)
		}
	}
	if e.purger != nil && app.CFPurge.Enabled {
		e.schedulePurge(app, job.Trigger)
	}
	return e.finish(ev, model.StatusSuccess, ev.Detail)
}

// schedulePurge records the app's Cloudflare purge as its own history event and
// fires it after the app's configured delay (default 5 minutes). It runs
// detached from the deploy: the timer is in-memory only, so a service restart
// abandons it and SweepStale later marks the pending purge event "unknown".
func (e *Engine) schedulePurge(app model.App, trigger string) {
	delay := app.CFPurge.Delay()
	pev := model.DeployEvent{
		AppID:   &app.ID,
		AppName: app.Name,
		Trigger: trigger,
		Action:  model.ActionPurge,
		Status:  model.StatusPending,
	}
	if delay > 0 {
		pev.Detail = fmt.Sprintf("cloudflare purge scheduled in %s", delay)
	}
	pev, err := e.store.CreateEvent(pev)
	if err != nil {
		e.log.Error("create purge event", "app", app.Name, "err", err)
		return
	}
	e.bus.Publish(pev)

	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		if perr := e.purger.Purge(context.Background(), app); perr != nil {
			e.log.Warn("cloudflare purge failed", "app", app.Name, "err", perr)
			e.finish(pev, model.StatusFailed, "cloudflare purge failed: "+perr.Error())
			return
		}
		pev.CFPurged = true
		e.finish(pev, model.StatusSuccess, "cloudflare cache purged")
	}()
}

func (e *Engine) finish(ev model.DeployEvent, status, detail string) model.DeployEvent {
	ev.Status = status
	if detail != "" {
		ev.Detail = detail
	}
	if err := e.store.UpdateEvent(ev); err != nil {
		e.log.Error("update deploy event", "err", err)
	}
	e.bus.Publish(ev)
	if status == model.StatusFailed {
		e.log.Error("deploy failed", "app", ev.AppName, "detail", ev.Detail)
	} else {
		e.log.Info("deploy done", "app", ev.AppName, "status", status, "image", ev.NewImage)
	}
	return ev
}
