package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func notificationPayload(id string, watermark int64, scopes []string) map[string]any {
	return map[string]any{"event_id": id, "projection_id": 1, "name": "event_totals", "version": 1, "generation": id, "watermark": watermark, "published_at": projectionTimestamp(watermark), "ready": true, "scope_keys": scopes, "scope_count": len(scopes)}
}
func assertBoundedNotification(t *testing.T, payload map[string]any, compact bool) []byte {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > projectionReadyPayloadMaxBytes {
		t.Fatalf("payload bytes=%d", len(body))
	}
	if compact {
		if payload["all_scopes"] != true || payload["scopes_truncated"] != true {
			t.Fatalf("missing projection refresh flags: %v", payload)
		}
		if _, ok := payload["scope_count"]; ok {
			t.Fatal("compact payload claims an exact count")
		}
		if _, ok := payload["scope_key"]; ok {
			t.Fatal("compact payload retains misleading singular scope")
		}
		var decoded struct {
			ScopeKeys []string `json:"scope_keys"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.ScopeKeys == nil || len(decoded.ScopeKeys) != 0 {
			t.Fatal("compact keys must be []")
		}
	}
	return body
}
func TestProjectionReadyPayloadByteBudget(t *testing.T) {
	for _, scope := range []string{strings.Repeat("x", 100), strings.Repeat("😀", 100), strings.Repeat("<\"\\", 100)} {
		t.Run(fmt.Sprint(len(scope)), func(t *testing.T) {
			scopes := make([]string, 5000)
			for i := range scopes {
				scopes[i] = fmt.Sprintf("%d%s", i, scope)
			}
			payload := notificationPayload("stable-id", 9007199254740993, scopes)
			payload["scope_key"] = scopes[0]
			body, err := boundedProjectionReadyPayload(payload)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedNotification(t, payload, true)
			again, err := boundedProjectionReadyPayload(payload)
			if err != nil || string(body) != string(again) {
				t.Fatal("normalization not idempotent")
			}
			if payload["event_id"] != "stable-id" || payload["watermark"] != int64(9007199254740993) {
				t.Fatal("identity or watermark changed")
			}
		})
	}
	// Optional generation history is bounded too.
	payload := notificationPayload("new", 1, []string{"a"})
	payload["generations"] = []string{strings.Repeat("g", 20000)}
	if _, err := boundedProjectionReadyPayload(payload); err != nil {
		t.Fatal(err)
	}
	assertBoundedNotification(t, payload, true)
}
func enqueueNotification(t *testing.T, ctx *sdk.AppCtx, payload map[string]any, attempts int) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO projection_event_outbox(event_id,project_id,projection_id,topic,payload,attempts,next_attempt_ms,created_at_ms) VALUES(?,?,?,?,?,?,0,?)`, payload["event_id"], ctx.CurrentProject(), payload["projection_id"], topicProjectionReady, string(body), attempts, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}
func notificationFixture(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	return a, ctx
}
func TestProjectionReadyCoalescingBoundedAndPrecise(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			a, ctx := notificationFixture(t)
			emitter := &flakyProjectionEmitter{}
			ctx.SetEmitter(emitter)
			for i := 0; i < 20; i++ {
				scopes := []string{"overlap", fmt.Sprintf("scope-%02d", i)}
				if large {
					for j := 0; j < 80; j++ {
						scopes = append(scopes, fmt.Sprintf("%d-%d-%s", i, j, strings.Repeat("界", 30)))
					}
				}
				payload := notificationPayload(fmt.Sprintf("id-%02d", i), int64(9007199254740993+i), scopes)
				payload["coverage_from"] = "2026-10-01T00:00:00Z"
				payload["coverage_to"] = fmt.Sprintf("2026-10-%02dT00:00:00Z", i+2)
				if body, _ := json.Marshal(payload); len(body) > projectionReadyPayloadMaxBytes {
					t.Fatal("individual event unexpectedly large")
				}
				enqueueNotification(t, ctx, payload, 0)
			}
			if err := a.deliverProjectionEvents(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			if len(emitter.events) != 1 {
				t.Fatalf("events=%d", len(emitter.events))
			}
			payload := emitter.events[0].(map[string]any)
			assertBoundedNotification(t, payload, large)
			if !large && (payload["scope_count"] != 21 || len(payload["scope_keys"].([]string)) != 21) {
				t.Fatalf("union count incorrect: %v", payload)
			}
			if payload["watermark"] != json.Number("9007199254741012") || payload["generation"] != "id-19" || payload["coverage_to"] != "2026-10-21T00:00:00Z" {
				t.Fatalf("newest metadata not retained: %v", payload)
			}
			var pending int
			if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_event_outbox`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("pending=%d err=%v", pending, err)
			}
		})
	}
}
func TestProjectionReadyCompactCoalesceStaysUnknown(t *testing.T) {
	a, ctx := notificationFixture(t)
	emitter := &flakyProjectionEmitter{}
	ctx.SetEmitter(emitter)
	payload := notificationPayload("compact", 1, []string{})
	payload["all_scopes"] = true
	payload["scopes_truncated"] = true
	payload["scope_count"] = 50000
	enqueueNotification(t, ctx, payload, 0)
	enqueueNotification(t, ctx, notificationPayload("small", 2, []string{"a"}), 0)
	if err := a.deliverProjectionEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	assertBoundedNotification(t, emitter.events[0].(map[string]any), true)
}

// Simulates a size-limited gateway and an ambiguous acknowledgement. It accepts
// once by event ID, then fails the acknowledgement; a retry must be identical.
type boundedNotificationEmitter struct {
	accepted map[string]string
	attempts int
	failAck  bool
}

func (*boundedNotificationEmitter) EmitWithProject(string, string, any) {}
func (e *boundedNotificationEmitter) EmitWithProjectAck(_ context.Context, _, _ string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if len(body) > projectionReadyPayloadMaxBytes {
		return errors.New("HTTP 400: payload too large")
	}
	id := data.(map[string]any)["event_id"].(string)
	if prior, ok := e.accepted[id]; ok && prior != string(body) {
		return errors.New("event identity reused with changed payload")
	}
	e.accepted[id] = string(body)
	e.attempts++
	if e.failAck {
		e.failAck = false
		return errors.New("lost acknowledgement")
	}
	return nil
}
func TestProjectionReadyLegacyRepairAndRetryAfterDatabaseReopen(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "p")
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	scopes := make([]string, 30000)
	for i := range scopes {
		scopes[i] = fmt.Sprintf("legacy-%05d-%s", i, strings.Repeat("x", 20))
	}
	enqueueNotification(t, ctx, notificationPayload("legacy-id", 1, scopes), 1100)
	emitter := &boundedNotificationEmitter{accepted: map[string]string{}, failAck: true}
	ctx.SetEmitter(emitter)
	if err := a.deliverProjectionEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var payload string
	var attempts, sealed int
	if err := ctx.AppDB().QueryRow(`SELECT payload,attempts,delivery_sealed FROM projection_event_outbox`).Scan(&payload, &attempts, &sealed); err != nil {
		t.Fatal(err)
	}
	if len(payload) > projectionReadyPayloadMaxBytes || attempts != 1101 || sealed != 1 {
		t.Fatalf("repair not durable bytes=%d attempts=%d sealed=%d", len(payload), attempts, sealed)
	}
	var normalized map[string]any
	_ = json.Unmarshal([]byte(payload), &normalized)
	assertBoundedNotification(t, normalized, true)
	var seq int
	var name, path string
	if err := ctx.AppDB().QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = ctx.AppDB().Close()
	reopened, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopened.SetMaxOpenConns(1)
	manifestBody, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(manifestBody)
	if err != nil {
		t.Fatal(err)
	}
	ctx = sdk.NewAppCtxForTest(manifest, reopened, nil, nil, nil).WithProject("p")
	ctx.SetEmitter(emitter)
	// A later publication must not be merged into the event already accepted.
	enqueueNotification(t, ctx, notificationPayload("later-id", 2, []string{"a"}), 0)
	if _, err := reopened.Exec(`UPDATE projection_event_outbox SET next_attempt_ms=0`); err != nil {
		t.Fatal(err)
	}
	a = &App{}
	if err := a.deliverProjectionEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	_ = reopened.QueryRow(`SELECT COUNT(*) FROM projection_event_outbox`).Scan(&pending)
	if pending != 0 || len(emitter.accepted) != 2 || emitter.attempts != 3 {
		t.Fatalf("retry/restart recovery pending=%d identities=%d attempts=%d", pending, len(emitter.accepted), emitter.attempts)
	}
}
func TestProjectionReadyFullPublicationIsBounded(t *testing.T) {
	a, ctx := notificationFixture(t)
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	p, err := a.loadProjection(ctx, "test-proj", "event_totals")
	if err != nil {
		t.Fatal(err)
	}
	// This test exercises notification sizing, with enough publication budget
	// for the race-instrumented multi-scope head switch.
	p.Options.MaxMs = 30000
	p.Options.PublishMs = 5000
	grouped := map[string][]map[string]any{}
	for i := 0; i < 1500; i++ {
		centre := fmt.Sprintf("centre-%05d-%s", i, strings.Repeat("界", 30))
		key, err := makeScopeKey(map[string]any{"centre_id": centre}, p.ScopeCols)
		if err != nil {
			t.Fatal(err)
		}
		grouped[key] = []map[string]any{{"centre_id": centre, "total": 1}}
	}
	if err := publishProjectionRows(ctx, p, item, grouped); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := ctx.AppDB().QueryRow(`SELECT payload FROM projection_event_outbox`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stored), &payload); err != nil {
		t.Fatal(err)
	}
	assertBoundedNotification(t, payload, true)
	result := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT COUNT(*) AS n FROM {event_totals}"})
	rows := result["rows"].([]map[string]any)
	if rows[0]["n"] != int64(1500) {
		t.Fatalf("published count=%v", rows)
	}
}

func TestProjectionReadySmallScopeRetainsPrecision(t *testing.T) {
	a, ctx := notificationFixture(t)
	emitter := &flakyProjectionEmitter{}
	ctx.SetEmitter(emitter)
	payload := notificationPayload("single", 7, []string{`["centre-é"]`})
	payload["scope_key"] = `["centre-é"]`
	enqueueNotification(t, ctx, payload, 0)
	if err := a.deliverProjectionEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got := emitter.events[0].(map[string]any)
	assertBoundedNotification(t, got, false)
	if got["scope_key"] != payload["scope_key"] || got["scope_count"] != 1 || got["all_scopes"] == true || got["scopes_truncated"] == true {
		t.Fatalf("small event lost precision: %v", got)
	}
}
func TestProjectionReadyCoalescedIdentityFrozenBeforeAmbiguousAck(t *testing.T) {
	a, ctx := notificationFixture(t)
	emitter := &boundedNotificationEmitter{accepted: map[string]string{}, failAck: true}
	ctx.SetEmitter(emitter)
	enqueueNotification(t, ctx, notificationPayload("first", 1, []string{"a"}), 0)
	enqueueNotification(t, ctx, notificationPayload("second", 2, []string{"b"}), 0)
	if err := a.deliverProjectionEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_event_outbox`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("batch not frozen pending=%d err=%v", pending, err)
	}
	enqueueNotification(t, ctx, notificationPayload("third", 3, []string{"b", "c"}), 0)
	if _, err := ctx.AppDB().Exec(`UPDATE projection_event_outbox SET next_attempt_ms=0`); err != nil {
		t.Fatal(err)
	}
	a = &App{}
	if err := a.deliverProjectionEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_event_outbox`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || len(emitter.accepted) != 2 || emitter.attempts != 3 {
		t.Fatalf("dedup recovery pending=%d accepted=%d attempts=%d", pending, len(emitter.accepted), emitter.attempts)
	}
}
func TestProjectionReadyEmptyFullRebuildInvalidatesProjection(t *testing.T) {
	a, ctx := notificationFixture(t)
	emitter := &flakyProjectionEmitter{}
	ctx.SetEmitter(emitter)
	runProjectionWorker(t, a, ctx)
	if len(emitter.events) != 1 {
		t.Fatalf("events=%d", len(emitter.events))
	}
	assertBoundedNotification(t, emitter.events[0].(map[string]any), true)
}
