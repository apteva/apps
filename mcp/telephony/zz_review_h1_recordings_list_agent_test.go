package main

import (
	"context"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// Agent filtering happens before LIMIT: an agent's older recordings are still
// returned when newer recordings belong to other agents.
func TestReviewH1RecordingsListFiltersAgentBeforeLimit(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	for _, c := range []struct {
		id    string
		agent int64
	}{{"mine", 7}, {"theirs", 8}} {
		call := testCall(c.id, "completed")
		call.ProjectID = "project-a"
		call.AgentID = c.agent
		if err := app.db().insertCall(call); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		if _, err := app.db().db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,storage_status,created_at) VALUES('old-mine','mine','project-a','twilio',1,'old-mine','stored','2026-01-01T00:00:00Z')`)
	for i := 0; i < 5; i++ {
		id := fmt.Sprint("new-theirs-", i)
		exec(`INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,storage_status,created_at) VALUES(?,'theirs','project-a','twilio',1,?,'stored',?)`, id, id, fmt.Sprintf("2026-02-0%dT00:00:00Z", i+1))
	}
	caller := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})
	result, err := app.toolRecordingsList(caller, ctx, map[string]any{"limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	list := result.(map[string]any)["recordings"].([]map[string]any)
	if len(list) != 1 || list[0]["id"] != "old-mine" {
		t.Fatalf("recordings=%v", list)
	}
}
