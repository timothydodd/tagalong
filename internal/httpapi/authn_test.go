package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectedEndpointRequiresAuth(t *testing.T) {
	srv, _, _ := rawTestServer(t)

	// No cookie → 401 on a protected endpoint.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth /api/apps = %d, want 401", rec.Code)
	}

	// Health, login, and me stay public.
	for _, p := range []string{"/api/healthz", "/api/me"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if p == "/api/healthz" && rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, rec.Code)
		}
		if p == "/api/me" && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s (anon) = %d, want 401", p, rec.Code)
		}
	}
}

func TestLoginBadPassword(t *testing.T) {
	srv, _, _ := rawTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login",
		bytes.NewBufferString(`{"username":"admin","password":"wrong"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("bad login should not set a cookie")
	}
}

func TestLoginThenAccessAndChangePassword(t *testing.T) {
	srv, _, _ := rawTestServer(t)
	cookie := loginCookie(t, srv)

	// The seeded account starts flagged as default.
	rec := httptest.NewRecorder()
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(cookie)
	srv.ServeHTTP(rec, meReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d, want 200", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"must_change_password":true`)) {
		t.Errorf("expected must_change_password=true, got %s", rec.Body.String())
	}

	// Protected endpoint now reachable with the cookie.
	rec = httptest.NewRecorder()
	appsReq := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	appsReq.AddCookie(cookie)
	srv.ServeHTTP(rec, appsReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed /api/apps = %d, want 200", rec.Code)
	}

	// Wrong current password is rejected.
	rec = httptest.NewRecorder()
	badChange := httptest.NewRequest(http.MethodPost, "/api/account/password",
		bytes.NewBufferString(`{"current_password":"nope","new_password":"supersecret1"}`))
	badChange.AddCookie(cookie)
	srv.ServeHTTP(rec, badChange)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("change with wrong current = %d, want 403", rec.Code)
	}

	// Too-short new password is rejected.
	rec = httptest.NewRecorder()
	shortChange := httptest.NewRequest(http.MethodPost, "/api/account/password",
		bytes.NewBufferString(`{"current_password":"admin","new_password":"short"}`))
	shortChange.AddCookie(cookie)
	srv.ServeHTTP(rec, shortChange)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("change with short new = %d, want 400", rec.Code)
	}

	// Valid change succeeds and clears the default flag.
	rec = httptest.NewRecorder()
	goodChange := httptest.NewRequest(http.MethodPost, "/api/account/password",
		bytes.NewBufferString(`{"current_password":"admin","new_password":"supersecret1"}`))
	goodChange.AddCookie(cookie)
	srv.ServeHTTP(rec, goodChange)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid change = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"must_change_password":false`)) {
		t.Errorf("expected must_change_password=false after change, got %s", rec.Body.String())
	}

	// Old password no longer works; new one does.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login",
		bytes.NewBufferString(`{"username":"admin","password":"admin"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password after change = %d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login",
		bytes.NewBufferString(`{"username":"admin","password":"supersecret1"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("new password login = %d, want 200", rec.Code)
	}
}
