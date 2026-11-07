package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func reviewH1FeedRegistered(a *App, agent int64) bool {
	a.aiPolicies.mu.Lock()
	defer a.aiPolicies.mu.Unlock()
	return a.aiPolicies.feeds[agent] != nil
}

// A feed that gives up must remove its registry entry, so a later call can
// start a new feed instead of finding a dead one.
func TestReviewH1AIPolicyFeedExitForgetsEntry(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 403) }))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "tok")
	t.Cleanup(app.stopAIPolicies)
	app.ensureAIPhaseFeed(ctx, 7)
	deadline := time.Now().Add(3 * time.Second)
	for reviewH1FeedRegistered(app, 7) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reviewH1FeedRegistered(app, 7) {
		t.Fatal("dead feed left registered")
	}
}

// 404 during a platform deploy is retried with backoff, not treated as final.
func TestReviewH1AIPolicyFeedRetriesTransientNotFound(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "tok")
	app.ensureAIPhaseFeed(ctx, 8)
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hits.Load() < 2 || !reviewH1FeedRegistered(app, 8) {
		t.Fatalf("feed gave up after 404: hits=%d registered=%v", hits.Load(), reviewH1FeedRegistered(app, 8))
	}
	app.stopAIPolicies()
}
