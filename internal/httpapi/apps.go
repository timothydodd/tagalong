package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/registry"
	"github.com/timothydodd/tagalong/internal/store"
)

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApps()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.loadApp(r)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	app, err := decodeApp(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.store.CreateApp(app)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	app, err := decodeApp(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	app.ID = id
	updated, err := s.store.UpdateApp(app)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteApp(id); err != nil {
		writeAppErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	token, err := s.store.RotateToken(id)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"webhook_token": token})
}

// deployApp handles a manual deploy. Body: {"tag":"..."} to deploy a specific
// tag, or empty/absent to rollout-restart the current image.
func (s *Server) deployApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.loadApp(r)
	if err != nil {
		writeAppErr(w, err)
		return
	}

	var body struct {
		Tag string `json:"tag"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	tag := strings.TrimSpace(body.Tag)

	job := deploy.Job{App: app, Trigger: model.TriggerManual}
	if tag != "" {
		job.Action = model.ActionPatch
		job.NewImage = app.ImageRepo + ":" + tag
		job.Tag = tag
	} else {
		job.Action = model.ActionRestart
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	ev := s.engine.DeploySync(ctx, job)
	status := http.StatusOK
	if ev.Status == model.StatusFailed {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, ev)
}

func (s *Server) appStatus(w http.ResponseWriter, r *http.Request) {
	app, err := s.loadApp(r)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	statuses := make([]deploy.TargetStatus, 0, len(app.Targets))
	for _, t := range app.Targets {
		statuses = append(statuses, s.k8s.Status(ctx, t))
	}
	writeJSON(w, http.StatusOK, statuses)
}

func (s *Server) appTags(w http.ResponseWriter, r *http.Request) {
	if s.tags == nil {
		writeErr(w, http.StatusServiceUnavailable, "registry client not configured")
		return
	}
	app, err := s.loadApp(r)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	tags, err := s.tags.ListTags(app.ImageRepo)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

// --- helpers ---

func (s *Server) loadApp(r *http.Request) (model.App, error) {
	id, err := idParam(r)
	if err != nil {
		return model.App{}, store.ErrNotFound
	}
	return s.store.GetApp(id)
}

func idParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

func decodeApp(r *http.Request) (model.App, error) {
	var app model.App
	if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
		return model.App{}, err
	}
	if err := normalizeApp(&app); err != nil {
		return model.App{}, err
	}
	return app, nil
}

// normalizeApp trims/defaults/validates an app in place. Shared by the JSON and
// YAML entry points so both enforce the same rules.
func normalizeApp(app *model.App) error {
	app.Name = strings.TrimSpace(app.Name)
	if app.Name == "" {
		return errors.New("name is required")
	}
	if app.ImageRepo == "" {
		return errors.New("image_repo is required")
	}
	// Normalize the image repo so it matches webhook payloads consistently.
	app.ImageRepo = registry.NormalizeRepo(app.ImageRepo)
	if app.TagStrategy == "" {
		app.TagStrategy = model.StrategyExact
	}
	switch app.TagStrategy {
	case model.StrategyExact, model.StrategyRegex, model.StrategyLatest, model.StrategySemver:
	default:
		return errors.New("invalid tag_strategy")
	}
	for i := range app.Targets {
		if app.Targets[i].Kind == "" {
			app.Targets[i].Kind = model.KindDeployment
		}
		if app.Targets[i].Namespace == "" {
			app.Targets[i].Namespace = "default"
		}
	}
	return nil
}

func writeAppErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}
