package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	errNotFound         = errors.New("PCB design not found")
	errRevisionConflict = errors.New("current PCB revision changed")
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

type rawJSONScanner struct{ target *json.RawMessage }

func (s rawJSONScanner) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*s.target = nil
	case string:
		*s.target = append((*s.target)[:0], v...)
	case []byte:
		*s.target = append((*s.target)[:0], v...)
	default:
		return fmt.Errorf("cannot scan %T as JSON", src)
	}
	return nil
}

func (s *Store) CreateDesign(project, name string, definition, operations []byte, hash, note, author string) (*Design, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name required")
	}
	if len(name) > 160 {
		return nil, errors.New("name exceeds 160 characters")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO pcb_designs(project_id,name) VALUES(?,?)`, project, name)
	if err != nil {
		return nil, err
	}
	designID, _ := res.LastInsertId()
	res, err = tx.Exec(`INSERT INTO pcb_revisions(design_id,project_id,parent_id,number,schema_version,definition_json,operations_json,source_sha256,note,author) VALUES(?,?,NULL,1,?,?,?,?,?,?)`, designID, project, pcbSchema, string(definition), nullableJSON(operations), hash, note, author)
	if err != nil {
		return nil, err
	}
	revisionID, _ := res.LastInsertId()
	if _, err = tx.Exec(`UPDATE pcb_designs SET current_revision_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, revisionID, designID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetDesign(project, designID)
}

func (s *Store) ListDesigns(project, q, status string, limit int) ([]Design, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where, args := []string{"project_id=?"}, []any{project}
	if q = strings.TrimSpace(q); q != "" {
		where = append(where, "name LIKE ?")
		args = append(args, "%"+q+"%")
	}
	if status == "archived" {
		where = append(where, "archived=1")
	} else if status != "all" {
		where = append(where, "archived=0")
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT id,project_id,name,status,COALESCE(current_revision_id,0),archived,created_at,updated_at FROM pcb_designs WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Design{}
	for rows.Next() {
		var d Design
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Status, &d.CurrentRevisionID, &d.Archived, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDesign(project string, id int64) (*Design, error) {
	var d Design
	err := s.db.QueryRow(`SELECT id,project_id,name,status,COALESCE(current_revision_id,0),archived,created_at,updated_at FROM pcb_designs WHERE project_id=? AND id=?`, project, id).Scan(&d.ID, &d.ProjectID, &d.Name, &d.Status, &d.CurrentRevisionID, &d.Archived, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if d.CurrentRevisionID != 0 {
		d.CurrentRevision, err = s.GetRevision(project, d.CurrentRevisionID)
		if err != nil {
			return nil, err
		}
		d.Validation, _ = s.LatestValidation(project, d.ID, d.CurrentRevisionID)
		d.Artifacts, _ = s.ListArtifacts(project, d.ID, d.CurrentRevisionID)
	}
	return &d, nil
}

func (s *Store) ArchiveDesign(project string, id int64, archived bool) (*Design, error) {
	status := "draft"
	flag := 0
	if archived {
		status = "archived"
		flag = 1
	}
	res, err := s.db.Exec(`UPDATE pcb_designs SET archived=?,status=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, flag, status, project, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errNotFound
	}
	return s.GetDesign(project, id)
}

func (s *Store) CreateRevision(project string, designID, expectedParent int64, definition, operations []byte, hash, note, author string) (*Revision, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRow(`SELECT COALESCE(current_revision_id,0) FROM pcb_designs WHERE project_id=? AND id=?`, project, designID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if current != expectedParent {
		return nil, fmt.Errorf("%w: expected %d, current is %d", errRevisionConflict, expectedParent, current)
	}
	var number int
	if err = tx.QueryRow(`SELECT COALESCE(MAX(number),0)+1 FROM pcb_revisions WHERE design_id=?`, designID).Scan(&number); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO pcb_revisions(design_id,project_id,parent_id,number,schema_version,definition_json,operations_json,source_sha256,note,author) VALUES(?,?,?,?,?,?,?,?,?,?)`, designID, project, current, number, pcbSchema, string(definition), nullableJSON(operations), hash, note, author)
	if err != nil {
		return nil, err
	}
	revisionID, _ := res.LastInsertId()
	res, err = tx.Exec(`UPDATE pcb_designs SET current_revision_id=?,status='draft',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND current_revision_id=?`, revisionID, project, designID, current)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errRevisionConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRevision(project, revisionID)
}

func (s *Store) GetRevision(project string, id int64) (*Revision, error) {
	var r Revision
	var parent sql.NullInt64
	var operations sql.NullString
	err := s.db.QueryRow(`SELECT id,design_id,project_id,parent_id,number,schema_version,definition_json,operations_json,source_sha256,note,author,created_at FROM pcb_revisions WHERE project_id=? AND id=?`, project, id).Scan(&r.ID, &r.DesignID, &r.ProjectID, &parent, &r.Number, &r.SchemaVersion, rawJSONScanner{&r.Definition}, &operations, &r.SourceSHA256, &r.Note, &r.Author, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if parent.Valid {
		r.ParentID = &parent.Int64
	}
	if operations.Valid {
		r.Operations = json.RawMessage(operations.String)
	}
	return &r, nil
}

func (s *Store) ListRevisions(project string, designID int64) ([]Revision, error) {
	rows, err := s.db.Query(`SELECT id,design_id,project_id,parent_id,number,schema_version,definition_json,operations_json,source_sha256,note,author,created_at FROM pcb_revisions WHERE project_id=? AND design_id=? ORDER BY number DESC`, project, designID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var r Revision
		var p sql.NullInt64
		var ops sql.NullString
		if err := rows.Scan(&r.ID, &r.DesignID, &r.ProjectID, &p, &r.Number, &r.SchemaVersion, rawJSONScanner{&r.Definition}, &ops, &r.SourceSHA256, &r.Note, &r.Author, &r.CreatedAt); err != nil {
			return nil, err
		}
		if p.Valid {
			r.ParentID = &p.Int64
		}
		if ops.Valid {
			r.Operations = json.RawMessage(ops.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SaveValidation(project string, designID, revisionID int64, report ValidationReport) (*ValidationRun, error) {
	raw, _ := json.Marshal(report)
	res, err := s.db.Exec(`INSERT INTO pcb_validation_runs(design_id,revision_id,project_id,status,errors,warnings,report_json) VALUES(?,?,?,?,?,?,?)`, designID, revisionID, project, report.Status, report.Errors, report.Warnings, string(raw))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.Exec(`UPDATE pcb_designs SET status=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, validationDesignStatus(report.Status), project, designID)
	return s.GetValidation(project, id)
}
func validationDesignStatus(status string) string {
	if status == "failed" {
		return "invalid"
	}
	return "validated"
}
func (s *Store) GetValidation(project string, id int64) (*ValidationRun, error) {
	var v ValidationRun
	var raw json.RawMessage
	err := s.db.QueryRow(`SELECT id,design_id,revision_id,status,errors,warnings,report_json,created_at FROM pcb_validation_runs WHERE project_id=? AND id=?`, project, id).Scan(&v.ID, &v.DesignID, &v.RevisionID, &v.Status, &v.Errors, &v.Warnings, rawJSONScanner{&raw}, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &v.Report); err != nil {
		return nil, err
	}
	return &v, nil
}
func (s *Store) LatestValidation(project string, designID, revisionID int64) (*ValidationRun, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM pcb_validation_runs WHERE project_id=? AND design_id=? AND revision_id=? ORDER BY id DESC LIMIT 1`, project, designID, revisionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetValidation(project, id)
}

func (s *Store) SaveArtifact(project string, a Artifact) (*Artifact, error) {
	res, err := s.db.Exec(`INSERT INTO pcb_artifacts(design_id,revision_id,project_id,kind,format,name,content_type,local_path,storage_file_id,sha256,size_bytes,metadata_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, a.DesignID, a.RevisionID, project, a.Kind, a.Format, a.Name, a.ContentType, a.LocalPath, a.StorageFileID, a.SHA256, a.SizeBytes, string(defaultJSON(a.Metadata)))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetArtifact(project, id)
}
func (s *Store) GetArtifact(project string, id int64) (*Artifact, error) {
	var a Artifact
	err := s.db.QueryRow(`SELECT id,design_id,revision_id,kind,format,name,content_type,local_path,storage_file_id,sha256,size_bytes,metadata_json,created_at FROM pcb_artifacts WHERE project_id=? AND id=?`, project, id).Scan(&a.ID, &a.DesignID, &a.RevisionID, &a.Kind, &a.Format, &a.Name, &a.ContentType, &a.LocalPath, &a.StorageFileID, &a.SHA256, &a.SizeBytes, rawJSONScanner{&a.Metadata}, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return &a, err
}
func (s *Store) ListArtifacts(project string, designID, revisionID int64) ([]Artifact, error) {
	q := `SELECT id,design_id,revision_id,kind,format,name,content_type,local_path,storage_file_id,sha256,size_bytes,metadata_json,created_at FROM pcb_artifacts WHERE project_id=? AND design_id=?`
	args := []any{project, designID}
	if revisionID != 0 {
		q += ` AND revision_id=?`
		args = append(args, revisionID)
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.DesignID, &a.RevisionID, &a.Kind, &a.Format, &a.Name, &a.ContentType, &a.LocalPath, &a.StorageFileID, &a.SHA256, &a.SizeBytes, rawJSONScanner{&a.Metadata}, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
func defaultJSON(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`{}`)
	}
	return b
}
