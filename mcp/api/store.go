package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type API struct {
	ID                    int64  `json:"id"`
	BrowserOriginsPending bool   `json:"browser_origins_pending"`
	BrowserOriginsError   string `json:"browser_origins_error,omitempty"`
	ExposureError         string `json:"exposure_error,omitempty"`
	ProjectID             string `json:"project_id,omitempty"`
	Slug                  string `json:"slug"`
	Name                  string `json:"name"`
	Description           string `json:"description,omitempty"`
	Status                string `json:"status"`
	Hostname              string `json:"hostname,omitempty"`
	DNSMode               string `json:"dns_mode"`
	DNSStatus             string `json:"dns_status,omitempty"`
	IngressStatus         string `json:"ingress_status,omitempty"`
	AllowHTTP             bool   `json:"allow_http"`
	CORSJSON              string `json:"cors_json,omitempty"`
	AuthJSON              string `json:"auth_json,omitempty"`
	CreatedAt             string `json:"created_at,omitempty"`
	UpdatedAt             string `json:"updated_at,omitempty"`
}

type APIRoute struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	APIID       int64  `json:"api_id"`
	Method      string `json:"method"`
	PathPattern string `json:"path_pattern"`
	TargetKind  string `json:"target_kind"`
	TargetRef   string `json:"target_ref"`
	TargetPath  string `json:"target_path,omitempty"`
	EventsJSON  string `json:"events_json,omitempty"`
	AuthJSON    string `json:"auth_json,omitempty"`
	CORSJSON    string `json:"cors_json,omitempty"`
	TimeoutMS   int    `json:"timeout_ms"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type APIKey struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id,omitempty"`
	APIID      int64  `json:"api_id"`
	Name       string `json:"name"`
	KeyPrefix  string `json:"key_prefix"`
	Status     string `json:"status"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

type RequestLog struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id,omitempty"`
	APIID      int64  `json:"api_id,omitempty"`
	RouteID    int64  `json:"route_id,omitempty"`
	Hostname   string `json:"hostname"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	StatusCode int    `json:"status_code"`
	TargetKind string `json:"target_kind,omitempty"`
	TargetRef  string `json:"target_ref,omitempty"`
	AuthKind   string `json:"auth_kind,omitempty"`
	Subject    string `json:"subject,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

type apiInput struct {
	ProjectID   string
	Slug        string
	Name        string
	Description string
	Status      string
	Hostname    string
	DNSMode     string
	AllowHTTP   bool
	CORSJSON    string
	AuthJSON    string
}

type routeInput struct {
	ProjectID   string
	APIID       int64
	Method      string
	PathPattern string
	TargetKind  string
	TargetRef   string
	TargetPath  string
	EventsJSON  string
	AuthJSON    string
	CORSJSON    string
	TimeoutMS   int
	Enabled     bool
	Priority    int
	PrioritySet bool
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func normalizeSlug(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !slugRe.MatchString(s) || strings.HasSuffix(s, "-") {
		return "", errors.New("slug must be lowercase letters, digits, and internal dashes")
	}
	return s, nil
}

func normalizeHostname(h string) (string, error) {
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if h == "" {
		return "", nil
	}
	if strings.ContainsAny(h, " \t\r\n/:?#") || len(h) > 253 {
		return "", errors.New("invalid hostname")
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) > 63 || !slugRe.MatchString(label) || strings.HasSuffix(label, "-") {
			return "", errors.New("invalid DNS label")
		}
	}
	return h, nil
}

func normalizeDNSMode(s string) (string, error) {
	if s == "" {
		return "manual", nil
	}
	switch s {
	case "manual", "domains", "skipped":
		return s, nil
	default:
		return "", errors.New("dns_mode must be manual, domains, or skipped")
	}
}

func normalizeStatus(s string) (string, error) {
	if s == "" {
		return "active", nil
	}
	switch s {
	case "active", "disabled":
		return s, nil
	default:
		return "", errors.New("status must be active or disabled")
	}
}

func normalizeMethod(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return "", errors.New("method required")
	}
	switch s {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "ANY":
		return s, nil
	default:
		return "", errors.New("method must be GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS, or ANY")
	}
}

func normalizePathPattern(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || !strings.HasPrefix(s, "/") {
		return "", errors.New("path_pattern must start with /")
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return "", errors.New("path_pattern must not contain whitespace")
	}
	s = strings.TrimRight(s, "/")
	if s == "" {
		return "/", nil
	}
	seen := map[string]bool{}
	parts := splitPath(s)
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "?#%\\") {
			return "", errors.New("invalid path segment")
		}
		if strings.Contains(part, "*") && (part != "*" || i != len(parts)-1) {
			return "", errors.New("wildcard must be a terminal segment")
		}
		if strings.HasPrefix(part, ":") {
			name := part[1:]
			if name == "" || seen[name] {
				return "", errors.New("invalid or duplicate path parameter")
			}
			seen[name] = true
		}
	}
	return s, nil
}

func normalizeTargetKind(s string) (string, error) {
	switch strings.TrimSpace(s) {
	case "function", "app", "http", "app_events":
		return strings.TrimSpace(s), nil
	default:
		return "", errors.New("target_kind must be function, app, http, or app_events")
	}
}

func validateTarget(kind, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("target_ref required")
	}
	if kind == "http" {
		u, err := url.Parse(ref)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("http target_ref must be an absolute http(s) URL")
		}
	}
	if (kind == "app_events" || kind == "app") && (!slugRe.MatchString(ref) || strings.HasSuffix(ref, "-")) {
		return errors.New("target_ref must be an app name")
	}
	return nil
}

const apiBaseCols = `id, project_id, slug, name, description, status, hostname, dns_mode,
	dns_status, ingress_status, allow_http, cors_json, auth_json, created_at, updated_at`
const apiCols = apiBaseCols + `,
 COALESCE((SELECT pending FROM api_policy_sync WHERE api_id=apis.id),0),
 COALESCE((SELECT error FROM api_policy_sync WHERE api_id=apis.id),''),
 COALESCE((SELECT group_concat(error, '; ') FROM api_exposures WHERE api_id=apis.id AND error<>''),'')`

func scanAPI(row interface{ Scan(dest ...any) error }) (*API, error) {
	var a API
	var allow int
	if err := row.Scan(&a.ID, &a.ProjectID, &a.Slug, &a.Name, &a.Description, &a.Status,
		&a.Hostname, &a.DNSMode, &a.DNSStatus, &a.IngressStatus, &allow,
		&a.CORSJSON, &a.AuthJSON, &a.CreatedAt, &a.UpdatedAt, &a.BrowserOriginsPending, &a.BrowserOriginsError, &a.ExposureError); err != nil {
		return nil, err
	}
	a.AllowHTTP = allow != 0
	return &a, nil
}

func ensureHostnameAvailable(db *sql.DB, host string, id int64) error {
	if host == "" {
		return nil
	}
	var conflicts int
	err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM apis WHERE hostname=? AND id<>?)+(SELECT COUNT(*) FROM api_exposures WHERE hostname=? AND api_id<>?)`, host, id, host, id).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts > 0 {
		return errors.New("hostname belongs to another API or has pending cleanup")
	}
	return nil
}

func dbCreateAPI(db *sql.DB, in apiInput) (*API, error) {
	if err := validateAuthPolicy(defaultJSON(in.AuthJSON)); err != nil {
		return nil, err
	}
	if _, err := parseEffectiveCORSPolicy(defaultJSON(in.CORSJSON), "{}"); err != nil {
		return nil, err
	}
	slug, err := normalizeSlug(in.Slug)
	if err != nil {
		return nil, err
	}
	host, err := normalizeHostname(in.Hostname)
	if err != nil {
		return nil, err
	}
	if err := ensureHostnameAvailable(db, host, 0); err != nil {
		return nil, err
	}
	dnsMode, err := normalizeDNSMode(in.DNSMode)
	if err != nil {
		return nil, err
	}
	status, err := normalizeStatus(in.Status)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = slug
	}
	now := time.Now().UTC().Format(time.RFC3339)
	allow := 0
	if in.AllowHTTP {
		allow = 1
	}
	res, err := db.Exec(`INSERT INTO apis
		(project_id, slug, name, description, status, hostname, dns_mode, allow_http, cors_json, auth_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, slug, name, strings.TrimSpace(in.Description), status, host, dnsMode, allow, defaultJSON(in.CORSJSON), defaultJSON(in.AuthJSON), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetAPIByID(db, in.ProjectID, id)
}

func dbUpdateAPI(db *sql.DB, pid string, id int64, patch map[string]any) (*API, error) {
	cur, err := dbGetAPIByID(db, pid, id)
	if err != nil || cur == nil {
		return cur, err
	}
	in := *cur
	if v, ok := patch["name"].(string); ok {
		in.Name = strings.TrimSpace(v)
	}
	if v, ok := patch["description"].(string); ok {
		in.Description = strings.TrimSpace(v)
	}
	if v, ok := patch["status"].(string); ok {
		in.Status, err = normalizeStatus(v)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := patch["hostname"].(string); ok {
		in.Hostname, err = normalizeHostname(v)
		if err != nil {
			return nil, err
		}
		if err := ensureHostnameAvailable(db, in.Hostname, id); err != nil {
			return nil, err
		}
	}
	if v, ok := patch["dns_mode"].(string); ok {
		in.DNSMode, err = normalizeDNSMode(v)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := patch["allow_http"].(bool); ok {
		in.AllowHTTP = v
	}
	if v, ok := patch["cors"]; ok {
		in.CORSJSON, err = marshalJSONDefault(v)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := patch["auth"]; ok {
		in.AuthJSON, err = normalizedAuthArg(map[string]any{"auth": v}, "auth", in.AuthJSON)
		if err != nil {
			return nil, err
		}
	}
	allow := 0
	if in.AllowHTTP {
		allow = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if in.Hostname != cur.Hostname || in.DNSMode != cur.DNSMode || in.AllowHTTP != cur.AllowHTTP || in.Status != cur.Status {
		in.DNSStatus, in.IngressStatus = "", ""
	}
	_, err = db.Exec(`UPDATE apis SET name=?, description=?, status=?, hostname=?, dns_mode=?,
		allow_http=?, cors_json=?, auth_json=?, updated_at=?, dns_status=?, ingress_status=? WHERE id=? AND project_id=?`,
		in.Name, in.Description, in.Status, in.Hostname, in.DNSMode, allow, defaultJSON(in.CORSJSON), defaultJSON(in.AuthJSON), now, in.DNSStatus, in.IngressStatus, id, pid)
	if err != nil {
		return nil, err
	}
	invalidateRoutes(db, pid, id)
	return dbGetAPIByID(db, pid, id)
}

func dbSetAPIExposureStatus(db *sql.DB, pid string, id int64, dnsStatus, ingressStatus string) {
	_, _ = db.Exec(`UPDATE apis SET dns_status=?, ingress_status=?, updated_at=? WHERE project_id=? AND id=?`,
		dnsStatus, ingressStatus, time.Now().UTC().Format(time.RFC3339), pid, id)
}

func dbGetAPIByID(db *sql.DB, pid string, id int64) (*API, error) {
	row := db.QueryRow(`SELECT `+apiCols+` FROM apis WHERE project_id=? AND id=?`, pid, id)
	a, err := scanAPI(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// Public dispatch still reads authoritative policy/status on every request,
// without querying management-only reconciliation diagnostics.
func dbGetPublicAPI(db *sql.DB, pid, field string, value any) (*API, error) {
	if field != "id" && field != "slug" && field != "hostname" {
		return nil, errors.New("invalid lookup field")
	}
	a, err := scanAPI(db.QueryRow(`SELECT `+apiBaseCols+`,0,'','' FROM apis WHERE project_id=? AND `+field+`=?`, pid, value))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func dbGetAPIBySlug(db *sql.DB, pid, slug string) (*API, error) {
	row := db.QueryRow(`SELECT `+apiCols+` FROM apis WHERE project_id=? AND slug=?`, pid, slug)
	a, err := scanAPI(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func dbGetAPIByHostname(db *sql.DB, pid, host string) (*API, error) {
	row := db.QueryRow(`SELECT `+apiCols+` FROM apis WHERE project_id=? AND hostname=?`, pid, host)
	a, err := scanAPI(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func dbListAPIs(db *sql.DB, pid string) ([]*API, error) {
	rows, err := db.Query(`SELECT `+apiCols+` FROM apis WHERE project_id=? ORDER BY slug`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*API
	for rows.Next() {
		a, err := scanAPI(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func dbListAllAPIs(db *sql.DB) ([]*API, error) {
	rows, err := db.Query(`SELECT ` + apiCols + ` FROM apis ORDER BY project_id, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*API
	for rows.Next() {
		a, err := scanAPI(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func dbDeleteAPI(db *sql.DB, pid string, id int64) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO api_policy_sync(api_id,project_id,delete_requested,pending)
 SELECT id,project_id,1,1 FROM apis WHERE project_id=? AND id=?
 ON CONFLICT(api_id) DO UPDATE SET delete_requested=1,pending=1`, pid, id); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE api_exposures SET cleanup=1 WHERE project_id=? AND api_id=?`, pid, id); err != nil {
		return false, err
	}
	for _, table := range []string{"api_request_logs", "api_keys", "api_routes"} {
		if _, err = tx.Exec("DELETE FROM "+table+" WHERE project_id=? AND api_id=?", pid, id); err != nil {
			return false, err
		}
	}
	res, err := tx.Exec("DELETE FROM apis WHERE project_id=? AND id=?", pid, id)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	invalidateRoutes(db, pid, id)
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const routeCols = `id, project_id, api_id, method, path_pattern, target_kind, target_ref,
	target_path, events_json, auth_json, cors_json, timeout_ms, enabled, priority, created_at, updated_at`

func scanRoute(row interface{ Scan(dest ...any) error }) (*APIRoute, error) {
	var r APIRoute
	var enabled int
	if err := row.Scan(&r.ID, &r.ProjectID, &r.APIID, &r.Method, &r.PathPattern, &r.TargetKind,
		&r.TargetRef, &r.TargetPath, &r.EventsJSON, &r.AuthJSON, &r.CORSJSON, &r.TimeoutMS, &enabled,
		&r.Priority, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
	return &r, nil
}

func dbUpsertRoute(db *sql.DB, in routeInput) (*APIRoute, string, error) {
	method, err := normalizeMethod(in.Method)
	if err != nil {
		return nil, "", err
	}
	pattern, err := normalizePathPattern(in.PathPattern)
	if err != nil {
		return nil, "", err
	}
	kind, err := normalizeTargetKind(in.TargetKind)
	if err != nil {
		return nil, "", err
	}
	if err := validateTarget(kind, in.TargetRef); err != nil {
		return nil, "", err
	}
	if kind == "app_events" {
		if method != "GET" {
			return nil, "", errors.New("app_events routes must use GET")
		}
		if _, err := parseAppEventsConfig(in.EventsJSON); err != nil {
			return nil, "", err
		}
	} else {
		in.EventsJSON = "{}"
	}
	if in.TimeoutMS == 0 {
		in.TimeoutMS = 30000
	}
	if in.TimeoutMS < 1 || in.TimeoutMS > 300000 {
		return nil, "", errors.New("timeout_ms must be between 1 and 300000")
	}
	if err := validateAuthPolicy(defaultJSON(in.AuthJSON)); err != nil {
		return nil, "", err
	}
	if in.TargetPath != "" && (!strings.HasPrefix(in.TargetPath, "/") || strings.ContainsAny(in.TargetPath, "?#\\\r\n")) {
		return nil, "", errors.New("target_path must be an absolute path without query or fragment")
	}
	if in.Priority == 0 && !in.PrioritySet {
		in.Priority = 100
	}
	enabled := 0
	if in.Enabled {
		enabled = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	existing, err := dbGetRouteByKey(db, in.ProjectID, in.APIID, method, pattern)
	if err != nil {
		return nil, "", err
	}
	if existing != nil {
		_, err = db.Exec(`UPDATE api_routes SET target_kind=?, target_ref=?, target_path=?, events_json=?,
			auth_json=?, cors_json=?, timeout_ms=?, enabled=?, priority=?, updated_at=?
			WHERE id=? AND project_id=?`,
			kind, strings.TrimSpace(in.TargetRef), strings.TrimSpace(in.TargetPath), defaultJSON(in.EventsJSON), defaultJSON(in.AuthJSON),
			defaultJSON(in.CORSJSON), in.TimeoutMS, enabled, in.Priority, now, existing.ID, in.ProjectID)
		if err != nil {
			return nil, "", err
		}
		invalidateRoutes(db, in.ProjectID, in.APIID)
		r, err := dbGetRouteByID(db, in.ProjectID, existing.ID)
		return r, "updated", err
	}
	res, err := db.Exec(`INSERT INTO api_routes
		(project_id, api_id, method, path_pattern, target_kind, target_ref, target_path, events_json, auth_json, cors_json, timeout_ms, enabled, priority, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(project_id,api_id,method,path_pattern) DO UPDATE SET
 target_kind=excluded.target_kind,target_ref=excluded.target_ref,target_path=excluded.target_path,
 events_json=excluded.events_json,auth_json=excluded.auth_json,cors_json=excluded.cors_json,
 timeout_ms=excluded.timeout_ms,enabled=excluded.enabled,priority=excluded.priority,updated_at=excluded.updated_at`,
		in.ProjectID, in.APIID, method, pattern, kind, strings.TrimSpace(in.TargetRef), strings.TrimSpace(in.TargetPath),
		defaultJSON(in.EventsJSON), defaultJSON(in.AuthJSON), defaultJSON(in.CORSJSON), in.TimeoutMS, enabled, in.Priority, now, now)
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	invalidateRoutes(db, in.ProjectID, in.APIID)
	r, err := dbGetRouteByID(db, in.ProjectID, id)
	return r, "created", err
}

func dbGetRouteByKey(db *sql.DB, pid string, apiID int64, method, pattern string) (*APIRoute, error) {
	row := db.QueryRow(`SELECT `+routeCols+` FROM api_routes WHERE project_id=? AND api_id=? AND method=? AND path_pattern=?`,
		pid, apiID, method, pattern)
	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func dbGetRouteByID(db *sql.DB, pid string, id int64) (*APIRoute, error) {
	row := db.QueryRow(`SELECT `+routeCols+` FROM api_routes WHERE project_id=? AND id=?`, pid, id)
	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func dbListRoutes(db *sql.DB, pid string, apiID int64) ([]*APIRoute, error) {
	rows, err := db.Query(`SELECT `+routeCols+` FROM api_routes WHERE project_id=? AND api_id=? ORDER BY priority, path_pattern`, pid, apiID)
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

func dbDeleteRoute(db *sql.DB, pid string, id int64) (bool, error) {
	route, err := dbGetRouteByID(db, pid, id)
	if err != nil {
		return false, err
	}
	res, err := db.Exec(`DELETE FROM api_routes WHERE project_id=? AND id=?`, pid, id)
	if route != nil {
		invalidateRoutes(db, pid, route.APIID)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbMatchRoute(db *sql.DB, pid string, apiID int64, method, path string) (*APIRoute, map[string]string, error) {
	routes, err := compiledRoutes(db, pid, apiID)
	if err != nil {
		return nil, nil, err
	}
	segments := splitPath(path)
	for _, r := range routes {
		if r.route.Method != "ANY" && r.route.Method != method {
			continue
		}
		if params, ok := matchSegments(r.segments, segments); ok {
			return r.route, params, nil
		}
	}
	return nil, nil, nil
}
func matchPath(pattern, path string) (map[string]string, bool) {
	return matchSegments(splitPath(pattern), splitPath(path))
}

func splitPath(s string) []string {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func makeAPIKey() (plaintext, prefix, hash string, err error) {
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	plaintext = "aptv_api_" + hex.EncodeToString(buf)
	prefix = plaintext[:18]
	sum := sha256.Sum256([]byte(plaintext))
	return plaintext, prefix, hex.EncodeToString(sum[:]), nil
}

func hashAPIKey(k string) string {
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:])
}

func dbCreateAPIKey(db *sql.DB, pid string, apiID int64, name string) (*APIKey, string, error) {
	plain, prefix, hash, err := makeAPIKey()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`INSERT INTO api_keys (project_id, api_id, name, key_prefix, key_hash, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'active', ?)`, pid, apiID, strings.TrimSpace(name), prefix, hash, now)
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	key, err := dbGetAPIKey(db, pid, id)
	return key, plain, err
}

func dbGetAPIKey(db *sql.DB, pid string, id int64) (*APIKey, error) {
	row := db.QueryRow(`SELECT id, project_id, api_id, name, key_prefix, status, COALESCE(last_used_at,''), created_at, COALESCE(revoked_at,'')
		FROM api_keys WHERE project_id=? AND id=?`, pid, id)
	var k APIKey
	if err := row.Scan(&k.ID, &k.ProjectID, &k.APIID, &k.Name, &k.KeyPrefix, &k.Status, &k.LastUsedAt, &k.CreatedAt, &k.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &k, nil
}

func dbListAPIKeys(db *sql.DB, pid string, apiID int64) ([]*APIKey, error) {
	rows, err := db.Query(`SELECT id, project_id, api_id, name, key_prefix, status, COALESCE(last_used_at,''), created_at, COALESCE(revoked_at,'')
		FROM api_keys WHERE project_id=? AND api_id=? ORDER BY created_at DESC`, pid, apiID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.APIID, &k.Name, &k.KeyPrefix, &k.Status, &k.LastUsedAt, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &k)
	}
	return out, rows.Err()
}

func validateAPIKey(db *sql.DB, pid string, apiID int64, plain string) (int64, bool, error) {
	prefix := ""
	if len(plain) >= 18 {
		prefix = plain[:18]
	}
	row := db.QueryRow(`SELECT id, key_hash, COALESCE(last_used_at,'') FROM api_keys WHERE project_id=? AND api_id=? AND key_prefix=? AND status='active'`, pid, apiID, prefix)
	var id int64
	var want, lastUsed string
	if err := row.Scan(&id, &want, &lastUsed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if subtle.ConstantTimeCompare([]byte(hashAPIKey(plain)), []byte(want)) != 1 {
		return 0, false, nil
	}
	if lastUsed < time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) {
		_, _ = db.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at < ?)`, time.Now().UTC().Format(time.RFC3339), id, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	}
	return id, true, nil
}

func dbValidateAPIKey(db *sql.DB, pid string, apiID int64, plain string) (bool, error) {
	_, ok, err := validateAPIKey(db, pid, apiID, plain)
	return ok, err
}

func dbRevokeAPIKey(db *sql.DB, pid string, id int64) (bool, error) {
	res, err := db.Exec(`UPDATE api_keys SET status='revoked', revoked_at=? WHERE project_id=? AND id=? AND status!='revoked'`,
		time.Now().UTC().Format(time.RFC3339), pid, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbInsertLog(db *sql.DB, l RequestLog) {
	_, _ = db.Exec(`INSERT INTO api_request_logs
		(project_id, api_id, route_id, hostname, method, path, status_code, target_kind, target_ref, auth_kind, subject, duration_ms, error, request_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ProjectID, nullableID(l.APIID), nullableID(l.RouteID), l.Hostname, l.Method, l.Path, l.StatusCode,
		l.TargetKind, l.TargetRef, l.AuthKind, l.Subject, l.DurationMS, l.Error, l.RequestID)
}

func dbListLogs(db *sql.DB, pid string, apiID int64, limit int) ([]*RequestLog, error) {
	return dbListLogsBefore(db, pid, apiID, limit, 0)
}
func dbListLogsBefore(db *sql.DB, pid string, apiID int64, limit int, beforeID int64) ([]*RequestLog, error) {
	flushRequestLogs(db)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, project_id, COALESCE(api_id,0), COALESCE(route_id,0), hostname, method, path,
		status_code, target_kind, target_ref, auth_kind, subject, duration_ms, error, request_id, created_at
		FROM api_request_logs WHERE project_id=? AND api_id=?`
	args := []any{pid, apiID}
	if beforeID > 0 {
		query += " AND id<?"
		args = append(args, beforeID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RequestLog
	for rows.Next() {
		var l RequestLog
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.APIID, &l.RouteID, &l.Hostname, &l.Method, &l.Path,
			&l.StatusCode, &l.TargetKind, &l.TargetRef, &l.AuthKind, &l.Subject, &l.DurationMS, &l.Error, &l.RequestID, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func defaultJSON(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

func marshalJSONDefault(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	b, err := jsonMarshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
