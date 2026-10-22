//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
)

// Real Chromium runs the shared softphone, microphone worklet and ICE controller
// against Telephony's actual media endpoint. All services are local fixtures.
func TestTURNBrowserMediaCredentialRenewal(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "turn-browser.js")
	build := exec.CommandContext(t.Context(), "bun", "build", "./frontend/tests/turn-browser-entry.ts", "--target=browser", "--outfile="+bundle)
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build browser test: %v\n%s", e, out)
	}
	source, e := os.ReadFile(bundle)
	if e != nil {
		t.Fatal(e)
	}
	worklet, e := os.ReadFile("ui/softphone-worklet.js")
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"udp", "tcp"} {
		t.Run(mode, func(t *testing.T) {
			endpoint, _ := localTURNTestRelay(t, mode)
			app, ctx, platform := turnTestFixture(t)
			row := insertSoftphoneCall(t, app, "in-progress")
			platform.response = func(sequence int64) rtcTURNCredentials {
				username := "temporary-v1"
				if sequence > 1 {
					username = "temporary-v2"
				}
				return rtcTURNCredentials{ICEServers: []webrtc.ICEServer{{URLs: []string{endpoint}, Username: username, Credential: "local-test-password"}}, ExpiresAt: time.Now().Add(time.Hour).Unix(), TTLSeconds: 3600}
			}
			saveTURNTestPolicy(t, app, ctx, nil, phoneTURNPolicy{Source: "app", Transport: mode, ICEPolicy: "relay"})
			server := softphoneTestServer(t, app)
			carrier := dialWS(t, server.URL+"/peer/"+row.ID+"/cb-secret")
			done := make(chan struct{})
			var workers sync.WaitGroup
			workers.Add(2)
			go func() {
				defer workers.Done()
				for {
					if _, _, e := wsutil.ReadServerData(carrier); e != nil {
						return
					}
				}
			}()
			go func() {
				defer workers.Done()
				timer := time.NewTicker(20 * time.Millisecond)
				defer timer.Stop()
				for {
					select {
					case <-done:
						return
					case <-timer.C:
						carrier.SetWriteDeadline(time.Now().Add(time.Second))
						if wsutil.WriteClientBinary(carrier, make([]byte, 960)) != nil {
							return
						}
					}
				}
			}()
			defer func() { close(done); carrier.Close(); workers.Wait() }()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/config":
					writeJSON(w, map[string]any{"media_url": "ws" + strings.TrimPrefix(server.URL, "http") + "/softphone/media/" + row.ID + "/peer-secret?transport=webrtc"})
				case "/module.js":
					w.Header().Set("Content-Type", "text/javascript")
					w.Write(source)
				case "/worklet.js":
					w.Header().Set("Content-Type", "text/javascript")
					w.Write(worklet)
				default:
					w.Header().Set("Content-Type", "text/html")
					w.Write([]byte(`<script type="module" src="/module.js"></script>`))
				}
			}))
			defer origin.Close()
			command := exec.CommandContext(t.Context(), "bun", "frontend/tests/turn-browser-run.ts")
			command.Env = append(os.Environ(), "TELEPHONY_TURN_BROWSER_ORIGIN="+origin.URL)
			out, e := command.CombinedOutput()
			if e != nil {
				t.Fatalf("browser managed TURN: %v\n%s", e, out)
			}
			var result struct {
				PeerPreserved bool `json:"peer_preserved"`
				MutePreserved bool `json:"mute_preserved"`
				Path          struct {
					CandidateType string `json:"candidateType"`
					RelayProtocol string `json:"relayProtocol"`
				} `json:"path"`
			}
			if json.Unmarshal(out, &result) != nil || !result.PeerPreserved || !result.MutePreserved || result.Path.CandidateType != "relay" || result.Path.RelayProtocol != mode {
				t.Fatalf("unexpected relay evidence: %s", out)
			}
			if platform.calls.Load() != 2 {
				t.Fatal("unexpected credential requests", platform.calls.Load())
			}
			t.Logf("Chromium TURN/%s kept its peer, sender and mute across credential rotation", mode)
		})
	}
}
