package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repo is the on-the-wire shape of a repository row. The internal
// integer id is exposed so HTTP/MCP callers can correlate audit
// entries; the slug is the durable handle every other tool takes.
type Repo struct {
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Framework      string `json:"framework"`
	StorageRoot    string `json:"storage_root"`
	Owner          string `json:"owner,omitempty"`
	BuildCmd       string `json:"build_cmd,omitempty"`
	StartCmd       string `json:"start_cmd,omitempty"`
	Port           int    `json:"port,omitempty"`
	EnvJSON        string `json:"env_json,omitempty"`
	DeployService  string `json:"deploy_service,omitempty"`
	LastDeployedAt string `json:"last_deployed_at,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	ArchivedAt     string `json:"archived_at,omitempty"`

	IsTemplate      bool   `json:"is_template,omitempty"`
	TemplateScope   string `json:"template_scope,omitempty"`   // 'private' | 'project' | 'global'
	TemplateTagline string `json:"template_tagline,omitempty"`
	TemplateIcon    string `json:"template_icon,omitempty"`
}

// IsArchived is true when archived_at is non-empty.
func (r *Repo) IsArchived() bool { return r.ArchivedAt != "" }

// ─── Inputs ─────────────────────────────────────────────────────────

type CreateRepoInput struct {
	Name        string
	Slug        string // optional; derived from Name when empty
	Description string
	Framework   string
	Owner       string
}

type DeployHints struct {
	BuildCmd *string `json:"build_cmd,omitempty"`
	StartCmd *string `json:"start_cmd,omitempty"`
	Port     *int    `json:"port,omitempty"`
	EnvJSON  *string `json:"env_json,omitempty"`
}

// ─── Slug helpers ───────────────────────────────────────────────────

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = fmt.Sprintf("repo-%d", time.Now().UnixNano())
	}
	return out
}

func validFramework(f string) bool {
	switch f {
	case "blank", "nextjs", "static", "go", "python":
		return true
	}
	return false
}

// ─── DB ops ─────────────────────────────────────────────────────────

func dbCreateRepo(db *sql.DB, projectID string, in CreateRepoInput) (*Repo, error) {
	if in.Name == "" {
		return nil, errors.New("name required")
	}
	slug := in.Slug
	if slug == "" {
		slug = slugify(in.Name)
	} else {
		slug = slugify(slug)
	}
	if in.Framework == "" {
		in.Framework = "blank"
	}
	if !validFramework(in.Framework) {
		return nil, fmt.Errorf("framework %q not supported", in.Framework)
	}
	storageRoot := "/repos/" + slug + "/"

	res, err := db.Exec(`
		INSERT INTO repositories (project_id, slug, name, description, framework, storage_root, owner)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, projectID, slug, in.Name, in.Description, in.Framework, storageRoot, in.Owner)
	if err != nil {
		// SQLite unique constraint name varies by version — match on the
		// stable substring so collisions surface as a friendly error.
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("slug %q already taken in this project", slug)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetRepoByID(db, projectID, id)
}

// repoColumns is the canonical column list for SELECTs. Kept in one
// place so adding a column means touching one constant + scanRepoRow.
const repoColumns = `
	id, project_id, slug, name, description, framework, storage_root, owner,
	build_cmd, start_cmd, port, env_json,
	deploy_service, IFNULL(last_deployed_at,''),
	created_at, updated_at, IFNULL(archived_at,''),
	is_template, IFNULL(template_scope,''), IFNULL(template_tagline,''), IFNULL(template_icon,'')
`

// rowScanner abstracts *sql.Row and *sql.Rows so one Scan helper covers
// both single-row queries and Rows.Next loops.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRepoRow(s rowScanner) (*Repo, error) {
	var r Repo
	err := s.Scan(
		&r.ID, &r.ProjectID, &r.Slug, &r.Name, &r.Description, &r.Framework, &r.StorageRoot, &r.Owner,
		&r.BuildCmd, &r.StartCmd, &r.Port, &r.EnvJSON,
		&r.DeployService, &r.LastDeployedAt,
		&r.CreatedAt, &r.UpdatedAt, &r.ArchivedAt,
		&r.IsTemplate, &r.TemplateScope, &r.TemplateTagline, &r.TemplateIcon,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func dbGetRepoByID(db *sql.DB, projectID string, id int64) (*Repo, error) {
	row := db.QueryRow(`SELECT `+repoColumns+` FROM repositories WHERE project_id = ? AND id = ?`, projectID, id)
	return scanRepoRow(row)
}

func dbGetRepoBySlug(db *sql.DB, projectID, slug string) (*Repo, error) {
	row := db.QueryRow(`SELECT `+repoColumns+` FROM repositories WHERE project_id = ? AND slug = ?`, projectID, slug)
	return scanRepoRow(row)
}

func dbListRepos(db *sql.DB, projectID string, includeArchived bool, q string) ([]*Repo, error) {
	query := `SELECT ` + repoColumns + ` FROM repositories WHERE project_id = ?`
	args := []any{projectID}
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	if q != "" {
		query += ` AND (slug LIKE ? OR name LIKE ?)`
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Repo
	for rows.Next() {
		r, err := scanRepoRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// dbPatchRepo updates name / description on an existing repo.
func dbPatchRepo(db *sql.DB, projectID, slug string, name, description *string) (*Repo, error) {
	r, err := dbGetRepoBySlug(db, projectID, slug)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("repo not found")
	}
	if name != nil {
		r.Name = *name
	}
	if description != nil {
		r.Description = *description
	}
	if _, err := db.Exec(`
		UPDATE repositories
		   SET name = ?, description = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND slug = ?
	`, r.Name, r.Description, projectID, slug); err != nil {
		return nil, err
	}
	return dbGetRepoBySlug(db, projectID, slug)
}

func dbSetDeployHints(db *sql.DB, projectID, slug string, h DeployHints) (*Repo, error) {
	r, err := dbGetRepoBySlug(db, projectID, slug)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("repo not found")
	}
	if h.BuildCmd != nil {
		r.BuildCmd = *h.BuildCmd
	}
	if h.StartCmd != nil {
		r.StartCmd = *h.StartCmd
	}
	if h.Port != nil {
		r.Port = *h.Port
	}
	if h.EnvJSON != nil {
		r.EnvJSON = *h.EnvJSON
	}
	if _, err := db.Exec(`
		UPDATE repositories
		   SET build_cmd = ?, start_cmd = ?, port = ?, env_json = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND slug = ?
	`, r.BuildCmd, r.StartCmd, r.Port, r.EnvJSON, projectID, slug); err != nil {
		return nil, err
	}
	return dbGetRepoBySlug(db, projectID, slug)
}

func dbArchiveRepo(db *sql.DB, projectID, slug string) error {
	_, err := db.Exec(`
		UPDATE repositories
		   SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND slug = ?
	`, projectID, slug)
	return err
}

func dbHardDeleteRepo(db *sql.DB, projectID, slug string) error {
	_, err := db.Exec(`DELETE FROM repositories WHERE project_id = ? AND slug = ?`, projectID, slug)
	return err
}

// dbRecordImport notes the source of a repo's initial files.
func dbRecordImport(db *sql.DB, repoID int64, source string) error {
	_, err := db.Exec(`INSERT INTO repo_imports (repo_id, source) VALUES (?, ?)`, repoID, source)
	return err
}

// ─── Templates ─────────────────────────────────────────────────────

func validTemplateScope(s string) bool {
	switch s {
	case "private", "project", "global":
		return true
	}
	return false
}

// dbSetTemplate flips a repo into (or out of) being a template.
// Passing on=false clears the template fields so it goes back to a
// regular repo. Existing forks are unaffected either way.
func dbSetTemplate(db *sql.DB, projectID, slug string, on bool, scope, tagline, icon string) (*Repo, error) {
	r, err := dbGetRepoBySlug(db, projectID, slug)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("repo not found")
	}
	if on {
		if scope == "" {
			scope = "private"
		}
		if !validTemplateScope(scope) {
			return nil, fmt.Errorf("invalid template_scope %q", scope)
		}
		if _, err := db.Exec(`
			UPDATE repositories
			   SET is_template = 1, template_scope = ?, template_tagline = ?, template_icon = ?,
			       updated_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND slug = ?
		`, scope, tagline, icon, projectID, slug); err != nil {
			return nil, err
		}
	} else {
		if _, err := db.Exec(`
			UPDATE repositories
			   SET is_template = 0, template_scope = NULL, template_tagline = NULL, template_icon = NULL,
			       updated_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND slug = ?
		`, projectID, slug); err != nil {
			return nil, err
		}
	}
	return dbGetRepoBySlug(db, projectID, slug)
}

// dbListUserTemplates returns templates visible to the given project:
// every template in this project (any scope) plus globally-scoped
// templates from any project. Project-scoped templates from *other*
// projects are intentionally hidden — that's what makes 'project'
// distinct from 'global'.
func dbListUserTemplates(db *sql.DB, projectID string) ([]*Repo, error) {
	query := `SELECT ` + repoColumns + ` FROM repositories
		WHERE is_template = 1 AND archived_at IS NULL
		  AND (project_id = ? OR template_scope = 'global')
		ORDER BY updated_at DESC`
	rows, err := db.Query(query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Repo
	for rows.Next() {
		r, err := scanRepoRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// dbRecordFork pins a child repo to its parent for "forked from"
// provenance. parentKind is 'user' (a slug in repositories) or
// 'embedded' (a name in templatesFS).
func dbRecordFork(db *sql.DB, childID int64, parentSlug, parentKind string) error {
	_, err := db.Exec(`
		INSERT INTO repo_forks (child_id, parent_slug, parent_kind) VALUES (?, ?, ?)
		ON CONFLICT(child_id) DO UPDATE SET parent_slug=excluded.parent_slug, parent_kind=excluded.parent_kind
	`, childID, parentSlug, parentKind)
	return err
}

// ForkInfo is what the UI shows on a forked repo card.
type ForkInfo struct {
	ParentSlug string `json:"parent_slug"`
	ParentKind string `json:"parent_kind"`
	ForkedAt   string `json:"forked_at"`
}

func dbGetFork(db *sql.DB, childID int64) (*ForkInfo, error) {
	row := db.QueryRow(`SELECT parent_slug, parent_kind, forked_at FROM repo_forks WHERE child_id = ?`, childID)
	var f ForkInfo
	if err := row.Scan(&f.ParentSlug, &f.ParentKind, &f.ForkedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &f, nil
}

// ─── Dev runs (v0.5.0) ────────────────────────────────────────────

// DevRun is one row in the dev_runs table — the supervisor's durable
// view of a per-repo dev process. status transitions:
//
//   stopped → starting → live → stopped (clean Stop)
//                              → crashed (non-zero exit)
//
// The supervisor lives inside the code sidecar; on a sidecar restart
// the reconcile pass at OnMount checks whether the recorded pid is
// still alive and demotes orphans to stopped.
type DevRun struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id"`
	RepoID     int64  `json:"repo_id"`
	Status     string `json:"status"`
	Port       int    `json:"port"`
	PID        int    `json:"pid"`
	Framework  string `json:"framework"`
	RunCmd     string `json:"run_cmd,omitempty"`
	EnvJSON    string `json:"env_json,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	StoppedAt  string `json:"stopped_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

const devRunCols = `id, project_id, repo_id, status, port, pid, framework,
		run_cmd, env_json, log_path,
		COALESCE(started_at, '') AS started_at,
		COALESCE(stopped_at, '') AS stopped_at,
		error`

func scanDevRunRow(s rowScanner) (*DevRun, error) {
	var dr DevRun
	if err := s.Scan(
		&dr.ID, &dr.ProjectID, &dr.RepoID, &dr.Status, &dr.Port, &dr.PID,
		&dr.Framework, &dr.RunCmd, &dr.EnvJSON, &dr.LogPath,
		&dr.StartedAt, &dr.StoppedAt, &dr.Error,
	); err != nil {
		return nil, err
	}
	return &dr, nil
}

// dbGetDevRun fetches the dev run row for a repo, if any. Returns
// (nil, nil) when no row exists — a never-run repo isn't an error.
func dbGetDevRun(db *sql.DB, projectID string, repoID int64) (*DevRun, error) {
	row := db.QueryRow(`SELECT `+devRunCols+` FROM dev_runs WHERE project_id = ? AND repo_id = ?`, projectID, repoID)
	dr, err := scanDevRunRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return dr, err
}

// dbUpsertDevRun creates or replaces the dev run row for a repo. The
// UNIQUE(project_id, repo_id) constraint enforces one-per-repo; this
// helper is the canonical write path so the panel and the agent can't
// race two starting rows. Used at start time to claim status='starting'
// before the process is actually spawned; the supervisor flips to live
// once the readiness probe succeeds (or to crashed/stopped on exit).
func dbUpsertDevRun(db *sql.DB, in DevRun) (*DevRun, error) {
	if in.ProjectID == "" || in.RepoID == 0 {
		return nil, errors.New("project_id and repo_id required")
	}
	if in.Status == "" {
		in.Status = "starting"
	}
	res, err := db.Exec(`
		INSERT INTO dev_runs (
			project_id, repo_id, status, port, pid, framework, run_cmd, env_json, log_path,
			started_at, error
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, repo_id) DO UPDATE SET
			status     = excluded.status,
			port       = excluded.port,
			pid        = excluded.pid,
			framework  = excluded.framework,
			run_cmd    = excluded.run_cmd,
			env_json   = excluded.env_json,
			log_path   = excluded.log_path,
			started_at = excluded.started_at,
			stopped_at = NULL,
			error      = excluded.error
	`, in.ProjectID, in.RepoID, in.Status, in.Port, in.PID, in.Framework,
		in.RunCmd, in.EnvJSON, in.LogPath, nullableTS(in.StartedAt), in.Error,
	)
	if err != nil {
		return nil, err
	}
	_ = res
	return dbGetDevRun(db, in.ProjectID, in.RepoID)
}

// dbUpdateDevRun mutates a subset of fields on an existing row. Mirrors
// the deploy app's dbUpdateBuild pattern — fixed allowlist + dynamic
// SET clause. The fields most often updated are status / pid / port at
// transition points and stopped_at / error on shutdown.
func dbUpdateDevRun(db *sql.DB, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{"status", "port", "pid", "framework", "run_cmd",
		"env_json", "log_path", "started_at", "stopped_at", "error"} {
		if v, ok := fields[k]; ok {
			cols = append(cols, k+" = ?")
			args = append(args, v)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE dev_runs SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

// dbListLiveDevRuns returns rows in starting|live status. Used by the
// boot reconcile pass — the orchestrator checks each PID against the
// real process table and demotes orphans to stopped.
func dbListLiveDevRuns(db *sql.DB) ([]*DevRun, error) {
	rows, err := db.Query(`SELECT ` + devRunCols + ` FROM dev_runs WHERE status IN ('starting','live')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DevRun
	for rows.Next() {
		dr, err := scanDevRunRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dr)
	}
	return out, rows.Err()
}

// nullableTS returns sql.NullString for empty strings so timestamp
// columns get NULL rather than '', which sqlite would otherwise store
// as a literal empty string and surprise downstream readers.
func nullableTS(s string) any {
	if s == "" {
		return nil
	}
	return s
}
