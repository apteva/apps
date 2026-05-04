package main

// crypto.go — password hashing (argon2id), token hashing (sha256),
// JWT signing/verification (EdDSA), JWKS encoding, random helpers.
//
// Only one third-party dep: golang.org/x/crypto/argon2. JWT is
// hand-rolled because the stdlib's crypto/ed25519 + encoding/json
// covers everything we need and pulling github.com/golang-jwt/jwt
// for one alg is overkill.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// ─── Password hashing (argon2id) ─────────────────────────────────────
//
// Output format: $argon2id$v=19$m=<KiB>,t=<iters>,p=<lanes>$<salt>$<hash>
// (matches the standard PHC string format the rest of the world uses,
// so passwords stay portable if we ever migrate off this app.)

const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 64 * 1024 // 64 MiB
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// parts: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("malformed password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("hash version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("hash argon version %d != runtime %d", version, argon2.Version)
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("hash params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// validatePassword enforces the install's policy. Returns "" on success
// or a human-readable reason on rejection. Length is checked first
// (cheapest); class count second (still cheap).
func validatePassword(pw string, minLen, classesRequired int) string {
	if len(pw) < minLen {
		return fmt.Sprintf("password must be at least %d characters", minLen)
	}
	var hasLower, hasUpper, hasDigit, hasSymbol bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	classes := 0
	for _, b := range []bool{hasLower, hasUpper, hasDigit, hasSymbol} {
		if b {
			classes++
		}
	}
	if classes < classesRequired {
		return fmt.Sprintf("password must include %d of [lowercase, uppercase, digit, symbol]", classesRequired)
	}
	return ""
}

// ─── Token hashing (sha256) ──────────────────────────────────────────
//
// We never store raw refresh tokens or one-time tokens. On lookup we
// hash the presented value and compare. sha256 is sufficient here —
// these tokens are 256 bits of entropy already (randSlug below);
// argon2 would just slow down every refresh for no security gain.

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ─── Random helpers ──────────────────────────────────────────────────

// randSlug returns a base64url-encoded random string with at least
// `bytes` bytes of entropy. Used for opaque tokens (refresh, verify,
// magic-link), client_id, kid, recovery codes (via formatRecovery).
func randSlug(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ─── JWT (EdDSA, compact serialization) ──────────────────────────────
//
// Hand-rolled because the stdlib gives us everything: crypto/ed25519
// for sign+verify, encoding/json + base64.RawURLEncoding for the
// compact form. Saves a dep + ~2MB of code we'd otherwise pull in.

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// jwtClaims is the standard set we issue. Auth callers can tack on
// extra claims via the Extra map (set into top-level via merging at
// sign time).
type jwtClaims struct {
	Iss   string         `json:"iss"`
	Sub   string         `json:"sub"`
	Aud   string         `json:"aud,omitempty"`
	Azp   string         `json:"azp,omitempty"`
	Iat   int64          `json:"iat"`
	Exp   int64          `json:"exp"`
	Email string         `json:"email,omitempty"`
	EVer  bool           `json:"email_verified,omitempty"`
	Extra map[string]any `json:"-"`
}

// jwtSign produces a compact-serialised JWT signed with the given
// EdDSA private key. The kid is embedded in the header so verifiers
// can pick the right public key from JWKS.
func jwtSign(priv ed25519.PrivateKey, kid string, claims jwtClaims) (string, error) {
	h := jwtHeader{Alg: "EdDSA", Typ: "JWT", Kid: kid}
	hb, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	// Marshal claims first, then merge Extra into the resulting object.
	// Doing it via two passes keeps json:"-" honest while letting
	// callers add arbitrary scope/role claims without growing this
	// struct.
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	if len(claims.Extra) > 0 {
		var m map[string]any
		if err := json.Unmarshal(cb, &m); err != nil {
			return "", err
		}
		for k, v := range claims.Extra {
			m[k] = v
		}
		cb, err = json.Marshal(m)
		if err != nil {
			return "", err
		}
	}
	enc := base64.RawURLEncoding.EncodeToString
	signing := enc(hb) + "." + enc(cb)
	sig := ed25519.Sign(priv, []byte(signing))
	return signing + "." + enc(sig), nil
}

// jwtVerify parses + verifies the signature against a key resolver
// (kid → public key). On success returns the parsed claims as a map.
//
// The resolver pattern lets callers walk the signing_keys table to
// find the matching kid, which supports the rotation drain window
// (retired-but-not-deleted keys still verify until tokens expire).
func jwtVerify(token string, keyForKid func(kid string) (ed25519.PublicKey, bool)) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed jwt")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("header b64: %w", err)
	}
	var h jwtHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, fmt.Errorf("header json: %w", err)
	}
	if h.Alg != "EdDSA" {
		return nil, fmt.Errorf("unexpected alg %q", h.Alg)
	}
	pub, ok := keyForKid(h.Kid)
	if !ok {
		return nil, fmt.Errorf("unknown kid %q", h.Kid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("sig b64: %w", err)
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("signature mismatch")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("claims b64: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, fmt.Errorf("claims json: %w", err)
	}
	// Expiry check.
	if expF, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() >= int64(expF) {
			return nil, errors.New("token expired")
		}
	}
	return claims, nil
}

// ─── PEM <-> ed25519 ─────────────────────────────────────────────────
//
// We store the keys as raw PEM blocks containing the ed25519
// 32-byte/64-byte byte strings, not PKIX-encoded — the keys are
// internal to this app, never exchanged with anything that needs
// PKIX. Saves a x509 import. Public keys exposed via JWKS use the
// "OKP" + Ed25519 form, which is just base64url(pub) — no PKIX.

func parseEd25519Private(pemStr string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if len(block.Bytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("bad private key length %d", len(block.Bytes))
	}
	return ed25519.PrivateKey(block.Bytes), nil
}

func parseEd25519Public(pemStr string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if len(block.Bytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("bad public key length %d", len(block.Bytes))
	}
	return ed25519.PublicKey(block.Bytes), nil
}

// jwk is a single JSON Web Key, in the OKP+Ed25519 form RFC 8037
// defines. JWKS = { "keys": [jwk, …] }.
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	X   string `json:"x"`
}

func jwkFromEd25519(kid string, pub ed25519.PublicKey) jwk {
	return jwk{
		Kty: "OKP",
		Crv: "Ed25519",
		Kid: kid,
		Alg: "EdDSA",
		Use: "sig",
		X:   base64.RawURLEncoding.EncodeToString(pub),
	}
}

// ─── parseTTL — loose duration parsing for test/config strings ───────

func parseTTL(s string, dflt time.Duration) time.Duration {
	if s == "" {
		return dflt
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
		return dflt
	}
	return time.Duration(n) * time.Second
}
