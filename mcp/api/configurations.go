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
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
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
		_, err = tx.Exec(`INSERT INTO api_configuration_routes(project_id,api_id,configuration_id,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at)
 SELECT project_id,api_id,?,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at FROM api_configuration_routes WHERE project_id=? AND configuration_id=?`, id, pid, sourceID)
	} else {
		_, err = tx.Exec(`INSERT INTO api_configuration_routes(project_id,api_id,configuration_id,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at)
 SELECT project_id,api_id,?,method,path_pattern,target_kind,target_ref,target_path,events_json,auth_json,cors_json,timeout_ms,enabled,priority,created_at,updated_at FROM api_routes WHERE project_id=? AND api_id=?`, id, pid, apiID)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetConfiguration(db, pid, id)
}

const stageCols = `id, project_id, api_id, name, configuration_id, hostname, status, cors_json, auth_json, created_at, updated_at`

func scanStage(row interface{ Scan(dest ...any) error }) (*APIStage, error) {
	var s APIStage
	return &s, row.Scan(&s.ID, &s.ProjectID, &s.APIID, &s.Name, &s.ConfigurationID, &s.Hostname, &s.Status, &s.CORSJSON, &s.AuthJSON, &s.CreatedAt, &s.UpdatedAt)
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
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`INSERT INTO api_stages(project_id,api_id,name,configuration_id,hostname,status,cors_json,auth_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.ProjectID, in.APIID, strings.TrimSpace(in.Name), in.ConfigurationID, host, status, in.CORSJSON, in.AuthJSON, now, now)
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

func dbUpdateStage(db *sql.DB, s *APIStage, patch map[string]any) (*APIStage, error) {
	name, host, status, cors, auth := s.Name, s.Hostname, s.Status, s.CORSJSON, s.AuthJSON
	var err error
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
	_, err = db.Exec(`UPDATE api_stages SET name=?,hostname=?,status=?,cors_json=?,auth_json=?,updated_at=? WHERE project_id=? AND id=?`, name, host, status, cors, auth, time.Now().UTC().Format(time.RFC3339), s.ProjectID, s.ID)
	if err != nil {
		return nil, err
	}
	return dbGetStage(db, s.ProjectID, s.ID)
}

func dbPromoteStage(db *sql.DB, pid string, stageID, configurationID int64) (*APIStage, error) {
	c, err := dbGetConfiguration(db, pid, configurationID)
	if err != nil || c == nil {
		if err == nil {
			err = errors.New("configuration not found")
		}
		return nil, err
	}
	s, err := dbGetStage(db, pid, stageID)
	if err != nil || s == nil {
		if err == nil {
			err = errors.New("stage not found")
		}
		return nil, err
	}
	if c.APIID != s.APIID {
		return nil, errors.New("configuration belongs to another api")
	}
	if _, err = db.Exec(`UPDATE api_stages SET configuration_id=?,updated_at=? WHERE project_id=? AND id=?`, configurationID, time.Now().UTC().Format(time.RFC3339), pid, stageID); err != nil {
		return nil, err
	}
	return dbGetStage(db, pid, stageID)
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
