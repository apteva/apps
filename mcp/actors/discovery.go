package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxActorDiscoveryBytes = 32 * 1024
const maxActorExportChunkBytes = 12 * 1024

func exportHint(id int64, revision int, operation string) map[string]any {
	args := map[string]any{"id": id, "revision": revision}
	if operation != "" {
		args["operation"] = operation
	}
	return map[string]any{"tool": "actors_definition_export", "arguments": args, "purpose": "Explicit editing/debugging export; base64 chunks must be assembled and hash-verified before use."}
}

func actorSummary(rec *actorRecord, latest int, after string, limit int) map[string]any {
	names := []string{}
	for name := range rec.Definition.Operations {
		names = append(names, name)
	}
	if len(names) == 0 {
		names = append(names, "run")
	}
	sort.Strings(names)
	start := sort.SearchStrings(names, after)
	if start < len(names) && names[start] == after {
		start++
	}
	end := min(start+limit, len(names))
	page := names[start:end]
	next := ""
	if end < len(names) && len(page) > 0 {
		next = page[len(page)-1]
	}
	steps := len(rec.Definition.Steps)
	for _, op := range rec.Definition.Operations {
		steps += len(op.Steps)
	}
	routes, datasets := 0, 0
	if rec.Definition.Crawl != nil {
		routes = len(rec.Definition.Crawl.Routes)
		datasets = len(rec.Definition.Crawl.Datasets)
	}
	return map[string]any{"id": rec.ID, "name": rec.Name, "description": rec.Description, "enabled": rec.Enabled, "revision": rec.Revision, "retrieved_revision": rec.Revision, "latest_revision": latest, "created_at": rec.CreatedAt, "updated_at": rec.UpdatedAt, "schema_version": rec.Definition.SchemaVersion, "operation_names": page, "operations": page, "operation_count": len(names), "operations_complete": end == len(names), "next_operation_after": next, "step_count": steps, "host_count": len(rec.Definition.AllowedHosts), "route_count": routes, "dataset_count": datasets}
}

func (a *App) discoverActor(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rec, err := getActorDiscoveryMetadata(ctx, int64ArgLocal(args, "id"), stringArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return map[string]any{"found": false, "actor": nil}, nil
	}
	latest := rec.Revision
	revision, err := requestedActorRevision(args, latest)
	if err != nil {
		return nil, err
	}
	raw, err := immutableActorJSON(ctx, rec.ID, revision)
	if err != nil {
		return nil, err
	}
	rec.Definition = actorDefinition{}
	if err = json.Unmarshal(raw, &rec.Definition); err != nil {
		return nil, err
	}
	rec.Revision = revision
	summary := actorSummary(rec, latest, stringArg(args, "operation_after"), boundedInt(intArg(args, "operation_limit"), 100, 1, 100))
	out := map[string]any{"found": true, "actor": summary, "retrieved_revision": revision, "latest_revision": latest, "operations": summary["operations"], "complete": true, "definition_included": false, "operations_complete": summary["operations_complete"], "next_operation_after": summary["next_operation_after"], "retrieve_operation": map[string]any{"tool": "actors_get", "arguments": map[string]any{"id": rec.ID, "revision": revision, "operation": "<operation_name>"}}, "definition_export": exportHint(rec.ID, revision, "")}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(b) > maxActorDiscoveryBytes {
		return nil, errors.New("incomplete-response: actor discovery exceeds response budget; reduce operation_limit")
	}
	return out, nil
}

func getActorDiscoveryMetadata(ctx *sdk.AppCtx, id int64, name string) (*actorRecord, error) {
	query := `SELECT id,name,COALESCE(description,''),enabled,revision,created_at,updated_at FROM actors_definitions WHERE project_id=? AND id=?`
	var lookup any = id
	if id <= 0 {
		query = `SELECT id,name,COALESCE(description,''),enabled,revision,created_at,updated_at FROM actors_definitions WHERE project_id=? AND name=?`
		lookup = strings.TrimSpace(name)
	}
	rec := &actorRecord{}
	var enabled int
	var created, updated time.Time
	err := ctx.AppDB().QueryRow(query, projectID(ctx), lookup).Scan(&rec.ID, &rec.Name, &rec.Description, &enabled, &rec.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec.Enabled = enabled != 0
	rec.CreatedAt = created.UTC().Format(time.RFC3339)
	rec.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return rec, nil
}

func requestedActorRevision(args map[string]any, latest int) (int, error) {
	if raw, ok := args["revision"]; ok {
		n := intArg(args, "revision")
		if n <= 0 || fmt.Sprint(raw) != fmt.Sprint(n) {
			return 0, errors.New("revision must be a positive integer")
		}
		return n, nil
	}
	return latest, nil
}
func immutableActorJSON(ctx *sdk.AppCtx, id int64, revision int) ([]byte, error) {
	var raw string
	err := ctx.AppDB().QueryRow(`SELECT definition_json FROM actors_versions WHERE project_id=? AND actor_id=? AND revision=?`, projectID(ctx), id, revision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("actor %d revision %d unavailable", id, revision)
	}
	return []byte(raw), err
}
func (a *App) discoverActors(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := boundedInt(intArg(args, "limit"), 20, 1, 50)
	after := int64ArgLocal(args, "after")
	if after < 0 {
		return nil, errors.New("after must be a nonnegative actor ID")
	}
	query := `SELECT id,name,COALESCE(description,''),enabled,revision,definition_json,created_at,updated_at FROM actors_definitions WHERE project_id=? AND id>?`
	params := []any{projectID(ctx), after}
	if _, ok := args["enabled"]; ok {
		query += ` AND enabled=?`
		params = append(params, boolArgDefault(args, "enabled", false))
	}
	query += ` ORDER BY id LIMIT ?`
	params = append(params, limit+1)
	rows, err := ctx.AppDB().Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	more := false
	used := 2048
	next := after
	for rows.Next() {
		rec, err := scanActor(rows)
		if err != nil {
			return nil, err
		}
		item := actorSummary(rec, rec.Revision, "", 100)
		b, _ := json.Marshal(item)
		if len(items) >= limit || used+len(b) > maxActorDiscoveryBytes {
			more = true
			break
		}
		items = append(items, item)
		used += len(b) + 1
		next = rec.ID
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if more && len(items) == 0 {
		return nil, errors.New("incomplete-response: one actor summary exceeds response budget; retrieve it by ID with a smaller operation_limit")
	}
	return map[string]any{"actors": items, "count": len(items), "has_more": more, "next_cursor": next, "complete": !more, "definition_included": false, "instructions": "Use actors_get with id, revision and operation for one complete contract. Use actors_definition_export only for editing/debugging."}, nil
}

func (a *App) toolActorDefinitionExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, supplied := args["revision"]; !supplied {
		return nil, errors.New("explicit immutable revision required for definition export")
	}
	rec, err := getActorOperationIdentity(ctx, int64ArgLocal(args, "id"), stringArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, errors.New("actor not found")
	}
	revision, err := requestedActorRevision(args, rec.Revision)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if _, ok := args["operation"]; ok {
		contract, err := a.buildActorOperation(ctx, args)
		if err != nil {
			return nil, err
		}
		raw, err = json.Marshal(contract)
		if err != nil {
			return nil, err
		}
	} else {
		raw, err = immutableActorJSON(ctx, rec.ID, revision)
		if err != nil {
			return nil, err
		}
	}
	offset := intArg(args, "offset")
	if value, supplied := args["offset"]; supplied && fmt.Sprint(value) != fmt.Sprint(offset) {
		return nil, errors.New("offset must be an integer")
	}
	if offset < 0 || offset > len(raw) {
		return nil, errors.New("offset outside immutable export")
	}
	limit := boundedInt(intArg(args, "limit"), maxActorExportChunkBytes, 1, maxActorExportChunkBytes)
	end := min(offset+limit, len(raw))
	digest := sha256.Sum256(raw)
	return map[string]any{"actor": map[string]any{"id": rec.ID, "name": rec.Name}, "retrieved_revision": revision, "latest_revision": rec.Revision, "operation_name": stringArg(args, "operation"), "content_type": "application/json", "encoding": "base64", "content_base64": base64.StdEncoding.EncodeToString(raw[offset:end]), "offset": offset, "next_offset": end, "has_more": end < len(raw), "complete": offset == 0 && end == len(raw), "chunk_complete": true, "total_bytes": len(raw), "sha256": hex.EncodeToString(digest[:]), "instructions": "Assemble every chunk for this exact revision and verify total_bytes and sha256; no chunk alone is an executable definition."}, nil
}

// The editor explicitly downloads an immutable definition. This route is never
// used by discovery/MCP and does not inject the JSON into an agent conversation.
func (a *App) handleActorDefinitionFile(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, 503, err.Error())
		return
	}
	id := actorHTTPID(r)
	rec, err := getActorOperationIdentity(ctx, id, "")
	if err != nil || rec == nil {
		httpErr(w, 404, "actor not found")
		return
	}
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || revision <= 0 {
		httpErr(w, 400, "explicit positive revision required")
		return
	}
	raw, err := immutableActorJSON(ctx, id, revision)
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	digest := sha256.Sum256(raw)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="actor-%d-revision-%d.json"`, id, revision))
	w.Header().Set("X-Actor-Revision", strconv.Itoa(revision))
	w.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Write(raw)
}
