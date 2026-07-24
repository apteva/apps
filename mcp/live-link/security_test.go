package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerKeepsCredentialsOutOfArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	for _, tc := range []struct {
		name       string
		params     StartParams
		secret     string
		secretFile string
		otherFile  string
	}{
		{
			name:       "cloudflare named",
			params:     StartParams{Mode: ModeNamed, Token: "named-secret-value", Hostname: "share.example.com", RunID: 1},
			secret:     "named-secret-value",
			secretFile: "tunnel.env",
			otherFile:  "ngrok.env",
		},
		{
			name:       "ngrok reserved domain",
			params:     StartParams{Mode: ModeNgrok, Target: "http://127.0.0.1:5280", Authtoken: "ngrok-secret-value", Hostname: "share.example.com", RunID: 2},
			secret:     "ngrok-secret-value",
			secretFile: "ngrok.env",
			otherFile:  "tunnel.env",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argsPath := filepath.Join(dir, "args")
			tunnelPath := filepath.Join(dir, "tunnel.env")
			ngrokPath := filepath.Join(dir, "ngrok.env")
			binary := filepath.Join(dir, "capture-agent")
			script := `#!/bin/sh
printf '%s\n' "$@" > "$CAPTURE_ARGS"
printf '%s' "${TUNNEL_TOKEN-}" > "$CAPTURE_TUNNEL"
printf '%s' "${NGROK_AUTHTOKEN-}" > "$CAPTURE_NGROK"
sleep 30
`
			if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CAPTURE_ARGS", argsPath)
			t.Setenv("CAPTURE_TUNNEL", tunnelPath)
			t.Setenv("CAPTURE_NGROK", ngrokPath)
			// A credential for the inactive provider must not leak through the
			// inherited environment.
			t.Setenv("TUNNEL_TOKEN", "stale-tunnel-secret")
			t.Setenv("NGROK_AUTHTOKEN", "stale-ngrok-secret")

			tc.params.Binary = binary
			mgr := NewManager(nil, nil)
			if err := mgr.Start(tc.params); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mgr.Stop() })
			waitForFile(t, argsPath)

			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(args), tc.secret) || strings.Contains(string(args), "--token") {
				t.Fatalf("credential leaked into argv: %q", args)
			}
			if tc.params.Mode == ModeNgrok {
				if strings.Contains(string(args), "--domain") || !strings.Contains(string(args), "--url\nhttps://share.example.com") {
					t.Fatalf("ngrok reserved-domain arguments are wrong: %q", args)
				}
			}
			secretValue, err := os.ReadFile(filepath.Join(dir, tc.secretFile))
			if err != nil {
				t.Fatal(err)
			}
			if string(secretValue) != tc.secret {
				t.Fatalf("credential env=%q, want selected provider credential", secretValue)
			}
			otherValue, err := os.ReadFile(filepath.Join(dir, tc.otherFile))
			if err != nil {
				t.Fatal(err)
			}
			if len(otherValue) != 0 {
				t.Fatalf("inactive provider credential was inherited: %q", otherValue)
			}
		})
	}
}

func TestZrokManagerUsesIsolatedHomeWithoutToken(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	envPath := filepath.Join(dir, "env")
	binary := filepath.Join(dir, "capture-zrok")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$CAPTURE_ARGS"
printf 'HOME=%s\nZROK2_HEADLESS=%s\nZROK2_DEFAULT_NAMESPACE=%s\nZROK2_API_ENDPOINT=%s\n' "$HOME" "${ZROK2_HEADLESS-}" "${ZROK2_DEFAULT_NAMESPACE-}" "${ZROK2_API_ENDPOINT-}" > "$CAPTURE_ENV"
sleep 30
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_ARGS", argsPath)
	t.Setenv("CAPTURE_ENV", envPath)
	t.Setenv("ZROK2_API_ENDPOINT", "https://stale.invalid")
	home := filepath.Join(dir, "isolated-home")
	mgr := NewManager(nil, nil)
	if err := mgr.Start(StartParams{
		Binary: binary, Target: "http://127.0.0.1:5280", Mode: ModeZrok, RunID: 9,
		Hostname: "https://safe-name.share.zrok.io", ZrokName: "safe-name",
		ZrokNamespace: "public", ZrokHome: home,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })
	waitForFile(t, argsPath)
	waitForFile(t, envPath)

	args, _ := os.ReadFile(argsPath)
	if got := string(args); got != "share\npublic\nhttp://127.0.0.1:5280\n--headless\n--force-local\n--name-selection\npublic:safe-name\n" {
		t.Fatalf("argv=%q", got)
	}
	env, _ := os.ReadFile(envPath)
	gotEnv := string(env)
	if !strings.Contains(gotEnv, "HOME="+home+"\n") ||
		!strings.Contains(gotEnv, "ZROK2_HEADLESS=true\n") ||
		!strings.Contains(gotEnv, "ZROK2_DEFAULT_NAMESPACE=public\n") ||
		!strings.Contains(gotEnv, "ZROK2_API_ENDPOINT=\n") {
		t.Fatalf("unexpected zrok environment: %q", gotEnv)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestStableTunnelNameDoesNotCollideOnTruncatedPrefix(t *testing.T) {
	a := stableTunnelName(nil, "abcdefgh-one.example.com")
	b := stableTunnelName(nil, "abcdefgh-two.example.com")
	if a == b {
		t.Fatalf("distinct hostnames produced the same tunnel name %q", a)
	}
	if len(a) > 32 || len(b) > 32 {
		t.Fatalf("tunnel name unexpectedly long: %q / %q", a, b)
	}
}

func TestHostnameAndTargetValidation(t *testing.T) {
	if err := validateHostnameInZone("Share.Example.com.", "example.com"); err != nil {
		t.Fatalf("valid subdomain rejected: %v", err)
	}
	for _, host := range []string{"example.net", "fakeexample.com", "bad_label.example.com", "-bad.example.com"} {
		if err := validateHostnameInZone(host, "example.com"); err == nil {
			t.Errorf("invalid/out-of-zone hostname %q accepted", host)
		}
	}
	for _, target := range []string{"file:///etc/passwd", "http://user:pass@localhost", "localhost:5280", "javascript:alert(1)"} {
		if err := validateTargetURL(target); err == nil {
			t.Errorf("unsafe target %q accepted", target)
		}
	}
}

func TestVerifiedDownloadEnforcesDigestAndBounds(t *testing.T) {
	body := []byte("verified-binary")
	sum := sha256.Sum256(body)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://localhost:1234/downgrade", http.StatusFound)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = oldTransport }()

	artifact := binaryArtifact{
		URL: srv.URL, SHA256: fmt.Sprintf("%x", sum), Version: "test",
		MaxDownload: 64, AllowedHosts: map[string]bool{"127.0.0.1": true},
	}
	dest := filepath.Join(t.TempDir(), "download")
	if err := downloadVerified(artifact, dest); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != string(body) {
		t.Fatalf("downloaded body=%q err=%v", got, err)
	}

	artifact.SHA256 = strings.Repeat("0", 64)
	if err := downloadVerified(artifact, dest); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum mismatch error=%v", err)
	}
	artifact.SHA256 = fmt.Sprintf("%x", sum)
	artifact.MaxDownload = 4
	if err := downloadVerified(artifact, dest); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized download error=%v", err)
	}
	artifact.MaxDownload = 64
	artifact.URL = srv.URL + "/redirect"
	if err := downloadVerified(artifact, dest); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("HTTP redirect downgrade error=%v", err)
	}
}

func TestArtifactValidationRejectsMalformedPins(t *testing.T) {
	base := binaryArtifact{
		URL: "https://downloads.example.com/tool", SHA256: strings.Repeat("a", 64),
		MaxDownload: 1, AllowedHosts: map[string]bool{"downloads.example.com": true},
	}
	for name, mutate := range map[string]func(*binaryArtifact){
		"http":     func(a *binaryArtifact) { a.URL = "http://downloads.example.com/tool" },
		"host":     func(a *binaryArtifact) { a.URL = "https://evil.example/tool" },
		"checksum": func(a *binaryArtifact) { a.SHA256 = strings.Repeat("z", 64) },
		"limit":    func(a *binaryArtifact) { a.MaxDownload = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			a := base
			mutate(&a)
			if err := validateArtifactURL(a); err == nil {
				t.Fatal("invalid artifact accepted")
			}
		})
	}
}
