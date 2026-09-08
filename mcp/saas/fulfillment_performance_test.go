package main

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFulfillmentWarmMountSkipsLargeHistory(t *testing.T) {
	_, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO saas_plan_actions(project_id,plan_key,event,app_name,tool_name) VALUES('proj-test','free','account_active','test','test')`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"result": strings.Repeat("x", 1024)})
	if _, err := db.Exec(`WITH RECURSIVE nums(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM nums WHERE x<10000)
		INSERT INTO saas_fulfillment_runs(project_id,account_id,plan_action_id,transition_id,event,app_name,tool_name,status,input_json,output_json)
		SELECT 'proj-test','a',1,CAST(x AS TEXT),'account_active','test','test','succeeded',?,? FROM nums`, string(raw), string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := scrubFulfillmentHistory(db); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	if err := scrubFulfillmentHistory(db); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("warm mount: 10000 rows, elapsed=%s allocated_bytes=%d", elapsed, allocated)
	// The old implementation allocated ~405 MB. Keep this loose enough for
	// runtime bookkeeping while catching accidental full-history processing.
	if allocated > 1<<20 {
		t.Fatalf("warm mount rescanned history: allocated %d bytes", allocated)
	}
}
