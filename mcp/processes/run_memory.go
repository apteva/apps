package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const memoryExecutionContract = "When previous work is relevant, use compact processes_runs filtered by assignment and processes_memory_list with the exact campaign/scope; fetch only selected run details. Save useful findings and unfinished work with processes_summary_update at meaningful checkpoints, blockers and completion, and stable-key processes_memory_upsert entries with exact source references. Recover revisions before overwriting; summaries never replace receipts, frozen instructions or approval. If a read is incomplete, follow all required processes_run_evidence references before acting."

// Semantic memory is agent-authored and never substitutes for execution evidence.
type RunSummary struct {
	Attempted   string                     `json:"attempted,omitempty"`
	Completed   string                     `json:"completed,omitempty"`
	Findings    string                     `json:"findings,omitempty"`
	Outcome     string                     `json:"outcome,omitempty"`
	Blocker     string                     `json:"blocker,omitempty"`
	NextActions string                     `json:"next_actions,omitempty"`
	References  []string                   `json:"references"`
	Fields      map[string]json.RawMessage `json:"fields,omitempty"`
}
type SavedSummary struct {
	RunID     string `json:"run_id"`
	Revision  int    `json:"revision"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	RunSummary
}
type MemoryEntry struct {
	Kind       string   `json:"kind"`
	Content    string   `json:"content"`
	References []string `json:"references"`
}
type SavedMemory struct {
	ProcessID    string `json:"process_id"`
	AssignmentID string `json:"assignment_id"`
	Scope        string `json:"scope"`
	Key          string `json:"key"`
	Revision     int    `json:"revision"`
	RunID        string `json:"run_id"`
	StepID       string `json:"step_id,omitempty"`
	Author       string `json:"author"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	MemoryEntry
}

func decodeMemory(v any, dst any, max int) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	if len(b) > max {
		return "", fmt.Errorf("memory exceeds %d bytes; split ledger entries or use exact evidence references", max)
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(dst); e != nil {
		return "", e
	}
	return string(b), nil
}
func validReferences(refs []string) bool {
	if len(refs) > 20 {
		return false
	}
	for _, r := range refs {
		if strings.TrimSpace(r) == "" || len(r) > 256 {
			return false
		}
	}
	return true
}
func (a *App) summary(run string, revision int) (*SavedSummary, error) {
	s := SavedSummary{RunID: run}
	var body string
	q := `SELECT revision,body_json,author,created_at FROM process_run_summaries WHERE run_id=? ORDER BY revision DESC LIMIT 1`
	args := []any{run}
	if revision > 0 {
		q = `SELECT revision,body_json,author,created_at FROM process_run_summaries WHERE run_id=? AND revision=?`
		args = append(args, revision)
	}
	e := a.db.QueryRow(q, args...).Scan(&s.Revision, &body, &s.Author, &s.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal([]byte(body), &s.RunSummary)
	return &s, e
}
func (a *App) memoryWriter(r Run, actor, step string) error {
	if actor == "operator" {
		return nil
	}
	if step != "" {
		all, e := a.steps(r.ID)
		if e != nil {
			return e
		}
		var s StepRun
		found := false
		for _, v := range all {
			if v.ID == step {
				s = v
				found = true
				break
			}
		}
		if !found {
			return errNotFound
		}
		if s.Executor.Kind != "agent" || !strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", s.Executor.AgentID)) {
			return errors.New("only the step executor may write its memory")
		}
		return nil
	}
	if !strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", r.Binding.OwnerAgentID)) {
		return errors.New("only the run owner may write run memory; other executors must supply their step_id")
	}
	return nil
}
func (a *App) summaryAction(project, actor, process string, args map[string]any, write bool) (any, error) {
	r, e := a.getRun(project, process, str(args, "run_id"))
	if e != nil {
		return nil, e
	}
	if !write {
		if _, provided := args["revision"]; provided {
			if _, e := memoryRevision(args, "revision", 1); e != nil {
				return nil, e
			}
		}
		s, e := a.summary(r.ID, number(args, "revision"))
		return map[string]any{"process_id": process, "run_id": r.ID, "procedure_version": r.Version, "assignment_id": r.AssignmentID, "run_state": r.State, "progress": r.Progress, "available": s != nil, "summary": s, "reread": runReread(process, r.ID)}, e
	}
	if e = a.memoryWriter(r, actor, str(args, "step_id")); e != nil {
		return nil, e
	}
	expected, e := memoryRevision(args, "expected_revision", 0)
	if e != nil {
		return nil, e
	}
	key := str(args, "idempotency_key")
	if key == "" || len(key) > 128 {
		return nil, errors.New("stable idempotency_key required (max 128 bytes)")
	}
	var s RunSummary
	body, e := decodeMemory(args["summary"], &s, 4096)
	if e != nil {
		return nil, e
	}
	if !validReferences(s.References) || len(s.Fields) > 12 || strings.TrimSpace(s.Attempted+s.Completed+s.Findings+s.Outcome+s.Blocker+s.NextActions) == "" {
		return nil, errors.New("summary needs a meaningful checkpoint and at most 20 exact references and 12 fields")
	}
	d, e := a.runDefinition(r)
	if e != nil {
		return nil, e
	}
	allowed := map[string]bool{}
	for _, f := range d.SummaryFields {
		allowed[f.Key] = true
	}
	for k, v := range s.Fields {
		if !allowed[k] {
			return nil, errors.New("summary field must be declared in the frozen procedure summary_fields")
		}
		if len(k) > 64 || len(v) > 256 {
			return nil, errors.New("summary field exceeds key/value limit")
		}
	}
	var saved int
	var previous, author string
	e = a.db.QueryRow(`SELECT revision,body_json,author FROM process_run_summaries WHERE run_id=? AND request_key=?`, r.ID, key).Scan(&saved, &previous, &author)
	if e == nil {
		if previous != body || author != actor {
			return nil, errors.New("idempotency key reused with different summary or author")
		}
		return a.summaryAction(project, actor, process, map[string]any{"run_id": r.ID, "revision": float64(saved)}, false)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	tx, e := a.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var revision int
	e = tx.QueryRow(`SELECT coalesce(max(revision),0) FROM process_run_summaries WHERE run_id=?`, r.ID).Scan(&revision)
	if e != nil {
		return nil, e
	}
	if revision != expected {
		return nil, errConflict
	}
	_, e = tx.Exec(`INSERT INTO process_run_summaries(run_id,revision,body_json,author,request_key,created_at) VALUES(?,?,?,?,?,?)`, r.ID, revision+1, body, actor, key, timestamp())
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return a.summaryAction(project, actor, process, map[string]any{"run_id": r.ID}, false)
}
func (a *App) memoryUpsert(project, actor, process string, args map[string]any) (any, error) {
	r, e := a.getRun(project, process, str(args, "run_id"))
	if e != nil {
		return nil, e
	}
	step := str(args, "step_id")
	if e = a.memoryWriter(r, actor, step); e != nil {
		return nil, e
	}
	expected, e := memoryRevision(args, "expected_revision", 0)
	if e != nil {
		return nil, e
	}
	scope, key := str(args, "scope"), str(args, "key")
	if scope == "" || len(scope) > 128 || key == "" || len(key) > 128 {
		return nil, errors.New("scope and stable key required (max 128 bytes each)")
	}
	var entry MemoryEntry
	body, e := decodeMemory(args["entry"], &entry, 2048)
	if e != nil {
		return nil, e
	}
	if entry.Kind == "" || len(entry.Kind) > 64 || strings.TrimSpace(entry.Content) == "" || !validReferences(entry.References) {
		return nil, errors.New("entry needs kind, content and at most 20 exact references")
	}
	tx, e := a.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var rev int
	var old, oldRun, oldStep, oldAuthor string
	e = tx.QueryRow(`SELECT revision,body_json,run_id,step_id,author FROM process_memory WHERE process_id=? AND assignment_id=? AND scope=? AND entry_key=?`, process, r.AssignmentID, scope, key).Scan(&rev, &old, &oldRun, &oldStep, &oldAuthor)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	// Identical retries never revise or move provenance to another run.
	duplicate := rev > 0 && old == body && oldRun == r.ID && oldStep == step && oldAuthor == actor
	if !duplicate {
		if rev != expected {
			return nil, errConflict
		}
		now := timestamp()
		_, e = tx.Exec(`INSERT INTO process_memory(process_id,assignment_id,scope,entry_key,revision,body_json,run_id,step_id,author,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(process_id,assignment_id,scope,entry_key) DO UPDATE SET revision=excluded.revision,body_json=excluded.body_json,run_id=excluded.run_id,step_id=excluded.step_id,author=excluded.author,updated_at=excluded.updated_at`, process, r.AssignmentID, scope, key, rev+1, body, r.ID, step, actor, now, now)
		if e != nil {
			return nil, e
		}
		rev++
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return map[string]any{"process_id": process, "assignment_id": r.AssignmentID, "scope": scope, "key": key, "revision": rev, "saved": true, "reread": RereadReference{Tool: "processes_memory_list", Args: map[string]any{"process_id": process, "assignment_id": r.AssignmentID, "scope": scope, "key": key}}}, nil
}
func pageLimit(args map[string]any) (int, error) {
	n := 10
	if v, ok := args["limit"]; ok {
		f, valid := v.(float64)
		if !valid || f != float64(int(f)) || f < 1 || f > 50 {
			return 0, errors.New("limit must be an integer from 1 to 50")
		}
		n = int(f)
	}
	return n, nil
}

// Cursors bind to the complete query, so a token cannot accidentally skip data
// when an agent changes assignment, dates, status or ledger search.
type pageCursor struct {
	Query string `json:"q"`
	Date  string `json:"d,omitempty"`
	ID    string `json:"i"`
}

func cursorToken(q, date, id string) string {
	b, _ := json.Marshal(pageCursor{q, date, id})
	return base64.RawURLEncoding.EncodeToString(b)
}
func parseCursor(token, q string) (pageCursor, error) {
	var c pageCursor
	if token == "" {
		return c, nil
	}
	if len(token) > 4096 {
		return c, errors.New("invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(token)
	if e == nil {
		e = json.Unmarshal(b, &c)
	}
	if e != nil || c.Query != q || c.ID == "" {
		return c, errors.New("invalid cursor for these filters")
	}
	return c, nil
}
func (a *App) memoryList(project, process string, args map[string]any) (any, error) {
	if _, e := a.get(project, process); e != nil {
		return nil, e
	}
	assignment, scope := str(args, "assignment_id"), str(args, "scope")
	if assignment == "" || scope == "" || len(scope) > 128 {
		return nil, errors.New("assignment_id and scope required")
	}
	if _, e := a.assignment(project, process, assignment); e != nil {
		return nil, e
	}
	limit, e := pageLimit(args)
	if e != nil {
		return nil, e
	}
	search, key, kind := str(args, "search"), str(args, "key"), str(args, "kind")
	if len(search) > 256 {
		return nil, errors.New("search exceeds 256 bytes")
	}
	qid := jsonText([]string{project, process, assignment, scope, search, key, kind})
	c, e := parseCursor(str(args, "cursor"), qid)
	if e != nil {
		return nil, e
	}
	q := `SELECT entry_key,revision,body_json,run_id,step_id,author,created_at,updated_at FROM process_memory WHERE process_id=? AND assignment_id=? AND scope=? AND entry_key>?`
	values := []any{process, assignment, scope, c.ID}
	if key != "" {
		q += ` AND entry_key=?`
		values = append(values, key)
	}
	if kind != "" {
		q += ` AND json_extract(body_json,'$.kind')=?`
		values = append(values, kind)
	}
	if search != "" {
		q += ` AND instr(lower(body_json),lower(?))>0`
		values = append(values, search)
	}
	q += ` ORDER BY entry_key LIMIT ?`
	values = append(values, limit+1)
	rows, e := a.db.Query(q, values...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []SavedMemory{}
	next := ""
	size := 1024
	for rows.Next() {
		s := SavedMemory{ProcessID: process, AssignmentID: assignment, Scope: scope}
		var body string
		if e = rows.Scan(&s.Key, &s.Revision, &body, &s.RunID, &s.StepID, &s.Author, &s.CreatedAt, &s.UpdatedAt); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(body), &s.MemoryEntry); e != nil {
			return nil, e
		}
		b, _ := json.Marshal(s)
		if len(out) >= limit || size+len(b) > 14*1024 {
			last := out[len(out)-1]
			next = cursorToken(qid, "", last.Key)
			break
		}
		size += len(b)
		out = append(out, s)
	}
	return map[string]any{"entries": out, "has_more": next != "", "next_cursor": next}, rows.Err()
}
func validateDate(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		return "", errors.New("date filters require RFC3339 timestamps")
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z"), nil
}

func memoryRevision(args map[string]any, key string, minimum int) (int, error) {
	f, valid := args[key].(float64)
	if !valid || f < float64(minimum) || f > 2147483647 || f != float64(int(f)) {
		return 0, fmt.Errorf("%s must be an integer from %d to 2147483647", key, minimum)
	}
	return int(f), nil
}
