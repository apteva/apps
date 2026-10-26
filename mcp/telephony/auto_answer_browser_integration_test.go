//go:build integration

package main

import (
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
	for _, delay := range []int{0, 300} {
		t.Run(strings.ReplaceAll(time.Duration(delay*int(time.Millisecond)).String(), ".", "_"), func(t *testing.T) {
			a := autoAnswerTestSetup(t)
			a.installID = 42
			phoneTestRingOfferCall(t, a, "auto-browser")
			autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "automatic", "delay_ms": delay}})
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
					if wsutil.WriteClientMessage(peer, op, data) != nil {
						return
					}
				}
			}()
			defer func() { peer.Close(); <-done }()

			run := exec.CommandContext(t.Context(), "bun", "frontend/tests/auto-answer-browser-run.ts")
			run.Env = append(os.Environ(), "TELEPHONY_AUTO_ANSWER_ORIGIN="+server.URL)
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
			if answers.Load() != 1 || result.State.AudioState != "live" {
				t.Fatalf("answers %d result %s", answers.Load(), out)
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
