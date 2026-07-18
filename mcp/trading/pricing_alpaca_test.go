package main

import (
	"encoding/json"
	"testing"
)

func TestParseAlpacaSnapshotsUsesLatestTradeTimestamp(t *testing.T) {
	raw := json.RawMessage(`{
		"AAPL": {
			"latestTrade": {"p": 211.5, "t": "2026-07-14T15:45:00Z"},
			"minuteBar": {"c": 211.4, "t": "2026-07-14T15:44:00Z"}
		}
	}`)
	marks, err := parseAlpacaSnapshots(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 {
		t.Fatalf("marks = %d, want 1", len(marks))
	}
	if marks[0].MarkedAt != "2026-07-14T15:45:00Z" {
		t.Fatalf("marked_at = %q, want latest trade timestamp", marks[0].MarkedAt)
	}
}
