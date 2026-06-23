package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

var errNotFound = sql.ErrNoRows

type Book struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	Title           string `json:"title"`
	Subtitle        string `json:"subtitle,omitempty"`
	AuthorName      string `json:"author_name,omitempty"`
	Description     string `json:"description,omitempty"`
	Kind            string `json:"kind"`
	Language        string `json:"language"`
	TargetWordCount int    `json:"target_word_count"`
	Status          string `json:"status"`
	ActualWordCount int    `json:"actual_word_count"`
	NodeCount       int    `json:"node_count"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	ArchivedAt      string `json:"archived_at,omitempty"`
}

type BookNode struct {
	ID              int64       `json:"id"`
	BookID          int64       `json:"book_id"`
	ParentID        *int64      `json:"parent_id,omitempty"`
	Type            string      `json:"type"`
	Title           string      `json:"title"`
	BodyMarkdown    string      `json:"body_markdown,omitempty"`
	Summary         string      `json:"summary,omitempty"`
	Position        int         `json:"position"`
	Status          string      `json:"status"`
	TargetWordCount int         `json:"target_word_count"`
	ActualWordCount int         `json:"actual_word_count"`
	CreatedAt       string      `json:"created_at,omitempty"`
	UpdatedAt       string      `json:"updated_at,omitempty"`
	Children        []*BookNode `json:"children,omitempty"`
}

type BookNote struct {
	ID        int64    `json:"id"`
	BookID    int64    `json:"book_id"`
	NodeID    *int64   `json:"node_id,omitempty"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	URL       string   `json:"url,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

type BookRevision struct {
	ID              int64  `json:"id"`
	BookID          int64  `json:"book_id"`
	NodeID          int64  `json:"node_id"`
	Title           string `json:"title"`
	BodyMarkdown    string `json:"body_markdown,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Status          string `json:"status"`
	TargetWordCount int    `json:"target_word_count"`
	ChangeSummary   string `json:"change_summary,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
}

type BookExport struct {
	ID            int64  `json:"id"`
	BookID        int64  `json:"book_id"`
	Format        string `json:"format"`
	StorageFileID string `json:"storage_file_id,omitempty"`
	Filename      string `json:"filename"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

type ExportResult struct {
	Export        BookExport `json:"export"`
	Content       string     `json:"content,omitempty"`
	ContentType   string     `json:"content_type"`
	SizeBytes     int        `json:"size_bytes"`
	StorageFileID string     `json:"storage_file_id,omitempty"`
	URL           string     `json:"url,omitempty"`
}

func createBook(db *sql.DB, projectID string, b *Book, createStarter bool) (*Book, error) {
	if strings.TrimSpace(b.Title) == "" {
		return nil, errors.New("title required")
	}
	if b.Kind == "" {
		b.Kind = "other"
	}
	if b.Language == "" {
		b.Language = "en"
	}
	if b.Status == "" {
		b.Status = "planning"
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO books (project_id, title, subtitle, author_name, description, kind, language, target_word_count, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, strings.TrimSpace(b.Title), b.Subtitle, b.AuthorName, b.Description, b.Kind, b.Language, b.TargetWordCount, b.Status)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if createStarter {
		if _, err := insertNodeTx(tx, &BookNode{BookID: id, Type: "front_matter", Title: "Introduction", Position: 0, Status: "draft"}); err != nil {
			return nil, err
		}
		if _, err := insertNodeTx(tx, &BookNode{BookID: id, Type: "chapter", Title: "Chapter 1", Position: 1, Status: "draft"}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getBook(db, id, projectID)
}

func listBooks(db *sql.DB, projectID string, includeArchived bool) ([]Book, error) {
	clauses := []string{"b.project_id = ?"}
	args := []any{projectID}
	if !includeArchived {
		clauses = append(clauses, "b.archived_at IS NULL")
	}
	rows, err := db.Query(`
		SELECT b.id, b.project_id, b.title, b.subtitle, b.author_name, b.description, b.kind, b.language,
		       b.target_word_count, b.status, b.created_at, b.updated_at, COALESCE(b.archived_at, ''),
		       COALESCE(SUM(CASE WHEN n.deleted_at IS NULL THEN n.actual_word_count ELSE 0 END), 0) AS actual_word_count,
		       COALESCE(SUM(CASE WHEN n.deleted_at IS NULL THEN 1 ELSE 0 END), 0) AS node_count
		FROM books b
		LEFT JOIN book_nodes n ON n.book_id = b.id
		WHERE `+strings.Join(clauses, " AND ")+`
		GROUP BY b.id
		ORDER BY b.updated_at DESC, b.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		var b Book
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.Title, &b.Subtitle, &b.AuthorName, &b.Description, &b.Kind, &b.Language, &b.TargetWordCount, &b.Status, &b.CreatedAt, &b.UpdatedAt, &b.ArchivedAt, &b.ActualWordCount, &b.NodeCount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func getBook(db *sql.DB, id int64, projectID string) (*Book, error) {
	var b Book
	err := db.QueryRow(`
		SELECT b.id, b.project_id, b.title, b.subtitle, b.author_name, b.description, b.kind, b.language,
		       b.target_word_count, b.status, b.created_at, b.updated_at, COALESCE(b.archived_at, ''),
		       COALESCE(SUM(CASE WHEN n.deleted_at IS NULL THEN n.actual_word_count ELSE 0 END), 0) AS actual_word_count,
		       COALESCE(SUM(CASE WHEN n.deleted_at IS NULL THEN 1 ELSE 0 END), 0) AS node_count
		FROM books b
		LEFT JOIN book_nodes n ON n.book_id = b.id
		WHERE b.id = ? AND b.project_id = ?
		GROUP BY b.id`, id, projectID).Scan(
		&b.ID, &b.ProjectID, &b.Title, &b.Subtitle, &b.AuthorName, &b.Description, &b.Kind, &b.Language,
		&b.TargetWordCount, &b.Status, &b.CreatedAt, &b.UpdatedAt, &b.ArchivedAt, &b.ActualWordCount, &b.NodeCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &b, err
}

func updateBook(db *sql.DB, id int64, projectID string, fields map[string]any) error {
	allowed := map[string]string{
		"title": "title", "subtitle": "subtitle", "author_name": "author_name", "description": "description",
		"kind": "kind", "language": "language", "status": "status", "target_word_count": "target_word_count",
	}
	sets, args := []string{}, []any{}
	for k, col := range allowed {
		v, ok := fields[k]
		if !ok {
			continue
		}
		switch k {
		case "target_word_count":
			n, ok := intArg(fields, k)
			if !ok {
				continue
			}
			sets = append(sets, col+" = ?")
			args = append(args, n)
		default:
			s, ok := v.(string)
			if !ok {
				continue
			}
			if k == "title" && strings.TrimSpace(s) == "" {
				return errors.New("title cannot be empty")
			}
			sets = append(sets, col+" = ?")
			args = append(args, s)
		}
	}
	if len(sets) == 0 {
		return errors.New("no updatable fields provided")
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now())
	args = append(args, id, projectID)
	res, err := db.Exec(`UPDATE books SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func archiveBook(db *sql.DB, id int64, projectID string) error {
	res, err := db.Exec(`UPDATE books SET archived_at = ?, updated_at = ? WHERE id = ? AND project_id = ?`, now(), now(), id, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func createNode(db *sql.DB, n *BookNode) (*BookNode, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := insertNodeTx(tx, n)
	if err != nil {
		return nil, err
	}
	if err := touchBookTx(tx, n.BookID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getNode(db, id)
}

func insertNodeTx(tx *sql.Tx, n *BookNode) (int64, error) {
	if n.BookID <= 0 {
		return 0, errors.New("book_id required")
	}
	if strings.TrimSpace(n.Title) == "" {
		return 0, errors.New("title required")
	}
	if n.Type == "" {
		n.Type = "chapter"
	}
	if n.Status == "" {
		n.Status = "draft"
	}
	if n.Position < 0 {
		pos, err := nextNodePositionTx(tx, n.BookID, n.ParentID)
		if err != nil {
			return 0, err
		}
		n.Position = pos
	}
	var parent any
	if n.ParentID != nil && *n.ParentID > 0 {
		parent = *n.ParentID
	}
	res, err := tx.Exec(`
		INSERT INTO book_nodes (book_id, parent_id, type, title, body_markdown, summary, position, status, target_word_count, actual_word_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.BookID, parent, n.Type, strings.TrimSpace(n.Title), n.BodyMarkdown, n.Summary, n.Position, n.Status, n.TargetWordCount, wordCount(n.BodyMarkdown))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func listNodes(db *sql.DB, bookID int64) ([]*BookNode, error) {
	rows, err := db.Query(`
		SELECT id, book_id, parent_id, type, title, body_markdown, summary, position, status,
		       target_word_count, actual_word_count, created_at, updated_at
		FROM book_nodes
		WHERE book_id = ? AND deleted_at IS NULL
		ORDER BY COALESCE(parent_id, 0), position, id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []*BookNode{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func nodeTree(nodes []*BookNode) []*BookNode {
	byParent := map[int64][]*BookNode{}
	for _, n := range nodes {
		key := int64(0)
		if n.ParentID != nil {
			key = *n.ParentID
		}
		n.Children = nil
		byParent[key] = append(byParent[key], n)
	}
	var attach func(parent int64) []*BookNode
	attach = func(parent int64) []*BookNode {
		kids := byParent[parent]
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].Position == kids[j].Position {
				return kids[i].ID < kids[j].ID
			}
			return kids[i].Position < kids[j].Position
		})
		for _, n := range kids {
			n.Children = attach(n.ID)
		}
		return kids
	}
	return attach(0)
}

func getNode(db *sql.DB, id int64) (*BookNode, error) {
	n, err := scanNode(db.QueryRow(`
		SELECT id, book_id, parent_id, type, title, body_markdown, summary, position, status,
		       target_word_count, actual_word_count, created_at, updated_at
		FROM book_nodes
		WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

type rowScanner interface{ Scan(...any) error }

func scanNode(s rowScanner) (*BookNode, error) {
	var n BookNode
	var parent sql.NullInt64
	if err := s.Scan(&n.ID, &n.BookID, &parent, &n.Type, &n.Title, &n.BodyMarkdown, &n.Summary, &n.Position, &n.Status, &n.TargetWordCount, &n.ActualWordCount, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, err
	}
	if parent.Valid {
		n.ParentID = &parent.Int64
	}
	return &n, nil
}

func updateNode(db *sql.DB, id int64, fields map[string]any) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := getNodeTx(tx, id)
	if err != nil {
		return err
	}
	if old == nil {
		return errNotFound
	}
	if err := insertRevisionTx(tx, old, strArg(fields, "change_summary")); err != nil {
		return err
	}
	allowed := map[string]string{
		"type": "type", "title": "title", "body_markdown": "body_markdown", "summary": "summary",
		"status": "status", "target_word_count": "target_word_count",
	}
	sets, args := []string{}, []any{}
	bodyChanged := false
	for k, col := range allowed {
		v, ok := fields[k]
		if !ok {
			continue
		}
		if k == "target_word_count" {
			n, ok := intArg(fields, k)
			if !ok {
				continue
			}
			sets = append(sets, col+" = ?")
			args = append(args, n)
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if k == "title" && strings.TrimSpace(s) == "" {
			return errors.New("title cannot be empty")
		}
		if k == "body_markdown" {
			bodyChanged = true
		}
		sets = append(sets, col+" = ?")
		args = append(args, s)
	}
	if bodyChanged {
		body := strArg(fields, "body_markdown")
		sets = append(sets, "actual_word_count = ?")
		args = append(args, wordCount(body))
	}
	if len(sets) == 0 {
		return errors.New("no updatable fields provided")
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now())
	args = append(args, id)
	if _, err := tx.Exec(`UPDATE book_nodes SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return err
	}
	if err := touchBookTx(tx, old.BookID); err != nil {
		return err
	}
	return tx.Commit()
}

func getNodeTx(tx *sql.Tx, id int64) (*BookNode, error) {
	n, err := scanNode(tx.QueryRow(`
		SELECT id, book_id, parent_id, type, title, body_markdown, summary, position, status,
		       target_word_count, actual_word_count, created_at, updated_at
		FROM book_nodes
		WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

func moveNode(db *sql.DB, id int64, parentID *int64, position int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	n, err := getNodeTx(tx, id)
	if err != nil {
		return err
	}
	if n == nil {
		return errNotFound
	}
	if parentID != nil {
		if *parentID == id {
			return errors.New("node cannot be its own parent")
		}
		if isDesc, err := isDescendantTx(tx, *parentID, id); err != nil {
			return err
		} else if isDesc {
			return errors.New("node cannot be moved under its own descendant")
		}
	}
	if position < 0 {
		pos, err := nextNodePositionTx(tx, n.BookID, parentID)
		if err != nil {
			return err
		}
		position = pos
	}
	if err := makeSiblingPositionRoomTx(tx, n.BookID, parentID, position, id); err != nil {
		return err
	}
	var parent any
	if parentID != nil && *parentID > 0 {
		parent = *parentID
	}
	if _, err := tx.Exec(`UPDATE book_nodes SET parent_id = ?, position = ?, updated_at = ? WHERE id = ?`, parent, position, now(), id); err != nil {
		return err
	}
	if err := normalizeSiblingPositionsTx(tx, n.BookID, parentID); err != nil {
		return err
	}
	if err := touchBookTx(tx, n.BookID); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteNode(db *sql.DB, id int64) error {
	n, err := getNode(db, id)
	if err != nil {
		return err
	}
	if n == nil {
		return errNotFound
	}
	res, err := db.Exec(`UPDATE book_nodes SET deleted_at = ?, updated_at = ? WHERE id = ?`, now(), now(), id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return errNotFound
	}
	_, _ = db.Exec(`UPDATE books SET updated_at = ? WHERE id = ?`, now(), n.BookID)
	return nil
}

func createNote(db *sql.DB, note *BookNote) (*BookNote, error) {
	if note.BookID <= 0 || strings.TrimSpace(note.Title) == "" {
		return nil, errors.New("book_id and title required")
	}
	if note.Type == "" {
		note.Type = "note"
	}
	tags, _ := json.Marshal(note.Tags)
	var node any
	if note.NodeID != nil && *note.NodeID > 0 {
		node = *note.NodeID
	}
	res, err := db.Exec(`
		INSERT INTO book_notes (book_id, node_id, type, title, body, url, tags_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		note.BookID, node, note.Type, note.Title, note.Body, note.URL, string(tags))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getNote(db, id)
}

func listNotes(db *sql.DB, bookID int64, nodeID *int64) ([]BookNote, error) {
	clauses := []string{"book_id = ?", "deleted_at IS NULL"}
	args := []any{bookID}
	if nodeID != nil {
		clauses = append(clauses, "node_id = ?")
		args = append(args, *nodeID)
	}
	rows, err := db.Query(`
		SELECT id, book_id, node_id, type, title, body, url, tags_json, created_at, updated_at
		FROM book_notes
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY updated_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookNote{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func getNote(db *sql.DB, id int64) (*BookNote, error) {
	n, err := scanNote(db.QueryRow(`
		SELECT id, book_id, node_id, type, title, body, url, tags_json, created_at, updated_at
		FROM book_notes
		WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

func scanNote(s rowScanner) (*BookNote, error) {
	var n BookNote
	var node sql.NullInt64
	var tags string
	if err := s.Scan(&n.ID, &n.BookID, &node, &n.Type, &n.Title, &n.Body, &n.URL, &tags, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, err
	}
	if node.Valid {
		n.NodeID = &node.Int64
	}
	_ = json.Unmarshal([]byte(tags), &n.Tags)
	if n.Tags == nil {
		n.Tags = []string{}
	}
	return &n, nil
}

func updateNote(db *sql.DB, id int64, fields map[string]any) error {
	allowed := map[string]string{"node_id": "node_id", "type": "type", "title": "title", "body": "body", "url": "url", "tags": "tags_json"}
	sets, args := []string{}, []any{}
	for k, col := range allowed {
		if _, ok := fields[k]; !ok {
			continue
		}
		switch k {
		case "node_id":
			n, ok := int64Arg(fields, k)
			if !ok || n <= 0 {
				sets = append(sets, col+" = NULL")
			} else {
				sets = append(sets, col+" = ?")
				args = append(args, n)
			}
		case "tags":
			tags := stringSliceArg(fields, k)
			b, _ := json.Marshal(tags)
			sets = append(sets, col+" = ?")
			args = append(args, string(b))
		default:
			s, ok := fields[k].(string)
			if !ok {
				continue
			}
			if k == "title" && strings.TrimSpace(s) == "" {
				return errors.New("title cannot be empty")
			}
			sets = append(sets, col+" = ?")
			args = append(args, s)
		}
	}
	if len(sets) == 0 {
		return errors.New("no updatable fields provided")
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now(), id)
	res, err := db.Exec(`UPDATE book_notes SET `+strings.Join(sets, ", ")+` WHERE id = ? AND deleted_at IS NULL`, args...)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return errNotFound
	}
	return nil
}

func deleteNote(db *sql.DB, id int64) error {
	res, err := db.Exec(`UPDATE book_notes SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, now(), now(), id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return errNotFound
	}
	return nil
}

func listRevisions(db *sql.DB, nodeID int64, limit int) ([]BookRevision, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := db.Query(`
		SELECT id, book_id, node_id, title, body_markdown, summary, status, target_word_count, change_summary, created_at
		FROM book_revisions
		WHERE node_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookRevision{}
	for rows.Next() {
		var r BookRevision
		if err := rows.Scan(&r.ID, &r.BookID, &r.NodeID, &r.Title, &r.BodyMarkdown, &r.Summary, &r.Status, &r.TargetWordCount, &r.ChangeSummary, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func restoreRevision(db *sql.DB, revisionID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var r BookRevision
	if err := tx.QueryRow(`
		SELECT id, book_id, node_id, title, body_markdown, summary, status, target_word_count, change_summary, created_at
		FROM book_revisions WHERE id = ?`, revisionID).
		Scan(&r.ID, &r.BookID, &r.NodeID, &r.Title, &r.BodyMarkdown, &r.Summary, &r.Status, &r.TargetWordCount, &r.ChangeSummary, &r.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound
		}
		return err
	}
	current, err := getNodeTx(tx, r.NodeID)
	if err != nil {
		return err
	}
	if current == nil {
		return errNotFound
	}
	if err := insertRevisionTx(tx, current, "before restoring revision"); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE book_nodes
		SET title = ?, body_markdown = ?, summary = ?, status = ?, target_word_count = ?,
		    actual_word_count = ?, updated_at = ?
		WHERE id = ?`,
		r.Title, r.BodyMarkdown, r.Summary, r.Status, r.TargetWordCount, wordCount(r.BodyMarkdown), now(), r.NodeID); err != nil {
		return err
	}
	if err := touchBookTx(tx, r.BookID); err != nil {
		return err
	}
	return tx.Commit()
}

func insertRevisionTx(tx *sql.Tx, n *BookNode, summary string) error {
	_, err := tx.Exec(`
		INSERT INTO book_revisions (book_id, node_id, title, body_markdown, summary, status, target_word_count, change_summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.BookID, n.ID, n.Title, n.BodyMarkdown, n.Summary, n.Status, n.TargetWordCount, summary)
	return err
}

func createExportRow(db *sql.DB, e *BookExport) (BookExport, error) {
	if e.Format == "" {
		e.Format = "markdown"
	}
	if e.Status == "" {
		e.Status = "created"
	}
	res, err := db.Exec(`
		INSERT INTO book_exports (book_id, format, storage_file_id, filename, status, error)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.BookID, e.Format, nullString(e.StorageFileID), e.Filename, e.Status, e.Error)
	if err != nil {
		return BookExport{}, err
	}
	e.ID, _ = res.LastInsertId()
	_ = db.QueryRow(`SELECT created_at FROM book_exports WHERE id = ?`, e.ID).Scan(&e.CreatedAt)
	return *e, nil
}

func renderMarkdownExport(book *Book, nodes []*BookNode, includeStatus bool) string {
	var b strings.Builder
	b.WriteString("# " + book.Title + "\n\n")
	if book.Subtitle != "" {
		b.WriteString(book.Subtitle + "\n\n")
	}
	if book.AuthorName != "" {
		b.WriteString("By " + book.AuthorName + "\n\n")
	}
	var walk func(list []*BookNode, depth int)
	walk = func(list []*BookNode, depth int) {
		for _, n := range list {
			heading := depth
			if heading < 1 {
				heading = 1
			}
			if heading > 6 {
				heading = 6
			}
			b.WriteString(strings.Repeat("#", heading) + " " + n.Title + "\n\n")
			if includeStatus {
				b.WriteString(fmt.Sprintf("<!-- type: %s; status: %s; words: %d -->\n\n", n.Type, n.Status, n.ActualWordCount))
			}
			if strings.TrimSpace(n.BodyMarkdown) != "" {
				b.WriteString(strings.TrimSpace(n.BodyMarkdown) + "\n\n")
			}
			walk(n.Children, depth+1)
		}
	}
	walk(nodeTree(nodes), 1)
	return strings.TrimSpace(b.String()) + "\n"
}

func nextNodePositionTx(tx *sql.Tx, bookID int64, parentID *int64) (int, error) {
	var pos sql.NullInt64
	var err error
	if parentID != nil && *parentID > 0 {
		err = tx.QueryRow(`SELECT COALESCE(MAX(position), -1) + 1 FROM book_nodes WHERE book_id = ? AND parent_id = ? AND deleted_at IS NULL`, bookID, *parentID).Scan(&pos)
	} else {
		err = tx.QueryRow(`SELECT COALESCE(MAX(position), -1) + 1 FROM book_nodes WHERE book_id = ? AND parent_id IS NULL AND deleted_at IS NULL`, bookID).Scan(&pos)
	}
	if err != nil {
		return 0, err
	}
	return int(pos.Int64), nil
}

func normalizeSiblingPositionsTx(tx *sql.Tx, bookID int64, parentID *int64) error {
	var rows *sql.Rows
	var err error
	if parentID != nil && *parentID > 0 {
		rows, err = tx.Query(`SELECT id FROM book_nodes WHERE book_id = ? AND parent_id = ? AND deleted_at IS NULL ORDER BY position, id`, bookID, *parentID)
	} else {
		rows, err = tx.Query(`SELECT id FROM book_nodes WHERE book_id = ? AND parent_id IS NULL AND deleted_at IS NULL ORDER BY position, id`, bookID)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE book_nodes SET position = ? WHERE id = ?`, i, id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func makeSiblingPositionRoomTx(tx *sql.Tx, bookID int64, parentID *int64, position int, excludeID int64) error {
	if parentID != nil && *parentID > 0 {
		_, err := tx.Exec(`
			UPDATE book_nodes
			SET position = position + 1
			WHERE book_id = ? AND parent_id = ? AND deleted_at IS NULL AND id <> ? AND position >= ?`,
			bookID, *parentID, excludeID, position)
		return err
	}
	_, err := tx.Exec(`
		UPDATE book_nodes
		SET position = position + 1
		WHERE book_id = ? AND parent_id IS NULL AND deleted_at IS NULL AND id <> ? AND position >= ?`,
		bookID, excludeID, position)
	return err
}

func isDescendantTx(tx *sql.Tx, candidateParent, nodeID int64) (bool, error) {
	cur := candidateParent
	for cur > 0 {
		if cur == nodeID {
			return true, nil
		}
		var parent sql.NullInt64
		if err := tx.QueryRow(`SELECT parent_id FROM book_nodes WHERE id = ?`, cur).Scan(&parent); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if !parent.Valid {
			return false, nil
		}
		cur = parent.Int64
	}
	return false, nil
}

func touchBookTx(tx *sql.Tx, bookID int64) error {
	_, err := tx.Exec(`UPDATE books SET updated_at = ? WHERE id = ?`, now(), bookID)
	return err
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func wordCount(s string) int {
	return len(regexp.MustCompile(`[\pL\pN][\pL\pN'’-]*`).FindAllString(s, -1))
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

func int64Arg(args map[string]any, key string) (int64, bool) {
	switch v := args[key].(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case json.Number:
		i, err := v.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func stringSliceArg(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []string:
		return v
	case []any:
		out := []string{}
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{}
	}
}

func nullableInt64Arg(args map[string]any, key string) (*int64, bool) {
	if _, ok := args[key]; !ok {
		return nil, false
	}
	n, ok := int64Arg(args, key)
	if !ok || n <= 0 {
		return nil, true
	}
	return &n, true
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func slugFilename(title string) string {
	title = strings.ToLower(title)
	var b strings.Builder
	lastDash := false
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "book"
	}
	return out
}
