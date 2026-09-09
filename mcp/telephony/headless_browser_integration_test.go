//go:build integration

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws/wsutil"
)

// Chromium executes the actual app-served SDK bundle, AudioWorklet and Worker.
// Only the platform gateway and carrier are fixtures; HTTP/WS and Telephony are real.
func TestTier2HeadlessBrowser(t *testing.T) {
	for _, surface := range []string{"headless", "panel", "application-user"} {
		t.Run(surface, func(t *testing.T) { runHeadlessBrowser(t, surface) })
	}
}

func runHeadlessBrowser(t *testing.T, surface string) {
	platform := newTier2PlatformGateway(t)
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(tier2Project), tk.WithEnv("APTEVA_GATEWAY_URL", platform.server.URL))
	created := tier2MCPAs(t, sc, "telephony_routes_create", map[string]any{"phone_number": tier2Number, "answer_mode": "human_browser"})
	route := created["route"].(map[string]any)
	tier2MCPAs(t, sc, "telephony_routes_configure_carrier", map[string]any{"route_id": route["id"]})
	var identityServer *httptest.Server
	if surface == "application-user" {
		identityServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer headless-browser-fixture" {
				http.Error(w, "invalid login", 401)
				return
			}
			writeTier2JSON(w, map[string]any{"user": map[string]any{"id": "browser-user", "organization_id": "test-org", "project_id": tier2Project}})
		}))
		defer identityServer.Close()
		// Route this number to a browser destination assigned to the user.
		request := func(method, path string, body any) map[string]any {
			data, _ := json.Marshal(body)
			req, _ := http.NewRequest(method, sc.URL()+path, bytes.NewReader(data))
			req.Header.Set("Authorization", "Bearer "+sc.Token())
			req.Header.Set("X-Apteva-Project-ID", tier2Project)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var result map[string]any
			if resp.StatusCode != 200 {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("fixture %s: %d %s", path, resp.StatusCode, raw)
			}
			if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			return result
		}
		destination := request("POST", "/routing/destinations/save", map[string]any{"name": "User desk", "kind": "browser", "config": map[string]any{}, "enabled": true})["id"].(string)
		flow := request("POST", "/routing/flows/save", map[string]any{"name": "User routing", "draft": map[string]any{"entry": "desk", "nodes": []any{map[string]any{"id": "desk", "type": "destination", "config": map[string]any{"destination_id": destination}}}}})
		request("POST", "/routing/flows/publish", map[string]any{"id": flow["id"]})
		request("POST", "/routing/routes/assign", map[string]any{"flow_id": flow["id"], "route_id": route["id"]})
		request("PUT", "/access/policy", phonePolicy{
			Users:     []phoneUser{{Identity: phoneIdentity{"auth", "11", "user", "browser-user", "test-org"}, Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{destination}}}},
			Providers: []phoneAuthProvider{{ID: "browser-login", IssuerApp: "auth", IssuerInstallID: "11", URL: identityServer.URL, Format: "apteva-auth", Actions: []string{"call.read", "call.answer", "call.attach", "call.hangup"}}},
		})
	}

	incoming, _ := json.Marshal(map[string]any{"data": map[string]any{
		"id": "headless-incoming", "event_type": "call.initiated", "occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
		"payload": map[string]any{"call_control_id": "call-control-test-1", "connection_id": "application-test-1", "direction": "incoming", "from": tier2Caller, "to": tier2Number},
	}})
	resp := tier2SignedPOST(t, platform, localSidecarURL(t, sc, created["inbound_url"].(string)), incoming)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("incoming status: %d", resp.StatusCode)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	hostOrigin := fmt.Sprintf("http://127.0.0.1:%d", hostPort)
	target, _ := url.Parse(sc.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		r.Host = target.Host
		if !strings.HasPrefix(r.URL.Path, "/user/") {
			r.Header.Set("Authorization", "Bearer "+sc.Token())
		}
		r.Header.Set("X-User-ID", "1")
		r.Header.Set("X-Apteva-Project-ID", tier2Project)
	}
	var audioVerified atomic.Bool
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", hostOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.URL.Path == "/fixture/audio-ready" {
			writeTier2JSON(w, map[string]bool{"ready": audioVerified.Load()})
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/apps/telephony")
		if path == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		media := strings.HasPrefix(path, "/_install/42/softphone/media/")
		if !media && (r.Header.Get("Authorization") != "Bearer headless-browser-fixture" || r.URL.Query().Get("project_id") != tier2Project || r.URL.Query().Get("install_id") != "42") {
			http.Error(w, "invalid fixture auth/scope", 403)
			return
		}
		if media {
			path = strings.TrimPrefix(path, "/_install/42")
		}
		if surface == "application-user" && !media && !strings.HasPrefix(path, "/user/") && !strings.HasPrefix(path, "/ui/frontend") {
			http.Error(w, "operator API unavailable to application browser", 403)
			return
		}
		r.URL.Path = path
		proxy.ServeHTTP(w, r)
	}))
	defer public.Close()
	if surface != "application-user" {
		script := exec.CommandContext(t.Context(), "bun", "frontend/tests/script-client.ts")
		script.Env = append(os.Environ(), "TELEPHONY_TEST_GATEWAY="+public.URL)
		if output, err := script.CombinedOutput(); err != nil {
			t.Fatalf("script-only client: %v\n%s", err, output)
		}

	}

	// A deterministic microphone file provides measurable browser capture audio.
	pcm := pcm16ToBytes(sinePCM(24000, 900, 24000*3))
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	binary.Write(&wav, binary.LittleEndian, uint32(36+len(pcm)))
	wav.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(24000), uint32(48000), uint16(2), uint16(16)} {
		binary.Write(&wav, binary.LittleEndian, v)
	}
	wav.WriteString("data")
	binary.Write(&wav, binary.LittleEndian, uint32(len(pcm)))
	wav.Write(pcm)
	micFile := filepath.Join(t.TempDir(), "microphone.wav")
	if err := os.WriteFile(micFile, wav.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bunx", "--no-install", "playwright", "test", "-c", "frontend/playwright.config.ts")
	cmd.Env = append(os.Environ(), "TELEPHONY_TEST_GATEWAY="+public.URL, fmt.Sprintf("TELEPHONY_HOST_PORT=%d", hostPort), "TELEPHONY_MIC_WAV="+micFile, "TELEPHONY_TEST_SURFACE="+surface)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	var answer tier2CarrierCall
	deadline := time.After(35 * time.Second)
waitAnswer:
	for {
		select {
		case call := <-platform.carrierCalls:
			if call.Tool == "answer_call" {
				answer = call
				break waitAnswer
			}
		case err := <-finished:
			t.Fatalf("browser ended before answering: %v\n%s", err, output.String())
		case <-deadline:
			t.Fatal("browser did not answer in time")
		}
	}
	carrier := dialTier2WS(t, rawSidecarMediaURL(t, sc, answer.Input["stream_url"].(string)))
	start, _ := json.Marshal(map[string]any{"event": "start", "stream_id": "headless-stream", "start": map[string]any{"call_control_id": "call-control-test-1", "stream_id": "headless-stream"}})
	if err := wsutil.WriteClientText(carrier, start); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		payload := base64.StdEncoding.EncodeToString(pcm16ToBytes(sinePCM(16000, 440, 320)))
		frame, _ := json.Marshal(map[string]any{"event": "media", "media": map[string]string{"payload": payload}})
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if wsutil.WriteClientText(carrier, frame) != nil {
					return
				}
			}
		}
	}()
	// Require non-silent audio that traversed Chromium capture and the carrier bridge.
	micDeadline := time.Now().Add(12 * time.Second)
	for {
		audio := readTier2CarrierAudio(t, carrier, 12*time.Second)
		if rmsPCM(audio) > 500 {
			audioVerified.Store(true)
			break
		}
		if time.Now().After(micDeadline) {
			t.Fatal("browser microphone did not reach carrier")
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("headless browser: %v\n%s", err, output.String())
		}
		t.Log(output.String())
	case <-time.After(45 * time.Second):
		t.Fatal("headless browser did not finish")
	}
}
