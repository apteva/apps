package main

import (
	"fmt"
	"strings"
	"sync"
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

func TestPolicyTimestampPropertyControlsStoredDayAndUpsertKey(t *testing.T) {
	db := testDashboardDB(t)
	saved, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "patreon", Topic: "daily_membership_snapshot",
		Kind: "aggregate_observation", Status: "active", ValidationMode: "reject", IngestMode: "upsert",
		UpsertPolicy: &EventIngestPolicy{
			Bucket: "day", Timezone: "UTC", Operation: "replace",
			Value: "props.paid_members", OutputProperty: "paid_members", Dimensions: []string{"props.page_id"},
		},
		Properties: []EventPropertySpec{
			{Key: "props.date", Type: "string", Required: true},
			{Key: "props.page_id", Type: "string", Required: true},
			{Key: "props.paid_members", Type: "number", Required: true},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if saved.UpsertPolicy == nil || saved.UpsertPolicy.TimestampProperty != "props.date" {
		t.Fatalf("default timestamp property = %#v, want props.date", saved.UpsertPolicy)
	}
	for i, paid := range []int{10, 12} {
		_, err := insertEvent(db, EventInsert{
			TS:  time.Date(2026, 7, 10+i, 15, 0, 0, 0, time.UTC).UnixMilli(),
			App: "patreon", Topic: "daily_membership_snapshot", ProjectID: "p1", Source: "track",
			Props: fmt.Sprintf(`{"date":"2026-06-16","page_id":"page-a","paid_members":%d}`, paid),
		})
		if err != nil {
			t.Fatalf("insert delayed snapshot: %v", err)
		}
	}
	rows, err := queryRows(db, Filter{ProjectID: "p1", App: "patreon", Topic: "daily_membership_snapshot"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("snapshot rows=%d want 1", len(rows))
	}
	wantTS := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC).UnixMilli()
	if rows[0].TS != wantTS || !strings.Contains(rows[0].UpsertKey, "day=2026-06-16") {
		t.Fatalf("stored snapshot ts=%d key=%q, want date-controlled bucket", rows[0].TS, rows[0].UpsertKey)
	}
}

func TestRawPlusRollupFailureRollsBackRawEvent(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "site", Topic: "page_viewed", Status: "active", ValidationMode: "warn", IngestMode: "raw_plus_rollup",
		RollupPolicy: &EventIngestPolicy{
			TargetTopic: "page_viewed_rollup", Bucket: "day", Operation: "increment", Value: 1,
			OutputProperty: "views", Dimensions: []string{"props.path"},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertEvent(db, EventInsert{
		TS: time.Now().UnixMilli(), App: "site", Topic: "page_viewed", ProjectID: "p1", Source: "track", Props: `{}`,
	}); err == nil {
		t.Fatal("expected missing rollup dimension to fail")
	}
	count, err := countEvents(db, Filter{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial raw event remained after rollup failure: count=%d", count)
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_spec_violations`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if violations != 0 {
		t.Fatalf("partial validation violations remained after rollup failure: count=%d", violations)
	}
}

func TestConcurrentRollupIncrementsAreAtomic(t *testing.T) {
	db := testDashboardDB(t)
	db.SetMaxOpenConns(1)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "site", Topic: "page_viewed", Status: "active", ValidationMode: "reject", IngestMode: "raw_plus_rollup",
		RollupPolicy: &EventIngestPolicy{
			TargetTopic: "page_viewed_rollup", Bucket: "day", Operation: "increment", Value: 1,
			OutputProperty: "views", Dimensions: []string{"props.path"},
		},
		Properties: []EventPropertySpec{{Key: "props.path", Type: "string", Required: true}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	const writes = 40
	start := make(chan struct{})
	errs := make(chan error, writes)
	var wg sync.WaitGroup
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := insertEvent(db, EventInsert{
				TS:  time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC).UnixMilli(),
				App: "site", Topic: "page_viewed", ProjectID: "p1", Source: "track", Props: `{"path":"/pricing"}`,
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent insert: %v", err)
		}
	}
	buckets, err := sumByValue(db, Filter{ProjectID: "p1", App: "site", Topic: "page_viewed_rollup"}, "props.views", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0]["sum"] != float64(writes) {
		t.Fatalf("rollup buckets=%#v want sum %d", buckets, writes)
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
