package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	errNotFound         = errors.New("design not found")
	errRevisionConflict = errors.New("current revision changed")
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

type rawJSONScanner struct{ target *json.RawMessage }

func (scanner rawJSONScanner) Scan(source any) error {
	switch value := source.(type) {
	case nil:
		*scanner.target = nil
	case string:
		*scanner.target = append((*scanner.target)[:0], value...)
	case []byte:
		*scanner.target = append((*scanner.target)[:0], value...)
	default:
		return fmt.Errorf("cannot scan %T as JSON", source)
	}
	return nil
}

func (s *Store) CreateDesign(project string, input CreateDesignInput) (*Design, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, errors.New("name required")
	}
	if len(name) > 160 {
		return nil, errors.New("name exceeds 160 characters")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = "parametric"
	}
	if kind != "parametric" && kind != "mesh" && kind != "sketch2d" {
		return nil, errors.New("kind must be parametric, mesh, or sketch2d")
	}
	tags, _ := json.Marshal(normalizeTags(input.Tags))
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO designs(project_id, name, description, kind, tags_json)
		VALUES(?, ?, ?, ?, ?)`, project, name, strings.TrimSpace(input.Description), kind, string(tags))
	if err != nil {
		return nil, err
	}
	designID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	hash := sourceHash(input.Definition, input.Parameters)
	res, err = tx.Exec(`INSERT INTO design_revisions
		(design_id, revision_number, definition_json, parameters_json, source_sha256, note, author)
		VALUES(?, 1, ?, ?, ?, ?, ?)`, designID, string(input.Definition), string(input.Parameters), hash, input.Note, input.Author)
	if err != nil {
		return nil, err
	}
	revisionID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE designs SET current_revision_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, revisionID, designID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetDesign(project, designID)
}

func (s *Store) ListDesigns(project, query, kind, status string, limit int) ([]Design, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id = ?"}
	args := []any{project}
	if query = strings.TrimSpace(query); query != "" {
		where = append(where, "(name LIKE ? OR description LIKE ? OR tags_json LIKE ?)")
		needle := "%" + query + "%"
		args = append(args, needle, needle, needle)
	}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if status == "" {
		status = "active"
	}
	if status != "all" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT id, project_id, name, description, kind, status, tags_json,
		COALESCE(current_revision_id, 0), created_at, updated_at
		FROM designs WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Design{}
	for rows.Next() {
		var item Design
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Name, &item.Description, &item.Kind,
			&item.Status, rawJSONScanner{&item.Tags}, &item.CurrentRevisionID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetDesign(project string, id int64) (*Design, error) {
	var item Design
	err := s.db.QueryRow(`SELECT id, project_id, name, description, kind, status, tags_json,
		COALESCE(current_revision_id, 0), created_at, updated_at
		FROM designs WHERE project_id = ? AND id = ?`, project, id).Scan(
		&item.ID, &item.ProjectID, &item.Name, &item.Description, &item.Kind,
		&item.Status, rawJSONScanner{&item.Tags}, &item.CurrentRevisionID, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if item.CurrentRevisionID != 0 {
		rev, err := s.GetRevision(project, item.CurrentRevisionID)
		if err != nil {
			return nil, err
		}
		item.CurrentRevision = rev
		item.LatestBuild, _ = s.LatestBuild(item.ID, rev.ID)
		item.Artifacts, _ = s.ListArtifacts(project, item.ID, rev.ID)
	}
	return &item, nil
}

func (s *Store) ArchiveDesign(project string, id int64, archived bool) (*Design, error) {
	status := "active"
	if archived {
		status = "archived"
	}
	res, err := s.db.Exec(`UPDATE designs SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, status, project, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errNotFound
	}
	return s.GetDesign(project, id)
}

func (s *Store) CreateRevision(project string, input CreateRevisionInput) (*Revision, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRow(`SELECT COALESCE(current_revision_id, 0) FROM designs WHERE project_id = ? AND id = ?`, project, input.DesignID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if current != input.ExpectedParent {
		return nil, fmt.Errorf("%w: expected %d, current is %d", errRevisionConflict, input.ExpectedParent, current)
	}
	var number int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(revision_number), 0) + 1 FROM design_revisions WHERE design_id = ?`, input.DesignID).Scan(&number); err != nil {
		return nil, err
	}
	hash := sourceHash(input.Definition, input.Parameters)
	res, err := tx.Exec(`INSERT INTO design_revisions
		(design_id, parent_revision_id, revision_number, definition_json, parameters_json, source_sha256, note, author)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, input.DesignID, current, number, string(input.Definition), string(input.Parameters), hash, input.Note, input.Author)
	if err != nil {
		return nil, err
	}
	revisionID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	res, err = tx.Exec(`UPDATE designs SET current_revision_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND project_id = ? AND current_revision_id = ?`, revisionID, input.DesignID, project, current)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRevision(project, revisionID)
}

func (s *Store) GetRevision(project string, id int64) (*Revision, error) {
	var rev Revision
	var parent sql.NullInt64
	err := s.db.QueryRow(`SELECT r.id, r.design_id, r.parent_revision_id, r.revision_number,
		r.definition_json, r.parameters_json, r.source_sha256, r.note, r.author, r.created_at
		FROM design_revisions r JOIN designs d ON d.id = r.design_id
		WHERE d.project_id = ? AND r.id = ?`, project, id).Scan(
		&rev.ID, &rev.DesignID, &parent, &rev.RevisionNumber, rawJSONScanner{&rev.Definition}, rawJSONScanner{&rev.Parameters},
		&rev.SourceSHA256, &rev.Note, &rev.Author, &rev.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if parent.Valid {
		rev.ParentRevisionID = &parent.Int64
	}
	return &rev, nil
}

func (s *Store) ListRevisions(project string, designID int64) ([]Revision, error) {
	rows, err := s.db.Query(`SELECT r.id, r.design_id, r.parent_revision_id, r.revision_number,
		r.definition_json, r.parameters_json, r.source_sha256, r.note, r.author, r.created_at
		FROM design_revisions r JOIN designs d ON d.id = r.design_id
		WHERE d.project_id = ? AND d.id = ? ORDER BY r.revision_number DESC`, project, designID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var rev Revision
		var parent sql.NullInt64
		if err := rows.Scan(&rev.ID, &rev.DesignID, &parent, &rev.RevisionNumber, rawJSONScanner{&rev.Definition},
			rawJSONScanner{&rev.Parameters}, &rev.SourceSHA256, &rev.Note, &rev.Author, &rev.CreatedAt); err != nil {
			return nil, err
		}
		if parent.Valid {
			rev.ParentRevisionID = &parent.Int64
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

func (s *Store) StartBuild(designID, revisionID int64) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO build_runs(design_id, revision_id, status, engine, engine_version)
		VALUES(?, ?, 'running', ?, ?)`, designID, revisionID, engineName, engineVersion)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishBuild(id int64, status string, report any, checks any, errorText string, duration time.Duration) (*BuildRun, error) {
	reportJSON, _ := json.Marshal(report)
	checksJSON, _ := json.Marshal(checks)
	_, err := s.db.Exec(`UPDATE build_runs SET status = ?, report_json = ?, checks_json = ?,
		error_text = ?, duration_ms = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, string(reportJSON), string(checksJSON), errorText, duration.Milliseconds(), id)
	if err != nil {
		return nil, err
	}
	return s.GetBuild(id)
}

func (s *Store) GetBuild(id int64) (*BuildRun, error) {
	var run BuildRun
	var completed sql.NullString
	err := s.db.QueryRow(`SELECT id, design_id, revision_id, status, engine, engine_version,
		report_json, checks_json, error_text, duration_ms, created_at, completed_at
		FROM build_runs WHERE id = ?`, id).Scan(&run.ID, &run.DesignID, &run.RevisionID, &run.Status,
		&run.Engine, &run.EngineVersion, rawJSONScanner{&run.Report}, rawJSONScanner{&run.Checks}, &run.ErrorText, &run.DurationMS,
		&run.CreatedAt, &completed)
	if err != nil {
		return nil, err
	}
	if completed.Valid {
		run.CompletedAt = completed.String
	}
	return &run, nil
}

func (s *Store) LatestBuild(designID, revisionID int64) (*BuildRun, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM build_runs WHERE design_id = ? AND revision_id = ?
		ORDER BY id DESC LIMIT 1`, designID, revisionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetBuild(id)
}

func (s *Store) SaveArtifact(artifact Artifact) (*Artifact, error) {
	metadata := artifact.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	var storage any
	if artifact.StorageFileID != nil {
		storage = *artifact.StorageFileID
	}
	var build any
	if artifact.BuildRunID != nil {
		build = *artifact.BuildRunID
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO artifacts
		(design_id, revision_id, build_run_id, kind, format, name, content_type, sha256,
		size_bytes, storage_file_id, local_path, metadata_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, artifact.DesignID, artifact.RevisionID, build,
		artifact.Kind, artifact.Format, artifact.Name, artifact.ContentType, artifact.SHA256,
		artifact.SizeBytes, storage, artifact.LocalPath, string(metadata))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		err = s.db.QueryRow(`SELECT id FROM artifacts WHERE revision_id = ? AND format = ? AND sha256 = ?`,
			artifact.RevisionID, artifact.Format, artifact.SHA256).Scan(&id)
		if err != nil {
			return nil, err
		}
	}
	return s.GetArtifact(id)
}

func (s *Store) GetArtifact(id int64) (*Artifact, error) {
	var item Artifact
	var build, storage sql.NullInt64
	err := s.db.QueryRow(`SELECT id, design_id, revision_id, build_run_id, kind, format, name,
		content_type, sha256, size_bytes, storage_file_id, local_path, metadata_json, created_at
		FROM artifacts WHERE id = ?`, id).Scan(&item.ID, &item.DesignID, &item.RevisionID, &build,
		&item.Kind, &item.Format, &item.Name, &item.ContentType, &item.SHA256, &item.SizeBytes,
		&storage, &item.LocalPath, rawJSONScanner{&item.Metadata}, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if build.Valid {
		item.BuildRunID = &build.Int64
	}
	if storage.Valid {
		item.StorageFileID = &storage.Int64
	}
	item.DownloadURL = fmt.Sprintf("/api/apps/design/api/artifacts/%d/content", item.ID)
	return &item, nil
}

func (s *Store) ListArtifacts(project string, designID, revisionID int64) ([]Artifact, error) {
	where := []string{"d.project_id = ?"}
	args := []any{project}
	if designID > 0 {
		where = append(where, "a.design_id = ?")
		args = append(args, designID)
	}
	if revisionID > 0 {
		where = append(where, "a.revision_id = ?")
		args = append(args, revisionID)
	}
	rows, err := s.db.Query(`SELECT a.id FROM artifacts a JOIN designs d ON d.id = a.design_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY a.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]Artifact, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetArtifact(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, nil
}
