package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func replacementFixture(t *testing.T, operation string) (*sql.DB, EventInsert, int64) {
	t.Helper()
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "membership", Topic: "members.current",
		Status: "active", ValidationMode: "reject", IngestMode: "upsert",
		UpsertPolicy: &EventIngestPolicy{
			Bucket: "day", Timezone: "UTC", TimestampProperty: "props.observed_at",
			Operation: operation, Value: "props.total_members", OutputProperty: "total_members",
			Dimensions: []string{"props.page_id"},
		},
		Properties: []EventPropertySpec{
			{Key: "props.page_id", Type: "string", Required: true},
			{Key: "props.observed_at", Type: "string", Required: true},
			{Key: "props.contract_version", Type: "string", Required: true},
			{Key: "props.total_members", Type: "number", Required: true},
			{Key: "props.free_members", Type: "number"},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	ev := EventInsert{ProjectID: "p1", App: "membership", Topic: "members.current", Source: "track", TS: 1788739200000,
		Props: `{"page_id":"fixture-page","observed_at":"2026-09-07T01:00:00Z","contract_version":"v1","total_members":686,"paid_members":41,"free_members":645,"source_event_id":123,"old_note":"obsolete","details":{"previous":true}}`}
	id, err := insertEvent(db, ev)
	if err != nil {
		t.Fatal(err)
	}
	return db, ev, id
}

func TestReplacePolicyDropsOmittedObservationProperties(t *testing.T) {
	for _, operation := range []string{"replace", ""} {
		t.Run("operation="+operation, func(t *testing.T) {
			db, ev, id := replacementFixture(t, operation)
			var key string
			if err := db.QueryRow(`SELECT upsert_key FROM events WHERE id=?`, id).Scan(&key); err != nil {
				t.Fatal(err)
			}
			ev.DeliveryID = "new-observation"
			ev.Props = `{"page_id":"fixture-page","observed_at":"2026-09-07T05:01:20Z","contract_version":"v1","total_members":693,"paid_members":41,"data_quality":"incomplete","rollup_eligible":false}`
			for i := 0; i < 2; i++ { // A transport replay must preserve the replacement and identity.
				got, err := insertEvent(db, ev)
				if err != nil || got != id {
					t.Fatalf("replacement/replay id=%d err=%v, want %d", got, err, id)
				}
			}
			var raw, gotKey string
			if err := db.QueryRow(`SELECT props,upsert_key FROM events WHERE id=?`, id).Scan(&raw, &gotKey); err != nil {
				t.Fatal(err)
			}
			var props map[string]any
			if err := json.Unmarshal([]byte(raw), &props); err != nil {
				t.Fatal(err)
			}
			for _, omitted := range []string{"free_members", "source_event_id", "old_note", "details"} {
				if _, exists := props[omitted]; exists {
					t.Errorf("replacement retained omitted %s: %s", omitted, raw)
				}
			}
			if gotKey != key || props["total_members"] != float64(693) || props["day"] != "2026-09-07" || props["bucket_start"] != float64(1788739200000) {
				t.Fatalf("identity, incoming value or regenerated bucket changed: key=%s props=%s", gotKey, raw)
			}
		})
	}
}

func TestReplacePolicyOptionalNullAndRequiredOmission(t *testing.T) {
	db, ev, id := replacementFixture(t, "replace")
	ev.Props = `{"page_id":"fixture-page","observed_at":"2026-09-07T05:01:20Z","contract_version":"v1","total_members":693,"free_members":null,"source_event_id":456}`
	if _, err := insertEvent(db, ev); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT props FROM events WHERE id=?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(before), &props); err != nil {
		t.Fatal(err)
	}
	if value, exists := props["free_members"]; !exists || value != nil || props["source_event_id"] != float64(456) {
		t.Fatalf("explicit null/provenance not preserved: %s", before)
	}
	for _, contract := range []string{"", `,"contract_version":null`} {
		ev.Props = `{"page_id":"fixture-page","observed_at":"2026-09-07T06:00:00Z","total_members":700` + contract + `}`
		_, err := insertEvent(db, ev)
		var rejected *rejectedEventError
		if !errors.As(err, &rejected) {
			t.Fatalf("missing/null required field accepted: %v", err)
		}
		var after string
		if err := db.QueryRow(`SELECT props FROM events WHERE id=?`, id).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("rejected replacement changed stored observation: %s", after)
		}
	}
}
