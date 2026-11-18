//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
)

// Real Chromium opens a ring-time connection, directly and through a managed
// TURN relay; the claim then attaches its microphone to that same connection
// and audio flows both ways. All services are local fixtures.
func TestStandbyRealBrowserPickup(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "standby-browser.js")
	build := exec.CommandContext(t.Context(), "bun", "build", "./frontend/tests/standby-browser-entry.ts", "--target=browser", "--outfile="+bundle)
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
	for _, mode := range []string{"direct", "relay"} {
		t.Run(mode, func(t *testing.T) {
			app, ctx, platform := turnTestFixture(t)
			t.Setenv("APTEVA_PUBLIC_URL", "https://example.test")
			if mode == "relay" {
				endpoint, _ := localTURNTestRelay(t, "udp")
				platform.response = func(int64) rtcTURNCredentials {
					return rtcTURNCredentials{ICEServers: []webrtc.ICEServer{{URLs: []string{endpoint}, Username: "temporary-v1", Credential: "local-test-password"}}, ExpiresAt: time.Now().Add(time.Hour).Unix(), TTLSeconds: 3600}
				}
				saveTURNTestPolicy(t, app, ctx, nil, phoneTURNPolicy{Source: "app", Transport: "udp", ICEPolicy: "relay"})
			}
			row := insertPendingTelnyxCall(t, app, "standby-browser-"+mode)
			grant := operatorStandby(t, app, row.ID)
			server := softphoneTestServer(t, app)
			lines := make(chan map[string]any, 2)
			pickupTimingHook = func(fields []any) {
				line := map[string]any{}
				for i := 0; i+1 < len(fields); i += 2 {
					line[fields[i].(string)] = fields[i+1]
				}
				lines <- line
			}
			t.Cleanup(func() { pickupTimingHook = nil })

			parked := make(chan struct{}, 1)
			var claim atomic.Pointer[string]
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/config":
					writeJSON(w, map[string]any{"standby_url": "ws" + strings.TrimPrefix(server.URL, "http") + "/softphone/media/" + row.ID + "/" + grant.token()})
				case "/parked":
					select {
					case parked <- struct{}{}:
					default:
					}
				case "/claim":
					if url := claim.Load(); url != nil {
						writeJSON(w, map[string]any{"ready": true, "media_url": *url})
						return
					}
					writeJSON(w, map[string]any{"ready": false})
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
			command := exec.CommandContext(t.Context(), "bun", "frontend/tests/standby-browser-run.ts")
			command.Env = append(os.Environ(), "TELEPHONY_STANDBY_BROWSER_ORIGIN="+origin.URL)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if e := command.Start(); e != nil {
				t.Fatal(e)
			}
			finished := make(chan error, 1)
			go func() { finished <- command.Wait() }()
			select {
			case <-parked:
			case e := <-finished:
				t.Fatalf("browser ended before parking: %v\n%s", e, output.String())
			case <-time.After(30 * time.Second):
				t.Fatalf("browser never parked\n%s", output.String())
			}
			waitStandbyState(t, standbyByID(app, grant.StandbyID), standbyParked)
			session := operatorAnswer(t, app, row.ID, `{"standby_id":"`+grant.StandbyID+`"}`)
			if session.Standby != "promoted" {
				t.Fatalf("session %+v", session)
			}
			// The carrier's media leg, as the carrier opens it after the answer.
			carrier := dialWS(t, server.URL+"/peer/"+row.ID+"/callback-secret")
			var microphone atomic.Int64
			done := make(chan struct{})
			var workers sync.WaitGroup
			workers.Add(2)
			go func() {
				defer workers.Done()
				for {
					data, op, e := wsutil.ReadServerData(carrier)
					if e != nil {
						return
					}
					if op == ws.OpBinary && len(data) > 0 {
						microphone.Add(1)
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
						if wsutil.WriteClientBinary(carrier, pcm16ToBytes(sinePCM(24000, 440, 480))) != nil {
							return
						}
					}
				}
			}()
			defer func() { close(done); carrier.Close(); workers.Wait() }()
			url := "ws" + strings.TrimPrefix(server.URL, "http") + "/softphone/media/" + row.ID + "/" + session.SessionToken
			claim.Store(&url)
			select {
			case e := <-finished:
				if e != nil {
					t.Fatalf("browser: %v\n%s", e, output.String())
				}
			case <-time.After(60 * time.Second):
				t.Fatalf("browser did not finish\n%s", output.String())
			}
			out := output.Bytes()
			if i := bytes.LastIndexByte(bytes.TrimSpace(out), '\n'); i >= 0 {
				out = out[i+1:]
			}
			var result struct {
				Peers       int     `json:"peers"`
				StandbyMS   float64 `json:"standby_ms"`
				PromoteMS   float64 `json:"promote_ms"`
				Sent        int     `json:"sent"`
				WaitingSent int     `json:"waiting_sent"`
				Received    int     `json:"received"`
				Reconnects  int     `json:"reconnects"`
				Path        struct {
					CandidateType string `json:"candidateType"`
				} `json:"path"`
			}
			if e := json.Unmarshal(out, &result); e != nil {
				t.Fatalf("result %v: %s", e, output.String())
			}
			if result.Peers != 1 || result.Reconnects != 0 || result.Received <= 10 || result.Sent <= result.WaitingSent+10 {
				t.Fatalf("unexpected browser result: %s", out)
			}
			if mode == "relay" && result.Path.CandidateType != "relay" || mode == "direct" && result.Path.CandidateType == "relay" {
				t.Fatalf("unexpected path: %s", out)
			}
			deadline := time.Now().Add(5 * time.Second)
			for microphone.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if microphone.Load() == 0 {
				t.Fatal("no microphone audio reached the carrier")
			}
			var line map[string]any
			select {
			case line = <-lines:
			case <-time.After(2 * time.Second):
				t.Fatal("no timing line")
			}
			if line["standby"] != true || line["transport"] != "webrtc" {
				t.Fatalf("timing line %v", line)
			}
			t.Logf("Chromium %s: standby %.0f ms during the ring, then %.0f ms from start() to ready; server timing %v; microphone frames at carrier %d; browser %s", mode, result.StandbyMS, result.PromoteMS, line, microphone.Load(), out)
		})
	}
}
