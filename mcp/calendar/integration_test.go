//go:build integration

package main

// Tier 2 — boot the real sidecar binary, talk MCP + REST. Validates
// SDK wiring (manifest parse, migrations, JSON-RPC dispatch, route
// mounting) end-to-end. Same pattern as crm/storage/jobs/tasks/status.
//
// Run with:  go test -tags integration ./...

import (
	"encoding/json"
	"strconv"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

func TestSidecar_FullCalendarFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// 1. Create a calendar via MCP.
	cal := sc.MCP("calendars_create", map[string]any{
		"name":  "Personal",
		"color": "#22c55e",
		"kind":  "personal",
	})
	calID := int64(cal["id"].(float64))
	if calID == 0 {
		t.Fatalf("calendars_create returned no id: %#v", cal)
	}

	// 2. Create a recurring event via MCP.
	event := sc.MCP("events_create", map[string]any{
		"calendar_id": calID,
		"title":       "Team standup",
		"start_at":    "2026-05-04T09:00:00Z",
		"end_at":      "2026-05-04T09:15:00Z",
		"rrule":       "FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=9",
	})
	eventID := int64(event["id"].(float64))
	if eventID == 0 {
		t.Fatalf("events_create returned no id: %#v", event)
	}

	// 3. List occurrences across three weeks via MCP. Result is a
	// JSON array; the testkit unwrap drops it into {text: "<json>"}
	// when the result isn't an object.
	listed := sc.MCP("events_list", map[string]any{
		"from": "2026-05-04T00:00:00Z",
		"to":   "2026-05-25T00:00:00Z",
	})
	var occ []map[string]any
	if text, ok := listed["text"].(string); ok {
		var wrap map[string]any
		if err := json.Unmarshal([]byte(text), &wrap); err == nil {
			if e, ok := wrap["events"].([]any); ok {
				for _, x := range e {
					occ = append(occ, x.(map[string]any))
				}
			}
		}
	} else if e, ok := listed["events"].([]any); ok {
		for _, x := range e {
			occ = append(occ, x.(map[string]any))
		}
	}
	if len(occ) != 9 {
		t.Errorf("expected 9 weekly occurrences, got %d", len(occ))
	}

	// 4. Read same data via REST.
	var rest map[string]any
	resp := sc.GET("/items?from=2026-05-04T00:00:00Z&to=2026-05-25T00:00:00Z", &rest)
	if resp.Status != 200 {
		t.Fatalf("REST GET: status=%d body=%s", resp.Status, resp.Body)
	}
	if e, ok := rest["events"].([]any); !ok || len(e) != 9 {
		t.Errorf("REST list mismatch: got %d events", len(e))
	}

	// 5. find_slot via REST should avoid the 9:00 standup window.
	var slotsResp map[string]any
	resp = sc.POST("/find_slot", map[string]any{
		"duration_minutes": 30,
		"window_start":     "2026-05-04T08:00:00Z",
		"window_end":       "2026-05-04T18:00:00Z",
		"working_hours": map[string]any{
			"mon": map[string]any{"start": "09:00", "end": "18:00"},
		},
		"limit": 5,
	}, &slotsResp)
	if resp.Status != 200 {
		t.Fatalf("find_slot: %d body=%s", resp.Status, resp.Body)
	}
	slots := slotsResp["slots"].([]any)
	if len(slots) == 0 {
		t.Errorf("find_slot returned no slots")
	}

	// 6. Holidays helper.
	hol := sc.MCP("holidays_set", map[string]any{
		"year":    2026,
		"country": "FR",
	})
	if int(hol["created"].(float64)) == 0 {
		t.Errorf("holidays_set didn't create any: %#v", hol)
	}

	// 7. Delete the event via REST.
	delResp := sc.DELETE("/items/" + strconv.FormatInt(eventID, 10))
	if delResp.Status != 204 {
		t.Errorf("DELETE event = %d", delResp.Status)
	}
}

func TestSidecar_RejectsBadRRule(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	cal := sc.MCP("calendars_create", map[string]any{"name": "x"})
	calID := int64(cal["id"].(float64))
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "events_create",
		"arguments": map[string]any{
			"calendar_id": calID,
			"title":       "bad",
			"start_at":    "2026-05-04T09:00:00Z",
			"end_at":      "2026-05-04T09:15:00Z",
			"rrule":       "FREQ=NOPE", // unsupported FREQ
		},
	})
	if err == nil {
		t.Errorf("expected JSON-RPC error envelope for bad rrule")
	}
}
