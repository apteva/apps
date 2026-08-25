package main

import (
	"database/sql"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func objectiveFixture(name, aggregation string) ObjectiveWrite {
	query := ObjectiveMetricQuery{Aggregation: aggregation, App: "billing", Topic: "payment_received"}
	unit := "count"
	currency := ""
	if oneOf(aggregation, "sum", "average", "min", "max", "latest", "change", "avg") {
		query.Value = "props.amount_usd"
		unit, currency = "money", "USD"
	}
	if aggregation == "distinct" {
		query.By = "props.subscriber_id"
	}
	return ObjectiveWrite{
		Name: name, Status: "active",
		Targets: []ObjectiveTarget{{
			Name: "August target", MetricKey: "custom", TargetValue: 2, Unit: unit, Currency: currency,
			Direction: "at_least", PeriodStart: 1_000, PeriodEnd: 5_000, Timezone: "UTC", Query: query,
		}},
	}
}

func insertObjectiveEvent(t *testing.T, db *sql.DB, project string, ts int64, props string) {
	t.Helper()
	if _, err := insertEvent(db, EventInsert{TS: ts, App: "billing", Topic: "payment_received", ProjectID: project, Source: "track", Props: props}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

func TestObjectiveProgressUsesStoredProjectScopedAnalyticsData(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	db := ctx.AppDB()
	insertObjectiveEvent(t, db, "h-sites", 2_000, `{"amount_usd":12.5,"subscriber_id":"a"}`)
	insertObjectiveEvent(t, db, "h-sites", 3_000, `{"amount_usd":7.5,"subscriber_id":"a"}`)
	insertObjectiveEvent(t, db, "h-sites", 4_000, `{"amount_usd":5,"subscriber_id":"b"}`)
	insertObjectiveEvent(t, db, "h-sites", 5_000, `{"amount_usd":999,"subscriber_id":"outside-period"}`)
	insertObjectiveEvent(t, db, "other-project", 2_000, `{"amount_usd":999,"subscriber_id":"outside-project"}`)

	for _, tc := range []struct {
		aggregation string
		want        float64
	}{
		{aggregation: "count", want: 3},
		{aggregation: "sum", want: 25},
		{aggregation: "distinct", want: 2},
		{aggregation: "average", want: 25.0 / 3.0},
		{aggregation: "avg", want: 25.0 / 3.0},
		{aggregation: "min", want: 5},
		{aggregation: "max", want: 12.5},
		{aggregation: "latest", want: 5},
		{aggregation: "change", want: -7.5},
	} {
		o, err := createObjective(db, "h-sites", objectiveFixture(tc.aggregation, tc.aggregation))
		if err != nil {
			t.Fatalf("create %s objective: %v", tc.aggregation, err)
		}
		progress, err := evaluateObjective(db, "h-sites", o.ID)
		if err != nil {
			t.Fatalf("evaluate %s objective: %v", tc.aggregation, err)
		}
		if len(progress) != 1 || progress[0].ActualValue == nil || *progress[0].ActualValue != tc.want || progress[0].Status != "ok" {
			t.Fatalf("%s progress = %#v, want actual %v and ok", tc.aggregation, progress, tc.want)
		}
	}
}

func TestMoneyObjectiveUsesSameReadOnlyAggregateAsDashboard(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	db := ctx.AppDB()
	if _, err := upsertFXRate(db, "h-sites", FXRate{BaseCurrency: "USD", QuoteCurrency: "EUR", AsOf: 1_000, Rate: 0.5, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	insertObjectiveEvent(t, db, "h-sites", 2_000, `{"amount_cents":10000,"currency":"USD"}`)
	insertObjectiveEvent(t, db, "h-sites", 3_000, `{"amount_cents":2500,"currency":"EUR"}`)
	in := ObjectiveWrite{Name: "EUR revenue", Status: "active", Targets: []ObjectiveTarget{{
		Name: "Revenue target", MetricKey: "money_sum", TargetValue: 70, Unit: "money", Currency: "EUR",
		Direction: "at_least", PeriodStart: 1_000, PeriodEnd: 5_000, Timezone: "UTC",
		Query: ObjectiveMetricQuery{Aggregation: "sum_money", App: "billing", Topic: "payment_received", Value: "props.amount_cents", CurrencyField: "props.currency", ReportingCurrency: "EUR", AmountUnit: "minor"},
	}}}
	objective, err := createObjective(db, "h-sites", in)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := evaluateObjective(db, "h-sites", objective.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) != 1 || progress[0].ActualValue == nil || *progress[0].ActualValue != 75 || !progress[0].Achieved {
		t.Fatalf("progress=%#v want 75 EUR achieved", progress)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id='h-sites'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("source event count=%d err=%v", rows, err)
	}
}

func TestDashboardGoalLinksAreProjectScopedAndReadOnly(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	db := ctx.AppDB()
	insertObjectiveEvent(t, db, "h-sites", 2_000, `{"amount_usd":10}`)
	insertObjectiveEvent(t, db, "h-sites", 3_000, `{"amount_usd":14}`)
	objective, err := createObjective(db, "h-sites", objectiveFixture("MRR goal", "latest"))
	if err != nil {
		t.Fatal(err)
	}
	targetID := objective.Targets[0].ID
	dashboard, err := createDashboard(db, "h-sites", "Finance", "", nil, []DashboardWidget{{
		Type: "stat", Title: "MRR", Config: map[string]any{
			"app": "billing", "topic": "payment_received", "aggregation": "latest", "value": "props.amount_usd",
			"objective_target_ids": []int64{targetID},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDashboardGoalLinks(db, "h-sites", dashboard.Widgets[0].Config); err != nil {
		t.Fatalf("valid goal link: %v", err)
	}
	if err := validateDashboardGoalLinks(db, "other-project", dashboard.Widgets[0].Config); err == nil {
		t.Fatal("cross-project goal link should fail")
	}
	goals, goalErrors, err := dashboardGoalsForWidgets(db, "h-sites", dashboard.Widgets)
	if err != nil {
		t.Fatal(err)
	}
	if goalErrors[dashboard.Widgets[0].ID] != "" || len(goals[dashboard.Widgets[0].ID]) != 1 {
		t.Fatalf("goals=%#v errors=%#v", goals, goalErrors)
	}
	progress := goals[dashboard.Widgets[0].ID][0]
	if progress.ActualValue == nil || *progress.ActualValue != 14 || progress.ObjectiveName != "MRR goal" || progress.Status != "ok" {
		t.Fatalf("goal progress=%#v", progress)
	}
	var cached int
	if err := db.QueryRow(`SELECT COUNT(*) FROM objective_progress WHERE target_id=?`, targetID).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Fatalf("dashboard read wrote %d objective progress row(s)", cached)
	}
}

func TestObjectiveQueryWhereAndDirections(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	db := ctx.AppDB()
	insertObjectiveEvent(t, db, "h-sites", 2_000, `{"amount_usd":12,"site":"alpha"}`)
	insertObjectiveEvent(t, db, "h-sites", 3_000, `{"amount_usd":8,"site":"beta"}`)
	in := objectiveFixture("Filtered revenue", "sum")
	in.Targets[0].TargetValue = 15
	in.Targets[0].Direction = "at_most"
	in.Targets[0].Query.Where = map[string]any{"props.site": "alpha"}
	o, err := createObjective(db, "h-sites", in)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := evaluateObjective(db, "h-sites", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := *progress[0].ActualValue; got != 12 || !progress[0].Achieved || *progress[0].ProgressPct != 100 {
		t.Fatalf("progress = %#v, want 12 achieved at_most", progress[0])
	}
}

func TestObjectiveRejectsSpoofedScopeAndInvalidQueries(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}
	in := objectiveFixture("Revenue", "sum")
	rawTargets := []any{map[string]any{
		"name": "Revenue", "target_value": 100, "unit": "money", "currency": "USD", "direction": "at_least",
		"period_start": 1000, "period_end": 5000, "timezone": "UTC",
		"query": map[string]any{"aggregation": "sum", "value": "props.amount_usd"},
	}}
	if _, err := app.toolObjectiveCreate(ctx, map[string]any{"project_id": "other", "name": "Revenue", "targets": rawTargets}); err == nil || !strings.Contains(err.Error(), "project_id is assigned by the platform") {
		t.Fatalf("spoof error = %v", err)
	}

	in.Targets[0].Query.ProjectID = "other"
	if _, err := createObjective(ctx.AppDB(), "h-sites", in); err == nil || !strings.Contains(err.Error(), "assigned by the objective target") {
		t.Fatalf("query scope error = %v", err)
	}
	in = objectiveFixture("Bad sum", "sum")
	in.Targets[0].Query.Value = "props.amount); DROP TABLE events;--"
	if _, err := createObjective(ctx.AppDB(), "h-sites", in); err == nil || !strings.Contains(err.Error(), "numeric event field") {
		t.Fatalf("unsafe value error = %v", err)
	}
	in = objectiveFixture("Bad where", "count")
	in.Targets[0].Query.Where = map[string]any{"project_id": "other"}
	if _, err := createObjective(ctx.AppDB(), "h-sites", in); err == nil || !strings.Contains(err.Error(), "must be props.X") {
		t.Fatalf("unsafe where error = %v", err)
	}
}

func TestObjectiveProgressRetainsLastGoodValueOnQueryError(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	db := ctx.AppDB()
	insertObjectiveEvent(t, db, "h-sites", 2_000, `{"amount_usd":10}`)
	o, err := createObjective(db, "h-sites", objectiveFixture("Revenue", "sum"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := evaluateObjective(db, "h-sites", o.ID)
	if err != nil || first[0].ActualValue == nil || *first[0].ActualValue != 10 {
		t.Fatalf("first progress = %#v, err=%v", first, err)
	}
	insertObjectiveEvent(t, db, "h-sites", 3_000, `{"amount_usd":"not-a-number"}`)
	second, err := evaluateObjective(db, "h-sites", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Status != "error" || second[0].ActualValue == nil || *second[0].ActualValue != 10 || !strings.Contains(second[0].Error, "non-numeric") {
		t.Fatalf("stale progress = %#v, want error with last good value 10", second[0])
	}
}

func TestObjectiveUpdateArchiveAndProjectIsolation(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	db := ctx.AppDB()
	o, err := createObjective(db, "h-sites", objectiveFixture("Old name", "count"))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updateObjective(db, "h-sites", o.ID, ObjectiveWrite{Name: "New name", Status: "paused", Targets: nil})
	if err != nil || updated.Name != "New name" || len(updated.Targets) != 1 {
		t.Fatalf("updated = %#v, err=%v", updated, err)
	}
	if _, err := getObjective(db, "other-project", o.ID); !errorsIsNoRows(err) {
		t.Fatalf("cross-project get error = %v, want no rows", err)
	}
	if err := archiveObjective(db, "other-project", o.ID); !errorsIsNoRows(err) {
		t.Fatalf("cross-project archive error = %v, want no rows", err)
	}
	if err := archiveObjective(db, "h-sites", o.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := listObjectives(db, "h-sites", "", "", false, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("active rows after archive = %#v, err=%v", rows, err)
	}
}

func errorsIsNoRows(err error) bool { return err == sql.ErrNoRows }
