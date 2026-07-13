package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Redirect struct {
	ID            int64  `json:"id"`
	Hostname      string `json:"hostname"`
	Path          string `json:"path"`
	MatchMode     string `json:"match_mode"`
	Destination   string `json:"destination"`
	StatusCode    int    `json:"status_code"`
	PreservePath  bool   `json:"preserve_path"`
	PreserveQuery bool   `json:"preserve_query"`
	ProjectID     string `json:"project_id,omitempty"`
	Notes         string `json:"notes,omitempty"`
	Hits          int64  `json:"hits"`
	LastHitAt     string `json:"last_hit_at,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type RedirectInput struct {
	Hostname      string
	Path          string
	MatchMode     string
	Destination   string
	StatusCode    int
	PreservePath  bool
	PreserveQuery bool
	ProjectID     string
	Notes         string
}

// RedirectPatch preserves the distinction between an omitted field and an
// explicit zero value. This is essential for booleans and for clearing notes.
type RedirectPatch struct {
	Hostname      *string
	Path          *string
	MatchMode     *string
	Destination   *string
	StatusCode    *int
	PreservePath  *bool
	PreserveQuery *bool
	Notes         *string
}

type HostClaim struct {
	Hostname  string
	ProjectID string
}

type HitCounts struct {
	HitsTotal int64  `json:"hits_total"`
	Date      string `json:"date"`
	DayHits   int64  `json:"day_hits"`
}

type RedirectStat struct {
	RuleID      int64  `json:"rule_id"`
	Destination string `json:"destination"`
	HitsTotal   int64  `json:"hits_total"`
	Date        string `json:"date"`
	DayHits     int64  `json:"day_hits"`
}

type RedirectStatsQuery struct {
	ProjectID string
	RuleID    int64
	From      string
	To        string
	Limit     int
	Offset    int
}

var (
	ErrConflict      = errors.New("a redirect already exists at this hostname+path+match")
	ErrNotFound      = errors.New("redirect not found")
	ErrHostnameOwned = errors.New("hostname is already owned by another project")
	ErrInvalidStats  = errors.New("invalid redirect stats query")
)

func normaliseHostname(h string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(h), "."))
}

func validateHostname(h string) error {
	if h == "" {
		return errors.New("hostname required")
	}
	if len(h) > 253 {
		return errors.New("hostname too long (>253 chars)")
	}
	if strings.Contains(h, "://") || strings.ContainsAny(h, "/?#[]@ :\t\r\n") {
		return errors.New("hostname must be a DNS name without scheme, port, path, credentials, or whitespace")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return errors.New("hostname must be a fully-qualified domain name")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("invalid hostname label %q", label)
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return fmt.Errorf("invalid hostname character %q", r)
			}
		}
	}
	return nil
}

func validateDestination(d string) error {
	if d == "" {
		return errors.New("destination required")
	}
	if strings.ContainsAny(d, "\r\n\x00") {
		return errors.New("destination must not contain control characters")
	}
	if strings.HasPrefix(d, "mailto:") || strings.HasPrefix(d, "tel:") {
		if strings.TrimSpace(strings.SplitN(d, ":", 2)[1]) == "" {
			return errors.New("mailto/tel destination must include a value")
		}
		return nil
	}
	u, err := url.Parse(d)
	if err != nil {
		return fmt.Errorf("invalid destination URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("destination scheme must be http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("destination must include a host")
	}
	return nil
}

func validateStatusCode(c int) error {
	switch c {
	case 301, 302, 307, 308:
		return nil
	default:
		return fmt.Errorf("status_code must be 301, 302, 307, or 308 (got %d)", c)
	}
}

func validateMatchMode(m string) error {
	if m != "exact" && m != "prefix" {
		return fmt.Errorf("match must be 'exact' or 'prefix' (got %q)", m)
	}
	return nil
}

func normalisePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func validatePath(p string) error {
	if strings.ContainsAny(p, "?#\r\n\x00") {
		return errors.New("path must not contain a query, fragment, or control characters")
	}
	return nil
}

func defaultInput(in *RedirectInput) {
	in.Hostname = normaliseHostname(in.Hostname)
	in.Path = normalisePath(in.Path)
	if in.MatchMode == "" {
		in.MatchMode = "exact"
	}
	if in.StatusCode == 0 {
		in.StatusCode = 302
	}
}

func validateInput(in RedirectInput) error {
	if err := validateHostname(in.Hostname); err != nil {
		return err
	}
	if err := validatePath(in.Path); err != nil {
		return err
	}
	if err := validateDestination(in.Destination); err != nil {
		return err
	}
	if err := validateStatusCode(in.StatusCode); err != nil {
		return err
	}
	if err := validateMatchMode(in.MatchMode); err != nil {
		return err
	}
	if in.PreservePath && in.MatchMode != "prefix" {
		return errors.New("preserve_path requires match='prefix'")
	}
	if in.PreservePath && (strings.HasPrefix(in.Destination, "mailto:") || strings.HasPrefix(in.Destination, "tel:")) {
		return errors.New("preserve_path requires an http or https destination")
	}
	return nil
}

const redirectCols = `id, hostname, path, match_mode, destination, status_code,
		preserve_path, preserve_query, project_id, notes, hits, last_hit_at,
		created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanRedirect(s rowScanner) (*Redirect, error) {
	var r Redirect
	var preservePath, preserveQuery int
	var lastHit sql.NullString
	if err := s.Scan(
		&r.ID, &r.Hostname, &r.Path, &r.MatchMode, &r.Destination, &r.StatusCode,
		&preservePath, &preserveQuery, &r.ProjectID, &r.Notes, &r.Hits, &lastHit,
		&r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.Hostname = normaliseHostname(r.Hostname)
	r.PreservePath = preservePath != 0
	r.PreserveQuery = preserveQuery != 0
	if lastHit.Valid {
		r.LastHitAt = lastHit.String
	}
	return &r, nil
}

func claimHostname(tx *sql.Tx, hostname, projectID string) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO redirect_hosts (hostname, project_id) VALUES (?, ?)`, hostname, projectID); err != nil {
		return err
	}
	var owner string
	if err := tx.QueryRow(`SELECT project_id FROM redirect_hosts WHERE hostname=?`, hostname).Scan(&owner); err != nil {
		return err
	}
	if owner != projectID {
		return fmt.Errorf("%w: %s belongs to project %q", ErrHostnameOwned, hostname, owner)
	}
	return nil
}

func semanticConflict(tx *sql.Tx, id int64, in RedirectInput) (bool, error) {
	var found int
	err := tx.QueryRow(`SELECT 1 FROM redirects
		WHERE lower(rtrim(trim(hostname), '.'))=? AND path=? AND match_mode=? AND project_id=? AND id<>?
		LIMIT 1`, in.Hostname, in.Path, in.MatchMode, in.ProjectID, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func dbInsertRedirect(db *sql.DB, in RedirectInput) (*Redirect, error) {
	defaultInput(&in)
	if err := validateInput(in); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := claimHostname(tx, in.Hostname, in.ProjectID); err != nil {
		return nil, err
	}
	if conflict, err := semanticConflict(tx, 0, in); err != nil {
		return nil, err
	} else if conflict {
		return nil, ErrConflict
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`INSERT INTO redirects
		(hostname, path, match_mode, destination, status_code, preserve_path,
		 preserve_query, project_id, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Hostname, in.Path, in.MatchMode, in.Destination, in.StatusCode,
		boolInt(in.PreservePath), boolInt(in.PreserveQuery), in.ProjectID, in.Notes, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetRedirect(db, id, in.ProjectID)
}

func dbUpdateRedirect(db *sql.DB, id int64, projectID string, patch RedirectPatch) (*Redirect, error) {
	existing, err := dbGetRedirect(db, id, projectID)
	if err != nil {
		return nil, err
	}
	merged := RedirectInput{
		Hostname: existing.Hostname, Path: existing.Path, MatchMode: existing.MatchMode,
		Destination: existing.Destination, StatusCode: existing.StatusCode,
		PreservePath: existing.PreservePath, PreserveQuery: existing.PreserveQuery,
		ProjectID: existing.ProjectID, Notes: existing.Notes,
	}
	if patch.Hostname != nil {
		merged.Hostname = *patch.Hostname
	}
	if patch.Path != nil {
		merged.Path = *patch.Path
	}
	if patch.MatchMode != nil {
		merged.MatchMode = *patch.MatchMode
	}
	if patch.Destination != nil {
		merged.Destination = *patch.Destination
	}
	if patch.StatusCode != nil {
		merged.StatusCode = *patch.StatusCode
	}
	if patch.PreservePath != nil {
		merged.PreservePath = *patch.PreservePath
	}
	if patch.PreserveQuery != nil {
		merged.PreserveQuery = *patch.PreserveQuery
	}
	if patch.Notes != nil {
		merged.Notes = *patch.Notes
	}
	merged.Hostname = normaliseHostname(merged.Hostname)
	merged.Path = normalisePath(merged.Path)
	if err := validateInput(merged); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := claimHostname(tx, merged.Hostname, projectID); err != nil {
		return nil, err
	}
	if conflict, err := semanticConflict(tx, id, merged); err != nil {
		return nil, err
	} else if conflict {
		return nil, ErrConflict
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`UPDATE redirects SET hostname=?, path=?, match_mode=?, destination=?,
		status_code=?, preserve_path=?, preserve_query=?, notes=?, updated_at=?
		WHERE id=? AND project_id=?`, merged.Hostname, merged.Path, merged.MatchMode,
		merged.Destination, merged.StatusCode, boolInt(merged.PreservePath),
		boolInt(merged.PreserveQuery), merged.Notes, now, id, projectID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if existing.Hostname != merged.Hostname {
		if _, err := tx.Exec(`DELETE FROM redirect_hosts WHERE hostname=? AND project_id=?
			AND NOT EXISTS (SELECT 1 FROM redirects WHERE lower(rtrim(trim(hostname), '.'))=? AND project_id=?)`,
			existing.Hostname, projectID, existing.Hostname, projectID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetRedirect(db, id, projectID)
}

func dbGetRedirect(db *sql.DB, id int64, projectID string) (*Redirect, error) {
	r, err := scanRedirect(db.QueryRow(`SELECT `+redirectCols+` FROM redirects WHERE id=? AND project_id=?`, id, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func dbDeleteRedirect(db *sql.DB, id int64, projectID string) (*Redirect, error) {
	existing, err := dbGetRedirect(db, id, projectID)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM redirects WHERE id=? AND project_id=?`, id, projectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM redirect_daily_stats WHERE rule_id=? AND project_id=?`, id, projectID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM redirect_hosts WHERE hostname=? AND project_id=?
		AND NOT EXISTS (SELECT 1 FROM redirects WHERE lower(rtrim(trim(hostname), '.'))=? AND project_id=?)`,
		existing.Hostname, projectID, existing.Hostname, projectID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return existing, nil
}

func dbListRedirects(db *sql.DB, hostname, projectID string, limit, offset int) ([]*Redirect, error) {
	if limit <= 0 || limit > 250 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + redirectCols + ` FROM redirects WHERE project_id=?`
	args := []any{projectID}
	if hostname != "" {
		q += ` AND lower(rtrim(trim(hostname), '.'))=?`
		args = append(args, normaliseHostname(hostname))
	}
	q += ` ORDER BY hostname ASC, length(path) DESC, path ASC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Redirect
	for rows.Next() {
		r, err := scanRedirect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func dbCountRedirects(db *sql.DB, hostname, projectID string) (int, error) {
	q := `SELECT count(*) FROM redirects WHERE project_id=?`
	args := []any{projectID}
	if hostname != "" {
		q += ` AND lower(rtrim(trim(hostname), '.'))=?`
		args = append(args, normaliseHostname(hostname))
	}
	var n int
	return n, db.QueryRow(q, args...).Scan(&n)
}

func dbCountHostRules(db *sql.DB, hostname string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM redirects WHERE lower(rtrim(trim(hostname), '.'))=?`, normaliseHostname(hostname)).Scan(&n)
	return n, err
}

func dbListHostClaims(db *sql.DB) ([]HostClaim, error) {
	rows, err := db.Query(`SELECT hostname, project_id FROM redirect_hosts ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostClaim
	for rows.Next() {
		var c HostClaim
		if err := rows.Scan(&c.Hostname, &c.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func hostnameOwner(db *sql.DB, hostname string) (string, error) {
	var projectID string
	err := db.QueryRow(`SELECT project_id FROM redirect_hosts WHERE hostname=?`, normaliseHostname(hostname)).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return projectID, err
}

func matchRedirect(db *sql.DB, hostname, path string) (*Redirect, error) {
	projectID, err := hostnameOwner(db, hostname)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return matchRedirectInProject(db, hostname, path, projectID)
}

func matchRedirectInProject(db *sql.DB, hostname, path, projectID string) (*Redirect, error) {
	hostname = normaliseHostname(hostname)
	path = normalisePath(path)
	rows, err := db.Query(`SELECT `+redirectCols+` FROM redirects
		WHERE lower(rtrim(trim(hostname), '.'))=? AND project_id=?`, hostname, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []*Redirect
	for rows.Next() {
		r, err := scanRedirect(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range candidates {
		if r.MatchMode == "exact" && r.Path == path {
			return r, nil
		}
	}
	var prefixes []*Redirect
	for _, r := range candidates {
		if r.MatchMode != "prefix" {
			continue
		}
		matches := r.Path == "/" || path == r.Path
		if strings.HasSuffix(r.Path, "/") {
			matches = matches || strings.HasPrefix(path, r.Path)
		} else {
			matches = matches || strings.HasPrefix(path, r.Path+"/")
		}
		if matches {
			prefixes = append(prefixes, r)
		}
	}
	if len(prefixes) == 0 {
		return nil, nil
	}
	sort.SliceStable(prefixes, func(i, j int) bool { return len(prefixes[i].Path) > len(prefixes[j].Path) })
	return prefixes[0], nil
}

func applyRule(r *Redirect, inboundPath, inboundQuery string) string {
	dest := r.Destination
	if r.PreservePath && r.MatchMode == "prefix" {
		inboundPath = normalisePath(inboundPath)
		leftover := inboundPath
		if r.Path != "/" {
			leftover = strings.TrimPrefix(inboundPath, r.Path)
		}
		if leftover != "" && leftover != "/" {
			if !strings.HasPrefix(leftover, "/") {
				leftover = "/" + leftover
			}
			dest = joinPath(dest, leftover)
		}
	}
	if r.PreserveQuery && inboundQuery != "" {
		dest = joinQuery(dest, inboundQuery)
	}
	return dest
}

func joinPath(dest, extra string) string {
	u, err := url.Parse(dest)
	if err != nil {
		return strings.TrimRight(dest, "/") + extra
	}
	u.Path = strings.TrimRight(u.Path, "/") + extra
	return u.String()
}

func joinQuery(dest, inboundQuery string) string {
	u, err := url.Parse(dest)
	if err != nil {
		sep := "?"
		if strings.Contains(dest, "?") {
			sep = "&"
		}
		return dest + sep + inboundQuery
	}
	inbound, err := url.ParseQuery(inboundQuery)
	if err != nil {
		if u.RawQuery == "" {
			u.RawQuery = inboundQuery
		} else {
			u.RawQuery += "&" + inboundQuery
		}
		return u.String()
	}
	existing := u.Query()
	for k, vs := range inbound {
		existing.Del(k)
		for _, v := range vs {
			existing.Add(k, v)
		}
	}
	u.RawQuery = existing.Encode()
	return u.String()
}

func dbRecordHit(db *sql.DB, ruleID int64, projectID string, at time.Time) (*HitCounts, error) {
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	date := at.Format("2006-01-02")
	timestamp := at.Format(time.RFC3339)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE redirects SET hits=hits+1, last_hit_at=? WHERE id=? AND project_id=?`, timestamp, ruleID, projectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	_, err = tx.Exec(`INSERT INTO redirect_daily_stats (rule_id, project_id, date, hits, updated_at)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(rule_id, date) DO UPDATE SET hits=hits+1, updated_at=excluded.updated_at`,
		ruleID, projectID, date, timestamp)
	if err != nil {
		return nil, err
	}
	counts := &HitCounts{Date: date}
	err = tx.QueryRow(`SELECT r.hits, s.hits
		FROM redirects r JOIN redirect_daily_stats s ON s.rule_id=r.id AND s.project_id=r.project_id
		WHERE r.id=? AND r.project_id=? AND s.date=?`, ruleID, projectID, date).
		Scan(&counts.HitsTotal, &counts.DayHits)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}

func normaliseStatsQuery(q RedirectStatsQuery) (RedirectStatsQuery, error) {
	if q.RuleID < 0 {
		return q, fmt.Errorf("%w: rule_id must be positive", ErrInvalidStats)
	}
	for _, field := range []struct{ label, value string }{{"from", q.From}, {"to", q.To}} {
		if field.value == "" {
			continue
		}
		parsed, err := time.Parse("2006-01-02", field.value)
		if err != nil || parsed.Format("2006-01-02") != field.value {
			return q, fmt.Errorf("%w: %s must be YYYY-MM-DD", ErrInvalidStats, field.label)
		}
	}
	if q.From != "" && q.To != "" && q.From > q.To {
		return q, fmt.Errorf("%w: from must be on or before to", ErrInvalidStats)
	}
	if q.Limit <= 0 || q.Limit > 250 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	return q, nil
}

func redirectStatsWhere(q RedirectStatsQuery) (string, []any) {
	where := ` WHERE r.project_id=?`
	args := []any{q.ProjectID}
	if q.RuleID > 0 {
		where += ` AND r.id=?`
		args = append(args, q.RuleID)
	}
	if q.From != "" {
		where += ` AND s.date>=?`
		args = append(args, q.From)
	}
	if q.To != "" {
		where += ` AND s.date<=?`
		args = append(args, q.To)
	}
	return where, args
}

func dbListRedirectStats(db *sql.DB, query RedirectStatsQuery) ([]RedirectStat, int, RedirectStatsQuery, error) {
	q, err := normaliseStatsQuery(query)
	if err != nil {
		return nil, 0, q, err
	}
	where, args := redirectStatsWhere(q)
	from := ` FROM redirect_daily_stats s
		JOIN redirects r ON r.id=s.rule_id AND r.project_id=s.project_id`
	var total int
	if err := db.QueryRow(`SELECT count(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, q, err
	}
	listArgs := append(append([]any{}, args...), q.Limit, q.Offset)
	rows, err := db.Query(`SELECT r.id, r.destination, r.hits, s.date, s.hits`+from+where+
		` ORDER BY s.date DESC, r.id ASC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, q, err
	}
	defer rows.Close()
	stats := make([]RedirectStat, 0)
	for rows.Next() {
		var stat RedirectStat
		if err := rows.Scan(&stat.RuleID, &stat.Destination, &stat.HitsTotal, &stat.Date, &stat.DayHits); err != nil {
			return nil, 0, q, err
		}
		stats = append(stats, stat)
	}
	return stats, total, q, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
