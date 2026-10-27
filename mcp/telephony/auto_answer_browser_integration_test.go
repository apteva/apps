//go:build integration

package main

import (
	"encoding/binary"
	"encoding/json"
	"github.com/gobwas/ws/wsutil"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutoAnswerRealBrowserPickup(t *testing.T) {
	for _, scenario := range []struct {
		delay   int
		warning string
	}{{0, ""}, {300, ""}, {0, "ok"}, {0, "cancel"}, {0, "blocked"}} {
		delay := scenario.delay
		t.Run(strings.ReplaceAll(time.Duration(delay*int(time.Millisecond)).String()+"-"+scenario.warning, ".", "_"), func(t *testing.T) {
			a := autoAnswerTestSetup(t)
			a.installID = 42
			phoneTestRingOfferCall(t, a, "auto-browser")
			autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "automatic", "delay_ms": delay, "warning_tone": map[string]any{"enabled": scenario.warning != "", "window_ms": 1000}}})
			t.Setenv("APTEVA_APP_TOKEN", "")
			t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
			bundle := filepath.Join(t.TempDir(), "client.js")
			build := exec.CommandContext(t.Context(), "bun", "build", "frontend/tests/auto-answer-browser-entry.ts", "--target=browser", "--outfile="+bundle)
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("bundle %v %s", err, out)
			}
			source, err := os.ReadFile(bundle)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			for _, route := range a.HTTPRoutes() {
				mux.HandleFunc(route.Pattern, route.Handler)
			}
			var answers atomic.Int32
			var microphoneFrames, nonSilentSamples atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					w.Header().Set("Content-Type", "text/html")
					w.Write([]byte(`<button>Available</button><script type="module" src="/client.js"></script>`))
					return
				case "/client.js":
					w.Header().Set("Content-Type", "text/javascript")
					w.Write(source)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/apps/telephony/_install/42/ui/frontend/") {
					http.ServeFile(w, r, filepath.Join("ui/frontend", filepath.Base(r.URL.Path)))
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/ui/frontend/") {
					http.ServeFile(w, r, filepath.Join("ui/frontend", filepath.Base(r.URL.Path)))
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/apps/telephony/_install/42/softphone/") {
					r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/telephony/_install/42")
					r.Header.Set("X-Apteva-Project-ID", "project-a")
					mux.ServeHTTP(w, r)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/") {
					r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
					r.Header.Set("X-Apteva-Project-ID", "project-a")
					if strings.HasPrefix(r.URL.Path, "/softphone/answer/") {
						answers.Add(1)
						record := httptest.NewRecorder()
						mux.ServeHTTP(record, r)
						if record.Code == 200 { // A local fake carrier confirms answer, without any external call.
							if _, err := a.db().db.Exec(`UPDATE calls SET status='in-progress',answered_at=? WHERE id='auto-browser'`, ringTime(time.Now())); err != nil {
								t.Error(err)
							}
						}
						for k, v := range record.Header() {
							w.Header()[k] = v
						}
						w.WriteHeader(record.Code)
						w.Write(record.Body.Bytes())
						return
					}
				}
				mux.ServeHTTP(w, r)
			}))
			defer server.Close()
			t.Setenv("APTEVA_PUBLIC_URL", server.URL)
			t.Setenv("APTEVA_GATEWAY_URL", server.URL)
			peer := dialWS(t, server.URL+"/peer/auto-browser/cb-auto-browser")
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					data, op, err := wsutil.ReadServerData(peer)
					if err != nil {
						return
					}
					if scenario.warning == "ok" && op == 2 {
						microphoneFrames.Add(1)
						for i := 0; i+1 < len(data); i += 2 {
							sample := int16(binary.LittleEndian.Uint16(data[i:]))
							if sample > 2 || sample < -2 {
								nonSilentSamples.Add(1)
							}
						}
					}
					if wsutil.WriteClientMessage(peer, op, data) != nil {
						return
					}
				}
			}()
			defer func() { peer.Close(); <-done }()

			run := exec.CommandContext(t.Context(), "bun", "frontend/tests/auto-answer-browser-run.ts")
			// A silent capture fixture makes any leaked warning tone observable at the caller peer.
			wav := make([]byte, 44+48000*2*3)
			copy(wav, "RIFF")
			binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
			copy(wav[8:], "WAVEfmt ")
			binary.LittleEndian.PutUint32(wav[16:], 16)
			binary.LittleEndian.PutUint16(wav[20:], 1)
			binary.LittleEndian.PutUint16(wav[22:], 1)
			binary.LittleEndian.PutUint32(wav[24:], 48000)
			binary.LittleEndian.PutUint32(wav[28:], 96000)
			binary.LittleEndian.PutUint16(wav[32:], 2)
			binary.LittleEndian.PutUint16(wav[34:], 16)
			copy(wav[36:], "data")
			binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))
			capture := filepath.Join(t.TempDir(), "silence.wav")
			if err := os.WriteFile(capture, wav, 0600); err != nil {
				t.Fatal(err)
			}
			run.Env = append(os.Environ(), "TELEPHONY_WARNING_CAPTURE="+capture, "TELEPHONY_AUTO_ANSWER_ORIGIN="+server.URL, "TELEPHONY_AUTO_WARNING_MODE="+scenario.warning)
			out, err := run.CombinedOutput()
			if err != nil {
				t.Fatalf("browser %v\n%s", err, out)
			}
			var result struct {
				State struct {
					AudioState string `json:"audioState"`
				}
				States []struct {
					State string `json:"state"`
				}
			}
			if err = json.Unmarshal(out, &result); err != nil {
				t.Fatalf("result %v %s", err, out)
			}
			if scenario.warning == "cancel" || scenario.warning == "blocked" {
				if answers.Load() != 0 {
					t.Fatalf("canceled/failed warning answered %d", answers.Load())
				}
				t.Logf("warning=%s answers=0", scenario.warning)
				return
			}
			if answers.Load() != 1 || result.State.AudioState != "live" {
				t.Fatalf("answers %d result %s", answers.Load(), out)
			}
			if scenario.warning == "ok" && (microphoneFrames.Load() == 0 || nonSilentSamples.Load() != 0) {
				t.Fatalf("warning contaminated caller audio: frames=%d non-silent=%d", microphoneFrames.Load(), nonSilentSamples.Load())
			}
			if scenario.warning == "ok" {
				t.Logf("warning precedes connection; caller PCM frames=%d, non-silent samples=%d", microphoneFrames.Load(), nonSilentSamples.Load())
			}
			countdown := false
			for _, s := range result.States {
				countdown = countdown || s.State == "countdown"
			}
			if countdown != (delay > 0) {
				t.Fatal("countdown does not match delay")
			}
			t.Logf("real browser pickup delay=%d answers=%d audio=%s", delay, answers.Load(), result.State.AudioState)
		})
	}
}
