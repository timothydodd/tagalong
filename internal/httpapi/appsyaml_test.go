package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/timothydodd/tagalong/internal/model"
)

func seedApp(t *testing.T, srv http.Handler, st interface {
	CreateApp(model.App) (model.App, error)
}) model.App {
	t.Helper()
	a, err := st.CreateApp(model.App{
		Name: "robo-dash", ImageRepo: "docker.io/timdoddcool/robo-dash",
		TagStrategy: model.StrategyExact, StrategyConf: model.StrategyConf{Pattern: "^[0-9a-f]{40}$"},
		Enabled: true, WebhookToken: "tok123", PollInterval: 300,
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "homedash", Container: "robo-dash"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestExportApps(t *testing.T) {
	srv, st, _ := testServer(t)
	seedApp(t, srv, st)

	req := httptest.NewRequest(http.MethodGet, "/api/apps/export", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("content-type = %q, want yaml", ct)
	}
	var f appsFile
	if err := yaml.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("unmarshal export: %v\n%s", err, rec.Body.String())
	}
	if len(f.Apps) != 1 || f.Apps[0].Name != "robo-dash" {
		t.Fatalf("export apps = %+v", f.Apps)
	}
	// Server-managed fields must not leak into the portable doc.
	if strings.Contains(rec.Body.String(), "created_at") || strings.Contains(rec.Body.String(), "\nid:") {
		t.Errorf("export leaked server-managed fields:\n%s", rec.Body.String())
	}
	// Webhook token is included so re-import keeps URLs stable.
	if f.Apps[0].WebhookToken != "tok123" {
		t.Errorf("export token = %q, want tok123", f.Apps[0].WebhookToken)
	}
}

func TestImportUpsertsByName(t *testing.T) {
	srv, st, _ := testServer(t)
	seedApp(t, srv, st) // existing "robo-dash"

	doc := `
apps:
  - name: robo-dash
    image_repo: timdoddcool/robo-dash
    tag_strategy: semver
    strategy_conf:
      constraint: ">=1.0.0"
    enabled: true
    targets:
      - namespace: default
        name: homedash
        container: robo-dash
  - name: brand-new
    image_repo: timdoddcool/brand-new
    tag_strategy: latest
    enabled: true
    targets:
      - namespace: web
        name: brand-new
        container: app
`
	req := httptest.NewRequest(http.MethodPost, "/api/apps/import", bytes.NewBufferString(doc))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Created != 1 || res.Updated != 1 {
		t.Fatalf("created=%d updated=%d, want 1/1", res.Created, res.Updated)
	}

	apps, _ := st.ListApps()
	if len(apps) != 2 {
		t.Fatalf("apps = %d, want 2", len(apps))
	}
	for _, a := range apps {
		switch a.Name {
		case "robo-dash":
			if a.TagStrategy != model.StrategySemver {
				t.Errorf("robo-dash strategy = %q, want semver (updated)", a.TagStrategy)
			}
			if a.WebhookToken != "tok123" {
				t.Errorf("robo-dash token = %q, want preserved tok123", a.WebhookToken)
			}
			// image_repo normalized on import
			if a.ImageRepo != "docker.io/timdoddcool/robo-dash" {
				t.Errorf("robo-dash image_repo = %q, not normalized", a.ImageRepo)
			}
		case "brand-new":
			if a.WebhookToken == "" {
				t.Errorf("brand-new should get a generated webhook token")
			}
		default:
			t.Errorf("unexpected app %q", a.Name)
		}
	}
}

func TestImportValidatesBeforeWriting(t *testing.T) {
	srv, st, _ := testServer(t)

	// Second app is invalid (no image_repo) → whole import must abort, nothing written.
	doc := `
apps:
  - name: ok-app
    image_repo: timdoddcool/ok
    tag_strategy: latest
  - name: bad-app
    tag_strategy: latest
`
	req := httptest.NewRequest(http.MethodPost, "/api/apps/import", bytes.NewBufferString(doc))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if apps, _ := st.ListApps(); len(apps) != 0 {
		t.Fatalf("apps = %d, want 0 (import must be atomic)", len(apps))
	}
}

func TestUpdateAppYAMLAllowsRename(t *testing.T) {
	srv, st, _ := testServer(t)
	app := seedApp(t, srv, st)

	doc := `
name: robo-dash-renamed
image_repo: timdoddcool/robo-dash
tag_strategy: exact
strategy_conf:
  pattern: "^[0-9a-f]{40}$"
enabled: true
targets:
  - namespace: default
    name: homedash
    container: robo-dash
`
	req := httptest.NewRequest(http.MethodPut, "/api/apps/"+strconv.FormatInt(app.ID, 10)+"/yaml", bytes.NewBufferString(doc))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetApp(app.ID)
	if got.Name != "robo-dash-renamed" {
		t.Errorf("name = %q, want renamed", got.Name)
	}
	if got.WebhookToken != "tok123" {
		t.Errorf("token = %q, want preserved tok123", got.WebhookToken)
	}
}
