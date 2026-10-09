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
// against the app's configured image_repo.
func (s *Server) hookDockerHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	res := s.processDockerHub(chi.URLParam(r, "token"), body, false)
	writeJSON(w, res.status, res.body)
}

func (s *Server) processDockerHub(token string, body []byte, relayed bool) hookResult {
	app, err := s.store.GetAppByToken(token)
	if err != nil {
		// Not ours — one of our agents may own the token.
		if !relayed && s.hub != nil && s.hub.HasAgents() {
			return s.relayDockerHub(token, body)
		}
		// Unknown token: 404, don't leak which tokens are valid beyond status.
		return hookErr(http.StatusNotFound, "unknown webhook token")
	}

	repo, tag, err := webhook.ParseDockerHub(body)
	if err != nil {
		return hookErr(http.StatusBadRequest, err.Error())
	}
	if repo != app.ImageRepo {
		s.log.Warn("dockerhub webhook repo mismatch", "app", app.Name, "token_repo", app.ImageRepo, "payload_repo", repo)
		return hookErr(http.StatusBadRequest, "payload repo does not match app")
	}

	return s.handleTrigger(app, tag, model.TriggerDockerHub)
}

// hookGitHub handles POST /hooks/github. It validates the HMAC signature, then
// maps the published container image to a configured app by normalized repo.
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

	res := s.processGitHub(body, false)
	writeJSON(w, res.status, res.body)
}

// processGitHub handles an already-authenticated GitHub payload: either one
// whose signature this instance verified, or one relayed by the hub (which
// verified it against its own secret).
func (s *Server) processGitHub(body []byte, relayed bool) hookResult {
	repo, tag, err := webhook.ParseGitHub(body)
	if errors.Is(err, webhook.ErrNotContainerPublish) {
		// Benign event we don't act on (ping, non-container, digest-only).
		return hookResult{http.StatusOK, map[string]string{"status": "ignored"}}
	}
	if err != nil {
		return hookErr(http.StatusBadRequest, err.Error())
	}

	app, err := s.store.GetAppByRepo(repo)
	if errors.Is(err, store.ErrNotFound) {
		// Not ours — pass it to our agents, if any.
		if !relayed && s.hub != nil && s.hub.HasAgents() {
			return s.relayGitHub(repo, body)
		}
		// Org-level webhook will send packages we don't track — no-op.
		return hookResult{http.StatusOK, map[string]string{"status": "no app for " + repo}}
	}
	if err != nil {
		return hookErr(http.StatusInternalServerError, err.Error())
	}

	return s.handleTrigger(app, tag, model.TriggerGitHub)
}

// relayDockerHub forwards a Docker Hub hook with an unknown token to the
// agents. At most one agent owns a token, so it's a 404 only when every agent
// definitively said so.
func (s *Server) relayDockerHub(token string, body []byte) hookResult {
	ctx, cancel := context.WithTimeout(context.Background(), relayWait)
	defer cancel()
	results := s.hub.Relay(ctx, relay.KindDockerHub, token, body)

	claimed := false
	for _, r := range results {
		if r.State != relay.StateDone || r.Status != http.StatusNotFound {
			claimed = true
		}
	}
	if !claimed {
		return hookErr(http.StatusNotFound, "unknown webhook token")
	}
	return relayResponse(results)
}

// relayGitHub forwards a GitHub hook for a repo with no local app to the agents.
func (s *Server) relayGitHub(repo string, body []byte) hookResult {
	ctx, cancel := context.WithTimeout(context.Background(), relayWait)
	defer cancel()
	s.log.Info("relaying github webhook to agents", "repo", repo)
	return relayResponse(s.hub.Relay(ctx, relay.KindGitHub, "", body))
}

// relayResponse summarizes the agents' outcomes for the webhook caller: 202 if
// any agent accepted or may still act on it, otherwise 200.
func relayResponse(results []relay.AgentResult) hookResult {
	status := http.StatusOK
	for _, r := range results {
		if r.State != relay.StateDone || r.Status == http.StatusAccepted {
			status = http.StatusAccepted
		}
	}
	return hookResult{status, map[string]any{"status": "relayed", "agents": results}}
}

// HandleRelayed runs a webhook relayed from the hub through this instance's
// receivers (agent mode) and returns the response they produced.
func (s *Server) HandleRelayed(_ context.Context, m relay.Message) (int, []byte) {
	var res hookResult
	switch m.Kind {
	case relay.KindDockerHub:
		res = s.processDockerHub(m.Token, m.Body, true)
	case relay.KindGitHub:
		res = s.processGitHub(m.Body, true)
	default:
		res = hookErr(http.StatusBadRequest, "unknown relay kind "+m.Kind)
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
