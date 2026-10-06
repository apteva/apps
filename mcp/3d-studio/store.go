package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apteva/apps/mcp/3d-studio/engine"
)

var errNotFound = errors.New("not found")
var errConflict = errors.New("revision conflict: reload the asset and select geometry again")

type Store struct{ db *sql.DB }
type Asset struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Head    int64  `json:"head_revision_id"`
	Updated string `json:"updated_at"`
}
type Revision struct {
	ID         int64           `json:"id"`
	AssetID    int64           `json:"asset_id"`
	ParentID   int64           `json:"parent_revision_id"`
	Document   engine.Document `json:"document"`
	SourceHash string          `json:"source_hash"`
	Note       string          `json:"note"`
	Created    string          `json:"created_at"`
}
type SavedSelection struct {
	ID         string           `json:"id"`
	AssetID    int64            `json:"asset_id"`
	RevisionID int64            `json:"revision_id"`
	Name       string           `json:"name"`
	Selection  engine.Selection `json:"selection"`
}
type Candidate struct {
	ID       string            `json:"id"`
	AssetID  int64             `json:"asset_id"`
	Base     int64             `json:"base_revision_id"`
	Result   engine.EditResult `json:"result"`
	Commands []engine.Command  `json:"commands"`
	Expires  int64             `json:"expires_at"`
}
type Artifact struct {
	ID          int64  `json:"id"`
	AssetID     int64  `json:"asset_id"`
	RevisionID  int64  `json:"revision_id"`
	CandidateID string `json:"candidate_id,omitempty"`
	Format      string `json:"format"`
	SHA256      string `json:"sha256"`
	URL         string `json:"url"`
}

func token() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func encoded(v any) string   { b, _ := json.Marshal(v); return string(b) }
func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	return err
}
func (s *Store) asset(project string, id int64) (Asset, error) {
	var a Asset
	err := s.db.QueryRow(`SELECT id,name,COALESCE(head_revision_id,0),updated_at FROM studio_assets WHERE project_id=? AND id=?`, project, id).Scan(&a.ID, &a.Name, &a.Head, &a.Updated)
	return a, missing(err)
}
func (s *Store) list(project string) ([]Asset, error) {
	rows, err := s.db.Query(`SELECT id,name,COALESCE(head_revision_id,0),updated_at FROM studio_assets WHERE project_id=? ORDER BY id DESC LIMIT 200`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		if err = rows.Scan(&a.ID, &a.Name, &a.Head, &a.Updated); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) create(project, name string, d engine.Document) (Revision, error) {
	name = strings.TrimSpace(name)
	if project == "" || name == "" || len(name) > 160 {
		return Revision{}, errors.New("project and name required; name maximum 160 characters")
	}
	if _, err := engine.Validate(d); err != nil {
		return Revision{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Revision{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO studio_assets(project_id,name) VALUES(?,?)`, project, name)
	if err != nil {
		return Revision{}, err
	}
	assetID, _ := res.LastInsertId()
	raw := encoded(d)
	res, err = tx.Exec(`INSERT INTO studio_revisions(asset_id,document_json,commands_json,source_hash,note) VALUES(?,?,'[]',?,'Initial model')`, assetID, raw, digest([]byte(raw)))
	if err != nil {
		return Revision{}, err
	}
	revID, _ := res.LastInsertId()
	if _, err = tx.Exec(`UPDATE studio_assets SET head_revision_id=? WHERE id=?`, revID, assetID); err != nil {
		return Revision{}, err
	}
	if err = tx.Commit(); err != nil {
		return Revision{}, err
	}
	return s.revision(project, assetID, revID)
}
func (s *Store) revision(project string, assetID, revisionID int64) (Revision, error) {
	a, err := s.asset(project, assetID)
	if err != nil {
		return Revision{}, err
	}
	if revisionID == 0 {
		revisionID = a.Head
	}
	var r Revision
	var raw string
	err = s.db.QueryRow(`SELECT id,asset_id,COALESCE(parent_revision_id,0),document_json,source_hash,note,created_at FROM studio_revisions WHERE asset_id=? AND id=?`, assetID, revisionID).Scan(&r.ID, &r.AssetID, &r.ParentID, &raw, &r.SourceHash, &r.Note, &r.Created)
	if err != nil {
		return r, missing(err)
	}
	err = json.Unmarshal([]byte(raw), &r.Document)
	return r, err
}
func (s *Store) history(project string, assetID int64) ([]map[string]any, error) {
	if _, err := s.asset(project, assetID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,COALESCE(parent_revision_id,0),note,source_hash,created_at FROM studio_revisions WHERE asset_id=? ORDER BY id DESC LIMIT 100`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, parent int64
		var note, hash, created string
		if err = rows.Scan(&id, &parent, &note, &hash, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "parent_revision_id": parent, "note": note, "source_hash": hash, "created_at": created})
	}
	return out, rows.Err()
}
func (s *Store) prior(project string, assetID int64, key, hash string) (*Revision, error) {
	if key == "" || len(key) > 160 {
		return nil, errors.New("request_key required, maximum 160 characters")
	}
	if _, err := s.asset(project, assetID); err != nil {
		return nil, err
	}
	var id int64
	var existing string
	err := s.db.QueryRow(`SELECT id,request_hash FROM studio_revisions WHERE asset_id=? AND request_key=?`, assetID, key).Scan(&id, &existing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existing != hash {
		return nil, errors.New("request_key already used with different input")
	}
	r, err := s.revision(project, assetID, id)
	return &r, err
}
func (s *Store) commit(project string, assetID, base int64, d engine.Document, commands []engine.Command, note, key, hash string, selections map[string]engine.Selection) (Revision, error) {
	if base <= 0 {
		return Revision{}, errors.New("expected_revision_id required")
	}
	if len(note) > 1000 {
		return Revision{}, errors.New("note exceeds 1000 characters")
	}
	if _, err := engine.Validate(d); err != nil {
		return Revision{}, err
	}
	if prior, err := s.prior(project, assetID, key, hash); prior != nil || err != nil {
		if prior != nil {
			return *prior, nil
		}
		return Revision{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Revision{}, err
	}
	defer tx.Rollback()
	// Reserve the head with a conditional write before inserting the child revision.
	res, err := tx.Exec(`UPDATE studio_assets SET updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND head_revision_id=?`, assetID, project, base)
	if err != nil {
		return Revision{}, err
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		return Revision{}, errConflict
	}
	for name, sel := range selections {
		if len(name) > 80 {
			return Revision{}, errors.New("selection name exceeds 80 characters")
		}
		if _, err := engine.Select(d, engine.Query{NodeID: sel.NodeID, Kind: sel.Kind, IDs: sel.IDs}); err != nil {
			return Revision{}, err
		}
	}
	raw := encoded(d)
	res, err = tx.Exec(`INSERT INTO studio_revisions(asset_id,parent_revision_id,document_json,commands_json,source_hash,note,request_key,request_hash) VALUES(?,?,?,?,?,?,?,?)`, assetID, base, raw, encoded(commands), digest([]byte(raw)), note, key, hash)
	if err != nil {
		return Revision{}, err
	}
	revID, _ := res.LastInsertId()
	for name, sel := range selections {
		if _, err = tx.Exec(`INSERT INTO studio_selections(id,asset_id,revision_id,name,selection_json) VALUES(?,?,?,?,?)`, token(), assetID, revID, name, encoded(sel)); err != nil {
			return Revision{}, err
		}
	}
	if _, err = tx.Exec(`UPDATE studio_assets SET head_revision_id=? WHERE id=?`, revID, assetID); err != nil {
		return Revision{}, err
	}
	if err = tx.Commit(); err != nil {
		return Revision{}, err
	}
	return s.revision(project, assetID, revID)
}
func (s *Store) saveSelection(project string, assetID, revisionID int64, name string, sel engine.Selection) (SavedSelection, error) {
	r, err := s.revision(project, assetID, revisionID)
	if err != nil {
		return SavedSelection{}, err
	}
	if len(name) > 80 {
		return SavedSelection{}, errors.New("selection name exceeds 80 characters")
	}
	if _, err = engine.Select(r.Document, engine.Query{NodeID: sel.NodeID, Kind: sel.Kind, IDs: sel.IDs}); err != nil {
		return SavedSelection{}, err
	}
	v := SavedSelection{token(), assetID, r.ID, name, sel}
	_, err = s.db.Exec(`INSERT INTO studio_selections(id,asset_id,revision_id,name,selection_json) VALUES(?,?,?,?,?)`, v.ID, assetID, r.ID, name, encoded(sel))
	return v, err
}
func (s *Store) selection(project, id string) (SavedSelection, error) {
	var v SavedSelection
	var raw string
	err := s.db.QueryRow(`SELECT s.id,s.asset_id,s.revision_id,s.name,s.selection_json FROM studio_selections s JOIN studio_assets a ON a.id=s.asset_id WHERE s.id=? AND a.project_id=?`, id, project).Scan(&v.ID, &v.AssetID, &v.RevisionID, &v.Name, &raw)
	if err != nil {
		return v, missing(err)
	}
	err = json.Unmarshal([]byte(raw), &v.Selection)
	return v, err
}
func (s *Store) saveCandidate(project string, assetID, base int64, result engine.EditResult, commands []engine.Command) (Candidate, error) {
	if _, err := s.revision(project, assetID, base); err != nil {
		return Candidate{}, err
	}
	c := Candidate{token(), assetID, base, result, commands, time.Now().Add(time.Hour).Unix()}
	if _, err := s.db.Exec(`DELETE FROM studio_candidates WHERE expires_at<?`, time.Now().Unix()); err != nil {
		return c, err
	}
	_, err := s.db.Exec(`INSERT INTO studio_candidates(id,asset_id,base_revision_id,result_json,commands_json,expires_at) VALUES(?,?,?,?,?,?)`, c.ID, assetID, base, encoded(result), encoded(commands), c.Expires)
	return c, err
}
func (s *Store) candidate(project, id string) (Candidate, error) {
	var c Candidate
	var raw, commands string
	err := s.db.QueryRow(`SELECT c.id,c.asset_id,c.base_revision_id,c.result_json,c.commands_json,c.expires_at FROM studio_candidates c JOIN studio_assets a ON a.id=c.asset_id WHERE c.id=? AND a.project_id=? AND c.expires_at>?`, id, project, time.Now().Unix()).Scan(&c.ID, &c.AssetID, &c.Base, &raw, &commands, &c.Expires)
	if err != nil {
		return c, missing(err)
	}
	if err = json.Unmarshal([]byte(raw), &c.Result); err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(commands), &c.Commands)
	return c, err
}
func (s *Store) artifact(project string, assetID, revisionID int64, candidate, format string, content []byte) (Artifact, error) {
	if _, err := s.revision(project, assetID, revisionID); err != nil {
		return Artifact{}, err
	}
	h := digest(content)
	res, err := s.db.Exec(`INSERT INTO studio_artifacts(asset_id,revision_id,candidate_id,format,sha256,content) VALUES(?,?,?,?,?,?)`, assetID, revisionID, candidate, format, h, content)
	if err != nil {
		return Artifact{}, err
	}
	id, _ := res.LastInsertId()
	return Artifact{id, assetID, revisionID, candidate, format, h, fmt.Sprintf("/api/artifacts/%d", id)}, nil
}
func (s *Store) artifactContent(project string, id int64) ([]byte, string, error) {
	var b []byte
	var format string
	err := s.db.QueryRow(`SELECT r.content,r.format FROM studio_artifacts r JOIN studio_assets a ON a.id=r.asset_id WHERE r.id=? AND a.project_id=?`, id, project).Scan(&b, &format)
	return b, format, missing(err)
}
