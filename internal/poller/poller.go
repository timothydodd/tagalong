// Package poller periodically checks registries for image updates on apps that
// have polling enabled, feeding any warranted deploys to the engine. It is the
// fallback for registries that can't deliver webhooks (e.g. registry.example.com).
package poller

import (
	"context"
	"log/slog"
	"regexp"
	"sort"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/strategy"
)

// tickInterval is how often the manager wakes to check which apps are due.
const tickInterval = 30 * time.Second

// minInterval is the smallest allowed per-app poll interval.
const minInterval = 60 * time.Second

// Store is the persistence surface the poller needs.
type Store interface {
	ListApps() ([]model.App, error)
}

// Engine accepts deploy jobs.
type Engine interface {
	Enqueue(deploy.Job)
}

// K8s reads the currently-deployed image for a target.
type K8s interface {
	CurrentImage(ctx context.Context, t model.Target) (string, error)
}

// Registry is the subset of the registry client the poller uses (an interface
// so the poll logic can be unit-tested without network access).
type Registry interface {
	ListTags(repo string) ([]string, error)
	HeadManifest(repo, tag string) (string, error)
	ManifestCreated(repo, tag string) (time.Time, error)
}

// Poller runs a single manager goroutine that polls due apps.
type Poller struct {
	store    Store
	engine   Engine
	k8s      K8s
	registry Registry
	log      *slog.Logger

	// lastPolled tracks the last poll time per app id (in-memory).
	lastPolled map[int64]time.Time
}

// New constructs a Poller.
func New(store Store, engine Engine, k8s K8s, reg Registry, log *slog.Logger) *Poller {
	return &Poller{
		store:      store,
		engine:     engine,
		k8s:        k8s,
		registry:   reg,
		log:        log,
		lastPolled: make(map[int64]time.Time),
	}
}

// Run blocks, polling until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	p.log.Info("poller started", "tick", tickInterval)
	for {
		select {
		case <-ctx.Done():
			p.log.Info("poller stopped")
			return
		case <-ticker.C:
			p.checkDue(ctx)
		}
	}
}

func (p *Poller) checkDue(ctx context.Context) {
	apps, err := p.store.ListApps()
	if err != nil {
		p.log.Warn("poller list apps", "err", err)
		return
	}
	now := time.Now()
	for _, app := range apps {
		if !app.PollEnabled || !app.Enabled {
			continue
		}
		interval := time.Duration(app.PollInterval) * time.Second
		if interval < minInterval {
			interval = minInterval
		}
		if last, ok := p.lastPolled[app.ID]; ok && now.Sub(last) < interval {
			continue
		}
		p.lastPolled[app.ID] = now
		p.pollApp(ctx, app)
	}
}

// pollApp evaluates a single app for updates according to its strategy.
func (p *Poller) pollApp(ctx context.Context, app model.App) {
	switch app.TagStrategy {
	case model.StrategyLatest:
		p.pollLatest(app)
	case model.StrategySemver:
		p.pollSemver(ctx, app)
	default: // exact | regex
		p.pollExact(ctx, app)
	}
}

// pollLatest compares the tracked rolling tag's digest against the last-seen
// digest; a change triggers a rollout-restart.
func (p *Poller) pollLatest(app model.App) {
	track := strategy.TrackTag(app)
	digest, err := p.registry.HeadManifest(app.ImageRepo, track)
	if err != nil {
		p.log.Warn("poll latest head", "app", app.Name, "err", err)
		return
	}
	if digest == app.LastSeenDigest {
		return // no change
	}
	// First observation seeds the digest without deploying (avoids a spurious
	// restart on the very first poll).
	if app.LastSeenDigest == "" {
		p.log.Info("poll latest seed", "app", app.Name, "digest", short(digest))
		p.engine.Enqueue(deploy.Job{
			App: app, Trigger: model.TriggerPoll, Action: model.ActionRestart,
			Tag: track, Digest: digest,
		})
		return
	}
	p.log.Info("poll latest change", "app", app.Name, "old", short(app.LastSeenDigest), "new", short(digest))
	p.engine.Enqueue(deploy.Job{
		App: app, Trigger: model.TriggerPoll, Action: model.ActionRestart,
		Tag: track, Digest: digest,
	})
}

// pollSemver finds the highest valid semver tag and deploys it if newer than
// what's live.
func (p *Poller) pollSemver(ctx context.Context, app model.App) {
	tags, err := p.registry.ListTags(app.ImageRepo)
	if err != nil {
		p.log.Warn("poll semver list", "app", app.Name, "err", err)
		return
	}
	best := ""
	var bestV *semver.Version
	for _, tag := range tags {
		v, err := semver.NewVersion(trimV(tag))
		if err != nil || v.Prerelease() != "" {
			continue
		}
		if bestV == nil || v.GreaterThan(bestV) {
			bestV, best = v, tag
		}
	}
	if best == "" {
		return
	}
	p.decideAndEnqueue(ctx, app, best)
}

// pollExact lists tags matching the pattern and deploys the one with the newest
// build timestamp if it differs from what's live.
func (p *Poller) pollExact(ctx context.Context, app model.App) {
	tags, err := p.registry.ListTags(app.ImageRepo)
	if err != nil {
		p.log.Warn("poll exact list", "app", app.Name, "err", err)
		return
	}
	var re *regexp.Regexp
	if pat := app.StrategyConf.Pattern; pat != "" {
		re, err = regexp.Compile(pat)
		if err != nil {
			p.log.Warn("poll exact bad pattern", "app", app.Name, "err", err)
			return
		}
	}
	type cand struct {
		tag     string
		created time.Time
	}
	var cands []cand
	for _, tag := range tags {
		if re != nil && !re.MatchString(tag) {
			continue
		}
		created, err := p.registry.ManifestCreated(app.ImageRepo, tag)
		if err != nil {
			continue // best-effort; skip tags we can't date
		}
		cands = append(cands, cand{tag, created})
	}
	if len(cands) == 0 {
		return
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].created.After(cands[j].created) })
	p.decideAndEnqueue(ctx, app, cands[0].tag)
}

// decideAndEnqueue reads the live tag, runs the strategy, and enqueues a deploy
// if warranted. Poll no-ops are silent (not recorded as events).
func (p *Poller) decideAndEnqueue(ctx context.Context, app model.App, tag string) {
	currentTag := ""
	if len(app.Targets) > 0 {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if img, err := p.k8s.CurrentImage(cctx, app.Targets[0]); err == nil {
			currentTag = strategy.TagOf(img)
		}
		cancel()
	}
	d := strategy.Decide(app, tag, currentTag)
	if !d.Deploy {
		return
	}
	p.log.Info("poll deploy", "app", app.Name, "tag", tag, "reason", d.Reason)
	p.engine.Enqueue(deploy.Job{
		App: app, Trigger: model.TriggerPoll, Action: d.Action,
		NewImage: d.NewImage, Tag: d.Tag,
	})
}

func trimV(tag string) string {
	if len(tag) > 0 && (tag[0] == 'v' || tag[0] == 'V') {
		return tag[1:]
	}
	return tag
}

func short(digest string) string {
	if len(digest) > 19 {
		return digest[:19]
	}
	return digest
}
