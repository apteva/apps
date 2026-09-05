package main

import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"testing"
	"time"
)

func TestAuditPerfSmoke(t *testing.T) {
	db := testDashboardDB(t)
	db.SetMaxOpenConns(1)
	_, err := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000)
 INSERT INTO events(ts,app,topic,project_id,source,props)
 SELECT 1700000000000+x*1000,'site','sale','p1','track','{"amount":100,"currency":"EUR"}' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"window": "all", "aggregation": "sum_money", "value": "props.amount", "currency_field": "props.currency", "reporting_currency": "EUR", "amount_unit": "major"}
	for _, n := range []int{1, 6} {
		plan := newEvaluationPlan(db)
		start := time.Now()
		for i := 0; i < n; i++ {
			_, err := evaluateWidget(plan, "p1", DashboardWidget{Type: "stat", Config: cfg}, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("100,000 rows, %d identical money stat widget(s): %s", n, time.Since(start))
	}
	start := time.Now()
	_, err = seriesForWidget(db, Filter{ProjectID: "p1"}, "minute", "props.amount", "", "latest")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("100,000 rows, latest-per-minute series: %s", time.Since(start))
}

func TestAuditNoDataBecomesAchieved(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	in := objectiveFixture("Maximum latency", "latest")
	in.Targets[0].Direction = "at_most"
	in.Targets[0].TargetValue = 100
	o, err := createObjective(ctx.AppDB(), "p1", in)
	if err != nil {
		t.Fatal(err)
	}
	p := measureObjectiveTarget(ctx.AppDB(), "p1", o.Targets[0], false)
	if p.Achieved {
		t.Errorf("latest with no observations returns actual=%v and achieved=true", *p.ActualValue)
	}
}

func TestAuditNonFinitePolicySilentlyEmptiesProps(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "site", Topic: "click", IngestMode: "upsert", UpsertPolicy: &EventIngestPolicy{Operation: "sum", Value: "NaN"}}, true)
	if err != nil {
		return
	}
	id, err := insertEvent(db, EventInsert{TS: 1000, ProjectID: "p1", App: "site", Topic: "click", Source: "track", Props: `{"original":true}`})
	if err != nil {
		return
	}
	var raw string
	if err = db.QueryRow(`SELECT props FROM events WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	t.Errorf("non-finite configured value returns success and loses props: %s", raw)
}

func TestAuditReferenceLabelEditReactivates(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertReferenceSet(db, ReferenceSet{ProjectID: "p1", Key: "sites"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upsertReferenceValue(db, "p1", "sites", ReferenceValue{Value: "old", Status: "inactive", Metadata: json.RawMessage(`{"owner":"a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := upsertReferenceValue(db, "p1", "sites", ReferenceValue{Value: "old", Label: "New label"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "inactive" {
		t.Errorf("label-only update reactivates reference and erases metadata: status=%s metadata=%s", got.Status, got.Metadata)
	}
}
