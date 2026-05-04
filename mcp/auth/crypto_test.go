package main

// crypto_test — round-trips for argon2id, JWT sign/verify, token
// hashing. No DB, no HTTP — pure helpers.

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestPasswordHash_RoundTrip(t *testing.T) {
	enc, err := hashPassword("CorrectHorse!Battery#9")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(enc, "$argon2id$") {
		t.Errorf("encoded hash doesn't look like PHC string: %q", enc)
	}
	ok, err := verifyPassword(enc, "CorrectHorse!Battery#9")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Error("verify returned false for matching password")
	}
	bad, _ := verifyPassword(enc, "wrong-password")
	if bad {
		t.Error("verify returned true for wrong password")
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		pw       string
		minLen   int
		classes  int
		wantPass bool
	}{
		{"short", 12, 2, false},                         // too short
		{"alllowercaseword", 12, 2, false},              // 1 class, want 2
		{"AllMixedCase12!", 12, 2, true},                // 4 classes
		{"abcDEF123456", 12, 2, true},                   // 3 classes, ok
		{"abcdefghijkl", 12, 1, true},                   // 1 class, ok at minimum 1
	}
	for _, c := range cases {
		got := validatePassword(c.pw, c.minLen, c.classes)
		pass := got == ""
		if pass != c.wantPass {
			t.Errorf("validatePassword(%q, %d, %d) = %q (pass=%v), want pass=%v",
				c.pw, c.minLen, c.classes, got, pass, c.wantPass)
		}
	}
}

func TestHashToken_Stable(t *testing.T) {
	h1 := hashToken("abc")
	h2 := hashToken("abc")
	h3 := hashToken("abd")
	if h1 != h2 {
		t.Errorf("same input → different hash: %q vs %q", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("different input → same hash: %q == %q", h1, h3)
	}
}

func TestRandSlug_Distinct(t *testing.T) {
	a, _ := randSlug(16)
	b, _ := randSlug(16)
	if a == b {
		t.Errorf("two slugs collided: %q", a)
	}
}

func TestJWT_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	now := time.Now()
	claims := jwtClaims{
		Iss:   "https://example.com",
		Sub:   "42",
		Aud:   "akc_test",
		Iat:   now.Unix(),
		Exp:   now.Add(15 * time.Minute).Unix(),
		Email: "alice@example.com",
		EVer:  true,
		Extra: map[string]any{"role": "admin"},
	}
	tok, err := jwtSign(priv, "kid-1", claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := jwtVerify(tok, func(kid string) (ed25519.PublicKey, bool) {
		if kid != "kid-1" {
			return nil, false
		}
		return pub, true
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got["sub"] != "42" {
		t.Errorf("sub = %v, want 42", got["sub"])
	}
	if got["role"] != "admin" {
		t.Errorf("extra claim role = %v, want admin", got["role"])
	}
}

func TestJWT_RejectsExpired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok, _ := jwtSign(priv, "kid-1", jwtClaims{
		Iss: "x", Sub: "1",
		Iat: time.Now().Add(-time.Hour).Unix(),
		Exp: time.Now().Add(-time.Minute).Unix(),
	})
	_, err := jwtVerify(tok, func(kid string) (ed25519.PublicKey, bool) {
		return pub, true
	})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected 'expired' error, got %v", err)
	}
}

func TestJWT_RejectsBadSignature(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok, _ := jwtSign(priv, "kid-1", jwtClaims{
		Iss: "x", Sub: "1",
		Iat: time.Now().Unix(),
		Exp: time.Now().Add(time.Minute).Unix(),
	})
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := jwtVerify(tok, func(kid string) (ed25519.PublicKey, bool) {
		return otherPub, true
	})
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected signature error, got %v", err)
	}
}
