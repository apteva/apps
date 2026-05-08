package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Instance is the on-the-wire shape of an instances row. Every
// consumer (Live Link, Deploy, Backup, future Containers) sees this
// shape via `instance_get` / `instance_list`.
type Instance struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Provider         string `json:"provider"`           // 'local' | 'hetzner' | future
	ProviderID       string `json:"provider_id,omitempty"`
	PublicIPv4       string `json:"public_ipv4,omitempty"`
	PublicIPv6       string `json:"public_ipv6,omitempty"`
	Status           string `json:"status"`             // pending|provisioning|ready|error|destroyed
	Region           string `json:"region,omitempty"`
	Size             string `json:"size,omitempty"`
	Image            string `json:"image,omitempty"`
	SSHUser          string `json:"ssh_user,omitempty"`
	// SSH keys are kept server-side only — never returned to MCP /
	// REST callers. Cleared in API responses by stripSecrets().
	SSHPrivateKey    string `json:"-"`
	SSHPublicKey     string `json:"ssh_public_key,omitempty"`
	TagsJSON         string `json:"tags_json,omitempty"`
	MonthlyCostCents int    `json:"monthly_cost_cents"`
	ErrorMessage     string `json:"error,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	ReadyAt          string `json:"ready_at,omitempty"`
	DestroyedAt      string `json:"destroyed_at,omitempty"`
}

// IsLocal returns true for the built-in localhost instance (id=0,
// provider=local). Used by the run/upload paths to switch between
// in-process exec and SSH.
func (i *Instance) IsLocal() bool { return i.Provider == "local" }

// stripSecrets clones the instance with the private key zeroed.
// Every API path returns the stripped form; only internal helpers
// (sshExec, scpUpload) read the unstripped row directly from the DB.
func (i *Instance) stripSecrets() *Instance {
	c := *i
	c.SSHPrivateKey = ""
	return &c
}

// ─── Inputs ────────────────────────────────────────────────────────

type CreateInstanceInput struct {
	Name             string
	Provider         string
	ProviderID       string
	PublicIPv4       string
	PublicIPv6       string
	Status           string
	Region           string
	Size             string
	Image            string
	SSHUser          string
	SSHPrivateKey    string
	SSHPublicKey     string
	TagsJSON         string
	MonthlyCostCents int
}

// ─── Errors ────────────────────────────────────────────────────────

var ErrInstanceNotFound = errors.New("instance not found")
var ErrLocalInstanceImmutable = errors.New("local instance (id 0) cannot be created or destroyed")

// ─── Local seed ────────────────────────────────────────────────────

// ensureLocalInstance inserts the id=0 localhost row if it isn't
// there. Called from OnMount — idempotent thanks to INSERT OR IGNORE.
// 127.0.0.1 is always reachable; provider='local' tells the
// run/upload paths to take the in-process shortcut.
func ensureLocalInstance(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO instances (
			id, name, provider, provider_id, public_ipv4, status, ssh_user, created_at, ready_at
		) VALUES (
			0, 'localhost', 'local', '', '127.0.0.1', 'ready', '', ?, ?
		)
	`, nowUTC(), nowUTC())
	return err
}

// ─── DB ops ────────────────────────────────────────────────────────

const instanceCols = `id, name, provider, provider_id, public_ipv4, public_ipv6,
		status, region, size, image, ssh_user, ssh_private_key, ssh_public_key,
		tags_json, monthly_cost_cents, error_message,
		COALESCE(created_at,''), COALESCE(ready_at,''), COALESCE(destroyed_at,'')`

func scanInstance(s rowScanner) (*Instance, error) {
	var i Instance
	if err := s.Scan(&i.ID, &i.Name, &i.Provider, &i.ProviderID, &i.PublicIPv4, &i.PublicIPv6,
		&i.Status, &i.Region, &i.Size, &i.Image, &i.SSHUser, &i.SSHPrivateKey, &i.SSHPublicKey,
		&i.TagsJSON, &i.MonthlyCostCents, &i.ErrorMessage,
		&i.CreatedAt, &i.ReadyAt, &i.DestroyedAt,
	); err != nil {
		return nil, err
	}
	return &i, nil
}

type rowScanner interface{ Scan(...any) error }

func dbGetInstance(db *sql.DB, id int64) (*Instance, error) {
	row := db.QueryRow(`SELECT `+instanceCols+` FROM instances WHERE id = ?`, id)
	inst, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInstanceNotFound
	}
	return inst, err
}

// dbCreateInstance inserts a new instance row. id is auto-assigned
// (sqlite chooses the next free positive integer; id=0 is reserved
// for local). Returns the newly-created row with its assigned id.
func dbCreateInstance(db *sql.DB, in CreateInstanceInput) (*Instance, error) {
	if in.Provider == "local" {
		return nil, ErrLocalInstanceImmutable
	}
	if in.Status == "" {
		in.Status = "pending"
	}
	res, err := db.Exec(`
		INSERT INTO instances (
			name, provider, provider_id, public_ipv4, public_ipv6, status,
			region, size, image, ssh_user, ssh_private_key, ssh_public_key,
			tags_json, monthly_cost_cents, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Provider, in.ProviderID, in.PublicIPv4, in.PublicIPv6, in.Status,
		in.Region, in.Size, in.Image, in.SSHUser, in.SSHPrivateKey, in.SSHPublicKey,
		nullStr(in.TagsJSON, "[]"), in.MonthlyCostCents, nowUTC(),
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return dbGetInstance(db, id)
}

// dbUpdateInstance updates a fixed allowlist of columns. Mirror of
// the deploy app's dbUpdateBuild pattern — keeps writes predictable
// and prevents accidental shadowing of immutable columns.
func dbUpdateInstance(db *sql.DB, id int64, fields map[string]any) error {
	if id == 0 {
		return ErrLocalInstanceImmutable
	}
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{
		"status", "provider_id", "public_ipv4", "public_ipv6",
		"region", "size", "image", "ssh_user", "ssh_private_key",
		"ssh_public_key", "tags_json", "monthly_cost_cents",
		"error_message", "ready_at", "destroyed_at",
	} {
		if v, ok := fields[k]; ok {
			cols = append(cols, k+" = ?")
			args = append(args, v)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE instances SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

// dbDeleteInstance hard-removes the row. Caller is responsible for
// terminating the upstream resource first (provider-specific). Local
// instance is immutable.
func dbDeleteInstance(db *sql.DB, id int64) error {
	if id == 0 {
		return ErrLocalInstanceImmutable
	}
	_, err := db.Exec(`DELETE FROM instances WHERE id = ?`, id)
	return err
}

// dbListInstances returns every row, optionally filtered by
// provider or status. Sort: id ASC so localhost (id=0) renders
// first in the panel.
func dbListInstances(db *sql.DB, providerFilter, statusFilter string) ([]*Instance, error) {
	q := `SELECT ` + instanceCols + ` FROM instances WHERE 1=1`
	args := []any{}
	if providerFilter != "" {
		q += ` AND provider = ?`
		args = append(args, providerFilter)
	}
	if statusFilter != "" {
		q += ` AND status = ?`
		args = append(args, statusFilter)
	}
	q += ` ORDER BY id ASC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

// ─── helpers ───────────────────────────────────────────────────────

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func nullStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
