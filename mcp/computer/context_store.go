package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ComputerContext struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Backend           string `json:"backend"`
	ProviderContextID string `json:"provider_context_id,omitempty"`
	PersistDefault    bool   `json:"persist_default"`
	AutoCreated       bool   `json:"auto_created"`
	MetadataJSON      string `json:"metadata_json,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	LastUsedAt        string `json:"last_used_at,omitempty"`
}

type contextCreateInput struct {
	ID                string
	Name              string
	Backend           string
	ProviderContextID string
	PersistDefault    bool
	AutoCreated       bool
	MetadataJSON      string
}

var errContextNotFound = errors.New("computer context not found")

const contextCols = `id, name, backend, provider_context_id, persist_default,
	auto_created, metadata_json, created_at, updated_at, COALESCE(last_used_at, '')`

func scanComputerContext(s rowScanner) (*ComputerContext, error) {
	var c ComputerContext
	var persist, auto int
	if err := s.Scan(&c.ID, &c.Name, &c.Backend, &c.ProviderContextID, &persist,
		&auto, &c.MetadataJSON, &c.CreatedAt, &c.UpdatedAt, &c.LastUsedAt); err != nil {
		return nil, err
	}
	c.PersistDefault = persist != 0
	c.AutoCreated = auto != 0
	return &c, nil
}

type rowScanner interface{ Scan(...any) error }

func dbCreateContext(db *sql.DB, in contextCreateInput) (*ComputerContext, error) {
	if in.ID == "" {
		in.ID = newContextID()
	}
	in.Backend = normalizeBackend(in.Backend)
	in.Name = strings.TrimSpace(in.Name)
	if in.MetadataJSON == "" {
		in.MetadataJSON = "{}"
	}
	now := nowUTC()
	_, err := db.Exec(`
		INSERT INTO computer_contexts (
			id, name, backend, provider_context_id, persist_default,
			auto_created, metadata_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, in.Backend, in.ProviderContextID, boolInt(in.PersistDefault),
		boolInt(in.AutoCreated), in.MetadataJSON, now, now,
	)
	if err != nil {
		return nil, err
	}
	return dbGetContext(db, in.ID)
}

func dbGetContext(db *sql.DB, id string) (*ComputerContext, error) {
	row := db.QueryRow(`SELECT `+contextCols+` FROM computer_contexts WHERE id = ?`, id)
	c, err := scanComputerContext(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errContextNotFound
	}
	return c, err
}

func dbGetContextByName(db *sql.DB, backend, name string) (*ComputerContext, error) {
	row := db.QueryRow(`SELECT `+contextCols+` FROM computer_contexts WHERE backend = ? AND name = ?`,
		normalizeBackend(backend), strings.TrimSpace(name))
	c, err := scanComputerContext(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errContextNotFound
	}
	return c, err
}

func dbGetContextsByName(db *sql.DB, name string) ([]*ComputerContext, error) {
	rows, err := db.Query(`SELECT `+contextCols+` FROM computer_contexts WHERE name = ? ORDER BY updated_at DESC`,
		strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ComputerContext
	for rows.Next() {
		c, err := scanComputerContext(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbGetContextByProviderID(db *sql.DB, backend, providerID string) (*ComputerContext, error) {
	row := db.QueryRow(`SELECT `+contextCols+` FROM computer_contexts WHERE backend = ? AND provider_context_id = ?`,
		normalizeBackend(backend), providerID)
	c, err := scanComputerContext(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errContextNotFound
	}
	return c, err
}

func dbListContexts(db *sql.DB, backend string) ([]*ComputerContext, error) {
	q := `SELECT ` + contextCols + ` FROM computer_contexts WHERE 1=1`
	args := []any{}
	if backend != "" {
		q += ` AND backend = ?`
		args = append(args, normalizeBackend(backend))
	}
	q += ` ORDER BY last_used_at IS NULL, last_used_at DESC, updated_at DESC, name ASC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ComputerContext
	for rows.Next() {
		c, err := scanComputerContext(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbUpdateContext(db *sql.DB, id string, fields map[string]any) (*ComputerContext, error) {
	sets := []string{}
	args := []any{}
	for _, k := range []string{"name", "provider_context_id", "persist_default", "auto_created", "metadata_json", "last_used_at"} {
		v, ok := fields[k]
		if !ok {
			continue
		}
		sets = append(sets, k+" = ?")
		switch b := v.(type) {
		case bool:
			args = append(args, boolInt(b))
		default:
			args = append(args, v)
		}
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = ?")
		args = append(args, nowUTC(), id)
		_, err := db.Exec(`UPDATE computer_contexts SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			return nil, err
		}
	}
	return dbGetContext(db, id)
}

func dbDeleteContext(db *sql.DB, id string) error {
	res, err := db.Exec(`DELETE FROM computer_contexts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errContextNotFound
	}
	return nil
}

func dbTouchContext(db *sql.DB, id string) {
	if db == nil || id == "" {
		return
	}
	_, _ = db.Exec(`UPDATE computer_contexts SET last_used_at = ?, updated_at = ? WHERE id = ?`,
		nowUTC(), nowUTC(), id)
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
