package main

// Stages (0.5.0): frozen definition versions + named stage pointers.
//
// Every definition change writes an immutable workflow_versions row.
// A stage (free-text name such as "staging" or "production") points
// at one of those versions and carries its own vars, an optional
// extra event filter and an active/paused status. Moving a stage is
// an explicit act (stage_set / promote_many / rollback); editing the
// workflow never moves a stage.
//
// A workflow with no stages behaves exactly as before: events and
// manual runs execute the live head definition.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var stageNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// WorkflowVersion is one frozen definition snapshot.
type WorkflowVersion struct {
	WorkflowID  int64  `json:"workflow_id"`
	Version     int    `json:"version"`
	Source      string `json:"source,omitempty"`
	SourceHash  string `json:"source_hash"`
	TriggerKind string `json:"trigger_kind"`
	TriggerJSON string `json:"trigger_json,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// StageFilter is an optional extra match on the triggering event,
// evaluated before any run is created.
//
//	when              — condition in the trigger-predicate grammar
//	                    (input.*, stage.* paths), strict like trigger.when.
//	source_install_id — only events whose frame carries this
//	                    source_install_id match. Frames without the
//	                    field are not filtered by it (the platform does
//	                    not send it yet).
type StageFilter struct {
	When            string `json:"when,omitempty"`
	SourceInstallID *int64 `json:"source_install_id,omitempty"`
}

func (f *StageFilter) empty() bool {
	return f == nil || (strings.TrimSpace(f.When) == "" && f.SourceInstallID == nil)
}

type Stage struct {
	ID              int64          `json:"id"`
	ProjectID       string         `json:"project_id,omitempty"`
	WorkflowID      int64          `json:"workflow_id"`
	WorkflowName    string         `json:"workflow_name,omitempty"`
	Name            string         `json:"name"`
	Version         int            `json:"version"`
	PreviousVersion *int           `json:"previous_version,omitempty"`
	Vars            map[string]any `json:"vars"`
	Filter          *StageFilter   `json:"filter,omitempty"`
	Status          string         `json:"status"`
	Rev             int            `json:"rev"`
	CreatedAt       string         `json:"created_at,omitempty"`
	UpdatedAt       string         `json:"updated_at,omitempty"`
}

// querier is satisfied by *sql.DB and *sql.Tx so lookups can run
// inside a promote transaction (the test DB is a single connection,
// so anything issued outside the open tx would deadlock).
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ─── Versions ──────────────────────────────────────────────────────

// txSnapshotVersion freezes the workflow's current head as an
// immutable version row. resolved, when non-nil, is the exact source
// bytes validated by the caller (needed for repo-backed sources);
// otherwise the inline source column is used. INSERT OR IGNORE keeps
// an existing snapshot untouched — versions are never rewritten.
func txSnapshotVersion(q querier, pid string, id int64, resolved []byte) error {
	var (
		version                       int
		source, hash, tkind, trigJSON string
	)
	err := q.QueryRow(`SELECT version, COALESCE(source,''), source_hash, trigger_kind, COALESCE(trigger_json,'')
		FROM workflows WHERE id = ? AND project_id = ?`, id, pid).
		Scan(&version, &source, &hash, &tkind, &trigJSON)
	if err != nil {
		return err
	}
	if len(resolved) > 0 {
		source = string(resolved)
	}
	if strings.TrimSpace(source) == "" {
		return nil
	}
	_, err = q.Exec(`INSERT OR IGNORE INTO workflow_versions
		(workflow_id, version, source, source_hash, trigger_kind, trigger_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, version, source, hash, tkind, nullStr(trigJSON), time.Now().UTC().Format(time.RFC3339))
	return err
}

func dbGetVersion(q querier, workflowID int64, version int) (*WorkflowVersion, error) {
	v := &WorkflowVersion{}
	err := q.QueryRow(`SELECT workflow_id, version, source, source_hash, trigger_kind,
			COALESCE(trigger_json,''), COALESCE(created_at,'')
		FROM workflow_versions WHERE workflow_id = ? AND version = ?`, workflowID, version).
		Scan(&v.WorkflowID, &v.Version, &v.Source, &v.SourceHash, &v.TriggerKind, &v.TriggerJSON, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// dbListVersions returns version metadata (no source) newest first.
func dbListVersions(q querier, workflowID int64, limit int) ([]*WorkflowVersion, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.Query(`SELECT workflow_id, version, source_hash, trigger_kind,
			COALESCE(trigger_json,''), COALESCE(created_at,'')
		FROM workflow_versions WHERE workflow_id = ? ORDER BY version DESC LIMIT ?`, workflowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WorkflowVersion{}
	for rows.Next() {
		v := &WorkflowVersion{}
		if err := rows.Scan(&v.WorkflowID, &v.Version, &v.SourceHash, &v.TriggerKind, &v.TriggerJSON, &v.CreatedAt); err == nil {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

// validateStageVersion loads a version and runs the definition
// validator on it. Used at every point a stage pointer moves.
func validateStageVersion(q querier, wf *Workflow, version int) (*WorkflowVersion, *WorkflowDef, error) {
	if version <= 0 {
		return nil, nil, fmt.Errorf("workflow %q: version must be a positive integer", wf.Name)
	}
	v, err := dbGetVersion(q, wf.ID, version)
	if err != nil {
		return nil, nil, err
	}
	if v == nil {
		return nil, nil, fmt.Errorf("workflow %q has no version %d", wf.Name, version)
	}
	def, err := ParseDefinition([]byte(v.Source))
	if err != nil {
		return nil, nil, fmt.Errorf("workflow %q version %d is invalid: %w", wf.Name, version, err)
	}
	return v, def, nil
}

// stagedWorkflow builds the per-run Workflow for a frozen version:
// the head row's identity (id, name, project, status) with the
// version's source, hash, trigger and parsed definition.
func stagedWorkflow(head *Workflow, v *WorkflowVersion) (*Workflow, error) {
	def, err := ParseDefinition([]byte(v.Source))
	if err != nil {
		return nil, fmt.Errorf("workflow %q version %d: %w", head.Name, v.Version, err)
	}
	w := *head
	w.Version = v.Version
	w.SourceKind = "inline"
	w.Source = v.Source
	w.SourceHash = v.SourceHash
	w.RepoID = nil
	w.RepoPath = ""
	w.TriggerKind = v.TriggerKind
	w.TriggerJSON = v.TriggerJSON
	w.Definition = def
	return &w, nil
}

// versionTrigger reads the trigger frozen with a version.
func versionTrigger(v *WorkflowVersion) (TriggerDef, bool) {
	return workflowTrigger(&Workflow{TriggerJSON: v.TriggerJSON, Source: v.Source})
}

// ─── Stage rows ────────────────────────────────────────────────────

const stageColumns = `s.id, s.project_id, s.workflow_id, w.name, s.name, s.version, s.previous_version,
	s.vars_json, COALESCE(s.filter_json,''), s.status, s.rev,
	COALESCE(s.created_at,''), COALESCE(s.updated_at,'')`

const stageFrom = ` FROM workflow_stages s JOIN workflows w ON w.id = s.workflow_id `

func scanStage(row scanRow) (*Stage, error) {
	s := &Stage{}
	var prev sql.NullInt64
	var varsJSON, filterJSON string
	if err := row.Scan(&s.ID, &s.ProjectID, &s.WorkflowID, &s.WorkflowName, &s.Name, &s.Version, &prev,
		&varsJSON, &filterJSON, &s.Status, &s.Rev, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if prev.Valid {
		p := int(prev.Int64)
		s.PreviousVersion = &p
	}
	s.Vars = map[string]any{}
	if varsJSON != "" {
		_ = json.Unmarshal([]byte(varsJSON), &s.Vars)
	}
	if filterJSON != "" {
		f := &StageFilter{}
		if json.Unmarshal([]byte(filterJSON), f) == nil && !f.empty() {
			s.Filter = f
		}
	}
	return s, nil
}

func dbGetStage(q querier, workflowID int64, name string) (*Stage, error) {
	s, err := scanStage(q.QueryRow(`SELECT `+stageColumns+stageFrom+`WHERE s.workflow_id = ? AND s.name = ?`, workflowID, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

func queryStages(q querier, where string, args ...any) ([]*Stage, error) {
	rows, err := q.Query(`SELECT `+stageColumns+stageFrom+`WHERE `+where+` ORDER BY w.name, s.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Stage{}
	for rows.Next() {
		s, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func dbListStagesForWorkflow(q querier, workflowID int64) ([]*Stage, error) {
	return queryStages(q, `s.workflow_id = ?`, workflowID)
}

// dbListStages lists a project's stages, optionally narrowed to one
// workflow and/or one stage name.
func dbListStages(q querier, pid string, workflowID int64, name string) ([]*Stage, error) {
	where := []string{"s.project_id = ?"}
	args := []any{pid}
	if workflowID != 0 {
		where = append(where, "s.workflow_id = ?")
		args = append(args, workflowID)
	}
	if name != "" {
		where = append(where, "s.name = ?")
		args = append(args, name)
	}
	return queryStages(q, strings.Join(where, " AND "), args...)
}

// ─── Mutations ─────────────────────────────────────────────────────

// errRevConflict is returned (wrapped) whenever expected_rev does
// not match; the enclosing transaction is rolled back.
var errRevConflict = errors.New("rev conflict")

func revConflict(wf *Workflow, stage string, have, want int) error {
	return fmt.Errorf("%w: stage %q of workflow %q is at rev %d, expected_rev was %d; nothing changed",
		errRevConflict, stage, wf.Name, have, want)
}

// stagePatch is the parsed stage_set input. Nil / unset fields keep
// the current value.
type stagePatch struct {
	Version     *int
	Vars        map[string]any
	VarsSet     bool
	Filter      *StageFilter
	FilterSet   bool
	Status      string
	ExpectedRev *int
}

func validateStageName(name string) error {
	if !stageNameRE.MatchString(name) {
		return fmt.Errorf("stage name %q must match ^[a-z0-9][a-z0-9-]{0,39}$", name)
	}
	return nil
}

func validateStageFilter(f *StageFilter) error {
	if f == nil {
		return nil
	}
	if strings.TrimSpace(f.When) != "" {
		if err := ValidateTriggerCondition(f.When); err != nil {
			return fmt.Errorf("filter.when: %w", err)
		}
	}
	return nil
}

// txUpsertStage creates or updates one stage inside tx. Returns the
// resulting stage and whether anything changed. A no-op call (same
// values) leaves rev and previous_version untouched, which is what
// makes stage_set / promote_many safe to retry.
func txUpsertStage(tx querier, pid string, wf *Workflow, name string, p stagePatch) (*Stage, bool, error) {
	if err := validateStageName(name); err != nil {
		return nil, false, err
	}
	if p.Status != "" && p.Status != "active" && p.Status != "paused" {
		return nil, false, fmt.Errorf("status %q must be active|paused", p.Status)
	}
	if p.FilterSet {
		if err := validateStageFilter(p.Filter); err != nil {
			return nil, false, err
		}
	}
	cur, err := dbGetStage(tx, wf.ID, name)
	if err != nil {
		return nil, false, err
	}
	if p.ExpectedRev != nil {
		have := 0
		if cur != nil {
			have = cur.Rev
		}
		if have != *p.ExpectedRev {
			return nil, false, revConflict(wf, name, have, *p.ExpectedRev)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)

	if cur == nil {
		if p.Version == nil {
			return nil, false, fmt.Errorf("stage %q does not exist on workflow %q yet; version is required to create it", name, wf.Name)
		}
		if _, _, err := validateStageVersion(tx, wf, *p.Version); err != nil {
			return nil, false, err
		}
		vars := p.Vars
		if vars == nil {
			vars = map[string]any{}
		}
		status := p.Status
		if status == "" {
			status = "active"
		}
		filterJSON := ""
		if p.FilterSet && !p.Filter.empty() {
			filterJSON = mustJSON(p.Filter)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_stages
			(project_id, workflow_id, name, version, previous_version, vars_json, filter_json, status, rev, created_at, updated_at)
			VALUES (?, ?, ?, ?, NULL, ?, ?, ?, 1, ?, ?)`,
			pid, wf.ID, name, *p.Version, mustJSON(vars), nullStr(filterJSON), status, now, now); err != nil {
			return nil, false, err
		}
		s, err := dbGetStage(tx, wf.ID, name)
		return s, true, err
	}

	newVersion := cur.Version
	prev := cur.PreviousVersion
	if p.Version != nil && *p.Version != cur.Version {
		if _, _, err := validateStageVersion(tx, wf, *p.Version); err != nil {
			return nil, false, err
		}
		newVersion = *p.Version
		old := cur.Version
		prev = &old
	}
	newVars := cur.Vars
	if p.VarsSet {
		newVars = p.Vars
		if newVars == nil {
			newVars = map[string]any{}
		}
	}
	newFilter := cur.Filter
	if p.FilterSet {
		newFilter = p.Filter
		if newFilter.empty() {
			newFilter = nil
		}
	}
	newStatus := cur.Status
	if p.Status != "" {
		newStatus = p.Status
	}
	filterJSON := ""
	if newFilter != nil {
		filterJSON = mustJSON(newFilter)
	}
	curFilterJSON := ""
	if cur.Filter != nil {
		curFilterJSON = mustJSON(cur.Filter)
	}
	if newVersion == cur.Version && mustJSON(newVars) == mustJSON(cur.Vars) &&
		filterJSON == curFilterJSON && newStatus == cur.Status {
		return cur, false, nil
	}
	var prevArg any
	if prev != nil {
		prevArg = *prev
	}
	if _, err := tx.Exec(`UPDATE workflow_stages
		SET version = ?, previous_version = ?, vars_json = ?, filter_json = ?, status = ?,
		    rev = rev + 1, updated_at = ?
		WHERE id = ?`,
		newVersion, prevArg, mustJSON(newVars), nullStr(filterJSON), newStatus, now, cur.ID); err != nil {
		return nil, false, err
	}
	s, err := dbGetStage(tx, wf.ID, name)
	return s, true, err
}

// txRollbackStage swaps version and previous_version. Pass
// expected_rev to make a retried rollback safe: without it, a second
// call rolls forward again.
func txRollbackStage(tx querier, wf *Workflow, name string, expectedRev *int) (*Stage, error) {
	cur, err := dbGetStage(tx, wf.ID, name)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("workflow %q has no stage %q", wf.Name, name)
	}
	if expectedRev != nil && cur.Rev != *expectedRev {
		return nil, revConflict(wf, name, cur.Rev, *expectedRev)
	}
	if cur.PreviousVersion == nil {
		return nil, fmt.Errorf("stage %q of workflow %q has no previous version to roll back to", name, wf.Name)
	}
	target := *cur.PreviousVersion
	if _, _, err := validateStageVersion(tx, wf, target); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE workflow_stages
		SET version = ?, previous_version = ?, rev = rev + 1, updated_at = ?
		WHERE id = ?`, target, cur.Version, time.Now().UTC().Format(time.RFC3339), cur.ID); err != nil {
		return nil, err
	}
	return dbGetStage(tx, wf.ID, name)
}

// ─── Workflow references ───────────────────────────────────────────

// getWorkflowRef resolves a tool's `workflow` argument: an integer
// id, a numeric string id, or a workflow name.
func getWorkflowRef(q querier, pid string, ref any) (*Workflow, error) {
	var id int64
	var name string
	switch v := ref.(type) {
	case float64:
		id = int64(v)
	case int:
		id = int64(v)
	case int64:
		id = v
	case string:
		v = strings.TrimSpace(v)
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			id = n
		} else {
			name = v
		}
	case map[string]any:
		if x, ok := v["id"]; ok {
			return getWorkflowRef(q, pid, x)
		}
		if x, ok := v["name"]; ok {
			return getWorkflowRef(q, pid, x)
		}
	}
	var row *sql.Row
	switch {
	case id > 0:
		row = q.QueryRow(`SELECT `+wfColumns+` FROM workflows WHERE id = ? AND project_id = ?`, id, pid)
	case name != "":
		row = q.QueryRow(`SELECT `+wfColumns+` FROM workflows WHERE name = ? AND project_id = ?`, name, pid)
	default:
		return nil, errors.New("workflow (id or name) required")
	}
	wf, err := scanWorkflow(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("workflow %v not found", ref)
	}
	return wf, err
}

// ─── Event dispatch helpers ────────────────────────────────────────

// eventSourcesForWorkflow lists the event sources a workflow should
// be subscribed to. Unstaged: the head trigger. Staged: the trigger
// of every active stage's frozen version.
func eventSourcesForWorkflow(q querier, wf *Workflow) ([]string, error) {
	stages, err := dbListStagesForWorkflow(q, wf.ID)
	if err != nil {
		return nil, err
	}
	if len(stages) == 0 {
		trig, ok := workflowTrigger(wf)
		if !ok || trig.Kind != "event" || trig.Source == "" {
			return nil, nil
		}
		return []string{trig.Source}, nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, st := range stages {
		if st.Status != "active" {
			continue
		}
		v, err := dbGetVersion(q, wf.ID, st.Version)
		if err != nil || v == nil {
			continue
		}
		trig, ok := versionTrigger(v)
		if !ok || trig.Kind != "event" || trig.Source == "" || seen[trig.Source] {
			continue
		}
		seen[trig.Source] = true
		out = append(out, trig.Source)
	}
	sort.Strings(out)
	return out, nil
}

// stageFilterMatches applies a stage's extra filter to an event.
// sourceInstallID is nil when the frame did not carry one; in that
// case the source_install_id clause is ignored.
func stageFilterMatches(f *StageFilter, sourceInstallID *int64, tctx TemplateContext) (bool, error) {
	if f == nil {
		return true, nil
	}
	if f.SourceInstallID != nil && sourceInstallID != nil && *f.SourceInstallID != *sourceInstallID {
		return false, nil
	}
	if strings.TrimSpace(f.When) != "" {
		return EvalTriggerCondition(f.When, tctx)
	}
	return true, nil
}

func stageTemplateCtx(st *Stage) map[string]any {
	vars := st.Vars
	if vars == nil {
		vars = map[string]any{}
	}
	return map[string]any{"name": st.Name, "vars": vars, "version": float64(st.Version)}
}

// parseInstallID accepts a JSON number or numeric string.
func parseInstallID(v any) (*int64, bool) {
	switch x := v.(type) {
	case float64:
		n := int64(x)
		return &n, true
	case int64:
		return &x, true
	case int:
		n := int64(x)
		return &n, true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return &n, true
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return &n, true
		}
	}
	return nil, false
}
