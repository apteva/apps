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
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws/wsutil"
)

// Exercise the real default frontend across distinct gateway/media origins and
// the backend grant registry. A network probe alone never tests call admission.
func TestManagedRuntimeAutomaticBrowserAnswerAndRenewal(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42, runtime: runtimeTestActive()}
	insertSoftphoneCall(t, a, "in-progress")
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	bundle := filepath.Join(t.TempDir(), "client.js")
	build := exec.CommandContext(t.Context(), "bun", "build", "frontend/tests/runtime-media-browser-entry.ts", "--target=browser", "--outfile="+bundle)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bundle: %v %s", err, out)
	}
	source, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var issues, answers, renewals atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(runtimeBrowserPrefix+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/answer/") {
			answers.Add(1)
		}
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/renew/") {
			renewals.Add(1)
		}
		a.handleRuntimeBrowser(w, r)
	})
	mux.HandleFunc("/peer/", a.handlePeerSocket)
	media := httptest.NewServer(mux)
	defer media.Close()
	a.runtime.endpoints["browser"] = media.URL
	var origin string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/module.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(source)
		case "/":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<script type="module" src="/module.js"></script>`))
		case "/api/runtime-endpoints":
			writeJSON(w, map[string]any{"managed": true, "browser_api_base": media.URL + runtimeBrowserPrefix})
		case "/api/runtime-browser-bootstrap":
			token := "grant-one"
			if issues.Add(1) > 1 {
				token = "grant-two"
			}
			writeJSON(w, map[string]any{"token": token, "expires_at": time.Now().Add(5 * time.Minute).Unix(), "endpoint": media.URL + runtimeBrowserPrefix})
		case "/api/softphone/answer/call-soft-1":
			// The previous frontend used this valid proxy route, which cannot register a direct grant.
			a.softphoneAnswer(w, r, "project-a", "call-soft-1")
		case "/api/calls":
			writeJSON(w, map[string]any{"calls": []map[string]string{{"id": "call-soft-1", "status": "in-progress"}}})
		case "/api/apps/callback/runtime/browser-origin":
			writeJSON(w, map[string]bool{"allowed": true})
		case "/api/apps/callback/runtime/browser-authorize":
			var body struct{ Token, Origin, Operation string }
			json.NewDecoder(r.Body).Decode(&body)
			want := "grant-one"
			if issues.Load() > 1 {
				want = "grant-two"
			}
			if body.Token != want || body.Origin != origin {
				http.Error(w, "denied", 403)
				return
			}
			writeJSON(w, runtimeBrowserGrant{InstallID: 42, Project: "project-a", UserID: 9, Deployment: "deployment-a", Generation: 1, Origin: origin, Expires: time.Now().Add(5 * time.Minute).Unix(), Principal: &sdk.TrustedPrincipal{Version: 1, UserID: 9, ProjectID: "project-a", ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}})
		default:
			http.Error(w, "unexpected gateway request", 400)
		}
	}))
	defer gateway.Close()
	origin = gateway.URL
	t.Setenv("APTEVA_GATEWAY_URL", origin)
	t.Setenv("APTEVA_PUBLIC_URL", origin)
	peer := dialWS(t, media.URL+"/peer/call-soft-1/cb-secret")
	defer peer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			data, op, err := wsutil.ReadServerData(peer)
			if err != nil {
				return
			}
			if err = wsutil.WriteClientMessage(peer, op, data); err != nil {
				return
			}
		}
	}()
	defer func() { peer.Close(); <-done }()
	run := exec.CommandContext(t.Context(), "bun", "frontend/tests/runtime-media-browser-run.ts")
	run.Env = append(os.Environ(), "TELEPHONY_RUNTIME_BROWSER_ORIGIN="+origin)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("managed browser: %v\n%s", err, out)
	}
	if answers.Load() != 1 || renewals.Load() != 1 || issues.Load() != 2 {
		t.Fatalf("actions answers=%d renewals=%d grants=%d", answers.Load(), renewals.Load(), issues.Load())
	}
	t.Log(string(out))
}
