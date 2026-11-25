package main

// Basis (observed / inferred / synthetic), access classes and retention.
//
// Every row in the world model says what kind of knowledge it is. Synthetic
// rows exist for demos and tests: they are always labelled, and forecasts,
// the ledger and extraction never read them.
//
// Evidence carries an access class. The panel is an operator surface and
// sees everything. Agents calling MCP tools see open evidence, internal
// evidence unless a grant denies it, and restricted evidence only with an
// explicit allow grant; anything else comes back redacted (ids, source,
// times and access class stay, content does not). Restricted evidence is
// never sent to the LLM.

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	basisObserved  = "observed"
	basisInferred  = "inferred"
	basisSynthetic = "synthetic"

	accessOpen       = "open"
	accessInternal   = "internal"
	accessRestricted = "restricted"

	// readPermission is declared under provides.permissions in apteva.yaml,
	// against the access_class resource.
	readPermission = "intelligence.evidence.read"
)

func validBasis(b string) bool {
	return b == basisObserved || b == basisInferred || b == basisSynthetic
}

// basisRank orders bases by how much they are worth as knowledge, so a
// source confirming an inferred entity upgrades it and never the reverse.
func basisRank(b string) int {
	switch b {
	case basisObserved:
		return 2
	case basisInferred:
		return 1
	}
	return 0
}

func validAccess(a string) bool {
	return a == accessOpen || a == accessInternal || a == accessRestricted
}

func accessRank(a string) int {
	switch a {
	case accessRestricted:
		return 2
	case accessInternal:
		return 1
	}
	return 0
}

// stricterAccess returns the more restrictive of two classes; empty values
// are ignored.
func stricterAccess(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || accessRank(a) >= accessRank(b) {
		return a
	}
	return b
}

// sourceAccess is the access class of a registered source, or "" when the
// slug is not a registered source.
func sourceAccess(db *sql.DB, pid, slug string) string {
	var a string
	if err := db.QueryRow(`SELECT access FROM intel_sources WHERE project_id = ? AND slug = ?`, pid, slug).Scan(&a); err != nil {
		return ""
	}
	return a
}

// newsAccess classifies news by provider: GDELT is open data; bound news
// APIs deliver licensed content and stay internal.
func newsAccess(provider string) string {
	if provider == "gdelt" || provider == "" {
		return accessOpen
	}
	return accessInternal
}

// ─── Readers ───────────────────────────────────────────────────────

// reader is who is reading. operator = the panel (project UI); otherwise an
// MCP caller whose grants decide restricted access.
type reader struct {
	operator bool
	caller   *sdk.Caller
}

const readerArg = "_reader"

var operatorReader = reader{operator: true}

// readerFrom returns the reader injected by the transport. Arguments from
// an agent can never hold a reader value, so a forged "_reader" key reads as
// an unprivileged caller.
func readerFrom(args map[string]any) reader {
	if r, ok := args[readerArg].(reader); ok {
		return r
	}
	return reader{}
}

func (r reader) canRead(access string) bool {
	if r.operator {
		return true
	}
	switch access {
	case "", accessOpen:
		return true
	case accessInternal:
		return r.caller.Allows(readPermission, "access/"+accessInternal)
	case accessRestricted:
		// Fail closed: restricted needs an explicit allow, never the
		// default effect or a missing caller.
		if r.caller == nil {
			return false
		}
		allows := []sdk.Grant{}
		for _, g := range r.caller.Grants {
			if g.Effect == "allow" {
				allows = append(allows, g)
			}
		}
		explicitOnly := &sdk.Caller{Grants: allows, DefaultEffect: "deny", Resources: r.caller.Resources}
		return explicitOnly.Allows(readPermission, "access/"+accessRestricted) && r.caller.Allows(readPermission, "access/"+accessRestricted)
	}
	return false
}

// redactEvidence strips content from a row the reader may not see. It
// returns true when the row was redacted.
func (r reader) redactEvidence(row map[string]any) bool {
	access, _ := row["access"].(string)
	if r.canRead(access) {
		return false
	}
	row["title"] = "[" + access + " evidence]"
	row["body"] = ""
	row["payload"] = json.RawMessage(`{}`)
	row["source_ref"] = ""
	row["entity_refs"] = []string{}
	row["locations"] = nil
	row["redacted"] = true
	return true
}

// redactRows redacts rows in place and returns how many were redacted.
func (r reader) redactRows(rows []map[string]any) int {
	n := 0
	for _, row := range rows {
		if r.redactEvidence(row) {
			n++
		}
	}
	return n
}

// withReader adapts a tool handler so the MCP transport injects the
// caller's grants. The panel path (toolRoute) injects operatorReader.
func withReader(fn func(*sdk.AppCtx, map[string]any) (any, error)) sdk.ToolHandlerCtx {
	return func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
		if args == nil {
			args = map[string]any{}
		}
		args[readerArg] = reader{caller: sdk.CallerFrom(c)}
		return fn(app, args)
	}
}

// readerScopedTools return evidence content and so must see the caller.
var readerScopedTools = map[string]bool{
	"search": true, "timeline": true, "replay": true, "entity_get": true, "question_get": true,
	"world_map": true, "data_quality": true, "evidence_get": true,
}

func applyReaderScopes(tools []sdk.Tool) []sdk.Tool {
	for i := range tools {
		if readerScopedTools[tools[i].Name] && tools[i].Handler != nil {
			tools[i].HandlerCtx = withReader(tools[i].Handler)
			tools[i].Handler = nil
		}
	}
	return tools
}

// ─── Retention ─────────────────────────────────────────────────────

// applyRetention removes the content of evidence older than its source's
// retention period. Title, body and payload go; ids, source, times, access
// and the content hash stay, so forecast snapshots still verify and the
// record of what was known remains.
func applyRetention(db *sql.DB, pid string, now time.Time) (int64, error) {
	rows, err := db.Query(`SELECT slug, connector, retention_days FROM intel_sources WHERE project_id = ? AND retention_days > 0`, pid)
	if err != nil {
		return 0, err
	}
	type rule struct {
		slug, connector string
		days            int
	}
	var rules []rule
	for rows.Next() {
		var r rule
		if err := rows.Scan(&r.slug, &r.connector, &r.days); err != nil {
			rows.Close()
			return 0, err
		}
		rules = append(rules, r)
	}
	rows.Close()
	var total int64
	for _, r := range rules {
		cutoff := ts(now.Add(-time.Duration(r.days) * 24 * time.Hour))
		res, err := db.Exec(`UPDATE intelligence_evidence SET title = '[expired under retention]', body = '', payload = '{"retention":"expired"}', content_expired_at = ?
			WHERE project_id = ? AND content_expired_at IS NULL AND COALESCE(ingested_at, observed_at) < ?
			  AND (source = ? OR json_extract(payload, '$.connector') = ?)`, ts(now), pid, cutoff, r.slug, r.connector)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
