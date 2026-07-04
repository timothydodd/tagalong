package deploy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/timothydodd/tagalong/internal/model"
)

type capturedReq struct {
	auth string
	body map[string]any
}

func mockCF(t *testing.T) (*CloudflarePurger, *[]capturedReq, func()) {
	t.Helper()
	var mu sync.Mutex
	var reqs []capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		mu.Lock()
		reqs = append(reqs, capturedReq{auth: r.Header.Get("Authorization"), body: m})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	}))
	p := &CloudflarePurger{
		http:    srv.Client(),
		token:   func() (string, error) { return "cf-token", nil },
		baseURL: srv.URL,
	}
	return p, &reqs, srv.Close
}

func TestCloudflarePurgeEverything(t *testing.T) {
	p, reqs, done := mockCF(t)
	defer done()

	app := model.App{CFPurge: model.CFPurge{Enabled: true, ZoneID: "zone1", Mode: model.CFModeEverything}}
	if err := p.Purge(context.Background(), app); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	r := (*reqs)[0]
	if r.auth != "Bearer cf-token" {
		t.Errorf("auth = %q", r.auth)
	}
	if r.body["purge_everything"] != true {
		t.Errorf("expected purge_everything, got %+v", r.body)
	}
}

func TestCloudflarePurgeURLsChunks(t *testing.T) {
	p, reqs, done := mockCF(t)
	defer done()

	// 65 URLs → 3 requests (30 + 30 + 5).
	urls := make([]string, 65)
	for i := range urls {
		urls[i] = "https://example.com/" + string(rune('a'+i%26))
	}
	app := model.App{CFPurge: model.CFPurge{Enabled: true, ZoneID: "z", Mode: model.CFModeURLs, URLs: urls}}
	if err := p.Purge(context.Background(), app); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(*reqs) != 3 {
		t.Fatalf("expected 3 chunked requests, got %d", len(*reqs))
	}
	total := 0
	for _, r := range *reqs {
		files, _ := r.body["files"].([]any)
		total += len(files)
		if len(files) > cfPurgeMaxURLs {
			t.Errorf("chunk exceeds max: %d", len(files))
		}
	}
	if total != 65 {
		t.Errorf("expected 65 total URLs across chunks, got %d", total)
	}
}

func TestCloudflarePurgeDisabled(t *testing.T) {
	p, reqs, done := mockCF(t)
	defer done()
	if err := p.Purge(context.Background(), model.App{CFPurge: model.CFPurge{Enabled: false}}); err != nil {
		t.Fatalf("disabled purge should be no-op, got %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no requests when disabled, got %d", len(*reqs))
	}
}

func TestCloudflarePurgeMissingZone(t *testing.T) {
	p, _, done := mockCF(t)
	defer done()
	err := p.Purge(context.Background(), model.App{CFPurge: model.CFPurge{Enabled: true}})
	if err == nil {
		t.Error("expected error for missing zone_id")
	}
}
