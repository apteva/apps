package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A permanently rejected (4xx) event is parked with its reason and the queue
// keeps going; 5xx stops the batch and the worker backs off.
func TestReviewH1LifecycleEventRejectionDoesNotBlockQueue(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, app, "park-events", "telnyx")
	app.db().db.Exec(`UPDATE call_events SET published_at='existing'`)
	t.Cleanup(func() { lifecyclePublishBackoff.reset(ctx.CurrentProject()) })
	var mu sync.Mutex
	status := map[string]int{}
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		topic, _ := body["topic"].(string)
		seen = append(seen, topic)
		if code := status[topic]; code != 0 {
			http.Error(w, "nope", code)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "local-test")
	now := time.Now().UTC()
	for i, topic := range []string{"bad.topic", "good.topic"} {
		at := now.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		if _, err := app.db().db.Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?)`,
			"ev-"+topic, row.ID, row.ProjectID, topic, 100+i, at, `{}`, at); err != nil {
			t.Fatal(err)
		}
	}
	status["bad.topic"] = 400
	if err := app.publishLifecycleEventsContext(t.Context(), ctx, ""); err != nil {
		t.Fatal(err)
	}
	var published, reason string
	app.db().db.QueryRow(`SELECT published_at, publish_error FROM call_events WHERE event_id='ev-bad.topic'`).Scan(&published, &reason)
	if published == "" || reason == "" {
		t.Fatalf("bad event not parked: published=%q reason=%q", published, reason)
	}
	app.db().db.QueryRow(`SELECT published_at FROM call_events WHERE event_id='ev-good.topic'`).Scan(&published)
	if published == "" {
		t.Fatal("later event blocked behind rejected one")
	}

	// 503: stays pending, worker backs off, explicit per-call publish still tries.
	at := now.Add(time.Second).Format(time.RFC3339Nano)
	app.db().db.Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at) VALUES('ev-flaky',?,?,'flaky.topic',200,?,'{}',?)`, row.ID, row.ProjectID, at, at)
	status["flaky.topic"] = 503
	if err := app.publishLifecycleEventsContext(t.Context(), ctx, ""); err == nil {
		t.Fatal("503 acknowledged")
	}
	mu.Lock()
	before := len(seen)
	mu.Unlock()
	if err := app.publishLifecycleEventsContext(t.Context(), ctx, ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(seen) != before {
		mu.Unlock()
		t.Fatal("worker retried during backoff")
	}
	delete(status, "flaky.topic")
	mu.Unlock()
	if err := app.publishLifecycleEvents(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	app.db().db.QueryRow(`SELECT published_at FROM call_events WHERE event_id='ev-flaky'`).Scan(&published)
	if published == "" {
		t.Fatal("transient event not published after recovery")
	}
}
