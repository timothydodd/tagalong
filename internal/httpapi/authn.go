package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/timothydodd/tagalong/internal/auth"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/store"
)

const (
	sessionCookie   = "tagalong_session"
	sessionTTL      = 7 * 24 * time.Hour
	defaultUsername = "admin"
	defaultPassword = "admin"
	minPasswordLen  = 8

	// Login throttle: attempts allowed per client IP per window. Also caps how
	// much PBKDF2 work an unauthenticated caller can force.
	loginWindow      = time.Minute
	loginMaxAttempts = 10
)

// rateLimiter is a fixed-window per-key attempt counter.
type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{attempts: make(map[string][]time.Time)}
}

// allow records an attempt for key and reports whether it is within limits.
func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	cutoff := now.Add(-loginWindow)
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= loginMaxAttempts {
		l.attempts[key] = kept
		return false
	}
	l.attempts[key] = append(kept, now)
	// Opportunistically drop idle keys so the map can't grow unbounded.
	if len(l.attempts) > 1000 {
		for k, ts := range l.attempts {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.attempts, k)
			}
		}
	}
	return true
}

// clientIP returns the request's client address without the port. RealIP
// middleware has already substituted proxy headers where present.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// SeedAdmin creates the default admin/admin account the first time tagalong runs
// against a fresh database. Subsequent boots are a no-op. The default password
// is flagged so the UI can nag the operator to change it.
func SeedAdmin(st *store.Store, log *slog.Logger) error {
	if h, _ := st.GetSetting(model.KeyAuthPasswordHash); h != "" {
		return nil
	}
	hash, err := auth.HashPassword(defaultPassword)
	if err != nil {
		return err
	}
	if err := st.SetSetting(model.KeyAuthUsername, defaultUsername); err != nil {
		return err
	}
	if err := st.SetSetting(model.KeyAuthPasswordHash, hash); err != nil {
		return err
	}
	if err := st.SetSetting(model.KeyAuthPasswordIsDefault, "1"); err != nil {
		return err
	}
	log.Warn("seeded default portal login — change the password in Settings",
		"username", defaultUsername, "password", defaultPassword)
	return nil
}

// loadOrCreateSessionSecret returns the persisted HMAC secret used to sign
// session cookies, generating and storing one on first use so tokens survive
// restarts.
func loadOrCreateSessionSecret(st *store.Store) ([]byte, error) {
	if v, _ := st.GetSetting(model.KeyAuthSessionSecret); v != "" {
		if b, err := base64.RawStdEncoding.DecodeString(v); err == nil && len(b) >= 32 {
			return b, nil
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := st.SetSetting(model.KeyAuthSessionSecret, base64.RawStdEncoding.EncodeToString(b)); err != nil {
		return nil, err
	}
	return b, nil
}

// secret returns the current session-signing secret (rotated on password change).
func (s *Server) secret() []byte {
	s.secretMu.RLock()
	defer s.secretMu.RUnlock()
	return s.sessionSecret
}

// rotateSessionSecret replaces the persisted HMAC secret, immediately
// invalidating every outstanding session token.
func (s *Server) rotateSessionSecret() error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	if err := s.store.SetSetting(model.KeyAuthSessionSecret, base64.RawStdEncoding.EncodeToString(b)); err != nil {
		return err
	}
	s.secretMu.Lock()
	s.sessionSecret = b
	s.secretMu.Unlock()
	return nil
}

// currentUser returns the authenticated username from the session cookie, if the
// cookie is present, correctly signed, and unexpired.
func (s *Server) currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	return auth.VerifySession(s.secret(), c.Value, time.Now().Unix())
}

// requireAuth rejects unauthenticated requests with 401. The SPA treats a 401 on
// any data call as "show the login screen".
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentUser(r); !ok {
			writeErr(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, user string) {
	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    auth.SignSession(s.secret(), user, exp.Unix()),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		MaxAge:   int(sessionTTL.Seconds()),
		// Secure is intentionally unset: the portal is commonly reached over
		// plain HTTP on the LAN. TLS termination happens at the proxy.
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// meResponse is the shared shape returned by login, me, and password change.
func (s *Server) meResponse(user string) map[string]any {
	def, _ := s.store.GetSetting(model.KeyAuthPasswordIsDefault)
	return map[string]any{"username": user, "must_change_password": def == "1"}
}

// login validates credentials and issues a session cookie.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.allow(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many login attempts; try again in a minute")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, _ := s.store.GetSetting(model.KeyAuthUsername)
	hash, _ := s.store.GetSetting(model.KeyAuthPasswordHash)
	// Always run VerifyPassword (even on username mismatch) to avoid leaking
	// which field was wrong via response timing.
	passOK := auth.VerifyPassword(hash, body.Password)
	if user == "" || hash == "" || !strings.EqualFold(strings.TrimSpace(body.Username), user) || !passOK {
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.setSessionCookie(w, user)
	writeJSON(w, http.StatusOK, s.meResponse(user))
}

// logout clears the session cookie.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// me reports the current session, or 401 if unauthenticated.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, s.meResponse(user))
}

// changePassword updates the admin password after verifying the current one.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	hash, _ := s.store.GetSetting(model.KeyAuthPasswordHash)
	if !auth.VerifyPassword(hash, body.CurrentPassword) {
		writeErr(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	if len(body.NewPassword) < minPasswordLen {
		writeErr(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	newHash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetSetting(model.KeyAuthPasswordHash, newHash); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.SetSetting(model.KeyAuthPasswordIsDefault, "0")
	// Rotate the signing secret so any previously issued (possibly stolen)
	// tokens stop validating, then re-issue the current session under the new
	// secret so this login stays valid.
	if err := s.rotateSessionSecret(); err != nil {
		s.log.Warn("rotate session secret after password change", "err", err)
	}
	s.setSessionCookie(w, user)
	writeJSON(w, http.StatusOK, s.meResponse(user))
}
