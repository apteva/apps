package main

import (
	"fmt"
	"testing"
	"time"
)

func TestEventSpecRejectsMissingRequiredProperty(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID:      "p1",
		App:            "patreon",
		Topic:          "post_views_daily_observed",
		Kind:           "aggregate_observation",
		Status:         "active",
		ValidationMode: "reject",
		Properties: []EventPropertySpec{
			{Key: "props.post_id", Type: "string", Required: true},
			{Key: "props.views", Type: "number", Required: true},
		},
	}, true)
	if err != nil {
		t.Fatalf("upsert spec: %v", err)
	}
	_, err = insertEvent(db, EventInsert{
		TS:        time.Now().UnixMilli(),
		App:       "patreon",
		Topic:     "post_views_daily_observed",
		ProjectID: "p1",
		Source:    "track",
		Props:     `{"post_id":"post_1"}`,
	})
	if err == nil {
		t.Fatal("expected missing required views to reject")
	}
}

func TestUpsertKeyUpdatesAggregateObservationAndSum(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID:      "p1",
		App:            "patreon",
		Topic:          "post_views_daily_observed",
		Kind:           "aggregate_observation",
		Status:         "active",
		ValidationMode: "warn",
		Properties: []EventPropertySpec{
			{Key: "props.post_id", Type: "string", Required: true},
			{Key: "props.views", Type: "number", Required: true},
			{Key: "upsert_key", Type: "string", Required: true},
		},
	}, true)
	if err != nil {
		t.Fatalf("upsert spec: %v", err)
	}
	for _, views := range []string{"10", "15"} {
		if _, err := insertEvent(db, EventInsert{
			TS:        time.Now().UnixMilli(),
			App:       "patreon",
			Topic:     "post_views_daily_observed",
			ProjectID: "p1",
			Source:    "track",
			UpsertKey: "post_1:2026-06-16",
			Props:     `{"post_id":"post_1","views":` + views + `}`,
		}); err != nil {
			t.Fatalf("insert aggregate: %v", err)
		}
	}
	var rows int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	buckets, err := sumByValue(db, Filter{ProjectID: "p1", App: "patreon", Topic: "post_views_daily_observed"}, "props.views", []string{"props.post_id"}, 10)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if len(buckets) != 1 || buckets[0]["sum"] != 15.0 {
		t.Fatalf("sum buckets = %#v, want one bucket sum 15", buckets)
	}
}

func TestEventSpecValidatesNestedProperties(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID:      "p1",
		App:            "site",
		Topic:          "page_viewed",
		Status:         "active",
		ValidationMode: "reject",
		Properties: []EventPropertySpec{
			{Key: "props.page.path", Type: "string", Required: true},
			{Key: "props.page.depth", Type: "number", Required: true},
		},
	}, true)
	if err != nil {
		t.Fatalf("upsert spec: %v", err)
	}
	_, err = insertEvent(db, EventInsert{
		TS:        time.Now().UnixMilli(),
		App:       "site",
		Topic:     "page_viewed",
		ProjectID: "p1",
		Source:    "track",
		Props:     `{"page":{"path":"/pricing","depth":2}}`,
	})
	if err != nil {
		t.Fatalf("insert nested event: %v", err)
	}
}

func TestListEventSpecsDoesNotDeadlockWithSingleSQLiteConnection(t *testing.T) {
	db := testDashboardDB(t)
	db.SetMaxOpenConns(1)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID:      "p1",
		App:            "patreon",
		Topic:          "post_views_daily_observed",
		Status:         "active",
		ValidationMode: "warn",
		Properties: []EventPropertySpec{
			{Key: "props.post_id", Type: "string", Required: true},
			{Key: "props.views", Type: "number", Required: true},
		},
	}, true)
	if err != nil {
		t.Fatalf("upsert spec: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		specs, err := listEventSpecs(db, specFilter{ProjectID: "p1"})
		if err == nil && (len(specs) != 1 || len(specs[0].Properties) != 2) {
			err = fmt.Errorf("specs = %#v, want one spec with two properties", specs)
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = db.Close()
		t.Fatal("listEventSpecs deadlocked with MaxOpenConns(1)")
	}
}

func TestEventSpecAutoUpsertPolicyReplacesDailyObservation(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID:      "p1",
		App:            "patreon",
		Topic:          "post_views_daily_observed",
		Kind:           "aggregate_observation",
		Status:         "active",
		ValidationMode: "reject",
		IngestMode:     "upsert",
		UpsertPolicy: &EventIngestPolicy{
			Bucket:         "day",
			Operation:      "replace",
			Value:          "props.views",
			OutputProperty: "views",
			Dimensions:     []string{"props.post_id"},
		},
		Properties: []EventPropertySpec{
			{Key: "props.post_id", Type: "string", Required: true},
			{Key: "props.views", Type: "number", Required: true},
			{Key: "upsert_key", Type: "string", Required: true},
		},
	}, true)
	if err != nil {
		t.Fatalf("upsert spec: %v", err)
	}
	ts := time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC).UnixMilli()
	for _, views := range []string{"10", "15"} {
		if _, err := insertEvent(db, EventInsert{
			TS:        ts,
			App:       "patreon",
			Topic:     "post_views_daily_observed",
			ProjectID: "p1",
			Source:    "track",
			Props:     `{"post_id":"post_1","views":` + views + `}`,
		}); err != nil {
			t.Fatalf("insert aggregate: %v", err)
		}
	}
	rows, err := queryRows(db, Filter{ProjectID: "p1", App: "patreon", Topic: "post_views_daily_observed"}, 10)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].UpsertKey == "" {
		t.Fatal("expected auto upsert key")
	}
	buckets, err := sumByValue(db, Filter{ProjectID: "p1", App: "patreon", Topic: "post_views_daily_observed"}, "props.views", []string{"props.post_id"}, 10)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if len(buckets) != 1 || buckets[0]["sum"] != 15.0 {
		t.Fatalf("sum buckets = %#v, want one bucket sum 15", buckets)
	}
}

func TestEventSpecRawPlusRollupPolicyCountsDailyEvents(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID:      "p1",
		App:            "site",
		Topic:          "page_viewed",
		Kind:           "occurrence",
		Status:         "active",
		ValidationMode: "warn",
		IngestMode:     "raw_plus_rollup",
		RollupPolicy: &EventIngestPolicy{
			TargetTopic:    "page_viewed_daily_rollup",
			Bucket:         "day",
			Operation:      "increment",
			Value:          1,
			OutputProperty: "views",
			Dimensions:     []string{"props.path"},
		},
		Properties: []EventPropertySpec{
			{Key: "props.path", Type: "string", Required: true},
		},
	}, true)
	if err != nil {
		t.Fatalf("upsert spec: %v", err)
	}
	ts := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 2; i++ {
		if _, err := insertEvent(db, EventInsert{
			TS:        ts,
			App:       "site",
			Topic:     "page_viewed",
			ProjectID: "p1",
			Source:    "track",
			Props:     `{"path":"/pricing"}`,
		}); err != nil {
			t.Fatalf("insert page view: %v", err)
		}
	}
	rawCount, err := countEvents(db, Filter{ProjectID: "p1", App: "site", Topic: "page_viewed"})
	if err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if rawCount != 2 {
		t.Fatalf("rawCount = %d, want 2", rawCount)
	}
	rollups, err := queryRows(db, Filter{ProjectID: "p1", App: "site", Topic: "page_viewed_daily_rollup"}, 10)
	if err != nil {
		t.Fatalf("query rollups: %v", err)
	}
	if len(rollups) != 1 {
		t.Fatalf("rollup rows = %d, want 1", len(rollups))
	}
	buckets, err := sumByValue(db, Filter{ProjectID: "p1", App: "site", Topic: "page_viewed_daily_rollup"}, "props.views", []string{"props.path"}, 10)
	if err != nil {
		t.Fatalf("sum rollup: %v", err)
	}
	if len(buckets) != 1 || buckets[0]["sum"] != 2.0 {
		t.Fatalf("rollup sum buckets = %#v, want one bucket sum 2", buckets)
	}
}
