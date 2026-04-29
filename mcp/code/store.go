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

func dbGetRepoByID(db *sql.DB, projectID string, id int64) (*Repo, error) {
	row := db.QueryRow(`
		SELECT id, project_id, slug, name, description, framework, storage_root, owner,
		       build_cmd, start_cmd, port, env_json,
		       deploy_service, IFNULL(last_deployed_at,''),
		       created_at, updated_at, IFNULL(archived_at,'')
		FROM repositories
		WHERE project_id = ? AND id = ?
	`, projectID, id)
	return scanRepo(row)
}

func dbGetRepoBySlug(db *sql.DB, projectID, slug string) (*Repo, error) {
	row := db.QueryRow(`
		SELECT id, project_id, slug, name, description, framework, storage_root, owner,
		       build_cmd, start_cmd, port, env_json,
		       deploy_service, IFNULL(last_deployed_at,''),
		       created_at, updated_at, IFNULL(archived_at,'')
		FROM repositories
		WHERE project_id = ? AND slug = ?
	`, projectID, slug)
	return scanRepo(row)
}

func scanRepo(row *sql.Row) (*Repo, error) {
	var r Repo
	err := row.Scan(
		&r.ID, &r.ProjectID, &r.Slug, &r.Name, &r.Description, &r.Framework, &r.StorageRoot, &r.Owner,
		&r.BuildCmd, &r.StartCmd, &r.Port, &r.EnvJSON,
		&r.DeployService, &r.LastDeployedAt,
		&r.CreatedAt, &r.UpdatedAt, &r.ArchivedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func dbListRepos(db *sql.DB, projectID string, includeArchived bool, q string) ([]*Repo, error) {
	query := `
		SELECT id, project_id, slug, name, description, framework, storage_root, owner,
		       build_cmd, start_cmd, port, env_json,
		       deploy_service, IFNULL(last_deployed_at,''),
		       created_at, updated_at, IFNULL(archived_at,'')
		FROM repositories
		WHERE project_id = ?
	`
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
		var r Repo
		if err := rows.Scan(
			&r.ID, &r.ProjectID, &r.Slug, &r.Name, &r.Description, &r.Framework, &r.StorageRoot, &r.Owner,
			&r.BuildCmd, &r.StartCmd, &r.Port, &r.EnvJSON,
			&r.DeployService, &r.LastDeployedAt,
			&r.CreatedAt, &r.UpdatedAt, &r.ArchivedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, &r)
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
