// Package auth provides password hashing and stateless session tokens for the
// portal login. It deliberately uses only the standard library (PBKDF2-HMAC-
// SHA256 for passwords, HMAC-SHA256 for session signatures) so tagalong pulls in
// no extra module dependency.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

const (
	pbkdf2Iter = 100_000 // OWASP-ish floor for PBKDF2-HMAC-SHA256
	saltLen    = 16
	keyLen     = 32
	hashScheme = "pbkdf2-sha256"
)

// pbkdf2SHA256 derives a key from password and salt per RFC 2898.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	numBlocks := (keyLen + hLen - 1) / hLen
	dk := make([]byte, 0, numBlocks*hLen)
	blockIdx := make([]byte, 4)

	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(blockIdx, uint32(block))
		prf.Write(blockIdx)
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// HashPassword returns an encoded PBKDF2 hash of the form
// "pbkdf2-sha256$<iter>$<saltB64>$<hashB64>".
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(password), salt, pbkdf2Iter, keyLen)
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

// VerifyPassword reports whether password matches the encoded hash. It is
// constant-time in the compare step and never panics on malformed input.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SignSession returns a signed token binding username until expUnix (a Unix
// timestamp). The token is "<payloadB64>.<sigB64>" and carries no server state.
func SignSession(secret []byte, username string, expUnix int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%d", username, expUnix)))
	return payload + "." + signHMAC(secret, payload)
}

// VerifySession validates token against secret and returns the bound username if
// the signature checks out and nowUnix is at or before the expiry.
func VerifySession(secret []byte, token string, nowUnix int64) (string, bool) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	want := signHMAC(secret, payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	user, expStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || nowUnix > exp {
		return "", false
	}
	return user, true
}

func signHMAC(secret []byte, msg string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
