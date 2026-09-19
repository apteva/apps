package main

import (
	"testing"
)

func TestCalculatedMetricEvaluatesSourceReferencesAndArithmetic(t *testing.T) {
	db := testDashboardDB(t)
	for _, event := range []EventInsert{
		{TS: 1000, App: "finance", Topic: "revenue", ProjectID: "p1", Source: "test", Props: `{"amount":100}`},
		{TS: 1000, App: "finance", Topic: "expense", ProjectID: "p1", Source: "test", Props: `{"amount":40}`},
	} {
		if _, err := insertEvent(db, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := upsertMetricDefinition(db, MetricDefinition{ProjectID: "p1", Key: "revenue", Label: "Revenue", Expression: map[string]any{"source": map[string]any{"app": "finance", "topic": "revenue", "value": "props.amount", "aggregation": "sum"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertMetricDefinition(db, MetricDefinition{ProjectID: "p1", Key: "expenses", Label: "Expenses", Expression: map[string]any{"source": map[string]any{"app": "finance", "topic": "expense", "value": "props.amount", "aggregation": "sum"}}}); err != nil {
		t.Fatal(err)
	}
	profit, err := upsertMetricDefinition(db, MetricDefinition{ProjectID: "p1", Key: "profit", Label: "Profit", Expression: map[string]any{"op": "subtract", "left": map[string]any{"metric": "revenue"}, "right": map[string]any{"metric": "expenses"}}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := evaluateMetric(db, "p1", profit, Filter{ProjectID: "p1"}, map[string]bool{})
	if err != nil || value != 60 {
		t.Fatalf("value=%v err=%v", value, err)
	}
}
