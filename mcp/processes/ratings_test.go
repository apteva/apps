package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func ratedWorkflow(t *testing.T, q *QualityConfig) (*App, *Process, Run) {
	t.Helper()
	a, _, _ := directSetup(t)
	d := workflowDefinition()
	d.Quality = q
	p := create(t, a, d)
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.Roles = map[string]Executor{"researcher": {Kind: "agent", AgentID: 8}, "writer": {Kind: "agent", AgentID: 7}, "reviewer": {Kind: "human"}, "publisher": {Kind: "agent", AgentID: 8}}
	if _, e := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); e != nil {
		t.Fatal(e)
	}
	p = status(t, a, p.ID, "active")
	raw, e := a.start(p.ProjectID, p.ID, "rated", "")
	if e != nil {
		t.Fatal(e)
	}
	return a, p, raw.(map[string]any)["run"].(Run)
}
func finishRun(t *testing.T, a *App, r Run, state string) {
	t.Helper()
	if _, e := a.db.Exec(`UPDATE process_step_runs SET state='completed' WHERE run_id=?`, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(`UPDATE process_runs SET state=? WHERE id=?`, state, r.ID); e != nil {
		t.Fatal(e)
	}
}
func rate(a *App, p *Process, actor string, args map[string]any) (map[string]any, error) {
	args["process_id"] = p.ID
	v, e := a.executeMCP(p.ProjectID, actor, "run_rate", args)
	if e != nil {
		return nil, e
	}
	b, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, nil
}

func TestRatingsValidateRevisionAndQueueReviews(t *testing.T) {
	a, p, r := ratedWorkflow(t, &QualityConfig{Tags: []string{"Wrong output", "Great"}, Criteria: []QualityCriterion{{Key: "accuracy", Label: "Accuracy", Type: "score"}, {Key: "on_time", Label: "On time", Type: "pass_fail"}, {Key: "tone", Label: "Tone", Type: "choice", Options: []string{"formal", "casual"}}}})
	if _, e := rate(a, p, "agent:9:main", map[string]any{"run_id": r.ID, "score": float64(4), "idempotency_key": "early"}); e == nil || !strings.Contains(e.Error(), "finished") {
		t.Fatalf("live run accepted a rating: %v", e)
	}
	finishRun(t, a, r, "completed")
	write := stepBy(t, a, r, "write")
	for _, bad := range []map[string]any{
		{"run_id": r.ID, "score": float64(6), "idempotency_key": "k"},
		{"run_id": r.ID, "score": float64(2), "idempotency_key": "k"},
		{"run_id": r.ID, "score": float64(4), "tags": []any{"Unknown"}, "idempotency_key": "k"},
		{"run_id": r.ID, "score": float64(4), "criteria": map[string]any{"speed": float64(3)}, "idempotency_key": "k"},
		{"run_id": r.ID, "score": float64(4), "criteria": map[string]any{"tone": "loud"}, "idempotency_key": "k"},
		{"run_id": r.ID, "score": float64(4)},
	} {
		if _, e := rate(a, p, "agent:9:main", bad); e == nil {
			t.Fatalf("accepted invalid rating %v", bad)
		}
	}
	if _, e := rate(a, p, "agent:7:worker", map[string]any{"run_id": r.ID, "step_id": write.ID, "score": float64(5), "idempotency_key": "self"}); e == nil || !strings.Contains(e.Error(), "executed") {
		t.Fatalf("executor rated its own step: %v", e)
	}
	low, e := rate(a, p, "agent:9:main", map[string]any{"run_id": r.ID, "step_id": write.ID, "score": float64(2), "tags": []any{"wrong output"}, "comment": "Draft missed the brief", "criteria": map[string]any{"accuracy": float64(2), "on_time": true, "tone": "formal"}, "idempotency_key": "write-low"})
	if e != nil {
		t.Fatal(e)
	}
	if low["review_item"] == nil || low["revision"].(float64) != 1 {
		t.Fatalf("low score did not queue a review: %v", low)
	}
	again, e := rate(a, p, "agent:9:other-thread", map[string]any{"run_id": r.ID, "step_id": write.ID, "score": float64(2), "tags": []any{"wrong output"}, "comment": "Draft missed the brief", "criteria": map[string]any{"accuracy": float64(2), "on_time": true, "tone": "formal"}, "idempotency_key": "write-low"})
	if e != nil || again["rating_id"] != low["rating_id"] || again["review_item"] != nil {
		t.Fatalf("identical retry was not idempotent: %v %v", again, e)
	}
	if _, e = rate(a, p, "agent:9:main", map[string]any{"run_id": r.ID, "step_id": write.ID, "score": float64(4), "idempotency_key": "write-low"}); e == nil {
		t.Fatal("idempotency key reused for a different rating")
	}
	revised, e := rate(a, p, "agent:9:main", map[string]any{"run_id": r.ID, "step_id": write.ID, "score": float64(3), "tags": []any{"Great"}, "idempotency_key": "write-revised"})
	if e != nil || revised["revision"].(float64) != 2 {
		t.Fatalf("revision not created: %v %v", revised, e)
	}
	for i, actor := range []string{"agent:9:main", "agent:10:main"} {
		if _, e = rate(a, p, actor, map[string]any{"run_id": r.ID, "score": float64(4 + i), "tags": []any{"Great"}, "criteria": map[string]any{"accuracy": float64(4 + i), "on_time": i == 0}, "idempotency_key": "run"}); e != nil {
			t.Fatal(e)
		}
	}
	got := mcpCall(t, a, p, "run_ratings", map[string]any{"run_id": r.ID})
	if n := len(got["ratings"].([]any)); n != 3 {
		t.Fatalf("expected 3 active ratings, got %d", n)
	}
	summary := got["summary"].(map[string]any)
	if summary["average"].(float64) != 4.5 || summary["count"].(float64) != 2 || summary["step_ratings"].(float64) != 1 {
		t.Fatalf("summary %v", summary)
	}
	history := mcpCall(t, a, p, "run_ratings", map[string]any{"run_id": r.ID, "include_history": true})
	if n := len(history["ratings"].([]any)); n != 4 {
		t.Fatalf("history should keep the superseded revision, got %d", n)
	}
	q := mcpCall(t, a, p, "quality_get", map[string]any{})
	v := q["versions"].([]any)[0].(map[string]any)
	if v["average"].(float64) != 4.5 || v["coverage"].(float64) != 1 || v["finished_runs"].(float64) != 1 {
		t.Fatalf("version quality %v", v)
	}
	ex := q["executors"].([]any)[0].(map[string]any)
	if ex["agent_id"].(float64) != 7 || ex["average"].(float64) != 3 {
		t.Fatalf("executor quality %v", ex)
	}
	criteria := map[string]map[string]any{}
	for _, raw := range q["criteria"].([]any) {
		c := raw.(map[string]any)
		criteria[c["key"].(string)] = c
	}
	if criteria["accuracy"]["average"].(float64) != 4.5 || criteria["on_time"]["pass_rate"].(float64) != 0.5 {
		t.Fatalf("criteria %v", criteria)
	}
	// Superseded revisions leave the aggregates: only the revised step tag counts.
	tags := q["tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["tag"] != "Great" {
		t.Fatalf("tags %v", tags)
	}
	page := mcpCall(t, a, p, "runs", map[string]any{})
	if page["runs"].([]any)[0].(map[string]any)["rating"] == nil {
		t.Fatal("runs history lacks the rating summary")
	}
	queue := mcpCall(t, a, p, "review_queue", map[string]any{})
	items := queue["items"].([]any)
	if len(items) != 1 || queue["active_count"].(float64) != 1 {
		t.Fatalf("queue %v", queue)
	}
	item := items[0].(map[string]any)
	id := item["id"].(string)
	if _, e = a.executeMCP(p.ProjectID, "agent:9:main", "review_update", map[string]any{"process_id": p.ID, "review_id": id, "state": "resolved", "expected_revision": float64(1)}); e == nil {
		t.Fatal("resolved without a resolution")
	}
	mcpCall(t, a, p, "review_update", map[string]any{"review_id": id, "state": "claimed", "expected_revision": float64(1)})
	if _, e = a.executeMCP(p.ProjectID, "agent:9:main", "review_update", map[string]any{"process_id": p.ID, "review_id": id, "state": "resolved", "resolution": "x", "expected_revision": float64(1)}); e != errConflict {
		t.Fatalf("stale revision accepted: %v", e)
	}
	done := mcpCall(t, a, p, "review_update", map[string]any{"review_id": id, "state": "resolved", "resolution": "Brief clarified in v2", "expected_revision": float64(2)})
	if done["state"] != "resolved" {
		t.Fatalf("resolve %v", done)
	}
	if mcpCall(t, a, p, "review_queue", map[string]any{})["active_count"].(float64) != 0 {
		t.Fatal("resolved item still active")
	}
	var topics []string
	rows, _ := a.db.Query(`SELECT topic FROM process_event_outbox WHERE topic LIKE 'run.rat%' OR topic LIKE 'review.%' ORDER BY sequence`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		topics = append(topics, s)
	}
	rows.Close()
	if strings.Join(topics, ",") != "run.rated,review.item_created,run.rating_revised,run.rated,run.rated,review.item_updated,review.item_updated" {
		t.Fatalf("events %v", topics)
	}
}

func TestThumbsScaleFailedRunReviewAndHTTP(t *testing.T) {
	a, p, r := ratedWorkflow(t, &QualityConfig{Scale: "thumbs", RequestRating: true, Review: &QualityReview{FailedRuns: true}})
	finishRun(t, a, r, "failed")
	if _, e := rate(a, p, "agent:9:main", map[string]any{"run_id": r.ID, "score": float64(4), "idempotency_key": "t"}); e == nil {
		t.Fatal("thumbs procedure accepted a 4")
	}
	h := a.HTTPRoutes()[1].Handler
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Apteva-Project-ID", p.ProjectID)
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	w := call("POST", "/processes/"+p.ID+"/runs/"+r.ID+"/ratings", `{"score":1,"tags":["Too slow"],"source":"reviewer","idempotency_key":"ui-1"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"source":"reviewer"`) {
		t.Fatalf("http rate %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/processes/reviews?state=active", "")
	var queue map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &queue)
	reasons := map[string]bool{}
	for _, raw := range queue["items"].([]any) {
		reasons[raw.(map[string]any)["reason"].(string)] = true
	}
	if w.Code != 200 || !reasons["failed"] || !reasons["low_score"] {
		t.Fatalf("project queue %d %s", w.Code, w.Body.String())
	}
	if w = call("GET", "/processes/"+p.ID+"/quality", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"scale":"thumbs"`) {
		t.Fatalf("quality %d %s", w.Code, w.Body.String())
	}
	if w = call("GET", "/processes/"+p.ID+"/ratings?max_score=1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"score":1`) {
		t.Fatalf("ratings page %d %s", w.Code, w.Body.String())
	}
	o, e := a.overview(p.ProjectID)
	if e != nil || o.Counts.Review != 2 {
		t.Fatalf("overview review count %v %v", o, e)
	}
	d, _ := a.runDefinition(r)
	if msg := ratingRequest(d.Quality, p.ID, r.ID); !strings.Contains(msg, "processes_run_rate") || !strings.Contains(msg, "thumbs up") {
		t.Fatalf("rating request %q", msg)
	}
	if ratingRequest(nil, p.ID, r.ID) != "" {
		t.Fatal("rating requested without procedure opt-in")
	}
}

func TestQualityConfigValidation(t *testing.T) {
	bad := []*QualityConfig{
		{Scale: "emoji"},
		{Tags: []string{"a", "A"}},
		{Criteria: []QualityCriterion{{Key: "c", Label: "C", Type: "choice", Options: []string{"one"}}}},
		{Criteria: []QualityCriterion{{Key: "c", Label: "C", Type: "slider"}}},
		{Review: &QualityReview{SamplePercent: 101}},
	}
	for _, q := range bad {
		if validateQuality(q) == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	d := def()
	d.Quality = &QualityConfig{Scale: "stars", Tags: []string{" Late "}}
	if e := d.validate(); e != nil || d.Quality.Tags[0] != "Late" {
		t.Fatalf("valid quality rejected: %v", e)
	}
}
