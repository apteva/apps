package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRuntimeHealthAndRecoveryStatus(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	a := &App{runtime: runtimeTestActive()}
	check := func(want int, state string, admit, recover bool) {
		t.Helper()
		w := httptest.NewRecorder()
		a.handleRuntimeHealth(w, httptest.NewRequest("GET", "/health", nil))
		if w.Code != want {
			t.Fatalf("health %d: %s", w.Code, w.Body.String())
		}
		r := httptest.NewRequest("GET", runtimeAppPrefix+"status", nil)
		r.Header.Set("Authorization", "Bearer runtime-secret")
		w = httptest.NewRecorder()
		a.handleRuntimeLifecycle(w, r)
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["state"] != state || got["admitting"] != admit || (got["recovery"] != nil) != recover {
			t.Fatalf("status %v", got)
		}
		if got["deployment_id"] != "deployment-a" || got["generation"] != float64(1) {
			t.Fatal("identity missing")
		}
	}
	check(200, "active", true, false)
	a.runtime.expires = time.Now().Add(-time.Second)
	check(503, "ownership_expired", false, false)
	a.runtime.fenceLocked("ownership_unverified", time.Now())
	check(503, "fenced", false, false)
	a.runtime.expires = time.Now().Add(time.Minute)
	check(503, "fenced", false, false) // A pre-fence lease is not re-verification.
	a.runtime.recoveryVerified = true
	check(503, "fenced", false, true)
	if a.runtimeOwned() {
		t.Fatal("fresh lease revived fenced execution")
	}
	a.runtime = &telephonyRuntime{deployment: "deployment-a", generation: 1, state: "standby"}
	check(200, "standby", false, false)
	a.runtime.state = "draining"
	check(503, "draining", false, false)
	a.runtime.state = "error"
	check(503, "error", false, false)
	r := httptest.NewRequest("GET", runtimeAppPrefix+"status", nil)
	w := httptest.NewRecorder()
	a.handleRuntimeLifecycle(w, r)
	if w.Code != 403 {
		t.Fatal("unauthenticated status exposed")
	}
}

func TestRuntimeHealthOverridesSDKLivenessWithoutAdmissionGate(t *testing.T) {
	a := &App{runtime: runtimeTestActive()}
	a.runtime.fenceLocked("ownership_unverified", time.Now())
	mux := http.NewServeMux()
	for _, r := range a.HTTPRoutes() {
		p := r.Pattern
		if r.Method != "" {
			p = r.Method + " " + p
		}
		mux.HandleFunc(p, r.Handler)
	}
	// The SDK's methodless liveness fallback coexists with app readiness.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true,"status":"ready"}`)) })
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/health", nil))
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"fenced"`) || strings.Contains(w.Body.String(), "runtime_not_admitting") {
			t.Fatalf("%s %d %s", method, w.Code, w.Body.String())
		}
	}
}
