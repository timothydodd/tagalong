package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/relay"
	"github.com/timothydodd/tagalong/internal/store"
	"github.com/timothydodd/tagalong/internal/strategy"
	"github.com/timothydodd/tagalong/internal/webhook"
)

// maxHookBody caps webhook request bodies.
const maxHookBody = 1 << 20 // 1 MiB

// hookResult is a webhook receiver's outcome: an HTTP status and JSON body.
// Receivers return it rather than writing directly so the same logic serves
// both direct webhooks and ones relayed from a hub.
type hookResult struct {
	status int
	body   any
}

func hookErr(status int, msg string) hookResult {
	return hookResult{status, map[string]string{"error": msg}}
}

// relayWait bounds how long a hub holds a webhook request open waiting for its
// agents to answer. Kept under the ~10s webhook timeouts of GitHub/Docker Hub.
const relayWait = 8 * time.Second

// hookDockerHub handles POST /hooks/dockerhub/{token}. The token identifies the
// app (and authenticates the caller). The payload's repo is cross-checked
// against the app's configured image_repo. With agents registered, the hook is
// also relayed to every agent.
func (s *Server) hookDockerHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	token := chi.URLParam(r, "token")

	app, tag, fail := s.resolveDockerHub(token, body, false)
	if !s.relaying() {
		if fail != nil {
			writeJSON(w, fail.status, fail.body)
			return
		}
		res := s.handleTrigger(app, tag, model.TriggerDockerHub)
		writeJSON(w, res.status, res.body)
		return
	}
	// A token we know but a bad/mismatched payload is rejected outright rather
	// than relayed; only an unknown token (404) still goes to the agents.
	if fail != nil && fail.status != http.StatusNotFound {
		writeJSON(w, fail.status, fail.body)
		return
	}

	var local func() hookResult
	if fail == nil {
		local = func() hookResult { return s.handleTrigger(app, tag, model.TriggerDockerHub) }
	}
	// When we recognized the token the caller is authenticated, so agents may
	// match their own copy of the app by repo (their token differs from ours).
	res := s.fanOut(relay.Message{Kind: relay.KindDockerHub, Token: token, Body: body, Verified: fail == nil}, local)
	writeJSON(w, res.status, res.body)
}

// resolveDockerHub maps a Docker Hub hook to an app and pushed tag, or returns
// the failure response. verified means the hub already authenticated the
// token, so when it isn't one of ours the app is matched by payload repo.
func (s *Server) resolveDockerHub(token string, body []byte, verified bool) (model.App, string, *hookResult) {
	app, err := s.store.GetAppByToken(token)
	if err != nil && verified {
		if repo, _, perr := webhook.ParseDockerHub(body); perr == nil {
			app, err = s.store.GetAppByRepo(repo)
		}
	}
	if err != nil {
		// Unknown token: 404, don't leak which tokens are valid beyond status.
		res := hookErr(http.StatusNotFound, "unknown webhook token")
		return model.App{}, "", &res
	}

	repo, tag, err := webhook.ParseDockerHub(body)
	if err != nil {
		res := hookErr(http.StatusBadRequest, err.Error())
		return model.App{}, "", &res
	}
	if repo != app.ImageRepo {
		s.log.Warn("dockerhub webhook repo mismatch", "app", app.Name, "token_repo", app.ImageRepo, "payload_repo", repo)
		res := hookErr(http.StatusBadRequest, "payload repo does not match app")
		return model.App{}, "", &res
	}
	return app, tag, nil
}

// hookGitHub handles POST /hooks/github. It validates the HMAC signature, then
// maps the published container image to a configured app by normalized repo.
// With agents registered, the hook is also relayed to every agent.
func (s *Server) hookGitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}

	secret, _ := s.store.GetSetting(model.KeyGitHubWebhookSecret)
	if secret == "" {
		// Unsigned hooks still work for trusted-LAN setups, but anyone who can
		// reach this endpoint can trigger deploys with a forged payload — make
		// sure that trade-off is impossible to miss in the logs.
		s.log.Warn("SECURITY: github webhook accepted WITHOUT signature verification — set a GitHub webhook secret in Settings",
			"remote", r.RemoteAddr)
	} else if !webhook.ValidateGitHubSignature(secret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeErr(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	app, tag, res := s.resolveGitHub(body)
	relaying := s.relaying()
	if res != nil && (!relaying || res.status != http.StatusNotFound) {
		if res.status == http.StatusNotFound {
			res.status = http.StatusOK // "no app" is a no-op, not an error
		}
		writeJSON(w, res.status, res.body)
		return
	}
	if !relaying {
		res := s.handleTrigger(app, tag, model.TriggerGitHub)
		writeJSON(w, res.status, res.body)
		return
	}

	var local func() hookResult
	if res == nil {
		local = func() hookResult { return s.handleTrigger(app, tag, model.TriggerGitHub) }
	}
	out := s.fanOut(relay.Message{Kind: relay.KindGitHub, Body: body}, local)
	writeJSON(w, out.status, out.body)
}

// resolveGitHub maps an already-authenticated GitHub payload to an app and
// tag, or returns the response to send. A 404 status marks "no app for this
// repo" (sent to the caller as a 200 no-op, but still relayed to agents).
func (s *Server) resolveGitHub(body []byte) (model.App, string, *hookResult) {
	repo, tag, err := webhook.ParseGitHub(body)
	if errors.Is(err, webhook.ErrNotContainerPublish) {
		// Benign event we don't act on (ping, non-container, digest-only).
		res := hookResult{http.StatusOK, map[string]string{"status": "ignored"}}
		return model.App{}, "", &res
	}
	if err != nil {
		res := hookErr(http.StatusBadRequest, err.Error())
		return model.App{}, "", &res
	}

	app, err := s.store.GetAppByRepo(repo)
	if errors.Is(err, store.ErrNotFound) {
		// Org-level webhook will send packages we don't track — no-op.
		res := hookResult{http.StatusNotFound, map[string]string{"status": "no app for " + repo}}
		return model.App{}, "", &res
	}
	if err != nil {
		res := hookErr(http.StatusInternalServerError, err.Error())
		return model.App{}, "", &res
	}
	return app, tag, nil
}

// relaying reports whether hooks should be relayed (this is a hub with agents).
func (s *Server) relaying() bool {
	return s.hub != nil && s.hub.HasAgents()
}

// fanOut relays a hook to every agent while handling it locally (local is nil
// when this instance has no app for it), and combines the outcomes.
func (s *Server) fanOut(m relay.Message, local func() hookResult) hookResult {
	ctx, cancel := context.WithTimeout(context.Background(), relayWait)
	defer cancel()
	agentsCh := make(chan []relay.AgentResult, 1)
	go func() { agentsCh <- s.hub.Relay(ctx, m.Kind, m.Token, m.Body, m.Verified) }()

	var lr *hookResult
	if local != nil {
		r := local()
		lr = &r
	}
	return relayResponse(lr, <-agentsCh)
}

// relayResponse combines the local outcome (nil if no local app) with the
// agents'. 202 if anything accepted or may still act on the hook; 404 if
// nothing here or on any agent knew it; otherwise the local status, or 200.
func relayResponse(local *hookResult, agents []relay.AgentResult) hookResult {
	body := map[string]any{"status": "relayed", "agents": agents}
	status := http.StatusNotFound
	if local != nil {
		body["local"] = local.body
		status = local.status
	}
	for _, r := range agents {
		switch {
		case r.State != relay.StateDone || r.Status == http.StatusAccepted:
			status = http.StatusAccepted
		case r.Status != http.StatusNotFound && status == http.StatusNotFound:
			status = http.StatusOK
		}
	}
	if local != nil && local.status == http.StatusAccepted {
		status = http.StatusAccepted
	}
	if status == http.StatusNotFound {
		body["error"] = "no app for this webhook here or on any agent"
	}
	return hookResult{status, body}
}

// HandleRelayed runs a webhook relayed from the hub through this instance's
// receivers (agent mode) and returns the response they produced. Relayed hooks
// are never relayed further.
func (s *Server) HandleRelayed(_ context.Context, m relay.Message) (int, []byte) {
	var res *hookResult
	switch m.Kind {
	case relay.KindDockerHub:
		app, tag, fail := s.resolveDockerHub(m.Token, m.Body, m.Verified)
		if res = fail; res == nil {
			r := s.handleTrigger(app, tag, model.TriggerDockerHub)
			res = &r
		}
	case relay.KindGitHub:
		// The hub verified the signature against its own secret.
		app, tag, fail := s.resolveGitHub(m.Body)
		if res = fail; res == nil {
			r := s.handleTrigger(app, tag, model.TriggerGitHub)
			res = &r
		} else if res.status == http.StatusNotFound {
			res.status = http.StatusOK // "no app" is a no-op, as for a direct hook
		}
	default:
		r := hookErr(http.StatusBadRequest, "unknown relay kind "+m.Kind)
		res = &r
	}
	b, _ := json.Marshal(res.body)
	return res.status, b
}

// handleTrigger runs the strategy decision for a webhook-delivered tag and, if a
// deploy is warranted, enqueues it. It returns fast (the deploy runs async).
// Matched-but-rejected tags are recorded as skipped events for visibility.
func (s *Server) handleTrigger(app model.App, tag, trigger string) hookResult {
	// Read the currently-deployed tag (best effort, short timeout) so the
	// strategy can avoid redundant deploys and compare semver ordering.
	currentTag := ""
	if len(app.Targets) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		if img, err := s.k8s.CurrentImage(ctx, app.Targets[0]); err == nil {
			currentTag = strategy.TagOf(img)
		}
		cancel()
	}

	d := strategy.Decide(app, tag, currentTag)
	if !d.Deploy {
		s.recordSkip(app, tag, trigger, d.Reason)
		return hookResult{http.StatusOK, map[string]string{"status": "skipped", "reason": d.Reason}}
	}

	s.engine.Enqueue(deploy.Job{
		App:      app,
		Trigger:  trigger,
		Action:   d.Action,
		NewImage: d.NewImage,
		Tag:      d.Tag,
	})
	return hookResult{http.StatusAccepted, map[string]string{"status": "deploying", "image": d.NewImage, "action": d.Action}}
}

func (s *Server) recordSkip(app model.App, tag, trigger, reason string) {
	_, err := s.store.CreateEvent(model.DeployEvent{
		AppID:    &app.ID,
		AppName:  app.Name,
		Trigger:  trigger,
		Action:   model.ActionSkipped,
		NewImage: app.ImageRepo + ":" + tag,
		Status:   model.StatusSkipped,
		Detail:   reason,
	})
	if err != nil {
		s.log.Warn("record skip event", "app", app.Name, "err", err)
	}
}
