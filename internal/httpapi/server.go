// Package httpapi exposes the REST API, webhook receivers, SSE stream, and the
// embedded SPA.
package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/events"
	"github.com/timothydodd/tagalong/internal/store"
	"github.com/timothydodd/tagalong/ui"
)

// TagLister proxies registry tag listing for the UI (implemented by the
// registry client; may be nil until Phase 3 wiring).
type TagLister interface {
	ListTags(repo string) ([]string, error)
}

// Server holds handler dependencies.
type Server struct {
	store         *store.Store
	engine        *deploy.Engine
	k8s           *deploy.K8s
	bus           *events.Bus
	tags          TagLister
	log           *slog.Logger
	sessionSecret []byte
}

// newServer builds the Server with its handler dependencies. Both NewServer and
// NewHooksHandler share it so the two handlers behave identically per route.
func newServer(st *store.Store, engine *deploy.Engine, k8s *deploy.K8s, bus *events.Bus, tags TagLister, log *slog.Logger) *Server {
	s := &Server{store: st, engine: engine, k8s: k8s, bus: bus, tags: tags, log: log}
	secret, err := loadOrCreateSessionSecret(st)
	if err != nil {
		// Fall back to an ephemeral secret so the process still serves; existing
		// sessions won't validate until a stable secret can be persisted.
		log.Error("session secret unavailable; using ephemeral (logins reset on restart)", "err", err)
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
	}
	s.sessionSecret = secret
	return s
}

// mountHooks registers the webhook receivers. It's shared so the hooks are
// reachable both on the full handler and on a hooks-only listener.
func (s *Server) mountHooks(r chi.Router) {
	r.Post("/hooks/dockerhub/{token}", s.hookDockerHub)
	r.Post("/hooks/github", s.hookGitHub)
}

// NewServer constructs the full HTTP handler tree: API, webhook receivers, and
// the embedded SPA.
func NewServer(st *store.Store, engine *deploy.Engine, k8s *deploy.K8s, bus *events.Bus, tags TagLister, log *slog.Logger) http.Handler {
	s := newServer(st, engine, k8s, bus, tags, log)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	r.Route("/api", func(r chi.Router) {
		// Public: liveness and the auth handshake.
		r.Get("/healthz", s.healthz)
		r.Post("/login", s.login)
		r.Post("/logout", s.logout)
		r.Get("/me", s.me)

		// Everything else requires a valid session.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Post("/account/password", s.changePassword)

			r.Get("/apps", s.listApps)
			r.Post("/apps", s.createApp)
			r.Get("/apps/export", s.exportApps)  // all apps as YAML
			r.Post("/apps/import", s.importApps) // upsert apps from YAML
			r.Get("/apps/{id}", s.getApp)
			r.Put("/apps/{id}", s.updateApp)
			r.Delete("/apps/{id}", s.deleteApp)
			r.Get("/apps/{id}/export", s.exportApp)   // single app as YAML
			r.Put("/apps/{id}/yaml", s.updateAppYAML) // replace single app from YAML
			r.Post("/apps/{id}/deploy", s.deployApp)
			r.Post("/apps/{id}/token/rotate", s.rotateToken)
			r.Get("/apps/{id}/status", s.appStatus)
			r.Get("/apps/{id}/tags", s.appTags)

			r.Get("/workloads", s.listWorkloads)

			r.Get("/events", s.listEvents)
			r.Get("/events/stream", s.streamEvents)

			r.Get("/settings", s.getSettings)
			r.Put("/settings", s.putSettings)
			r.Get("/settings/registries", s.listRegistries)
			r.Put("/settings/registries", s.putRegistry)
			r.Delete("/settings/registries/{registry}", s.deleteRegistry)
		})
	})

	// Webhook receivers.
	s.mountHooks(r)

	// Embedded SPA with history-fallback.
	r.Handle("/*", spaHandler(ui.FS()))

	return r
}

// NewHooksHandler returns a handler serving ONLY the webhook receivers, for
// binding to a separate listener that can be exposed publicly while the portal
// and API stay on the private main listener. Anything other than /hooks/* is
// 404 — the SPA and /api are deliberately not mounted here.
func NewHooksHandler(st *store.Store, engine *deploy.Engine, k8s *deploy.K8s, bus *events.Bus, tags TagLister, log *slog.Logger) http.Handler {
	s := newServer(st, engine, k8s, bus, tags, log)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	s.mountHooks(r)

	return r
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// spaHandler serves static files from fsys, falling back to index.html for any
// path that doesn't resolve to a file (client-side routing).
func spaHandler(fsys fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(fsys))
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(fsys, p); err != nil {
			// Not a real file → serve index.html so the SPA router handles it.
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			serveIndex(w, r2, fsys)
			return
		}
		fileServer.ServeHTTP(w, r)
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "ui not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
