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

// Redirect is the on-the-wire shape of a row in the redirects table.
type Redirect struct {
	ID            int64  `json:"id"`
	Hostname      string `json:"hostname"`
	Path          string `json:"path"`
	MatchMode     string `json:"match_mode"` // "exact" | "prefix"
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

// RedirectInput is the canonical write shape, shared by add/update.
// Pointer fields on update let the handler distinguish "leave alone"
// from "set to zero value."
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

// ─── Validation ───────────────────────────────────────────────────

func validateHostname(h string) error {
	if h == "" {
		return errors.New("hostname required")
	}
	if strings.ContainsAny(h, " \t\r\n") {
		return errors.New("hostname must not contain whitespace")
	}
	if strings.Contains(h, "://") {
		return errors.New("hostname must not include a scheme")
	}
	if strings.ContainsAny(h, "/?#") {
		return errors.New("hostname must not include a path, query, or fragment")
	}
	if strings.Contains(h, ":") {
		return errors.New("hostname must not include a port")
	}
	if len(h) > 253 {
		return errors.New("hostname too long (>253 chars)")
	}
	return nil
}

// validateDestination wants a parseable absolute URL with http/https
// scheme. We allow schemeless mailto:/tel: — common short-link uses.
func validateDestination(d string) error {
	if d == "" {
		return errors.New("destination required")
	}
	if strings.HasPrefix(d, "mailto:") || strings.HasPrefix(d, "tel:") {
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
	}
	return fmt.Errorf("status_code must be 301, 302, 307, or 308 (got %d)", c)
}

func validateMatchMode(m string) error {
	switch m {
	case "exact", "prefix":
		return nil
	}
	return fmt.Errorf("match must be 'exact' or 'prefix' (got %q)", m)
}

// normalisePath ensures every stored path starts with '/' and has no
// trailing slash unless it's the root. Keeps matching predictable.
func normalisePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for strings.HasSuffix(p, "/") && len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

func defaultInput(in *RedirectInput) {
	if in.Path == "" {
		in.Path = "/"
	}
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
	return nil
}

// ─── DB ops ────────────────────────────────────────────────────────

const redirectCols = `id, hostname, path, match_mode, destination, status_code,
		preserve_path, preserve_query, project_id, notes, hits, last_hit_at,
		created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

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
	r.PreservePath = preservePath != 0
	r.PreserveQuery = preserveQuery != 0
	if lastHit.Valid {
		r.LastHitAt = lastHit.String
	}
	return &r, nil
}

// ErrConflict — a rule already exists at this (hostname, path, match)
// for the same project. The tool layer maps this to 409.
var ErrConflict = errors.New("a redirect already exists at this hostname+path+match")

// ErrNotFound — the row asked for doesn't exist.
var ErrNotFound = errors.New("redirect not found")

func dbInsertRedirect(db *sql.DB, in RedirectInput) (*Redirect, error) {
	defaultInput(&in)
	if err := validateInput(in); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`INSERT INTO redirects
		(hostname, path, match_mode, destination, status_code,
		 preserve_path, preserve_query, project_id, notes,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Hostname, in.Path, in.MatchMode, in.Destination, in.StatusCode,
		boolInt(in.PreservePath), boolInt(in.PreserveQuery), in.ProjectID, in.Notes,
		now, now)
	if err != nil {
		// SQLite returns a UNIQUE constraint error string we surface as
		// a clean conflict — saves the caller from sniffing the message.
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetRedirect(db, id)
}

func dbUpdateRedirect(db *sql.DB, id int64, in RedirectInput) (*Redirect, error) {
	existing, err := dbGetRedirect(db, id)
	if err != nil {
		return nil, err
	}
	// Merge: input fields override existing; empty strings on path/
	// match_mode mean "leave alone." Status code 0 means leave alone.
	merged := RedirectInput{
		Hostname:      pickStr(in.Hostname, existing.Hostname),
		Path:          pickStr(in.Path, existing.Path),
		MatchMode:     pickStr(in.MatchMode, existing.MatchMode),
		Destination:   pickStr(in.Destination, existing.Destination),
		StatusCode:    pickInt(in.StatusCode, existing.StatusCode),
		PreservePath:  in.PreservePath,
		PreserveQuery: in.PreserveQuery,
		ProjectID:     pickStr(in.ProjectID, existing.ProjectID),
		Notes:         pickStr(in.Notes, existing.Notes),
	}
	if err := validateInput(merged); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`UPDATE redirects
		SET hostname = ?, path = ?, match_mode = ?, destination = ?, status_code = ?,
		    preserve_path = ?, preserve_query = ?, project_id = ?, notes = ?,
		    updated_at = ?
		WHERE id = ?`,
		merged.Hostname, merged.Path, merged.MatchMode, merged.Destination, merged.StatusCode,
		boolInt(merged.PreservePath), boolInt(merged.PreserveQuery), merged.ProjectID, merged.Notes,
		now, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	return dbGetRedirect(db, id)
}

func dbGetRedirect(db *sql.DB, id int64) (*Redirect, error) {
	row := db.QueryRow(`SELECT `+redirectCols+` FROM redirects WHERE id = ?`, id)
	r, err := scanRedirect(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func dbDeleteRedirect(db *sql.DB, id int64) error {
	res, err := db.Exec(`DELETE FROM redirects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// dbListRedirects supports optional filtering by hostname/project and
// simple pagination. Sort by hostname then path so the panel can group
// visually without client-side resorting.
func dbListRedirects(db *sql.DB, hostname, projectID string, limit, offset int) ([]*Redirect, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + redirectCols + ` FROM redirects WHERE 1=1`
	args := []any{}
	if hostname != "" {
		q += ` AND hostname = ?`
		args = append(args, hostname)
	}
	if projectID != "" {
		q += ` AND project_id = ?`
		args = append(args, projectID)
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

// dbDistinctHostnames returns the set of hostnames that have at least
// one rule, scoped to project_id when supplied. Used on redirect_remove
// to decide whether the last rule for a hostname has just gone away so
// we can unregister the route.
func dbDistinctHostnames(db *sql.DB, projectID string) ([]string, error) {
	q := `SELECT DISTINCT hostname FROM redirects`
	args := []any{}
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ─── Matching ─────────────────────────────────────────────────────

// matchRedirect picks the best rule for an inbound (hostname, path).
// Selection order:
//   1. hostname must match exactly
//   2. exact-path rules beat prefix-path rules
//   3. among prefix rules, longest path wins
//
// Returns nil when nothing matches. ProjectID is filtered when non-
// empty so installs with multiple project scopes don't bleed rules
// between projects.
func matchRedirect(db *sql.DB, hostname, path string) (*Redirect, error) {
	path = normalisePath(path)
	rows, err := db.Query(`SELECT `+redirectCols+`
		FROM redirects WHERE hostname = ?`, hostname)
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
	// First pass: exact matches.
	for _, r := range candidates {
		if r.MatchMode == "exact" && r.Path == path {
			return r, nil
		}
	}
	// Second pass: prefix matches, longest first.
	var prefixes []*Redirect
	for _, r := range candidates {
		if r.MatchMode != "prefix" {
			continue
		}
		// Treat path '/' as "matches everything." Otherwise require the
		// inbound path to start at a path-segment boundary so '/blog'
		// matches '/blog' and '/blog/post' but not '/blogfoo'.
		if r.Path == "/" || path == r.Path || strings.HasPrefix(path, r.Path+"/") {
			prefixes = append(prefixes, r)
		}
	}
	if len(prefixes) == 0 {
		return nil, nil
	}
	sort.SliceStable(prefixes, func(i, j int) bool {
		return len(prefixes[i].Path) > len(prefixes[j].Path)
	})
	return prefixes[0], nil
}

// applyRule turns a matched rule + the inbound request URL into the
// final Location URL. Handles preserve_path (prefix-only) and
// preserve_query (always honoured).
func applyRule(r *Redirect, inboundPath, inboundQuery string) string {
	dest := r.Destination

	// preserve_path: append the inbound path's leftover (after the
	// rule's prefix) onto the destination. Only valid for prefix rules.
	if r.PreservePath && r.MatchMode == "prefix" {
		inboundPath = normalisePath(inboundPath)
		leftover := ""
		if r.Path == "/" {
			leftover = inboundPath
		} else if strings.HasPrefix(inboundPath, r.Path) {
			leftover = strings.TrimPrefix(inboundPath, r.Path)
		}
		if leftover != "" && leftover != "/" {
			dest = joinPath(dest, leftover)
		}
	}

	if r.PreserveQuery && inboundQuery != "" {
		dest = joinQuery(dest, inboundQuery)
	}
	return dest
}

// joinPath appends a path segment to a destination URL, preserving
// any existing path on the destination. URL parsing keeps query and
// fragment intact.
func joinPath(dest, extra string) string {
	u, err := url.Parse(dest)
	if err != nil {
		// Fall back to string concat — better than dropping the leftover.
		return strings.TrimRight(dest, "/") + extra
	}
	u.Path = strings.TrimRight(u.Path, "/") + extra
	return u.String()
}

// joinQuery merges an inbound query string onto the destination's
// existing query. Inbound keys win on conflict — that's the rule the
// agent is most likely to want (caller intent overrides).
func joinQuery(dest, inboundQuery string) string {
	u, err := url.Parse(dest)
	if err != nil {
		sep := "?"
		if strings.Contains(dest, "?") {
			sep = "&"
		}
		return dest + sep + inboundQuery
	}
	existing := u.Query()
	inbound, _ := url.ParseQuery(inboundQuery)
	for k, vs := range inbound {
		existing.Del(k)
		for _, v := range vs {
			existing.Add(k, v)
		}
	}
	u.RawQuery = existing.Encode()
	return u.String()
}

// dbRecordHit bumps the hit counter best-effort. Errors are logged by
// the caller, never propagated — the redirect itself must succeed.
func dbRecordHit(db *sql.DB, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE redirects SET hits = hits + 1, last_hit_at = ? WHERE id = ?`, now, id)
	return err
}

// ─── tiny helpers ─────────────────────────────────────────────────

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func pickStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func pickInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
