package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// APIConfiguration is an immutable snapshot of an API's policies and routes.
// The existing apis/api_routes tables remain the legacy mutable API surface.
type APIConfiguration struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	APIID       int64  `json:"api_id"`
	Version     int64  `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	AllowHTTP   bool   `json:"allow_http"`
	CORSJSON    string `json:"cors_json,omitempty"`
	AuthJSON    string `json:"auth_json,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type APIStage struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id"`
	APIID           int64  `json:"api_id"`
	Name            string `json:"name"`
	ConfigurationID int64  `json:"configuration_id"`
	Hostname        string `json:"hostname,omitempty"`
	Status          string `json:"status"`
	CORSJSON        string `json:"cors_json,omitempty"`
	AuthJSON        string `json:"auth_json,omitempty"`
	// Vars are substituted into {{stage.vars.<key>}} route target
	// placeholders for requests served by this stage.
	Vars map[string]string `json:"vars"`
	// PreviousConfigurationID is the configuration the stage pointed at
	// before its last promote/rollback; 0 when there is none.
	PreviousConfigurationID int64 `json:"previous_configuration_id"`
	// Rev is bumped on every change and checked against expected_rev.
	Rev       int64  `json:"rev"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// stageRevConflictError reports an expected_rev mismatch; nothing changed.
type stageRevConflictError struct{ expected, current int64 }

func (e *stageRevConflictError) Error() string {
	return fmt.Sprintf("stage rev conflict: expected rev %d, current rev %d; re-read the stage and retry", e.expected, e.current)
}

const configurationCols = `id, project_id, api_id, version, name, description,
 allow_http, cors_json, auth_json, created_at`

func scanConfiguration(row interface{ Scan(dest ...any) error }) (*APIConfiguration, error) {
	var c APIConfiguration
	var allow int
	err := row.Scan(&c.ID, &c.ProjectID, &c.APIID, &c.Version, &c.Name, &c.Description,
		&allow, &c.CORSJSON, &c.AuthJSON, &c.CreatedAt)
	c.AllowHTTP = allow != 0
	return &c, err
}

func dbGetConfiguration(db *sql.DB, pid string, id int64) (*APIConfiguration, error) {
	c, err := scanConfiguration(db.QueryRow(`SELECT `+configurationCols+` FROM api_configurations WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbListConfigurations(db *sql.DB, pid string, apiID int64) ([]*APIConfiguration, error) {
	rows, err := db.Query(`SELECT `+configurationCols+` FROM api_configurations WHERE project_id=? AND api_id=? ORDER BY version DESC`, pid, apiID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIConfiguration
	for rows.Next() {
		c, err := scanConfiguration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbListConfigurationRoutes(db *sql.DB, pid string, configurationID int64) ([]*APIRoute, error) {
	rows, err := db.Query(`SELECT `+routeCols+` FROM api_configuration_routes WHERE project_id=? AND configuration_id=? ORDER BY priority, path_pattern`, pid, configurationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIRoute
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func dbGetConfigurationRouteByID(db *sql.DB, pid string, id int64) (*APIRoute, error) {
	r, err := scanRoute(db.QueryRow(`SELECT `+routeCols+` FROM api_configuration_routes WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// dbCreateConfiguration snapshots either the current mutable API or an
// existing configuration. The source is copied in one transaction so a stage
// can never observe a partially-created configuration.
func dbCreateConfiguration(db *sql.DB, pid string, apiID, sourceID int64, name string) (*APIConfiguration, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var apiName, description, corsJSON, authJSON string
	var allow int
	if err := tx.QueryRow(`SELECT name,description,allow_http,cors_json,auth_json FROM apis WHERE project_id=? AND id=?`, pid, apiID).
		Scan(&apiName, &description, &allow, &corsJSON, &authJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("api not found")
		}
		return nil, err
	}
	if sourceID != 0 {
		var sourceAPI int64
		if err := tx.QueryRow(`SELECT api_id FROM api_configurations WHERE project_id=? AND id=?`, pid, sourceID).Scan(&sourceAPI); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("configuration not found")
			}
			return nil, err
		}
		if sourceAPI != apiID {
			return nil, errors.New("configuration belongs to another api")
		}
		if err := tx.QueryRow(`SELECT name,description,allow_http,cors_json,auth_json FROM api_configurations WHERE project_id=? AND id=?`, pid, sourceID).
			Scan(&apiName, &description, &allow, &corsJSON, &authJSON); err != nil {
			return nil, err
		}
	}
	var version int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM api_configurations WHERE project_id=? AND api_id=?`, pid, apiID).Scan(&version); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("v%d", version)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`INSERT INTO api_configurations(project_id,api_id,version,name,description,allow_http,cors_json,auth_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		pid, apiID, version, name, description, allow, defaultJSON(corsJSON), defaultJSON(authJSON), now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if sourceID != 0 {
		_, err = tx.Exec(`INSERT INTO api_configuration_routes(project_id,api_id,configuration_id,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at,target_stage,websocket_json)
 SELECT project_id,api_id,?,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at,target_stage,websocket_json FROM api_configuration_routes WHERE project_id=? AND configuration_id=?`, id, pid, sourceID)
	} else {
		_, err = tx.Exec(`INSERT INTO api_configuration_routes(project_id,api_id,configuration_id,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at,target_stage,websocket_json)
 SELECT project_id,api_id,?,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at,target_stage,websocket_json FROM api_routes WHERE project_id=? AND api_id=?`, id, pid, apiID)
	}
	if err != nil {
		return nil, err
	}
	if _, err := configurationStageVarRefs(tx, pid, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetConfiguration(db, pid, id)
}

const stageCols = `id, project_id, api_id, name, configuration_id, hostname, status, cors_json, auth_json, vars_json, previous_configuration_id, rev, created_at, updated_at`

func scanStage(row interface{ Scan(dest ...any) error }) (*APIStage, error) {
	var s APIStage
	var vars string
	err := row.Scan(&s.ID, &s.ProjectID, &s.APIID, &s.Name, &s.ConfigurationID, &s.Hostname, &s.Status, &s.CORSJSON, &s.AuthJSON, &vars, &s.PreviousConfigurationID, &s.Rev, &s.CreatedAt, &s.UpdatedAt)
	s.Vars = parseStageVarsJSON(vars)
	return &s, err
}

type rowQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// configurationStageVarRefs lists the stage variable keys referenced by a
// configuration's route targets.
func configurationStageVarRefs(q rowQuerier, pid string, configurationID int64) (map[string]struct{}, error) {
	rows, err := q.Query(`SELECT method,path_pattern,target_ref,target_path,target_stage,events_json FROM api_configuration_routes WHERE project_id=? AND configuration_id=?`, pid, configurationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]struct{}{}
	for rows.Next() {
		var method, pattern, ref, path, stage, events string
		if err := rows.Scan(&method, &pattern, &ref, &path, &stage, &events); err != nil {
			return nil, err
		}
		if err := routeStageVarRefs(ref, path, stage, events, refs); err != nil {
			return nil, fmt.Errorf("route %s %s: %w", method, pattern, err)
		}
	}
	return refs, rows.Err()
}

// ensureStageVarsCover rejects a stage write that would leave a referenced
// variable undefined. Requests still fail closed if one is ever missing.
func ensureStageVarsCover(q rowQuerier, pid string, configurationID int64, stageName string, vars map[string]string) error {
	refs, err := configurationStageVarRefs(q, pid, configurationID)
	if err != nil {
		return err
	}
	if missing := missingStageVars(refs, vars); len(missing) > 0 {
		return errMissingStageVars(stageName, missing)
	}
	return nil
}

func dbGetStage(db *sql.DB, pid string, id int64) (*APIStage, error) {
	s, err := scanStage(db.QueryRow(`SELECT `+stageCols+` FROM api_stages WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbGetStageByName(db *sql.DB, pid string, apiID int64, name string) (*APIStage, error) {
	s, err := scanStage(db.QueryRow(`SELECT `+stageCols+` FROM api_stages WHERE project_id=? AND api_id=? AND name=?`, pid, apiID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbGetStageByHostname(db *sql.DB, pid, hostname string) (*APIStage, error) {
	s, err := scanStage(db.QueryRow(`SELECT `+stageCols+` FROM api_stages WHERE project_id=? AND hostname=?`, pid, hostname))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbListStages(db *sql.DB, pid string, apiID int64) ([]*APIStage, error) {
	rows, err := db.Query(`SELECT `+stageCols+` FROM api_stages WHERE project_id=? AND api_id=? ORDER BY name`, pid, apiID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIStage
	for rows.Next() {
		s, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func ensureStageHostnameAvailable(db *sql.DB, pid, host string, stageID int64) error {
	if host == "" {
		return nil
	}
	var n int
	err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM apis WHERE project_id=? AND hostname=?)+(SELECT COUNT(*) FROM api_exposures WHERE project_id=? AND hostname=? AND api_id<>0)+(SELECT COUNT(*) FROM api_stages WHERE project_id=? AND hostname=? AND id<>?)`, pid, host, pid, host, pid, host, stageID).Scan(&n)
	if err != nil {
		return err
	}
	if n != 0 {
		return errors.New("hostname belongs to another API or stage")
	}
	return nil
}

func dbCreateStage(db *sql.DB, in APIStage) (*APIStage, error) {
	host, err := normalizeHostname(in.Hostname)
	if err != nil {
		return nil, err
	}
	if err = normalizeStageName(in.Name); err != nil {
		return nil, err
	}
	status, err := normalizeStatus(in.Status)
	if err != nil {
		return nil, err
	}
	if in.ConfigurationID == 0 {
		return nil, errors.New("configuration_id required")
	}
	c, err := dbGetConfiguration(db, in.ProjectID, in.ConfigurationID)
	if err != nil || c == nil {
		if err == nil {
			err = errors.New("configuration not found")
		}
		return nil, err
	}
	if c.APIID != in.APIID {
		return nil, errors.New("configuration belongs to another api")
	}
	if err := ensureStageHostnameAvailable(db, in.ProjectID, host, 0); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.CORSJSON) != "" {
		if _, err := parseEffectiveCORSPolicy(c.CORSJSON, in.CORSJSON); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(in.AuthJSON) != "" {
		if err := validateAuthPolicy(in.AuthJSON); err != nil {
			return nil, err
		}
	}
	vars, varsJSON, err := normalizeStageVars(in.Vars)
	if err != nil {
		return nil, err
	}
	if err := ensureStageVarsCover(db, in.ProjectID, in.ConfigurationID, strings.TrimSpace(in.Name), vars); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`INSERT INTO api_stages(project_id,api_id,name,configuration_id,hostname,status,cors_json,auth_json,vars_json,previous_configuration_id,rev,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,0,1,?,?)`, in.ProjectID, in.APIID, strings.TrimSpace(in.Name), in.ConfigurationID, host, status, in.CORSJSON, in.AuthJSON, varsJSON, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetStage(db, in.ProjectID, id)
}

func normalizeStageName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 63 {
		return errors.New("stage name must be 1-63 characters")
	}
	for i, r := range name {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return errors.New("invalid stage name")
		}
	}
	return nil
}

func dbUpdateStage(db *sql.DB, s *APIStage, patch map[string]any, expectedRev int64) (*APIStage, error) {
	if expectedRev != 0 && expectedRev != s.Rev {
		return nil, &stageRevConflictError{expectedRev, s.Rev}
	}
	name, host, status, cors, auth := s.Name, s.Hostname, s.Status, s.CORSJSON, s.AuthJSON
	varsJSON, err := json.Marshal(s.Vars)
	if err != nil {
		return nil, err
	}
	if v, ok := patch["name"].(string); ok {
		name = strings.TrimSpace(v)
		err = normalizeStageName(name)
	}
	if err != nil {
		return nil, err
	}
	if v, ok := patch["hostname"].(string); ok {
		host, err = normalizeHostname(v)
		if err != nil {
			return nil, err
		}
		if err = ensureStageHostnameAvailable(db, s.ProjectID, host, s.ID); err != nil {
			return nil, err
		}
	}
	if v, ok := patch["status"].(string); ok {
		status, err = normalizeStatus(v)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := patch["cors"]; ok {
		cors, err = marshalJSONDefault(v)
		if err != nil {
			return nil, err
		}
		c, _ := dbGetConfiguration(db, s.ProjectID, s.ConfigurationID)
		if c != nil {
			if _, err = parseEffectiveCORSPolicy(c.CORSJSON, cors); err != nil {
				return nil, err
			}
		}
	}
	if v, ok := patch["auth"]; ok {
		auth, err = normalizedAuthArg(map[string]any{"auth": v}, "auth", auth)
		if err != nil {
			return nil, err
		}
	}
	vars := s.Vars
	if v, ok := patch["vars"]; ok {
		var canonical string
		vars, canonical, err = normalizeStageVars(v)
		if err != nil {
			return nil, err
		}
		varsJSON = []byte(canonical)
	}
	if err := ensureStageVarsCover(db, s.ProjectID, s.ConfigurationID, name, vars); err != nil {
		return nil, err
	}
	res, err := db.Exec(`UPDATE api_stages SET name=?,hostname=?,status=?,cors_json=?,auth_json=?,vars_json=?,rev=rev+1,updated_at=? WHERE project_id=? AND id=? AND rev=?`, name, host, status, cors, auth, string(varsJSON), time.Now().UTC().Format(time.RFC3339), s.ProjectID, s.ID, s.Rev)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		current, err := dbGetStage(db, s.ProjectID, s.ID)
		if err != nil || current == nil {
			return nil, errors.New("stage not found")
		}
		return nil, &stageRevConflictError{s.Rev, current.Rev}
	}
	return dbGetStage(db, s.ProjectID, s.ID)
}

// dbPromoteStage points a stage at a configuration in one transaction:
// the stage is read, expected_rev checked, the target validated and the
// pointer moved together, recording the replaced configuration as previous.
func dbPromoteStage(db *sql.DB, pid string, stageID, configurationID, expectedRev int64) (*APIStage, error) {
	if configurationID == 0 {
		return nil, errors.New("configuration_id required")
	}
	return dbMoveStage(db, pid, stageID, expectedRev, func(*APIStage) (int64, error) { return configurationID, nil })
}

// dbRollbackStage moves a stage to configurationID, or to its previous
// configuration when configurationID is 0.
func dbRollbackStage(db *sql.DB, pid string, stageID, configurationID, expectedRev int64) (*APIStage, error) {
	return dbMoveStage(db, pid, stageID, expectedRev, func(s *APIStage) (int64, error) {
		if configurationID != 0 {
			return configurationID, nil
		}
		if s.PreviousConfigurationID == 0 {
			return 0, errors.New("stage has no previous configuration to roll back to; pass configuration_id")
		}
		return s.PreviousConfigurationID, nil
	})
}

func dbMoveStage(db *sql.DB, pid string, stageID, expectedRev int64, target func(*APIStage) (int64, error)) (*APIStage, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	s, err := scanStage(tx.QueryRow(`SELECT `+stageCols+` FROM api_stages WHERE project_id=? AND id=?`, pid, stageID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("stage not found")
	}
	if err != nil {
		return nil, err
	}
	if expectedRev != 0 && expectedRev != s.Rev {
		return nil, &stageRevConflictError{expectedRev, s.Rev}
	}
	configurationID, err := target(s)
	if err != nil {
		return nil, err
	}
	var configAPI int64
	if err := tx.QueryRow(`SELECT api_id FROM api_configurations WHERE project_id=? AND id=?`, pid, configurationID).Scan(&configAPI); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("configuration not found")
		}
		return nil, err
	}
	if configAPI != s.APIID {
		return nil, errors.New("configuration belongs to another api")
	}
	if configurationID == s.ConfigurationID {
		// Already serving it: no change, no rev bump.
		return s, nil
	}
	if err := ensureStageVarsCover(tx, pid, configurationID, s.Name, s.Vars); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`UPDATE api_stages SET configuration_id=?,previous_configuration_id=?,rev=rev+1,updated_at=? WHERE project_id=? AND id=? AND rev=?`,
		configurationID, s.ConfigurationID, time.Now().UTC().Format(time.RFC3339), pid, stageID, s.Rev)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, &stageRevConflictError{s.Rev, s.Rev + 1}
	}
	updated, err := scanStage(tx.QueryRow(`SELECT `+stageCols+` FROM api_stages WHERE project_id=? AND id=?`, pid, stageID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// dbGetPublicStageAPI overlays a stage and its immutable configuration onto
// the existing API object. Legacy hostname lookups never enter this path.
func dbGetPublicStageAPI(db *sql.DB, pid, host string) (*API, error) {
	s, err := dbGetStageByHostname(db, pid, host)
	if err != nil || s == nil {
		return nil, err
	}
	a, err := dbGetPublicAPI(db, pid, "id", s.APIID)
	if err != nil || a == nil {
		return a, err
	}
	c, err := dbGetConfiguration(db, pid, s.ConfigurationID)
	if err != nil || c == nil {
		return nil, err
	}
	a.StageID, a.ConfigurationID = s.ID, c.ID
	a.StageName = s.Name
	varsJSON, err := json.Marshal(s.Vars)
	if err != nil {
		return nil, err
	}
	a.StageVarsJSON = string(varsJSON)
	a.Hostname, a.AllowHTTP, a.CORSJSON, a.AuthJSON = s.Hostname, c.AllowHTTP, c.CORSJSON, c.AuthJSON
	if strings.TrimSpace(s.CORSJSON) != "" {
		a.CORSJSON = s.CORSJSON
	}
	if strings.TrimSpace(s.AuthJSON) != "" {
		a.AuthJSON = s.AuthJSON
	}
	if a.Status == "active" {
		a.Status = s.Status
	}
	return a, nil
}

func configurationDiff(db *sql.DB, pid string, leftID, rightID int64) (map[string]any, error) {
	left, err := dbListConfigurationRoutes(db, pid, leftID)
	if err != nil {
		return nil, err
	}
	right, err := dbListConfigurationRoutes(db, pid, rightID)
	if err != nil {
		return nil, err
	}
	key := func(r *APIRoute) string { return r.Method + " " + r.PathPattern }
	leftBy, rightBy := map[string]*APIRoute{}, map[string]*APIRoute{}
	for _, r := range left {
		leftBy[key(r)] = r
	}
	for _, r := range right {
		rightBy[key(r)] = r
	}
	var added, removed, changed []string
	for k, r := range rightBy {
		if l := leftBy[k]; l == nil {
			added = append(added, k)
		} else {
			lb, _ := json.Marshal(l)
			rb, _ := json.Marshal(r)
			if string(lb) != string(rb) {
				changed = append(changed, k)
			}
		}
	}
	for k := range leftBy {
		if rightBy[k] == nil {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return map[string]any{"left_configuration_id": leftID, "right_configuration_id": rightID, "added": added, "removed": removed, "changed": changed}, nil
}
