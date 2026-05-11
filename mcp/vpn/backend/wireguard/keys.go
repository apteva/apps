package wireguard

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// generateKeypair produces a Curve25519 private/public keypair in
// WireGuard's base64-encoded form. WireGuard clamps the private key
// per the X25519 spec (RFC 7748 §5) before deriving the public key:
//
//	priv[0]  &= 248
//	priv[31] &= 127
//	priv[31] |=  64
//
// Skipping the clamp produces "valid" public keys that nonetheless
// fail to derive a shared secret with peers — so we do it here
// instead of trusting random bytes alone.
func generateKeypair() (priv, pub string, err error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", "", fmt.Errorf("read random: %w", err)
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64

	pubBytes, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("derive public: %w", err)
	}
	return base64.StdEncoding.EncodeToString(k[:]),
		base64.StdEncoding.EncodeToString(pubBytes),
		nil
}

// generatePSK returns 32 fresh random bytes, base64-encoded —
// WireGuard's preshared-key format. Used as an extra symmetric layer
// on every peer (cheap, opt-in but always-on here).
func generatePSK() (string, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.StdEncoding.EncodeToString(k[:]), nil
}
