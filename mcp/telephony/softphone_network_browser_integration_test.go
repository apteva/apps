//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	bench "github.com/apteva/apps/mcp/telephony/benchmarks/softphone"
	"github.com/pion/webrtc/v4"
	"time"
)

func TestNetworkCheckBrowserNoMicrophoneOrCall(t *testing.T) {
	a, _, _ := turnTestFixture(t)
	defer a.networkChecks.stopAll()
	bundle := filepath.Join(t.TempDir(), "network.js")
	build := exec.CommandContext(t.Context(), "bun", "build", "frontend/tests/network-check-browser-entry.ts", "--target=browser", "--outfile="+bundle)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bundle: %v %s", err, out)
	}
	source, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/module.js" {
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(source)
			return
		}
		if r.URL.Path == "/softphone/network-check" {
			r.URL.RawQuery = "project_id=project-a"
			a.handleNetworkCheck(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<script type="module" src="/module.js"></script>`))
	}))
	defer server.Close()
	command := exec.CommandContext(t.Context(), "bun", "frontend/tests/network-check-browser-run.ts")
	command.Env = append(os.Environ(), "TELEPHONY_NETWORK_BROWSER_ORIGIN="+server.URL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real browser network check: %v\n%s", err, output)
	}
	var parsed map[string]any
	if json.Unmarshal(output, &parsed) != nil {
		t.Fatal(string(output))
	}
	t.Log(string(output))
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.networkChecks.mu.Lock()
		remaining := len(a.networkChecks.sessions)
		a.networkChecks.mu.Unlock()
		if remaining == 0 && a.legacyWork.Load() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("browser left network check resources", remaining, a.legacyWork.Load())
		}
		time.Sleep(time.Millisecond * 10)
	}
	var calls int
	a.db().db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&calls)
	if calls != 0 {
		t.Fatal("readiness check created call", calls)
	}
}

// The diagnostic itself must identify actual impaired datagrams, without a
// microphone, fake getStats counters, real carrier call or host firewall change.
func TestNetworkCheckBrowserDetectsRealDatagramLoss(t *testing.T) {
	a, ctx, platform := turnTestFixture(t)
	defer a.networkChecks.stopAll()
	link := bench.Link{Kbps: 10000, LatencyMS: 20, UDPLossProbability: .2}
	relay, err := bench.NewTURNFixture(link, link, 20261025)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	platform.result = rtcTURNCredentials{ICEServers: []webrtc.ICEServer{{URLs: []string{relay.URL()}, Username: relay.Username, Credential: relay.Password}}, ExpiresAt: time.Now().Add(time.Hour).Unix(), TTLSeconds: 3600}
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app", ICEPolicy: "relay", Transport: "udp"})
	bundle := filepath.Join(t.TempDir(), "network.js")
	build := exec.CommandContext(t.Context(), "bun", "build", "frontend/tests/network-check-browser-entry.ts", "--target=browser", "--outfile="+bundle)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bundle: %v %s", err, out)
	}
	source, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/module.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(source)
		case "/arm":
			relay.Arm(time.Now())
			w.WriteHeader(204)
		case "/softphone/network-check":
			r.URL.RawQuery = "project_id=project-a"
			a.handleNetworkCheck(w, r)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<script type="module" src="/module.js"></script>`))
		}
	}))
	defer server.Close()
	command := exec.CommandContext(t.Context(), "bun", "frontend/tests/network-check-browser-run.ts")
	command.Env = append(os.Environ(), "TELEPHONY_NETWORK_BROWSER_ORIGIN="+server.URL, "TELEPHONY_NETWORK_EXPECTED=degraded-relay")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("impaired readiness check: %v %s", err, output)
	}
	t.Log(string(output))
	stats := relay.Stats()
	if stats["up"].Packets < 100 || stats["down"].Packets < 100 {
		t.Fatal("impairment not exercised", stats)
	}
	var calls int
	if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&calls); err != nil || calls != 0 {
		t.Fatal("probe created call", calls, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for a.legacyWork.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.legacyWork.Load() != 0 {
		t.Fatal("probe leaked runtime work")
	}
}
