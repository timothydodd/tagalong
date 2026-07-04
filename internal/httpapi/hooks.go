package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/store"
	"github.com/timothydodd/tagalong/internal/strategy"
	"github.com/timothydodd/tagalong/internal/webhook"
)

// maxHookBody caps webhook request bodies.
const maxHookBody = 1 << 20 // 1 MiB

// hookDockerHub handles POST /hooks/dockerhub/{token}. The token identifies the
// app (and authenticates the caller). The payload's repo is cross-checked
// against the app's configured image_repo.
func (s *Server) hookDockerHub(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	app, err := s.store.GetAppByToken(token)
	if err != nil {
		// Unknown token: 404, don't leak which tokens are valid beyond status.
		writeErr(w, http.StatusNotFound, "unknown webhook token")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	repo, tag, err := webhook.ParseDockerHub(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if repo != app.ImageRepo {
		s.log.Warn("dockerhub webhook repo mismatch", "app", app.Name, "token_repo", app.ImageRepo, "payload_repo", repo)
		writeErr(w, http.StatusBadRequest, "payload repo does not match app")
		return
	}

	s.handleTrigger(w, app, tag, model.TriggerDockerHub)
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
	if !webhook.ValidateGitHubSignature(secret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeErr(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	repo, tag, err := webhook.ParseGitHub(body)
	if errors.Is(err, webhook.ErrNotContainerPublish) {
		// Benign event we don't act on (ping, non-container, digest-only).
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	app, err := s.store.GetAppByRepo(repo)
	if errors.Is(err, store.ErrNotFound) {
		// Org-level webhook will send packages we don't track — no-op.
		writeJSON(w, http.StatusOK, map[string]string{"status": "no app for " + repo})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.handleTrigger(w, app, tag, model.TriggerGitHub)
}

// handleTrigger runs the strategy decision for a webhook-delivered tag and, if a
// deploy is warranted, enqueues it. It returns fast (the deploy runs async).
// Matched-but-rejected tags are recorded as skipped events for visibility.
func (s *Server) handleTrigger(w http.ResponseWriter, app model.App, tag, trigger string) {
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
		writeJSON(w, http.StatusOK, map[string]string{"status": "skipped", "reason": d.Reason})
		return
	}

	s.engine.Enqueue(deploy.Job{
		App:      app,
		Trigger:  trigger,
		Action:   d.Action,
		NewImage: d.NewImage,
		Tag:      d.Tag,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deploying", "image": d.NewImage, "action": d.Action})
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
