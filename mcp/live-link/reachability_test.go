package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClassifyProbe(t *testing.T) {
	zrokPage := []byte(`<html><body><div>zrok</div><div>share 34jh5n0wnkbj not found!</div><div>are you running zrok2 share for this share?</div></body></html>`)
	cases := []struct {
		name   string
		status int
		body   []byte
		err    error
		ok     bool
		detail string
	}{
		{"target ok", 200, []byte("hello"), nil, true, ""},
		{"target's own 404 is reachable", 404, []byte("404 page not found"), nil, true, ""},
		{"target's own 401 is reachable", 401, []byte("unauthorized"), nil, true, ""},
		{"zrok share not found", 404, zrokPage, nil, false, zrokShareNotFoundHint},
		{"zrok hint text on other status", 200, []byte("Are you running zrok2 share"), nil, false, zrokShareNotFoundHint},
		{"bad gateway", 502, nil, nil, false, "HTTP 502"},
		{"cloudflare 530", 530, nil, nil, false, "HTTP 530"},
		{"network error", 0, nil, errors.New("dial tcp: no such host"), false, "not reachable: dial tcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, detail := classifyProbe(tc.status, tc.body, tc.err)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (detail %q)", ok, tc.ok, detail)
			}
			if !strings.Contains(detail, tc.detail) {
				t.Fatalf("detail = %q, want it to contain %q", detail, tc.detail)
			}
		})
	}
}

func TestExplainExitReasonShareUnauthorized(t *testing.T) {
	raw := "exit 1: [ERROR]: unable to create share (unable to create share: [POST /share][401] shareUnauthorized)"
	got := explainExitReason(raw)
	if !strings.HasPrefix(got, zrokShareUnauthorizedHint) || !strings.HasSuffix(got, raw) {
		t.Fatalf("explainExitReason = %q", got)
	}
	if again := explainExitReason(got); again != got {
		t.Fatalf("explainExitReason is not idempotent: %q", again)
	}
	if other := explainExitReason("exit 1: something else"); other != "exit 1: something else" {
		t.Fatalf("unrelated reason changed: %q", other)
	}
}

func TestSetReachabilityScopedToActiveRun(t *testing.T) {
	m := NewManager(nil, nil)
	if m.SetReachability(1, true, "") {
		t.Fatal("idle manager accepted a reachability result")
	}
	m.mu.Lock()
	m.status = StatusRunning
	m.runID = 7
	m.mu.Unlock()
	if m.SetReachability(6, false, "stale") {
		t.Fatal("result for an older run was accepted")
	}
	if !m.SetReachability(7, false, zrokShareNotFoundHint) {
		t.Fatal("first result for the active run was not reported as a change")
	}
	if m.SetReachability(7, false, zrokShareNotFoundHint) {
		t.Fatal("identical result reported as a change")
	}
	snap := m.Snapshot()
	if snap.Reachable == nil || *snap.Reachable || snap.ReachDetail != zrokShareNotFoundHint || snap.ReachCheckedAt.IsZero() {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !m.SetReachability(7, true, "") {
		t.Fatal("recovery was not reported as a change")
	}
}

func TestProbePublicURLUsesNonBrowserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	status, body, err := probePublicURL(context.Background(), srv.URL)
	if err != nil || status != 200 || string(body) != "ok" {
		t.Fatalf("probe = %d %q %v", status, body, err)
	}
	if !strings.HasPrefix(gotUA, "apteva-live-link/") || strings.Contains(gotUA, "Mozilla") {
		t.Fatalf("user agent = %q", gotUA)
	}
}
