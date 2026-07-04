package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/timothydodd/tagalong/internal/model"
)

// maxYAMLBytes caps import bodies to keep a bad paste from allocating wildly.
const maxYAMLBytes = 1 << 20 // 1 MiB

// appYAML is the portable, declarative view of an App used for YAML import and
// export. It deliberately omits server-managed fields (id, timestamps,
// last_seen_*) so a file round-trips cleanly and reads well in git. The
// snake_case json tags match the REST API and are honored by sigs.k8s.io/yaml.
type appYAML struct {
	Name         string             `json:"name"`
	ImageRepo    string             `json:"image_repo"`
	TagStrategy  string             `json:"tag_strategy"`
	StrategyConf model.StrategyConf `json:"strategy_conf,omitempty"`
	Targets      []targetYAML       `json:"targets"`
	WebhookToken string             `json:"webhook_token,omitempty"`
	PollEnabled  bool               `json:"poll_enabled"`
	PollInterval int                `json:"poll_interval_sec,omitempty"`
	CFPurge      model.CFPurge      `json:"cf_purge,omitempty"`
	Enabled      bool               `json:"enabled"`
}

type targetYAML struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Container string `json:"container"`
}

// appsFile is the top-level shape of an export/import document.
type appsFile struct {
	Apps []appYAML `json:"apps"`
}

func toAppYAML(a model.App) appYAML {
	targets := make([]targetYAML, 0, len(a.Targets))
	for _, t := range a.Targets {
		targets = append(targets, targetYAML{
			Namespace: t.Namespace, Kind: t.Kind, Name: t.Name, Container: t.Container,
		})
	}
	return appYAML{
		Name:         a.Name,
		ImageRepo:    a.ImageRepo,
		TagStrategy:  a.TagStrategy,
		StrategyConf: a.StrategyConf,
		Targets:      targets,
		WebhookToken: a.WebhookToken,
		PollEnabled:  a.PollEnabled,
		PollInterval: a.PollInterval,
		CFPurge:      a.CFPurge,
		Enabled:      a.Enabled,
	}
}

func (y appYAML) toModel() model.App {
	targets := make([]model.Target, 0, len(y.Targets))
	for _, t := range y.Targets {
		targets = append(targets, model.Target{
			Namespace: t.Namespace, Kind: t.Kind, Name: t.Name, Container: t.Container,
		})
	}
	return model.App{
		Name:         y.Name,
		ImageRepo:    y.ImageRepo,
		TagStrategy:  y.TagStrategy,
		StrategyConf: y.StrategyConf,
		Targets:      targets,
		WebhookToken: y.WebhookToken,
		PollEnabled:  y.PollEnabled,
		PollInterval: y.PollInterval,
		CFPurge:      y.CFPurge,
		Enabled:      y.Enabled,
	}
}

func writeYAML(w http.ResponseWriter, filename string, v any) {
	out, err := yaml.Marshal(v)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.WriteHeader(http.StatusOK)
	w.Write(out)
}

// exportApps returns every app as a single YAML document ({apps: [...]}).
func (s *Server) exportApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApps()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	f := appsFile{Apps: make([]appYAML, 0, len(apps))}
	for _, a := range apps {
		f.Apps = append(f.Apps, toAppYAML(a))
	}
	writeYAML(w, "tagalong-apps.yaml", f)
}

// exportApp returns a single app as a bare YAML document.
func (s *Server) exportApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.loadApp(r)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeYAML(w, fmt.Sprintf("tagalong-%s.yaml", app.Name), toAppYAML(app))
}

// importApps upserts a {apps: [...]} document. Apps are matched by name
// (case-insensitive): existing → update, new → create. Nothing is deleted. The
// whole document is validated before any write, so a bad entry aborts cleanly.
func (s *Server) importApps(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxYAMLBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var f appsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
		return
	}
	if len(f.Apps) == 0 {
		writeErr(w, http.StatusBadRequest, "no apps found; expected a top-level 'apps:' list")
		return
	}

	existing, err := s.store.ListApps()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	byName := make(map[string]model.App, len(existing))
	for _, a := range existing {
		byName[strings.ToLower(a.Name)] = a
	}

	// Pass 1: validate & normalize everything up front.
	prepared := make([]model.App, len(f.Apps))
	seen := make(map[string]bool, len(f.Apps))
	for i, y := range f.Apps {
		app := y.toModel()
		if err := normalizeApp(&app); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("apps[%d]: %s", i, err))
			return
		}
		key := strings.ToLower(app.Name)
		if seen[key] {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("apps[%d]: duplicate name %q in document", i, app.Name))
			return
		}
		seen[key] = true
		prepared[i] = app
	}

	// Pass 2: apply.
	var created, updated int
	names := make([]string, 0, len(prepared))
	for _, app := range prepared {
		if cur, ok := byName[strings.ToLower(app.Name)]; ok {
			app.ID = cur.ID
			if app.WebhookToken == "" {
				app.WebhookToken = cur.WebhookToken // preserve existing webhook URL
			}
			if _, err := s.store.UpdateApp(app); err != nil {
				writeErr(w, http.StatusInternalServerError, fmt.Sprintf("update %q: %s", app.Name, err))
				return
			}
			updated++
		} else {
			if _, err := s.store.CreateApp(app); err != nil {
				writeErr(w, http.StatusInternalServerError, fmt.Sprintf("create %q: %s", app.Name, err))
				return
			}
			created++
		}
		names = append(names, app.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created,
		"updated": updated,
		"apps":    names,
	})
}

// updateAppYAML replaces a single existing app (by id) from a bare YAML body.
// Renames are allowed since the target is keyed by id, not name.
func (s *Server) updateAppYAML(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	cur, err := s.store.GetApp(id)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxYAMLBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var y appYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
		return
	}
	app := y.toModel()
	app.ID = id
	if app.WebhookToken == "" {
		app.WebhookToken = cur.WebhookToken
	}
	if err := normalizeApp(&app); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.store.UpdateApp(app)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
