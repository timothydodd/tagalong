package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/timothydodd/tagalong/internal/deploy"
)

func TestListWorkloads(t *testing.T) {
	a := readyDeploy("default", "homedash", "robo-dash", "timdoddcool/robo-dash:v1")
	b := readyDeploy("web", "site", "nginx", "nginx:1.27")
	srv, _, _ := testServer(t, a, b)

	req := httptest.NewRequest(http.MethodGet, "/api/workloads", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var wls []deploy.Workload
	if err := json.Unmarshal(rec.Body.Bytes(), &wls); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(wls) != 2 {
		t.Fatalf("workloads = %d, want 2", len(wls))
	}
	byName := map[string]deploy.Workload{}
	for _, w := range wls {
		byName[w.Name] = w
	}
	hd, ok := byName["homedash"]
	if !ok {
		t.Fatalf("homedash missing: %+v", wls)
	}
	if hd.Namespace != "default" || hd.Kind != "Deployment" {
		t.Errorf("homedash = %+v", hd)
	}
	if len(hd.Containers) != 1 || hd.Containers[0].Name != "robo-dash" ||
		hd.Containers[0].Image != "timdoddcool/robo-dash:v1" {
		t.Errorf("homedash containers = %+v", hd.Containers)
	}
}
