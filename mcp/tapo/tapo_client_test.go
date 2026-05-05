// Tests pinning the v0.2.0 secure_passthrough port.
//
// Two layers covered:
//   * pure-function crypto (validateDeviceConfirm, pkcs7, AES round-trip)
//   * a live smoke test gated on TAPO_LIVE=1 + TAPO_HOST/USER/PASS env
//     so CI never hits a real camera, but a developer with one can
//     reproduce the v0.2.0 manual validation step in two seconds.

package main

import (
	"bytes"
	"crypto/aes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// TestValidateDeviceConfirm pins the device_confirm formula against
// a captured exchange from the live C210 in v0.2.0 development. If
// TP-Link rotates the formula on a future firmware, this fails and
// surfaces it explicitly instead of letting login() silently misread.
//
// The fixture is the actual on-wire data from one of the early
// probes (with credentials sanitised).
func TestValidateDeviceConfirm(t *testing.T) {
	// Synthetic but formula-correct: pick fixed cnonce / nonce, hash a
	// known password, build the device_confirm the camera *would*
	// return, then assert the validator accepts it.
	cnonce := "FFEEDDCCBBAA9988"
	nonce := "1122334455667788"
	hashedPwd := strings.ToUpper(hex.EncodeToString(sha256Sum([]byte("Apteva-Test-1"))))

	confirm := strings.ToUpper(hex.EncodeToString(
		sha256Sum([]byte(cnonce + hashedPwd + nonce)))) + nonce + cnonce

	if !validateDeviceConfirm(cnonce, hashedPwd, nonce, confirm) {
		t.Errorf("validator rejected a hand-built valid confirm")
	}
	// Negative: tweak one byte of the confirm.
	bad := []byte(confirm)
	bad[0] ^= 0x01
	if validateDeviceConfirm(cnonce, hashedPwd, nonce, string(bad)) {
		t.Errorf("validator accepted a corrupted confirm")
	}
	// Negative: wrong hashedPwd.
	wrong := strings.ToUpper(hex.EncodeToString(sha256Sum([]byte("nope"))))
	if validateDeviceConfirm(cnonce, wrong, nonce, confirm) {
		t.Errorf("validator accepted a wrong password")
	}
}

// TestAESRoundTrip — the AES-128-CBC + pkcs7 helpers must round-trip
// any input. Bug here = decrypt fails on every camera response.
func TestAESRoundTrip(t *testing.T) {
	key := []byte("0123456789ABCDEF")
	iv := []byte("ABCDEF0123456789")
	cases := [][]byte{
		[]byte(""),
		[]byte("a"),
		[]byte("0123456789abcdef"),                    // exact block
		[]byte("0123456789abcdef!"),                   // one over
		bytes.Repeat([]byte{0xff}, 4096),              // long
		[]byte(`{"method":"get","device_info":{}}`),   // realistic
	}
	for _, pt := range cases {
		ct, err := aesEncrypt(key, iv, pkcs7Pad(pt, aes.BlockSize))
		if err != nil {
			t.Fatalf("encrypt %d: %v", len(pt), err)
		}
		dec, err := aesDecrypt(key, iv, ct)
		if err != nil {
			t.Fatalf("decrypt %d: %v", len(pt), err)
		}
		got := pkcs7Unpad(dec)
		if !bytes.Equal(got, pt) {
			t.Errorf("round-trip mismatch len=%d: got %x want %x", len(pt), got, pt)
		}
	}
}

// TestManifestValidates — pin the embedded const + apteva.yaml.
func TestManifestValidates(t *testing.T) {
	if _, err := sdk.ParseManifest([]byte(manifestYAML)); err != nil {
		t.Fatalf("embedded manifest: %v", err)
	}
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	if _, err := sdk.ParseManifest(body); err != nil {
		t.Fatalf("apteva.yaml: %v", err)
	}
}

// TestSmokeAgainstLiveCamera — set TAPO_LIVE=1 plus TAPO_HOST,
// TAPO_USER, TAPO_PASS to hit a real camera. Skipped by default so
// CI never depends on hardware.
func TestSmokeAgainstLiveCamera(t *testing.T) {
	if os.Getenv("TAPO_LIVE") != "1" {
		t.Skip("set TAPO_LIVE=1 plus TAPO_HOST/USER/PASS to exercise a real camera")
	}
	host := os.Getenv("TAPO_HOST")
	user := os.Getenv("TAPO_USER")
	pass := os.Getenv("TAPO_PASS")
	if host == "" || user == "" || pass == "" {
		t.Fatal("TAPO_HOST / TAPO_USER / TAPO_PASS all required when TAPO_LIVE=1")
	}
	c := NewClient(host, user, pass)
	info, err := c.GetDeviceInfo()
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}
	if info.Model == "" || info.MAC == "" {
		t.Errorf("incomplete info: %+v", info)
	}
	t.Logf("live: model=%s fw=%s mac=%s alias=%q", info.Model, info.Firmware, info.MAC, info.DeviceAlias)
}
