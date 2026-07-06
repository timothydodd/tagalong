package auth

import "testing"

func TestHashVerifyPassword(t *testing.T) {
	h, err := HashPassword("admin")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "admin") {
		t.Error("correct password did not verify")
	}
	if VerifyPassword(h, "Admin") {
		t.Error("wrong password verified")
	}
	// Two hashes of the same password differ (random salt).
	h2, _ := HashPassword("admin")
	if h == h2 {
		t.Error("expected distinct salted hashes")
	}
	// Malformed input never panics and never verifies.
	for _, bad := range []string{"", "x", "pbkdf2-sha256$notanint$a$b", "a$b$c$d"} {
		if VerifyPassword(bad, "admin") {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}

func TestSignVerifySession(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok := SignSession(secret, "admin", 1000)

	if u, ok := VerifySession(secret, tok, 999); !ok || u != "admin" {
		t.Fatalf("valid token: got (%q,%v), want (admin,true)", u, ok)
	}
	// Expired.
	if _, ok := VerifySession(secret, tok, 1001); ok {
		t.Error("expired token verified")
	}
	// Wrong secret.
	if _, ok := VerifySession([]byte("wrongwrongwrongwrongwrongwrong12"), tok, 999); ok {
		t.Error("token verified under wrong secret")
	}
	// Tampered payload.
	if _, ok := VerifySession(secret, "tampered."+tok, 999); ok {
		t.Error("tampered token verified")
	}
	if _, ok := VerifySession(secret, "garbage", 999); ok {
		t.Error("garbage token verified")
	}
}
