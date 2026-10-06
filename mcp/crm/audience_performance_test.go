package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAudiencePolicyUsesContactFirstIndexes(t *testing.T) {
	ctx := newTestCtx(t)
	for _, channel := range []string{"email", "sms", "whatsapp"} {
		t.Run(channel, func(t *testing.T) {
			reason, address := audiencePolicySQL(channel, false)
			rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN SELECT `+reason+`, `+address+`
				FROM contacts c WHERE c.project_id = ?`, "test-proj")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			channelSeeks, deliverySeeks := 0, 0
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(detail, "SEARCH cc USING INDEX ix_channel_contact") {
					channelSeeks++
				}
				if strings.Contains(detail, "SEARCH ds") {
					if !strings.Contains(detail, "project_id=? AND channel_id=? AND transport=?") {
						t.Fatalf("delivery lookup must use the contact's channel, not project routes: %s", detail)
					}
					deliverySeeks++
				}
				if strings.Contains(detail, "SCAN ds") || strings.Contains(detail, "SCAN cc") {
					t.Fatalf("unexpected route scan: %s", detail)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			// Healthy, five exclusion reasons, and preferred address all need seeks.
			if deliverySeeks != 7 || channelSeeks < 7 {
				t.Fatalf("channel/delivery seeks=%d/%d, want at least 7/7", channelSeeks, deliverySeeks)
			}
		})
	}
}

func TestResolveAudienceRealisticSize(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	// Match projects with 8k-14k routes, including many exclusions that must
	// examine several delivery-health predicates. Prepare in one transaction.
	const contacts = 12000
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO contacts(id, project_id, display_name) SELECT i, 'test-proj', 'Contact ' || i FROM n`, contacts); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO contact_channels(project_id, contact_id, kind, value, is_primary)
		SELECT project_id, id, 'email', 'contact-' || id || '@example.test', 1 FROM contacts`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE contact_channel_delivery_state SET suppressed=1
		WHERE channel_id IN (SELECT id FROM contact_channels WHERE contact_id % 5 = 0)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	segment, err := dbSegmentCreate(db, "test-proj", &Segment{Name: "All active", Kind: "dynamic", Definition: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	out, err := (&App{}).toolResolveAudience(callCtx, ctx, map[string]any{
		"channel": "email", "segment_id": segment.ID, "limit": 49,
	})
	if err != nil {
		t.Fatalf("12k-contact full count must finish within the generous 5s budget: %v", err)
	}
	r := out.(*AudienceResolution)
	if r.RawCount != contacts || r.EligibleCount != 9600 || r.ExcludedCount != 2400 || r.ExcludedByReason["suppressed"] != 2400 {
		t.Fatalf("incorrect full counts: %+v", r)
	}
	if len(r.Recipients) != 40 || len(r.Exclusions) != 9 || !r.HasMore || r.NextAfterContactID != 49 {
		t.Fatalf("incorrect first page: %+v", r)
	}
	t.Logf("12k-contact full counts and 49-contact page: %s", time.Since(start))
	// Count-free paging must keep the same results but not compute full counts.
	out, err = (&App{}).toolResolveAudience(callCtx, ctx, map[string]any{
		"channel": "email", "segment_id": segment.ID, "limit": 49, "include_counts": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := out.(*AudienceResolution)
	if page.RawCount != 0 || page.EligibleCount != 0 || page.ExcludedCount != 0 || len(page.ExcludedByReason) != 0 ||
		!reflect.DeepEqual(page.Recipients, r.Recipients) || !reflect.DeepEqual(page.Exclusions, r.Exclusions) || page.NextAfterContactID != r.NextAfterContactID {
		t.Fatalf("count-free page changed audience semantics: %+v", page)
	}
}

func TestResolveAudienceReadPoolAndCancellation(t *testing.T) {
	app := &App{}
	ctx := newTestCtx(t)
	contact := mustCreate(t, ctx, map[string]any{"channels": []any{
		map[string]any{"kind": "email", "value": "reader@example.test", "is_primary": true},
	}})
	list, err := dbListCreate(ctx.AppDB(), "test-proj", &List{Name: "Read pool"})
	if err != nil {
		t.Fatal(err)
	}
	if err = dbListAddContact(ctx.AppDB(), "test-proj", list.ID, contact.ID, "test"); err != nil {
		t.Fatal(err)
	}
	sources := []map[string]any{{"contact_id": contact.ID}, {"list_id": list.ID}}
	for _, kind := range []string{"dynamic", "static"} {
		s, err := dbSegmentCreate(ctx.AppDB(), "test-proj", &Segment{
			Name: kind, Kind: kind, ListID: &list.ID,
			Definition: json.RawMessage(fmt.Sprintf(`[{"predicate":"in_list","list_id":%d}]`, list.ID)),
		})
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, map[string]any{"segment_id": s.ID})
	}
	path := filepath.Join(t.TempDir(), "reader.db")
	if _, err := ctx.AppDB().Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	defer ctx.SetAppReadDBForTest(reader)()
	// Hold the only writer connection. All sources must succeed through the
	// read-only pool, including list/segment metadata and reference validation.
	writerConn, err := ctx.AppDB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()
	for _, source := range sources {
		args := map[string]any{"channel": "email"}
		for k, v := range source {
			args[k] = v
		}
		callCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := app.toolResolveAudience(callCtx, ctx, args)
		cancel()
		if err != nil {
			t.Fatalf("source %v did not use read pool: %v", source, err)
		}
		if r := out.(*AudienceResolution); r.EligibleCount != 1 || len(r.Recipients) != 1 {
			t.Fatalf("source %v returned %+v", source, r)
		}
	}
	// Waiting for a busy read pool must honor cancellation at every stage.
	readConn, err := reader.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer readConn.Close()
	for _, source := range sources {
		for _, counts := range []bool{false, true} {
			args := map[string]any{"channel": "email", "include_counts": counts}
			for k, v := range source {
				args[k] = v
			}
			callCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			out, err := app.toolResolveAudience(callCtx, ctx, args)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) || out != nil {
				t.Fatalf("source %v counts=%v: partial success or lost cancellation: %v, %v", source, counts, out, err)
			}
		}
	}
	callCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.toolResolveAudience(callCtx, ctx, map[string]any{"channel": "email", "contact_id": contact.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already-cancelled request: %v", err)
	}
	for _, tool := range app.MCPTools() {
		if tool.Name == "contacts_resolve_audience" && (tool.HandlerCtx == nil || tool.Handler != nil) {
			t.Fatal("audience tool must receive the real per-call context")
		}
	}
}

func TestResolveAudienceCancelsRunningQueries(t *testing.T) {
	ctx := newTestCtx(t)
	contact := mustCreate(t, ctx, map[string]any{"channels": []any{
		map[string]any{"kind": "email", "value": "cancel@example.test"},
	}})
	// An intentionally oversized source makes both the aggregate and sorted
	// page run long enough to cancel while SQLite is executing, not just while
	// waiting for a connection. The normal audience never contains duplicates.
	source := &audienceSource{
		query: `WITH RECURSIVE spin(n) AS MATERIALIZED (SELECT 1 UNION ALL SELECT n+1 FROM spin WHERE n<100000000)
			SELECT ? AS contact_id FROM spin`,
		args: []any{contact.ID},
	}
	for _, counts := range []bool{false, true} {
		callCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		result, err := resolveAudience(callCtx, ctx.AppDB(), source, "test-proj", "email", 0, 49, false, counts)
		cancel()
		if err == nil || result != nil || !errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("counts=%v: running query did not cancel cleanly: %+v, %v", counts, result, err)
		}
	}
	callCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ctx.AppDB().PingContext(callCtx); err != nil {
		t.Fatalf("cancelled query did not release its connection: %v", err)
	}
}

func TestAudiencePolicyPreservesLegacyResults(t *testing.T) {
	ctx := newTestCtx(t)
	type route struct {
		status                  string
		suppressed, quarantined int
	}
	tests := []struct {
		reason string
		routes []route
	}{
		{"eligible", []route{{"active", 0, 0}}},
		{"suppressed", []route{{"active", 1, 0}}},
		{"quarantined", []route{{"active", 0, 1}}},
		{"complained", []route{{"complained", 0, 0}}},
		{"hard_bounced", []route{{"hard_bounced", 0, 0}}},
		{"unsubscribed", []route{{"unsubscribed", 0, 0}}},
		{"unmessageable", []route{{"missing", 0, 0}}},
		{"no_channel", nil},
		{"automated", []route{{"active", 0, 0}}},
		{"do_not_contact", []route{{"active", 0, 0}}},
		{"eligible", []route{{"hard_bounced", 0, 0}, {"active", 0, 0}, {"active", 0, 0}}},
		{"suppressed", []route{{"complained", 1, 1}}},
		{"quarantined", []route{{"unsubscribed", 0, 1}}},
	}
	want := map[int64]string{}
	for i, tc := range tests {
		channels := []any{}
		for j := range tc.routes {
			channels = append(channels, map[string]any{"kind": "email", "value": fmt.Sprintf("case-%d-%d@example.test", i, j), "is_primary": j == 0})
		}
		contact := mustCreate(t, ctx, map[string]any{"channels": channels})
		want[contact.ID] = tc.reason
		for j, route := range tc.routes {
			if route.status == "missing" {
				if _, err := ctx.AppDB().Exec(`DELETE FROM contact_channel_delivery_state WHERE channel_id=?`, contact.Channels[j].ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := ctx.AppDB().Exec(`UPDATE contact_channel_delivery_state SET status=?, suppressed=?, quarantined=? WHERE channel_id=?`, route.status, route.suppressed, route.quarantined, contact.Channels[j].ID); err != nil {
				t.Fatal(err)
			}
		}
		if tc.reason == "automated" {
			if err := dbAddTag(ctx.AppDB(), "test-proj", contact.ID, tagAutomated); err != nil {
				t.Fatal(err)
			}
		}
		if tc.reason == "do_not_contact" {
			if err := dbSetAttribute(ctx.AppDB(), "test-proj", contact.ID, "do_not_contact", true, "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, includeAutomated := range []bool{false, true} {
		reason, address := audiencePolicySQL("email", includeAutomated)
		query := `SELECT c.id, ` + reason + `, COALESCE(` + address + `, '') FROM contacts c WHERE c.project_id=? ORDER BY c.id`
		read := func(query string) []string {
			rows, err := ctx.AppDB().Query(query, "test-proj")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var result []string
			for rows.Next() {
				var id int64
				var reason, address string
				if err := rows.Scan(&id, &reason, &address); err != nil {
					t.Fatal(err)
				}
				expected := want[id]
				if includeAutomated && expected == "automated" {
					expected = "eligible"
				}
				if reason != expected {
					t.Fatalf("contact %d: got %s, want %s", id, reason, expected)
				}
				result = append(result, fmt.Sprintf("%d/%s/%s", id, reason, address))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return result
		}
		fixed := read(query)
		legacy := read(strings.ReplaceAll(query, "CROSS JOIN contact_channel_delivery_state", "JOIN contact_channel_delivery_state"))
		if !reflect.DeepEqual(fixed, legacy) {
			t.Fatalf("audience policy changed: fixed=%v legacy=%v", fixed, legacy)
		}
	}
}
