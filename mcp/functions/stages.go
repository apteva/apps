package main

// Stages (v1.22.0).
//
// A stage is a named pointer from one function to one of its immutable
// versions, with its own settings: vars layered over the function env,
// and optional access / limits overrides. Stage names are free text
// (staging, production, eu-1, ...) and carry no built-in meaning.
//
// Rules:
//   - A function without stages behaves exactly as before: callers that pass
//     neither stage nor version run the active version with the function's
//     own settings. Deploying never moves a stage pointer.
//   - functions_invoke {stage} runs the stage's version with the stage's
//     effective settings. An unknown stage is an error (no fallback); a paused
//     stage refuses. {version} runs that ready version with base settings.
//     stage and version are mutually exclusive.
//   - Nested functions_invoke calls made by a handler through context.call
//     inherit the caller's stage when the callee has a stage of that name
//     (paused refuses). Otherwise the callee runs its active version. An
//     explicit stage or version in the nested input always wins.
//   - Each stage / pinned-version target runs in its own worker pool keyed by
//     version and effective configuration hash, so workers are never shared
//     across different stage settings or with the active configuration.
//   - Promotion and rollback are all-or-nothing across their items, record the
//     replaced version as previous_version_id, and bump rev. expected_rev
//     guards every mutation; a mismatch changes nothing.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var stageNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

const maxStageItems = 200

// StageLimits optionally overrides a function's limits for one stage. Zero
// values inherit the function's setting.
type StageLimits struct {
	TimeoutMS   int `json:"timeout_ms,omitempty"`
	MaxMemoryMB int `json:"max_memory_mb,omitempty"`
	RuntimePolicy
}

type Stage struct {
	ID                int64             `json:"id"`
	FunctionID        int64             `json:"function_id"`
	FunctionName      string            `json:"function_name,omitempty"`
	Name              string            `json:"name"`
	VersionID         int64             `json:"version_id"`
	Version           int               `json:"version"`
	PreviousVersionID *int64            `json:"previous_version_id,omitempty"`
	PreviousVersion   *int              `json:"previous_version,omitempty"`
	Vars              map[string]string `json:"vars,omitempty"`
	Access            *FunctionAccess   `json:"access,omitempty"`
	Limits            *StageLimits      `json:"limits,omitempty"`
	Status            string            `json:"status"`
	Rev               int64             `json:"rev"`
	CreatedAt         string            `json:"created_at,omitempty"`
	UpdatedAt         string            `json:"updated_at,omitempty"`

	varsJSON, accessJSON, limitsJSON string
}

type stageStore interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// Sentinel errors let HTTP map stage failures to precise status codes.
var (
	errStageNotFound = errors.New("stage not found")
	errStagePaused   = errors.New("stage paused")
	errStageConflict = errors.New("stage conflict")
)

type stageError struct {
	kind error
	msg  string
}

func (e *stageError) Error() string { return e.msg }
func (e *stageError) Unwrap() error { return e.kind }

func stageErr(kind error, format string, args ...any) error {
	return &stageError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

const stageSelect = `SELECT s.id, s.function_id, f.name, s.name, s.version_id,
		COALESCE((SELECT version FROM function_versions WHERE id=s.version_id),0),
		s.previous_version_id, (SELECT version FROM function_versions WHERE id=s.previous_version_id),
		COALESCE(s.vars_json,''), COALESCE(s.access_json,''), COALESCE(s.limits_json,''),
		s.status, s.rev, s.created_at, s.updated_at
	FROM function_stages s JOIN functions f ON f.id=s.function_id AND f.project_id=s.project_id`

func scanStage(row scanRow) (*Stage, error) {
	st := &Stage{}
	var prevID, prevVersion sql.NullInt64
	if err := row.Scan(&st.ID, &st.FunctionID, &st.FunctionName, &st.Name, &st.VersionID, &st.Version,
		&prevID, &prevVersion, &st.varsJSON, &st.accessJSON, &st.limitsJSON,
		&st.Status, &st.Rev, &st.CreatedAt, &st.UpdatedAt); err != nil {
		return nil, err
	}
	if prevID.Valid {
		v := prevID.Int64
		st.PreviousVersionID = &v
	}
	if prevVersion.Valid {
		v := int(prevVersion.Int64)
		st.PreviousVersion = &v
	}
	if st.varsJSON != "" {
		if err := json.Unmarshal([]byte(st.varsJSON), &st.Vars); err != nil {
			return nil, err
		}
	}
	if st.accessJSON != "" {
		if err := json.Unmarshal([]byte(st.accessJSON), &st.Access); err != nil {
			return nil, err
		}
	}
	if st.limitsJSON != "" {
		if err := json.Unmarshal([]byte(st.limitsJSON), &st.Limits); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func dbListStages(q stageStore, pid string, fnID int64) ([]*Stage, error) {
	rows, err := q.Query(stageSelect+` WHERE s.project_id=? AND s.function_id=? ORDER BY s.name`, pid, fnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Stage{}
	for rows.Next() {
		st, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func dbGetStage(q stageStore, pid string, fnID int64, name string) (*Stage, error) {
	st, err := scanStage(q.QueryRow(stageSelect+` WHERE s.project_id=? AND s.function_id=? AND s.name=?`, pid, fnID, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return st, err
}

// maskStage hides var values unless secrets were explicitly requested,
// matching how functions_get masks a function's env.
func maskStage(st *Stage) *Stage {
	c := *st
	if len(st.Vars) > 0 {
		c.Vars = map[string]string{}
		for k := range st.Vars {
			c.Vars[k] = "[redacted]"
		}
	}
	return &c
}

func maskStages(in []*Stage, reveal bool) []*Stage {
	out := make([]*Stage, 0, len(in))
	for _, st := range in {
		if reveal {
			out = append(out, st)
		} else {
			out = append(out, maskStage(st))
		}
	}
	return out
}

// ─── Argument parsing ─────────────────────────────────────────────

func validateStageName(name string) error {
	if !stageNameRE.MatchString(name) {
		return fmt.Errorf("stage %q must match ^[a-z0-9][a-z0-9-]{0,39}$", name)
	}
	return nil
}

// functionRef resolves a function reference (name string or integer id)
// within a project. Returns the id and name.
func functionRef(q stageStore, pid string, raw any) (int64, string, error) {
	var row *sql.Row
	switch v := raw.(type) {
	case string:
		name := strings.TrimSpace(v)
		if name == "" {
			return 0, "", errors.New("function (name or id) required")
		}
		row = q.QueryRow(`SELECT id, name FROM functions WHERE project_id=? AND name=?`, pid, name)
	case float64, int, int64, json.Number:
		id := int64Arg(map[string]any{"v": v}, "v")
		if n, ok := v.(json.Number); ok {
			id, _ = n.Int64()
		}
		if id <= 0 || (fmt.Sprint(v) != strconv.FormatInt(id, 10)) {
			return 0, "", errors.New("function id must be a positive integer")
		}
		row = q.QueryRow(`SELECT id, name FROM functions WHERE project_id=? AND id=?`, pid, id)
	case nil:
		return 0, "", errors.New("function (name or id) required")
	default:
		return 0, "", errors.New("function must be a name or an integer id")
	}
	var id int64
	var name string
	if err := row.Scan(&id, &name); err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("function %v not found", raw)
	} else if err != nil {
		return 0, "", err
	}
	return id, name, nil
}

// functionArg reads the function reference from `function`, falling back
// to the id / name arguments every other tool accepts.
func functionArg(args map[string]any) any {
	if v, ok := args["function"]; ok && v != nil {
		return v
	}
	if id := int64Arg(args, "id"); id != 0 {
		return id
	}
	if name := strArg(args, "name"); name != "" {
		return name
	}
	return nil
}

// optionalRev reads expected_rev. ok=false when absent.
func optionalRev(args map[string]any) (int64, bool, error) {
	raw, has := args["expected_rev"]
	if !has || raw == nil {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case float64:
		if v != float64(int64(v)) || v < 0 {
			return 0, false, errors.New("expected_rev must be a non-negative integer")
		}
		return int64(v), true, nil
	case int:
		return int64(v), v >= 0, nil
	case int64:
		return v, v >= 0, nil
	case json.Number:
		n, err := v.Int64()
		return n, err == nil, err
	}
	return 0, false, errors.New("expected_rev must be a non-negative integer")
}

func versionNumberArg(args map[string]any, key string) (int, bool, error) {
	raw, has := args[key]
	if !has || raw == nil {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case float64:
		if v != float64(int(v)) || v <= 0 {
			return 0, false, fmt.Errorf("%s must be a positive version number", key)
		}
		return int(v), true, nil
	case int:
		if v <= 0 {
			return 0, false, fmt.Errorf("%s must be a positive version number", key)
		}
		return v, true, nil
	case int64:
		if v <= 0 {
			return 0, false, fmt.Errorf("%s must be a positive version number", key)
		}
		return int(v), true, nil
	}
	return 0, false, fmt.Errorf("%s must be a positive version number", key)
}

func encodeStageVars(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	if err := validateFunctionArgs(map[string]any{"env": raw}, false); err != nil {
		return "", errors.New(strings.Replace(err.Error(), "environment", "vars", 1))
	}
	m := raw.(map[string]any)
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func encodeStageAccess(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	if err := validateFunctionArgs(map[string]any{"access": raw}, false); err != nil {
		return "", err
	}
	b, _ := json.Marshal(raw)
	var access FunctionAccess
	if err := json.Unmarshal(b, &access); err != nil {
		return "", err
	}
	b, err := json.Marshal(access)
	return string(b), err
}

func encodeStageLimits(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	if _, ok := raw.(map[string]any); !ok {
		return "", errors.New("limits must be an object")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	var limits StageLimits
	if err = d.Decode(&limits); err != nil {
		return "", fmt.Errorf("limits: %w", err)
	}
	if err = limits.RuntimePolicy.validate(); err != nil {
		return "", err
	}
	if limits.TimeoutMS < 0 || limits.TimeoutMS > maxTimeoutMS {
		return "", fmt.Errorf("limits.timeout_ms must be between 1 and %d", maxTimeoutMS)
	}
	if limits.MaxMemoryMB != 0 && (limits.MaxMemoryMB < 16 || limits.MaxMemoryMB > effectiveMaxMemoryMB()) {
		return "", fmt.Errorf("limits.max_memory_mb must be between 16 and %d", effectiveMaxMemoryMB())
	}
	if limits == (StageLimits{}) {
		return "", nil
	}
	b, err = json.Marshal(limits)
	return string(b), err
}

// readyVersionID resolves a ready version number of one function.
func readyVersionID(q stageStore, pid string, fnID int64, fnName string, version int) (int64, error) {
	var id int64
	var status string
	err := q.QueryRow(`SELECT id, build_status FROM function_versions WHERE function_id=? AND project_id=? AND version=?`, fnID, pid, version).Scan(&id, &status)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("function %q has no version %d", fnName, version)
	}
	if err != nil {
		return 0, err
	}
	if status != "ready" {
		return 0, fmt.Errorf("function %q version %d build_status=%s — only a ready version can be staged", fnName, version, status)
	}
	return id, nil
}

func conflict(fnName, stage string, have, want int64) error {
	return stageErr(errStageConflict, "stage conflict: function %q stage %q is at rev %d, expected_rev %d; nothing was changed", fnName, stage, have, want)
}

// ─── Mutations ────────────────────────────────────────────────────

type stageChange struct {
	FunctionID int64  `json:"function_id"`
	Function   string `json:"function"`
	Stage      *Stage `json:"stage"`
	Changed    bool   `json:"changed"`
}

// stageSet creates a stage (version required) or updates its settings.
// It never changes the version of an existing stage: promotion does that.
// Re-sending the same settings is a no-op that keeps rev.
func stageSet(db *sql.DB, pid string, args map[string]any) (*stageChange, error) {
	name := strArg(args, "stage")
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	version, hasVersion, err := versionNumberArg(args, "version")
	if err != nil {
		return nil, err
	}
	rev, hasRev, err := optionalRev(args)
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status")
	if _, has := args["status"]; has && status != "active" && status != "paused" {
		return nil, errors.New("status must be active or paused")
	}
	var vars, access, limits string
	_, setVars := args["vars"]
	if setVars {
		if vars, err = encodeStageVars(args["vars"]); err != nil {
			return nil, err
		}
	}
	_, setAccess := args["access"]
	if setAccess {
		if access, err = encodeStageAccess(args["access"]); err != nil {
			return nil, err
		}
	}
	_, setLimits := args["limits"]
	if setLimits {
		if limits, err = encodeStageLimits(args["limits"]); err != nil {
			return nil, err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	fnID, fnName, err := functionRef(tx, pid, functionArg(args))
	if err != nil {
		return nil, err
	}
	cur, err := dbGetStage(tx, pid, fnID, name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	changed := true
	if cur == nil {
		if hasRev && rev != 0 {
			return nil, conflict(fnName, name, 0, rev)
		}
		if !hasVersion {
			return nil, fmt.Errorf("stage %q does not exist on function %q; version is required to create it", name, fnName)
		}
		versionID, err := readyVersionID(tx, pid, fnID, fnName, version)
		if err != nil {
			return nil, err
		}
		if status == "" {
			status = "active"
		}
		if _, err = tx.Exec(`INSERT INTO function_stages(project_id,function_id,name,version_id,vars_json,access_json,limits_json,status,rev,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,1,?,?)`,
			pid, fnID, name, versionID, nullStr(vars), nullStr(access), nullStr(limits), status, now, now); err != nil {
			return nil, err
		}
	} else {
		if hasVersion && version != cur.Version {
			return nil, fmt.Errorf("stage %q of function %q is on version %d; change versions with functions_stage_promote_many", name, fnName, cur.Version)
		}
		if !setVars {
			vars = cur.varsJSON
		}
		if !setAccess {
			access = cur.accessJSON
		}
		if !setLimits {
			limits = cur.limitsJSON
		}
		if status == "" {
			status = cur.Status
		}
		changed = vars != cur.varsJSON || access != cur.accessJSON || limits != cur.limitsJSON || status != cur.Status
		if hasRev && rev != cur.Rev && changed {
			return nil, conflict(fnName, name, cur.Rev, rev)
		}
		if changed {
			res, err := tx.Exec(`UPDATE function_stages SET vars_json=?, access_json=?, limits_json=?, status=?, rev=rev+1, updated_at=? WHERE id=? AND rev=?`,
				nullStr(vars), nullStr(access), nullStr(limits), status, now, cur.ID, cur.Rev)
			if err != nil {
				return nil, err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return nil, conflict(fnName, name, cur.Rev+1, cur.Rev)
			}
		}
	}
	st, err := dbGetStage(tx, pid, fnID, name)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &stageChange{FunctionID: fnID, Function: fnName, Stage: st, Changed: changed}, nil
}

// stageDelete removes one stage. Deleting a missing stage is a no-op.
func stageDelete(db *sql.DB, pid string, args map[string]any) (*stageChange, error) {
	name := strArg(args, "stage")
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	rev, hasRev, err := optionalRev(args)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	fnID, fnName, err := functionRef(tx, pid, functionArg(args))
	if err != nil {
		return nil, err
	}
	cur, err := dbGetStage(tx, pid, fnID, name)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return &stageChange{FunctionID: fnID, Function: fnName}, nil
	}
	if hasRev && rev != cur.Rev {
		return nil, conflict(fnName, name, cur.Rev, rev)
	}
	if _, err = tx.Exec(`DELETE FROM function_stages WHERE id=? AND rev=?`, cur.ID, cur.Rev); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &stageChange{FunctionID: fnID, Function: fnName, Stage: cur, Changed: true}, nil
}

type stageItem struct {
	ref     any
	version int
	rev     int64
	hasRev  bool
}

func parseStageItems(raw any, needVersion bool) ([]stageItem, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, errors.New("at least one item is required")
	}
	if len(list) > maxStageItems {
		return nil, fmt.Errorf("at most %d items per call", maxStageItems)
	}
	out := make([]stageItem, 0, len(list))
	for i, entry := range list {
		var item stageItem
		switch v := entry.(type) {
		case map[string]any:
			item.ref = functionArg(v)
			var err error
			if item.rev, item.hasRev, err = optionalRev(v); err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			if needVersion {
				n, has, err := versionNumberArg(v, "version")
				if err != nil {
					return nil, fmt.Errorf("item %d: %w", i, err)
				}
				if !has {
					return nil, fmt.Errorf("item %d: version is required", i)
				}
				item.version = n
			}
		default:
			if needVersion {
				return nil, fmt.Errorf("item %d must be an object {function, version, expected_rev?}", i)
			}
			item.ref = v
		}
		out = append(out, item)
	}
	return out, nil
}

// stagePromoteMany points one stage of several functions at new versions in
// a single transaction. Any invalid item aborts the whole call. Promoting a
// stage to the version it already runs is a no-op (rev unchanged).
func stagePromoteMany(db *sql.DB, pid string, args map[string]any) ([]*stageChange, error) {
	name := strArg(args, "stage")
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	items, err := parseStageItems(args["items"], true)
	if err != nil {
		return nil, err
	}
	return applyStageItems(db, pid, name, items, func(tx *sql.Tx, fnID int64, fnName string, cur *Stage, item stageItem, now string) (bool, error) {
		versionID, err := readyVersionID(tx, pid, fnID, fnName, item.version)
		if err != nil {
			return false, err
		}
		if versionID == cur.VersionID {
			return false, nil
		}
		if item.hasRev && item.rev != cur.Rev {
			return false, conflict(fnName, name, cur.Rev, item.rev)
		}
		res, err := tx.Exec(`UPDATE function_stages SET previous_version_id=version_id, version_id=?, rev=rev+1, updated_at=? WHERE id=? AND rev=?`, versionID, now, cur.ID, cur.Rev)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return false, conflict(fnName, name, cur.Rev+1, cur.Rev)
		}
		return true, nil
	})
}

// stageRollback moves each listed function's stage back to its previous
// version (swapping current and previous, so a second rollback undoes the
// first). A function without a previous version aborts the whole call.
func stageRollback(db *sql.DB, pid string, args map[string]any) ([]*stageChange, error) {
	name := strArg(args, "stage")
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	items, err := parseStageItems(args["functions"], false)
	if err != nil {
		return nil, errors.New("functions: " + err.Error())
	}
	return applyStageItems(db, pid, name, items, func(tx *sql.Tx, fnID int64, fnName string, cur *Stage, item stageItem, now string) (bool, error) {
		if item.hasRev && item.rev != cur.Rev {
			return false, conflict(fnName, name, cur.Rev, item.rev)
		}
		if cur.PreviousVersionID == nil || cur.PreviousVersion == nil {
			return false, fmt.Errorf("function %q stage %q has no previous version to roll back to", fnName, name)
		}
		if _, err := readyVersionID(tx, pid, fnID, fnName, *cur.PreviousVersion); err != nil {
			return false, err
		}
		res, err := tx.Exec(`UPDATE function_stages SET version_id=previous_version_id, previous_version_id=version_id, rev=rev+1, updated_at=? WHERE id=? AND rev=?`, now, cur.ID, cur.Rev)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return false, conflict(fnName, name, cur.Rev+1, cur.Rev)
		}
		return true, nil
	})
}

func applyStageItems(db *sql.DB, pid, name string, items []stageItem, apply func(*sql.Tx, int64, string, *Stage, stageItem, string) (bool, error)) ([]*stageChange, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seen := map[int64]bool{}
	changes := make([]*stageChange, 0, len(items))
	for i, item := range items {
		fnID, fnName, err := functionRef(tx, pid, item.ref)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w; nothing was changed", i, err)
		}
		if seen[fnID] {
			return nil, fmt.Errorf("item %d: function %q listed twice; nothing was changed", i, fnName)
		}
		seen[fnID] = true
		cur, err := dbGetStage(tx, pid, fnID, name)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, stageErr(errStageNotFound, "item %d: function %q has no stage %q (create it with functions_stage_set); nothing was changed", i, fnName, name)
		}
		changed, err := apply(tx, fnID, fnName, cur, item, now)
		if err != nil {
			var se *stageError
			if errors.As(err, &se) {
				return nil, err
			}
			return nil, fmt.Errorf("item %d: %w; nothing was changed", i, err)
		}
		changes = append(changes, &stageChange{FunctionID: fnID, Function: fnName, Changed: changed})
	}
	for _, c := range changes {
		if c.Stage, err = dbGetStage(tx, pid, c.FunctionID, name); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return changes, nil
}

// afterStageChange retires the stage's worker pools and emits an event for
// every function whose stage actually changed.
func afterStageChange(ctx *sdk.AppCtx, pid string, changes []*stageChange, stage string, deleted bool) {
	for _, c := range changes {
		if c == nil || !c.Changed {
			continue
		}
		if p := currentPool(); p != nil {
			p.retireKeyedPools(c.FunctionID, stagePoolPrefix(stage))
		}
		data := map[string]any{"id": c.FunctionID, "name": c.Function, "stage": stage, "deleted": deleted}
		if c.Stage != nil && !deleted {
			data["version"] = c.Stage.Version
			data["rev"] = c.Stage.Rev
			data["status"] = c.Stage.Status
		}
		if ctx != nil {
			ctx.EmitWithProject("function.stage_changed", pid, data)
		}
	}
}

// ─── Invocation targets ───────────────────────────────────────────

type stageContextKey struct{}

func stageFromContext(ctx context.Context) string {
	s, _ := ctx.Value(stageContextKey{}).(string)
	return s
}

func stagePoolPrefix(stage string) string { return "stage:" + stage + ":" }

// stageFunction builds the effective function a stage runs: the stage's
// version, function env overlaid with stage vars, and stage access / limits
// overrides. The pool key names the version and the effective config.
func stageFunction(fn *Function, st *Stage) *Function {
	c := *fn
	c.Stages = nil
	c.RuntimeReadiness = nil
	versionID := st.VersionID
	c.ActiveVersionID = &versionID
	env := make(map[string]string, len(fn.Env)+len(st.Vars))
	for k, v := range fn.Env {
		env[k] = v
	}
	for k, v := range st.Vars {
		env[k] = v
	}
	c.Env = env
	if st.Access != nil {
		access := *st.Access
		c.Access = &access
	}
	if l := st.Limits; l != nil {
		if l.TimeoutMS > 0 {
			c.TimeoutMS = l.TimeoutMS
		}
		if l.MaxMemoryMB > 0 {
			c.MaxMemoryMB = l.MaxMemoryMB
		}
		c.Limits = mergeRuntimePolicy(fn.Limits, l.RuntimePolicy)
	}
	c.stage = st.Name
	c.poolKey = fmt.Sprintf("%s%d:%s:%d", stagePoolPrefix(st.Name), versionID, configHash(&c), c.TimeoutMS)
	return &c
}

func mergeRuntimePolicy(base, over RuntimePolicy) RuntimePolicy {
	out := base
	if over.Class != "" {
		out.Class = over.Class
	}
	if over.Concurrency != 0 {
		out.Concurrency = over.Concurrency
	}
	if over.MaxIdle != nil {
		n := *over.MaxIdle
		out.MaxIdle = &n
	}
	if over.WarmWorkers != 0 {
		out.WarmWorkers = over.WarmWorkers
	}
	if over.IdleMS != 0 {
		out.IdleMS = over.IdleMS
	}
	if over.QueueMS != 0 {
		out.QueueMS = over.QueueMS
	}
	if over.AppMS != 0 {
		out.AppMS = over.AppMS
	}
	if over.IntegrationMS != 0 {
		out.IntegrationMS = over.IntegrationMS
	}
	return out
}

// resolveInvocationTarget picks what one invocation runs. Neither stage nor
// version returns fn unchanged (active version, function settings).
func resolveInvocationTarget(db *sql.DB, fn *Function, stage string, version int) (*Function, error) {
	if stage != "" && version != 0 {
		return nil, errors.New("stage and version are mutually exclusive")
	}
	switch {
	case stage != "":
		if err := validateStageName(stage); err != nil {
			return nil, err
		}
		st, err := dbGetStage(db, fn.ProjectID, fn.ID, stage)
		if err != nil {
			return nil, err
		}
		if st == nil {
			return nil, stageErr(errStageNotFound, "function %q has no stage %q", fn.Name, stage)
		}
		if st.Status != "active" {
			return nil, stageErr(errStagePaused, "stage %q of function %q is paused", stage, fn.Name)
		}
		return stageFunction(fn, st), nil
	case version != 0:
		v, err := dbGetVersionByNumber(db, fn.ProjectID, fn.ID, version)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("function %q has no version %d", fn.Name, version)
		}
		if v.BuildStatus != "ready" {
			return nil, fmt.Errorf("function %q version %d build_status=%s", fn.Name, version, v.BuildStatus)
		}
		if fn.ActiveVersionID != nil && *fn.ActiveVersionID == v.ID {
			return fn, nil
		}
		c := *fn
		id := v.ID
		c.ActiveVersionID = &id
		c.poolKey = fmt.Sprintf("version:%d:%s", id, configHash(&c))
		return &c, nil
	}
	return fn, nil
}

// targetArgs reads stage / version from tool, nested or HTTP arguments.
func targetArgs(args map[string]any) (string, int, error) {
	stage := ""
	if raw, has := args["stage"]; has && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return "", 0, errors.New("stage must be a string")
		}
		stage = s
	}
	version, _, err := versionNumberArg(args, "version")
	if err != nil {
		return "", 0, err
	}
	if stage != "" && version != 0 {
		return "", 0, errors.New("stage and version are mutually exclusive")
	}
	return stage, version, nil
}

// nestedTarget applies the nested inheritance rule: explicit stage/version
// wins; otherwise the caller's stage is used when the callee defines it.
func nestedTarget(parent context.Context, db *sql.DB, fn *Function, input map[string]any) (*Function, error) {
	stage, version, err := targetArgs(input)
	if err != nil {
		return nil, err
	}
	if stage == "" && version == 0 {
		inherited := stageFromContext(parent)
		if inherited == "" {
			return fn, nil
		}
		st, err := dbGetStage(db, fn.ProjectID, fn.ID, inherited)
		if err != nil {
			return nil, err
		}
		if st == nil {
			return fn, nil
		}
		stage = inherited
	}
	return resolveInvocationTarget(db, fn, stage, version)
}

// ensureVersionArtifact builds a stage / pinned version's artifact when it
// is missing (for example after a restart on an ephemeral build root).
func (p *pool) ensureVersionArtifact(parent context.Context, ctx *sdk.AppCtx, fn *Function, v *FunctionVersion, spec runtimeSpec) error {
	dir := versionDir(p.buildBase, v)
	if artifactAvailable(dir, v) {
		return nil
	}
	if err := p.recoverSnapshots(parent, []*FunctionVersion{v}); err != nil {
		return err
	}
	src, err := resolveVersionSourceContext(parent, ctx, v)
	if err != nil {
		return err
	}
	if _, err = ensureBuiltContext(parent, p.buildBase, v, spec, src); err != nil {
		return fmt.Errorf("prepare stage version: %s", redactSecrets(err.Error(), fn.Env))
	}
	return nil
}

// ─── Version retention ────────────────────────────────────────────

// versionUnreferenced is true when no function's active pointer and no
// stage's current or previous pointer names the version (3 id arguments).
const versionUnreferenced = `NOT EXISTS (SELECT 1 FROM functions WHERE active_version_id=?)
	AND NOT EXISTS (SELECT 1 FROM function_stages WHERE version_id=?)
	AND NOT EXISTS (SELECT 1 FROM function_stages WHERE previous_version_id=?)`

// collectVersion deletes one unreferenced, idle version and its artifact.
// It returns false without error when the version is in use or referenced.
func (p *pool) collectVersion(v *FunctionVersion) (bool, error) {
	p.mu.Lock()
	for w := range p.all {
		if w.versionID == v.ID {
			p.mu.Unlock()
			return false, nil
		}
	}
	p.mu.Unlock()
	// A concurrent rollback or promotion must not race artifact collection.
	release, err := lockBuild(p.life, fmt.Sprintf("activation-%d", v.FunctionID))
	if err != nil {
		return false, err
	}
	defer release()
	dir := versionDir(p.buildBase, v)
	p.mu.Lock()
	if p.versionRefs[dir] > 0 {
		p.mu.Unlock()
		return false, nil
	}
	p.collecting[dir] = true
	p.mu.Unlock()
	// The row goes first and only when still unreferenced: a stage promoted
	// after the candidate query keeps its version.
	res, err := p.ctx.AppDB().Exec(`DELETE FROM function_versions WHERE id=? AND build_status IN ('ready','failed') AND `+versionUnreferenced, v.ID, v.ID, v.ID, v.ID)
	var n int64
	if err == nil {
		n, err = res.RowsAffected()
	}
	if err != nil || n != 1 {
		p.mu.Lock()
		delete(p.collecting, dir)
		p.mu.Unlock()
		return false, err
	}
	_ = removeTree(dir)
	// Older harness generations and the pre-1.7.1 layout are no longer runnable.
	variants, _ := filepath.Glob(filepath.Join(p.buildBase, v.ArtifactKey, fmt.Sprintf("v%d-*", v.Version)))
	for _, old := range variants {
		_ = removeTree(old)
	}
	old := filepath.Clean(v.BuildDir)
	root := filepath.Clean(p.buildBase) + string(os.PathSeparator)
	if old != dir && strings.HasPrefix(old, root) {
		_ = removeTree(old)
	}
	p.versions.Delete(v.ID)
	p.artifacts.Delete(dir)
	return true, nil
}

// pruneVersions keeps the newest `keep` versions plus every version the
// active pointer or any stage (current or previous) references.
func (p *pool) pruneVersions(pid string, fnID int64, keep int) (deleted, retained []int, err error) {
	rows, err := p.ctx.AppDB().Query(`SELECT `+fnVerColumns+` FROM function_versions v
		WHERE v.function_id=? AND v.project_id=? AND v.build_status IN ('ready','failed')
		AND v.version NOT IN (SELECT version FROM function_versions WHERE function_id=? ORDER BY version DESC LIMIT ?)
		ORDER BY v.version`, fnID, pid, fnID, keep)
	if err != nil {
		return nil, nil, err
	}
	var candidates []*FunctionVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	deleted, retained = []int{}, []int{}
	for _, v := range candidates {
		ok, err := p.collectVersion(v)
		if err != nil {
			return deleted, retained, err
		}
		if ok {
			deleted = append(deleted, v.Version)
		} else {
			retained = append(retained, v.Version)
		}
	}
	return deleted, retained, nil
}

// ─── MCP tools ────────────────────────────────────────────────────

func stageFunctionRefSchema() map[string]any {
	return map[string]any{"description": "Function name (or integer id).", "type": []any{"string", "integer"}}
}

func stageTools(a *App) []sdk.Tool {
	stageName := map[string]any{"type": "string", "description": "Stage name, ^[a-z0-9][a-z0-9-]{0,39}$ (for example staging, production)."}
	rev := map[string]any{"type": "integer", "description": "Optional optimistic lock: the stage rev you last read (0 = must not exist yet). A mismatch changes nothing."}
	return []sdk.Tool{
		{
			Name:        "functions_stage_set",
			Description: "Create a stage or update its settings. Creating requires version (a ready version number). An existing stage keeps its version (use functions_stage_promote_many). vars (string map, layered over the function env; replaces the stage's vars), access and limits (timeout_ms, max_memory_mb, concurrency, ...; null inherits the function's), status active|paused. Same settings again is a no-op.",
			InputSchema: schemaObject(map[string]any{
				"function":        stageFunctionRefSchema(),
				"stage":           stageName,
				"version":         map[string]any{"type": "integer", "description": "Version number; required when creating."},
				"vars":            map[string]any{"type": []any{"object", "null"}, "description": "Stage variables layered over the function env."},
				"access":          map[string]any{"type": []any{"object", "null"}, "description": "Access override: apps and integrations arrays of app.tool or app.* rules."},
				"limits":          map[string]any{"type": []any{"object", "null"}, "description": "Limit overrides: timeout_ms, max_memory_mb, class, concurrency, max_idle_workers, idle_timeout_ms, queue_timeout_ms, app_timeout_ms, integration_timeout_ms."},
				"status":          map[string]any{"type": "string", "enum": []any{"active", "paused"}},
				"expected_rev":    rev,
				"include_secrets": map[string]any{"type": "boolean", "description": "Return var values (default false)."},
				"_project_id":     map[string]any{"type": "string"},
			}, []string{"function", "stage"}),
			Handler: a.toolStageSet,
		},
		{
			Name:        "functions_stage_list",
			Description: "List a function's stages with their version, previous version, settings, status and rev. Var values are masked unless include_secrets.",
			InputSchema: schemaObject(map[string]any{
				"function":        stageFunctionRefSchema(),
				"include_secrets": map[string]any{"type": "boolean"},
				"_project_id":     map[string]any{"type": "string"},
			}, []string{"function"}),
			Handler: a.toolStageList,
		},
		{
			Name:        "functions_stage_delete",
			Description: "Delete a function's stage. Deleting a missing stage is a no-op (deleted=false).",
			InputSchema: schemaObject(map[string]any{
				"function":     stageFunctionRefSchema(),
				"stage":        stageName,
				"expected_rev": rev,
				"_project_id":  map[string]any{"type": "string"},
			}, []string{"function", "stage"}),
			Handler: a.toolStageDelete,
		},
		{
			Name:        "functions_stage_promote_many",
			Description: "Point one stage of one or more functions at new versions, all-or-nothing in one transaction (one item = single promote). items: [{function, version, expected_rev?}]. The replaced version becomes previous_version; rev increases. Already on that version = no change.",
			InputSchema: schemaObject(map[string]any{
				"stage": stageName,
				"items": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
					"function": stageFunctionRefSchema(), "version": map[string]any{"type": "integer"}, "expected_rev": rev,
				}, "required": []any{"function", "version"}}},
				"_project_id": map[string]any{"type": "string"},
			}, []string{"items", "stage"}),
			Handler: a.toolStagePromoteMany,
		},
		{
			Name:        "functions_stage_rollback",
			Description: "Move one stage of the listed functions back to their previous version, all-or-nothing. functions: [name | id | {function, expected_rev?}]. Errors (changing nothing) if any has no previous version. Current and previous swap, so a second rollback undoes the first.",
			InputSchema: schemaObject(map[string]any{
				"stage":       stageName,
				"functions":   map[string]any{"type": "array", "items": map[string]any{}},
				"_project_id": map[string]any{"type": "string"},
			}, []string{"functions", "stage"}),
			Handler: a.toolStageRollback,
		},
		{
			Name:        "functions_versions_prune",
			Description: "Delete old versions of a function, keeping the newest `keep` plus the active version and every version a stage references (current or previous). Versions in use by a worker are kept too.",
			InputSchema: schemaObject(map[string]any{
				"function":    stageFunctionRefSchema(),
				"keep":        map[string]any{"type": "integer", "description": "Newest versions to keep (at least 1)."},
				"_project_id": map[string]any{"type": "string"},
			}, []string{"function", "keep"}),
			Handler: a.toolVersionsPrune,
		},
	}
}

func (a *App) toolStageSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	change, err := stageSet(dbFor(ctx), pid, args)
	if err != nil {
		return nil, err
	}
	afterStageChange(ctx, pid, []*stageChange{change}, change.Stage.Name, false)
	st := change.Stage
	if args["include_secrets"] != true {
		st = maskStage(st)
	}
	return map[string]any{"function": change.Function, "stage": st, "changed": change.Changed}, nil
}

func (a *App) toolStageList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fnID, fnName, err := functionRef(dbFor(ctx), pid, functionArg(args))
	if err != nil {
		return nil, err
	}
	stages, err := dbListStages(dbFor(ctx), pid, fnID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"function": fnName, "function_id": fnID, "stages": maskStages(stages, args["include_secrets"] == true), "count": len(stages)}, nil
}

func (a *App) toolStageDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	change, err := stageDelete(dbFor(ctx), pid, args)
	if err != nil {
		return nil, err
	}
	afterStageChange(ctx, pid, []*stageChange{change}, strArg(args, "stage"), true)
	return map[string]any{"function": change.Function, "stage": strArg(args, "stage"), "deleted": change.Changed}, nil
}

func stageChangesOut(stage string, changes []*stageChange) map[string]any {
	items := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		item := map[string]any{"function": c.Function, "function_id": c.FunctionID, "changed": c.Changed}
		if c.Stage != nil {
			item["version"] = c.Stage.Version
			item["previous_version"] = c.Stage.PreviousVersion
			item["rev"] = c.Stage.Rev
			item["status"] = c.Stage.Status
		}
		items = append(items, item)
	}
	return map[string]any{"stage": stage, "items": items, "count": len(items)}
}

func (a *App) toolStagePromoteMany(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	changes, err := stagePromoteMany(dbFor(ctx), pid, args)
	if err != nil {
		return nil, err
	}
	afterStageChange(ctx, pid, changes, strArg(args, "stage"), false)
	return stageChangesOut(strArg(args, "stage"), changes), nil
}

func (a *App) toolStageRollback(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	changes, err := stageRollback(dbFor(ctx), pid, args)
	if err != nil {
		return nil, err
	}
	afterStageChange(ctx, pid, changes, strArg(args, "stage"), false)
	return stageChangesOut(strArg(args, "stage"), changes), nil
}

func (a *App) toolVersionsPrune(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return pruneFromArgs(dbFor(ctx), pid, args)
}

func pruneFromArgs(db *sql.DB, pid string, args map[string]any) (map[string]any, error) {
	keep, has, err := versionNumberArg(args, "keep")
	if err != nil || !has {
		return nil, errors.New("keep must be an integer of at least 1")
	}
	fnID, fnName, err := functionRef(db, pid, functionArg(args))
	if err != nil {
		return nil, err
	}
	p := currentPool()
	if p == nil {
		return nil, errors.New("runtime unavailable")
	}
	deleted, retained, err := p.pruneVersions(pid, fnID, keep)
	if err != nil {
		return nil, err
	}
	return map[string]any{"function": fnName, "function_id": fnID, "keep": keep, "deleted": deleted, "retained_referenced": retained}, nil
}

// ─── HTTP mirror ──────────────────────────────────────────────────
//
//   GET    /functions/<id>/stages             functions_stage_list
//   POST   /functions/<id>/stages             functions_stage_set (body: stage, ...)
//   DELETE /functions/<id>/stages/<stage>     functions_stage_delete
//   POST   /functions/<id>/versions/prune     functions_versions_prune (body: keep)
//   POST   /stages/promote                    functions_stage_promote_many
//   POST   /stages/rollback                   functions_stage_rollback

func stageHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errStageConflict), errors.Is(err, errStagePaused):
		return http.StatusConflict
	case errors.Is(err, errStageNotFound):
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

func (a *App) handleHTTPFunctionStages(w http.ResponseWriter, r *http.Request, id int64, rest string) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	db := globalCtx.AppDB()
	switch {
	case rest == "" && r.Method == http.MethodGet:
		stages, err := dbListStages(db, pid, id)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"function_id": id, "stages": maskStages(stages, r.URL.Query().Get("include_secrets") == "1"), "count": len(stages)})
	case rest == "" && r.Method == http.MethodPost:
		body, err := decodeObjectBody(w, r)
		if err != nil {
			httpErr(w, requestErrorStatus(err), err.Error())
			return
		}
		body["function"] = id
		change, err := stageSet(db, pid, body)
		if err != nil {
			httpErr(w, stageHTTPStatus(err), err.Error())
			return
		}
		afterStageChange(globalCtx, pid, []*stageChange{change}, change.Stage.Name, false)
		httpJSON(w, map[string]any{"function": change.Function, "stage": maskStage(change.Stage), "changed": change.Changed})
	case rest != "" && r.Method == http.MethodDelete:
		args := map[string]any{"function": id, "stage": rest}
		if v := r.URL.Query().Get("expected_rev"); v != "" {
			args["expected_rev"] = float64(parseInt64(v))
		}
		change, err := stageDelete(db, pid, args)
		if err != nil {
			httpErr(w, stageHTTPStatus(err), err.Error())
			return
		}
		afterStageChange(globalCtx, pid, []*stageChange{change}, rest, true)
		httpJSON(w, map[string]any{"function": change.Function, "stage": rest, "deleted": change.Changed})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPruneVersions(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := decodeObjectBody(w, r)
	if err != nil {
		httpErr(w, requestErrorStatus(err), err.Error())
		return
	}
	body["function"] = id
	out, err := pruneFromArgs(globalCtx.AppDB(), pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPStages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := decodeObjectBody(w, r)
	if err != nil {
		httpErr(w, requestErrorStatus(err), err.Error())
		return
	}
	var changes []*stageChange
	switch strings.TrimPrefix(r.URL.Path, "/stages/") {
	case "promote":
		changes, err = stagePromoteMany(globalCtx.AppDB(), pid, body)
	case "rollback":
		changes, err = stageRollback(globalCtx.AppDB(), pid, body)
	default:
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		httpErr(w, stageHTTPStatus(err), err.Error())
		return
	}
	afterStageChange(globalCtx, pid, changes, strArg(body, "stage"), false)
	httpJSON(w, stageChangesOut(strArg(body, "stage"), changes))
}

// httpTarget applies ?stage= / ?version= on the HTTP invoke routes.
func httpTarget(w http.ResponseWriter, r *http.Request, fn *Function) (*Function, bool) {
	q := r.URL.Query()
	args := map[string]any{}
	if s := q.Get("stage"); s != "" {
		args["stage"] = s
	}
	if v := q.Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "version must be a positive version number")
			return nil, false
		}
		args["version"] = n
	}
	stage, version, err := targetArgs(args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	target, err := resolveInvocationTarget(globalCtx.AppDB(), fn, stage, version)
	if err != nil {
		httpErr(w, stageHTTPStatus(err), err.Error())
		return nil, false
	}
	return target, true
}

// attachStages adds a function's stages (masked unless reveal) to fn.
func attachStages(db *sql.DB, fn *Function, reveal bool) error {
	stages, err := dbListStages(db, fn.ProjectID, fn.ID)
	if err != nil {
		return err
	}
	if len(stages) > 0 {
		fn.Stages = maskStages(stages, reveal)
	}
	return nil
}
