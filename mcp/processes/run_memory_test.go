package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func memoryRun(t *testing.T, a *App, p *Process, key string) Run {
	t.Helper()
	r, e := a.reserveRun(p, "manual", key, "")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func mcpCall(t *testing.T, a *App, p *Process, action string, args map[string]any) map[string]any {
	t.Helper()
	args["process_id"] = p.ID
	v, e := a.executeMCP(p.ProjectID, "agent:7:owner", action, args)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(v)
	var result map[string]any
	if e = json.Unmarshal(b, &result); e != nil {
		t.Fatal(e)
	}
	return result
}
func TestCompactHistorySizePagingFiltersAndExactMemory(t *testing.T) {
	a, _ := setup(t)
	d := def()
	d.SummaryFields = []SummaryField{{Key: "campaign", Label: "Campaign"}, {Key: "qualified", Label: "Qualified leads"}}
	p := create(t, a, d)
	ids := map[string]bool{}
	for i := 0; i < 48; i++ {
		r := memoryRun(t, a, p, fmt.Sprint(i))
		ids[r.ID] = true
		if _, e := a.db.Exec(`UPDATE process_runs SET result=?,state='completed',created_at='2026-10-01T12:00:00Z' WHERE id=?`, strings.Repeat("exact evidence ", 10000), r.ID); e != nil {
			t.Fatal(e)
		}
		mcpCall(t, a, p, "summary_update", map[string]any{"run_id": r.ID, "expected_revision": float64(0), "idempotency_key": "first", "summary": map[string]any{"outcome": strings.Repeat("done ", 500), "references": []string{"catalog:9007199254740993"}, "fields": map[string]any{"campaign": "October", "qualified": 3}}})
	}
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	maxPage := 0
	for {
		args := map[string]any{"limit": float64(50), "cursor": cursor, "status": "completed", "assignment_id": "assignment-" + p.ID, "after": "2026-10-01T00:00:00Z", "before": "2026-10-02T00:00:00Z"}
		page := mcpCall(t, a, p, "runs", args)
		b, _ := json.Marshal(page)
		if len(b) > maxPage {
			maxPage = len(b)
		}
		if len(b) > 16*1024 {
			t.Fatalf("history exceeded budget: %d", len(b))
		}
		if strings.Contains(string(b), "exact evidence exact evidence") || page["direct_runs"] != nil || page["dispatches"] != nil {
			t.Fatal("nested/duplicate history returned")
		}
		rows := page["runs"].([]any)
		for _, raw := range rows {
			row := raw.(map[string]any)
			id := row["id"].(string)
			if seen[id] || !ids[id] {
				t.Fatal("lost or repeated ID")
			}
			seen[id] = true
			if !row["summary_available"].(bool) {
				t.Fatal("missing checkpoint")
			}
			ref := row["reread"].(map[string]any)
			full := mcpCall(t, a, p, "run_get", ref["args"].(map[string]any))
			if full["complete"] != false {
				t.Fatal("large run should be deferred")
			}
		}
		pages++
		cursor, _ = page["next_cursor"].(string)
		if page["has_more"] == false {
			break
		}
		if len(rows) == 0 {
			t.Fatal("pagination made no progress")
		}
	}
	legacy, e := a.runs(p.ProjectID, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	fullBytes, _ := json.Marshal(legacy)
	t.Logf("Full legacy history: %d bytes; largest compact page: %d bytes; %d pages, 48 exact run IDs", len(fullBytes), maxPage, pages)
	if len(seen) != 48 || pages < 2 {
		t.Fatalf("got %d rows in %d pages", len(seen), pages)
	}
	page := mcpCall(t, a, p, "runs", map[string]any{"limit": float64(1)})
	cursor = page["next_cursor"].(string)
	if _, e := a.executeMCP(p.ProjectID, "agent:7:owner", "runs", map[string]any{"process_id": p.ID, "cursor": cursor, "status": "failed"}); e == nil {
		t.Fatal("cursor filter mismatch accepted")
	}
	if _, e := a.executeMCP("other", "agent:7:owner", "runs", map[string]any{"process_id": p.ID}); e == nil {
		t.Fatal("project leak")
	}
	empty := mcpCall(t, a, p, "runs", map[string]any{"status": "failed"})
	if len(empty["runs"].([]any)) != 0 {
		t.Fatal("status filter failed")
	}
}
func TestRunCheckpointRecoveryIdempotencyAndPermissions(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	r := memoryRun(t, a, p, "one")
	missing := mcpCall(t, a, p, "summary_get", map[string]any{"run_id": r.ID})
	if missing["available"] != false {
		t.Fatal("invented old summary")
	}
	args := map[string]any{"run_id": r.ID, "expected_revision": float64(0), "idempotency_key": "checkpoint-1", "summary": map[string]any{"attempted": "Checked north district", "blocker": "Needs source access", "next_actions": "Search south after access", "references": []string{"catalog:9007199254740993"}}}
	first := mcpCall(t, a, p, "summary_update", args)
	retry := mcpCall(t, a, p, "summary_update", args)
	if jsonText(first) != jsonText(retry) {
		t.Fatal("retry altered revision")
	}
	if _, e := a.executeMCP(p.ProjectID, "agent:8:other", "summary_update", args); e == nil {
		t.Fatal("wrong owner wrote memory")
	}
	bad := map[string]any{"process_id": p.ID, "run_id": r.ID, "expected_revision": float64(0), "idempotency_key": "second", "summary": map[string]any{"outcome": "done", "references": []string{}}}
	if _, e := a.executeMCP(p.ProjectID, "agent:7:owner", "summary_update", bad); e != errConflict {
		t.Fatalf("stale revision accepted: %v", e)
	}
	bad["expected_revision"] = float64(1)
	bad["summary"] = map[string]any{"outcome": strings.Repeat("x", 4096)}
	if _, e := a.executeMCP(p.ProjectID, "agent:7:owner", "summary_update", bad); e == nil {
		t.Fatal("oversized memory accepted")
	}
	args["expected_revision"] = float64(1)
	args["idempotency_key"] = "checkpoint-2"
	args["summary"] = map[string]any{"completed": "North checked", "outcome": "Continue south", "references": []string{"catalog:9007199254740993"}}
	mcpCall(t, a, p, "summary_update", args)
	// New App object on the same persisted DB represents a context/restart recovery.
	recovered := &App{ctx: a.ctx, db: a.db}
	old := mcpCall(t, recovered, p, "summary_get", map[string]any{"run_id": r.ID, "revision": float64(1)})
	if !strings.Contains(jsonText(old), "Needs source access") || !strings.Contains(jsonText(old), "9007199254740993") {
		t.Fatal("checkpoint/evidence identity lost")
	}
	saved, e := a.getRun(p.ProjectID, p.ID, r.ID)
	if e != nil || saved.State != r.State || saved.Result != "" {
		t.Fatal("summary changed execution authority")
	}
}
func TestLedgerScopeSearchConcurrentUpdates(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	r := memoryRun(t, a, p, "first")
	args := map[string]any{"process_id": p.ID, "run_id": r.ID, "scope": "campaign-a", "key": "area:north", "expected_revision": float64(0), "entry": map[string]any{"kind": "searched", "content": "North was exhausted; do not repeat", "references": []string{"catalog:9007199254740993"}}}
	first := mcpCall(t, a, p, "memory_upsert", args)
	retry := mcpCall(t, a, p, "memory_upsert", args)
	if jsonText(first) != jsonText(retry) {
		t.Fatal("non-idempotent ledger")
	}
	scope := map[string]any{"assignment_id": r.AssignmentID, "scope": "campaign-a", "search": "exhausted", "kind": "searched"}
	page := mcpCall(t, a, p, "memory_list", scope)
	if len(page["entries"].([]any)) != 1 {
		t.Fatal("search failed")
	}
	scope["scope"] = "campaign-b"
	if len(mcpCall(t, a, p, "memory_list", scope)["entries"].([]any)) != 0 {
		t.Fatal("campaign leak")
	}
	other, e := a.saveAssignment(p.ProjectID, p.ID, "", 0, AssignmentConfig{Name: "Other", OwnerAgentID: 8, FollowLatest: true})
	if e != nil {
		t.Fatal(e)
	}
	scope["assignment_id"] = other.ID
	scope["scope"] = "campaign-a"
	if len(mcpCall(t, a, p, "memory_list", scope)["entries"].([]any)) != 0 {
		t.Fatal("assignment leak")
	}
	r2 := memoryRun(t, a, p, "second")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := a.executeMCP(p.ProjectID, "agent:7:owner", "memory_upsert", map[string]any{"process_id": p.ID, "run_id": r2.ID, "scope": "campaign-a", "key": "area:north", "expected_revision": float64(1), "entry": map[string]any{"kind": "searched", "content": fmt.Sprint("new checkpoint ", i), "references": []string{"catalog:9007199254740993"}}})
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if e == errConflict {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent overwrite lost revision")
	}
}
func TestExactEvidencePagesAndChangedHash(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	r := memoryRun(t, a, p, "large")
	exact := strings.Repeat("\"😀 <tag> 9007199254740993\n", 8000)
	if _, e := a.db.Exec(`UPDATE process_runs SET result=? WHERE id=?`, exact, r.ID); e != nil {
		t.Fatal(e)
	}
	full := mcpCall(t, a, p, "run_get", map[string]any{"run_id": r.ID})
	b, _ := json.Marshal(full)
	if len(b) > detailBudget || full["complete"] != false {
		t.Fatal("run budget/completeness invalid")
	}
	args := full["deferred_sections"].(map[string]any)["run"].(map[string]any)["reread"].(map[string]any)["args"].(map[string]any)
	var joined strings.Builder
	hash := ""
	for {
		page := mcpCall(t, a, p, "run_evidence", args)
		b, _ := json.Marshal(page)
		if len(b) > 16*1024 {
			t.Fatal("evidence page oversized")
		}
		joined.WriteString(page["data"].(string))
		hash = page["sha256"].(string)
		if page["complete"] == true {
			break
		}
		args = page["next"].(map[string]any)["args"].(map[string]any)
	}
	sum := sha256.Sum256([]byte(joined.String()))
	if hex.EncodeToString(sum[:]) != hash {
		t.Fatal("hash mismatch")
	}
	var saved Run
	if e := json.Unmarshal([]byte(joined.String()), &saved); e != nil || saved.Result != exact {
		t.Fatal("lossy evidence")
	}
	a.db.Exec(`UPDATE process_runs SET result='changed' WHERE id=?`, r.ID)
	if _, e := a.executeMCP(p.ProjectID, "agent:7:owner", "run_evidence", map[string]any{"process_id": p.ID, "run_id": r.ID, "section": "run", "sha256": hash}); e == nil {
		t.Fatal("mixed checkpoints accepted")
	}
	http, e := a.execute(p.ProjectID, "operator", "run_get", map[string]any{"process_id": p.ID, "run_id": r.ID})
	if e != nil || http.(map[string]any)["run"].(Run).Result != "changed" {
		t.Fatal("full HTTP state changed")
	}
}

func TestOversizedWorkerReadRetainsAllRequiredEvidence(t *testing.T) {
	a, _ := setup(t)
	d := def()
	d.Instructions = strings.Repeat("frozen-policy ", 5000)
	for i := 0; i < 30; i++ {
		s := Step{Key: fmt.Sprint("step", i), Name: fmt.Sprint("Step ", i), Role: "worker", Instructions: "Read exact ancestors", ExpectedOutput: "Exact receipt"}
		if i > 0 {
			s.DependsOn = []string{fmt.Sprint("step", i-1)}
		}
		d.Steps = append(d.Steps, s)
	}
	p := create(t, a, d)
	r := memoryRun(t, a, p, "worker")
	if e := a.initWorkflow(&r, d); e != nil {
		t.Fatal(e)
	}
	all, e := a.steps(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range all[:29] {
		if _, e = a.db.Exec(`UPDATE process_step_runs SET state='completed',output=?,updated_by='operator' WHERE id=?`, strings.Repeat("exact😀9007199254740993 ", 700), s.ID); e != nil {
			t.Fatal(e)
		}
	}
	response := mcpCall(t, a, p, "step_get", map[string]any{"run_id": r.ID, "step_id": all[29].ID})
	b, _ := json.Marshal(response)
	if len(b) > detailBudget || response["complete"] != false {
		t.Fatal("worker read not bounded", len(b))
	}
	deps := response["dependencies"].(map[string]any)
	if len(deps) != 29 {
		t.Fatal("ancestor manifest incomplete")
	}
	for _, raw := range deps {
		dep := raw.(map[string]any)
		args := dep["evidence"].(map[string]any)["reread"].(map[string]any)["args"].(map[string]any)
		page := mcpCall(t, a, p, "run_evidence", args)
		if !strings.Contains(page["data"].(string), "operator") { // first chunk may be occupied by definition; reconstruct all chunks below
			var joined strings.Builder
			for {
				joined.WriteString(page["data"].(string))
				if page["complete"] == true {
					break
				}
				page = mcpCall(t, a, p, "run_evidence", page["next"].(map[string]any)["args"].(map[string]any))
			}
			var s StepRun
			if e = json.Unmarshal([]byte(joined.String()), &s); e != nil || s.UpdatedBy != "operator" || !strings.Contains(s.Output, "9007199254740993") {
				t.Fatal("approval/evidence lost")
			}
		}
	}
	ref := response["context_evidence"].(map[string]any)["reread"].(map[string]any)
	page := mcpCall(t, a, p, "run_evidence", ref["args"].(map[string]any))
	var definitionJSON strings.Builder
	for {
		definitionJSON.WriteString(page["data"].(string))
		if page["complete"] == true {
			break
		}
		page = mcpCall(t, a, p, "run_evidence", page["next"].(map[string]any)["args"].(map[string]any))
	}
	var frozen struct {
		Instructions string `json:"instructions"`
		Steps        []Step `json:"steps"`
	}
	if e = json.Unmarshal([]byte(definitionJSON.String()), &frozen); e != nil || frozen.Instructions != d.Instructions || len(frozen.Steps) != 0 {
		t.Fatal("frozen policy reference broken", e)
	}
}

func TestHistoryExportGroupsAndValidation(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	r := memoryRun(t, a, p, "one")
	a.db.Exec(`UPDATE process_runs SET state='blocked',result='exact receipt',error='Access required' WHERE id=?`, r.ID)
	for _, status := range []string{"attention", "ongoing"} {
		page := mcpCall(t, a, p, "runs", map[string]any{"status": status})
		if len(page["runs"].([]any)) != 1 {
			t.Fatal("group filter lost run")
		}
	}
	export, e := a.execute(p.ProjectID, "operator", "runs", map[string]any{"process_id": p.ID, "view": "export", "limit": float64(1)})
	if e != nil {
		t.Fatal(e)
	}
	records := export.(map[string]any)["runs"].([]any)
	if len(records) != 1 || records[0].(map[string]any)["run"].(Run).Result != "exact receipt" {
		t.Fatal("export lost exact state")
	}
	for _, bad := range []map[string]any{{"limit": float64(0)}, {"limit": float64(51)}, {"limit": float64(1.5)}, {"after": "invalid"}, {"after": "2026-10-08T00:00:00Z", "before": "2026-10-01T00:00:00Z"}, {"cursor": "garbage"}, {"status": "nonsense"}} {
		bad["process_id"] = p.ID
		if _, e = a.executeMCP(p.ProjectID, "agent:7:owner", "runs", bad); e == nil {
			t.Fatal("invalid page option accepted", bad)
		}
	}
}

func TestRunMemoryMigrationPreservesHistoricalRows(t *testing.T) {
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "existing.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	files, e := os.ReadDir("migrations")
	if e != nil {
		t.Fatal(e)
	}
	for _, file := range files {
		if file.Name() >= "018_run_memory.sql" {
			continue
		}
		body, e := os.ReadFile(filepath.Join("migrations", file.Name()))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(body)); e != nil {
			t.Fatalf("%s: %v", file.Name(), e)
		}
	}
	if _, e = db.Exec(`INSERT INTO processes(id,project_id,created_at,updated_at) VALUES('process','project','created','updated'); INSERT INTO process_versions VALUES('process',1,'{"instructions":"frozen"}','operator','created'); INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,result,state) VALUES('run','process',1,'manual','key','created','Exact: 9007199254740993','completed')`); e != nil {
		t.Fatal(e)
	}
	before := func() string {
		var result, state, definition string
		if e := db.QueryRow(`SELECT r.result,r.state,v.body_json FROM process_runs r JOIN process_versions v ON v.process_id=r.process_id AND v.version=r.version WHERE r.id='run'`).Scan(&result, &state, &definition); e != nil {
			t.Fatal(e)
		}
		return result + state + definition
	}()
	migration, e := os.ReadFile("migrations/018_run_memory.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(migration)); e != nil {
		t.Fatal(e)
	}
	var result, state, definition string
	db.QueryRow(`SELECT r.result,r.state,v.body_json FROM process_runs r JOIN process_versions v ON v.process_id=r.process_id AND v.version=r.version WHERE r.id='run'`).Scan(&result, &state, &definition)
	if result+state+definition != before {
		t.Fatal("migration changed historical evidence")
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM process_run_summaries`).Scan(&count)
	if count != 0 {
		t.Fatal("migration invented checkpoints")
	}
}

func TestHistoryNanosecondDatesAndCursorOrdering(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	ids := []string{}
	for i, date := range []string{"2026-10-08T10:00:00Z", "2026-10-08T10:00:00.12Z", "2026-10-08T10:00:00.123Z"} {
		r := memoryRun(t, a, p, fmt.Sprint(i))
		ids = append(ids, r.ID)
		if _, e := a.db.Exec(`UPDATE process_runs SET created_at=? WHERE id=?`, date, r.ID); e != nil {
			t.Fatal(e)
		}
	}
	cursor := ""
	for i := 2; i >= 0; i-- {
		page := mcpCall(t, a, p, "runs", map[string]any{"limit": float64(1), "cursor": cursor})
		rows := page["runs"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["id"] != ids[i] {
			t.Fatal("fractional timestamp order changed")
		}
		cursor = page["next_cursor"].(string)
	}
	page := mcpCall(t, a, p, "runs", map[string]any{"after": "2026-10-08T12:00:00.120000001+02:00", "before": "2026-10-08T10:00:00.123Z"})
	rows := page["runs"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != ids[2] {
		t.Fatal("date normalization lost precision")
	}
}

func TestRunMemoryPublishesOnlyCommittedNewRevisions(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	r := memoryRun(t, a, p, "one")
	args := map[string]any{"run_id": r.ID, "expected_revision": float64(0), "idempotency_key": "one", "summary": map[string]any{"outcome": "Saved checkpoint", "references": []string{}}}
	mcpCall(t, a, p, "summary_update", args)
	mcpCall(t, a, p, "summary_update", args)
	ledger := map[string]any{"run_id": r.ID, "scope": "scope", "key": "key", "expected_revision": float64(0), "entry": map[string]any{"kind": "checked", "content": "Saved finding", "references": []string{}}}
	mcpCall(t, a, p, "memory_upsert", ledger)
	mcpCall(t, a, p, "memory_upsert", ledger)
	var count int
	a.db.QueryRow(`SELECT count(*) FROM process_event_outbox WHERE topic IN ('run.summary_updated','run.memory_updated')`).Scan(&count)
	if count != 2 {
		t.Fatal("duplicate or lost memory events", count)
	}
}

func TestHistoryExactNumericOwnerAndFrozenSummaryConfiguration(t *testing.T) {
	a, _ := setup(t)
	d := def()
	d.OwnerAgentID = 9007199254740993
	d.SummaryFields = []SummaryField{{Key: "campaign", Label: "Campaign"}}
	p := create(t, a, d)
	r := memoryRun(t, a, p, "one")
	page, e := a.executeMCP(p.ProjectID, "agent:7:reader", "runs", map[string]any{"process_id": p.ID})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(jsonText(page), `"owner_agent_id":9007199254740993`) {
		t.Fatal("numeric owner ID rounded")
	}
	d.SummaryFields = []SummaryField{{Key: "later", Label: "Future label"}}
	if _, e = a.save(p.ProjectID, p.ID, "operator", 1, d); e != nil {
		t.Fatal(e)
	}
	args := map[string]any{"process_id": p.ID, "run_id": r.ID, "expected_revision": float64(0), "idempotency_key": "one", "summary": map[string]any{"outcome": "Saved", "references": []string{}, "fields": map[string]any{"later": "not in frozen v1"}}}
	actor := "agent:9007199254740993:worker"
	if _, e = a.executeMCP(p.ProjectID, actor, "summary_update", args); e == nil {
		t.Fatal("run used current rather than frozen summary fields")
	}
	args["summary"] = map[string]any{"outcome": "Saved", "references": []string{}, "fields": map[string]any{"campaign": "Original version"}}
	if _, e = a.executeMCP(p.ProjectID, actor, "summary_update", args); e != nil {
		t.Fatal(e)
	}
}

func TestCompactHistoryIncludesDelegatedExecutorFilters(t *testing.T) {
	a, _ := setup(t)
	d := def()
	d.Steps = []Step{{Key: "research", Name: "Research", Role: "worker", Instructions: "Check source", ExpectedOutput: "Receipt"}}
	p := create(t, a, d)
	r := memoryRun(t, a, p, "one")
	if e := a.initWorkflow(&r, d); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(`UPDATE process_step_runs SET executor_json='{"kind":"agent","agent_id":8}' WHERE run_id=?`, r.ID); e != nil {
		t.Fatal(e)
	}
	page := mcpCall(t, a, p, "runs", map[string]any{"owner_agent_id": float64(8)})
	rows := page["runs"].([]any)
	if len(rows) != 1 || len(rows[0].(map[string]any)["executor_agent_ids"].([]any)) != 1 {
		t.Fatal("delegated owner filter lost metadata")
	}
	page = mcpCall(t, a, p, "runs", map[string]any{"owner_agent_id": float64(9)})
	if len(page["runs"].([]any)) != 0 {
		t.Fatal("wrong executor filter")
	}
}
