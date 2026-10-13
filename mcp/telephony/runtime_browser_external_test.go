package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManagedRuntimeExternalBrowserOriginPreflightAndRead(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	a.installID = 42
	a.runtime = runtimeTestActive()
	a.runtime.endpoints["browser"] = "https://voice.test"
	origin := "http://127.0.0.1:5397"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["origin"] != origin {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/api/apps/callback/runtime/browser-origin" {
			writeJSON(w, map[string]bool{"allowed": true})
			return
		}
		if body["token"] != "scoped" {
			w.WriteHeader(403)
			return
		}
		writeJSON(w, runtimeBrowserGrant{InstallID: 42, Project: "project-a", UserID: 9, Deployment: "deployment-a", Generation: 1, Origin: origin, Expires: time.Now().Add(time.Minute).Unix()})
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	t.Setenv("APTEVA_APP_TOKEN", "secret")
	for _, method := range []string{"OPTIONS", "GET"} {
		r := httptest.NewRequest(method, runtimeBrowserPrefix+"/calls", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer scoped")
		w := httptest.NewRecorder()
		a.handleRuntimeBrowser(w, r)
		if w.Code != 200 && w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatal("missing exact CORS origin")
		}
	}
}
