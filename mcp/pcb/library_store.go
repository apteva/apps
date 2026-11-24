package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var errLibraryPartNotFound = errors.New("library part not found")

// UpsertLibraryPart stores (or replaces) a project library part by part ID.
func (s *Store) UpsertLibraryPart(project string, part *LibraryPart, author string) error {
	if err := validateLibraryPart(part); err != nil {
		return err
	}
	if _, ok := builtinLibraryPart(part.ID); ok {
		return fmt.Errorf("library part id %q is reserved by a built-in part", part.ID)
	}
	body, err := json.Marshal(part)
	if err != nil {
		return err
	}
	search := strings.ToLower(strings.Join(append([]string{part.ID, part.Name, part.Description, part.Value, part.Manufacturer, part.MPN, part.LCSC, part.Footprint.LibraryID}, part.Keywords...), " "))
	_, err = s.db.Exec(`INSERT INTO pcb_library_parts(project_id,part_id,name,category,search_text,source,source_sha256,part_json,created_by) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_id,part_id) DO UPDATE SET name=excluded.name,category=excluded.category,search_text=excluded.search_text,source=excluded.source,source_sha256=excluded.source_sha256,part_json=excluded.part_json,updated_at=CURRENT_TIMESTAMP`,
		project, part.ID, part.Name, part.Category, search, part.Provenance.Source, part.Provenance.SourceSHA256, string(body), author)
	return err
}

// GetLibraryPart returns a project part, or a built-in part of that ID.
func (s *Store) GetLibraryPart(project, partID string) (*LibraryPart, error) {
	partID = strings.TrimSpace(partID)
	if part, ok := builtinLibraryPart(partID); ok {
		return part, nil
	}
	if s == nil || s.db == nil {
		return nil, errLibraryPartNotFound
	}
	var body string
	err := s.db.QueryRow(`SELECT part_json FROM pcb_library_parts WHERE project_id=? AND part_id=?`, project, partID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errLibraryPartNotFound
	}
	if err != nil {
		return nil, err
	}
	var part LibraryPart
	if err := json.Unmarshal([]byte(body), &part); err != nil {
		return nil, err
	}
	return &part, nil
}

// ListLibraryParts returns built-in parts followed by project parts that
// match the query and category, ordered by category then name.
func (s *Store) ListLibraryParts(project, query, category string, limit int) ([]LibraryPartSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	out := []LibraryPartSummary{}
	for _, part := range builtinLibrary() {
		if part.matches(query, category) {
			out = append(out, part.summary())
		}
	}
	if s != nil && s.db != nil {
		rows, err := s.db.Query(`SELECT part_json FROM pcb_library_parts WHERE project_id=? ORDER BY category, name LIMIT 1000`, project)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return nil, err
			}
			var part LibraryPart
			if err := json.Unmarshal([]byte(body), &part); err != nil {
				continue
			}
			if part.matches(query, category) {
				out = append(out, part.summary())
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
