// Multi-site v2.0 — site model, CRUD, and per-request resolution.
//
// One project hosts 1..N sites. Each site has its own posts, terms,
// menus, settings, theme, and optionally a public hostname. Sites are
// soft-deleted (archived_at); deleting the default refuses unless
// another site has been promoted to default first.
//
// resolveSiteID is the single seam every tool + REST handler calls to
// figure out which site this request applies to. Resolution order:
//
//   1. _site_id arg (integer, agent injects after tool selection)
//   2. _site arg (slug, easier for agents to specify)
//   3. ?site=<slug> query param (panel + headless REST callers)
//   4. X-Apteva-Site header (future platform routing)
//   5. Host header → sites.hostname match (domain-linked public traffic)
//   6. The project's only live site, if there's exactly one (preserves
//      single-site UX after v2.0 migration)
//   7. The project's default site (is_default=1), if set
//   8. Hard error — must supply a selector or set a default
//
// This makes single-site installs feel identical to v1, while multi-
// site installs require an explicit selector when ambiguous.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type Site struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id,omitempty"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname,omitempty"`
	IsDefault  bool   `json:"is_default"`
	CreatedAt  string `json:"created_at,omitempty"`
	ArchivedAt string `json:"archived_at,omitempty"`
}

// ── DB layer ─────────────────────────────────────────────────────

func dbCreateSite(db *sql.DB, projectID, slug, name, hostname string) (*Site, error) {
	if slug == "" {
		return nil, errors.New("slug required")
	}
	slug = slugify(slug)
	if name == "" {
		name = slug
	}
	// Is this the first live site for the project? If so, make it default.
	var existing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sites WHERE project_id=? AND archived_at IS NULL`, projectID).Scan(&existing); err != nil {
		return nil, err
	}
	isDefault := 0
	if existing == 0 {
		isDefault = 1
	}
	res, err := db.Exec(`INSERT INTO sites (project_id, slug, name, hostname, is_default) VALUES (?, ?, ?, ?, ?)`,
		projectID, slug, name, hostname, isDefault)
	if err != nil {
		return nil, fmt.Errorf("insert site: %w", err)
	}
	id, _ := res.LastInsertId()
	return dbGetSite(db, projectID, id)
}

func dbGetSite(db *sql.DB, projectID string, id int64) (*Site, error) {
	row := db.QueryRow(`SELECT id, project_id, slug, name, hostname, is_default, created_at, archived_at
		FROM sites WHERE project_id=? AND id=?`, projectID, id)
	return scanSite(row)
}

func dbGetSiteBySlug(db *sql.DB, projectID, slug string) (*Site, error) {
	row := db.QueryRow(`SELECT id, project_id, slug, name, hostname, is_default, created_at, archived_at
		FROM sites WHERE project_id=? AND slug=? AND archived_at IS NULL`, projectID, slug)
	return scanSite(row)
}

func dbGetSiteByHostname(db *sql.DB, hostname string) (*Site, error) {
	row := db.QueryRow(`SELECT id, project_id, slug, name, hostname, is_default, created_at, archived_at
		FROM sites WHERE hostname=? AND archived_at IS NULL`, hostname)
	return scanSite(row)
}

func dbListSites(db *sql.DB, projectID string, includeArchived bool) ([]Site, error) {
	q := `SELECT id, project_id, slug, name, hostname, is_default, created_at, archived_at
		FROM sites WHERE project_id=?`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY is_default DESC, name`
	rows, err := db.Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, nil
}

func scanSite(row rowScanner) (*Site, error) {
	var s Site
	var created, archived sql.NullString
	var isDef int
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Slug, &s.Name, &s.Hostname, &isDef, &created, &archived); err != nil {
		return nil, err
	}
	s.IsDefault = isDef == 1
	if created.Valid {
		s.CreatedAt = created.String
	}
	if archived.Valid {
		s.ArchivedAt = archived.String
	}
	return &s, nil
}

func dbUpdateSite(db *sql.DB, projectID string, id int64, name, hostname *string) (*Site, error) {
	sets := []string{}
	args := []any{}
	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if hostname != nil {
		sets = append(sets, "hostname = ?")
		args = append(args, *hostname)
	}
	if len(sets) == 0 {
		return dbGetSite(db, projectID, id)
	}
	args = append(args, projectID, id)
	if _, err := db.Exec(`UPDATE sites SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, args...); err != nil {
		return nil, fmt.Errorf("update site: %w", err)
	}
	return dbGetSite(db, projectID, id)
}

func dbArchiveSite(db *sql.DB, projectID string, id int64) error {
	// Refuse if this is the default — caller must promote another
	// site to default first.
	var isDef int
	if err := db.QueryRow(`SELECT is_default FROM sites WHERE project_id=? AND id=? AND archived_at IS NULL`,
		projectID, id).Scan(&isDef); err != nil {
		return fmt.Errorf("site not found: %w", err)
	}
	if isDef == 1 {
		return errors.New("can't archive the default site — promote another site to default first via sites_set_default")
	}
	_, err := db.Exec(`UPDATE sites SET archived_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, projectID, id)
	return err
}

func dbSetDefaultSite(db *sql.DB, projectID string, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Verify the target exists + is live.
	var ok int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sites WHERE project_id=? AND id=? AND archived_at IS NULL`,
		projectID, id).Scan(&ok); err != nil || ok == 0 {
		return errors.New("site not found or archived")
	}
	// Clear current default, set new one — the partial unique index
	// (sites_default_uniq) enforces only one live default per project.
	if _, err := tx.Exec(`UPDATE sites SET is_default=0 WHERE project_id=? AND is_default=1`, projectID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sites SET is_default=1 WHERE project_id=? AND id=?`, projectID, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureDefaultSite makes sure the project has a default site row.
// Called at OnMount for project-scoped installs so a fresh install
// immediately has somewhere to put content. Idempotent — no-op if a
// default already exists.
func ensureDefaultSite(db *sql.DB, projectID string) (*Site, error) {
	if projectID == "" {
		return nil, errors.New("ensureDefaultSite: projectID required")
	}
	if s, err := dbGetDefaultSite(db, projectID); err == nil && s != nil {
		return s, nil
	}
	return dbCreateSite(db, projectID, "main", "Main", "")
}

func dbGetDefaultSite(db *sql.DB, projectID string) (*Site, error) {
	row := db.QueryRow(`SELECT id, project_id, slug, name, hostname, is_default, created_at, archived_at
		FROM sites WHERE project_id=? AND is_default=1 AND archived_at IS NULL`, projectID)
	return scanSite(row)
}

// ── per-request resolution ───────────────────────────────────────

// resolveSiteIDFromArgs picks a site from an MCP tool's args map.
// See file-header comment for the resolution order.
func resolveSiteIDFromArgs(db *sql.DB, projectID string, args map[string]any) (int64, error) {
	if v, ok := asInt64(args["_site_id"]); ok && v > 0 {
		return v, nil
	}
	if slug := asString(args["_site"]); slug != "" {
		s, err := dbGetSiteBySlug(db, projectID, slug)
		if err != nil || s == nil {
			return 0, fmt.Errorf("site %q not found", slug)
		}
		return s.ID, nil
	}
	if slug := asString(args["site"]); slug != "" {
		s, err := dbGetSiteBySlug(db, projectID, slug)
		if err != nil || s == nil {
			return 0, fmt.Errorf("site %q not found", slug)
		}
		return s.ID, nil
	}
	return resolveOnlyOrDefaultSite(db, projectID)
}

// resolveSiteIDFromRequest picks a site for an HTTP request, prioritising
// per-request signals (query param, header) before falling back to host
// lookup and then the project default.
func resolveSiteIDFromRequest(db *sql.DB, projectID string, r *http.Request) (int64, error) {
	if v := r.URL.Query().Get("site"); v != "" {
		s, err := dbGetSiteBySlug(db, projectID, v)
		if err != nil || s == nil {
			return 0, fmt.Errorf("site %q not found", v)
		}
		return s.ID, nil
	}
	if v := r.Header.Get("X-Apteva-Site"); v != "" {
		s, err := dbGetSiteBySlug(db, projectID, v)
		if err != nil || s == nil {
			return 0, fmt.Errorf("site %q not found", v)
		}
		return s.ID, nil
	}
	// Hostname → site lookup for domain-linked public traffic.
	host := stripPort(r.Host)
	if host != "" {
		if s, err := dbGetSiteByHostname(db, host); err == nil && s != nil && s.ProjectID == projectID {
			return s.ID, nil
		}
	}
	return resolveOnlyOrDefaultSite(db, projectID)
}

// resolveOnlyOrDefaultSite — the single-site-friendly fallback. If
// there's exactly one live site, use it; otherwise use the project's
// default; otherwise error.
func resolveOnlyOrDefaultSite(db *sql.DB, projectID string) (int64, error) {
	rows, err := db.Query(`SELECT id, is_default FROM sites WHERE project_id=? AND archived_at IS NULL`, projectID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	var defaultID int64
	for rows.Next() {
		var id int64
		var isDef int
		if err := rows.Scan(&id, &isDef); err != nil {
			return 0, err
		}
		ids = append(ids, id)
		if isDef == 1 {
			defaultID = id
		}
	}
	switch {
	case len(ids) == 0:
		return 0, errors.New("project has no sites — call sites_create first")
	case len(ids) == 1:
		return ids[0], nil
	case defaultID != 0:
		return defaultID, nil
	default:
		return 0, errors.New("project has multiple sites and no default; pass site=<slug> or call sites_set_default")
	}
}

func stripPort(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}
