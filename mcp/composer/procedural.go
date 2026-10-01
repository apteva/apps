package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	procedureSchemaVersion  = "composer-procedure/v1"
	maxProcedureFileBytes   = 512 * 1024
	maxProcedureSourceBytes = 2 * 1024 * 1024
)

type ProcedureRecord struct {
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	LatestRevision int    `json:"latest_revision"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type ProcedureRevisionRecord struct {
	ProcedureID int64             `json:"procedure_id"`
	Revision    int               `json:"revision"`
	Runtime     string            `json:"runtime"`
	Entrypoint  string            `json:"entrypoint"`
	Target      string            `json:"target"`
	OutputKind  string            `json:"output_kind"`
	Manifest    map[string]any    `json:"manifest"`
	Files       map[string]string `json:"files,omitempty"`
	SourceHash  string            `json:"source_hash"`
	CreatedAt   string            `json:"created_at"`
}

func supportedProcedureRuntime(runtime string) bool {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "python-3.13-media", "bun-1-media", "web-canvas-1", "go-1.25-media":
		return true
	default:
		return false
	}
}

func normalizeProcedureSource(args map[string]any) (runtime, entrypoint, target, outputKind string, manifest map[string]any, files map[string]string, sourceHash string, err error) {
	runtime = strings.ToLower(strings.TrimSpace(strArg(args, "runtime", "")))
	entrypoint = strings.TrimSpace(strArg(args, "entrypoint", ""))
	target = strings.ToLower(strings.TrimSpace(strArg(args, "target", "clip")))
	outputKind = strings.ToLower(strings.TrimSpace(strArg(args, "output_kind", "video")))
	if !supportedProcedureRuntime(runtime) {
		err = fmt.Errorf("unsupported runtime %q", runtime)
		return
	}
	if target != "clip" && target != "audio" && target != "still" && target != "composition" {
		err = errors.New("target must be clip|audio|still|composition")
		return
	}
	if outputKind != "video" && outputKind != "image" && outputKind != "audio" {
		err = errors.New("output_kind must be video|image|audio")
		return
	}
	if target == "audio" && outputKind != "audio" {
		err = errors.New("audio target requires output_kind audio")
		return
	}
	if target == "still" && outputKind != "image" {
		err = errors.New("still target requires output_kind image")
		return
	}
	manifest = mapArg(args, "manifest")
	if manifest == nil {
		manifest = map[string]any{}
	}
	manifest["schema"] = procedureSchemaVersion
	manifest["runtime"] = runtime
	manifest["entrypoint"] = entrypoint
	manifest["target"] = target
	manifest["output_kind"] = outputKind
	files, err = procedureFilesArg(args["files"])
	if err != nil {
		return
	}
	if len(files) == 0 {
		if source, ok := args["source"].(string); ok && source != "" {
			if entrypoint == "" {
				entrypoint = defaultProcedureEntrypoint(runtime)
				manifest["entrypoint"] = entrypoint
			}
			files = map[string]string{entrypoint: source}
		}
	}
	if entrypoint == "" {
		err = errors.New("entrypoint required")
		return
	}
	entrypoint, err = safeProcedurePath(entrypoint)
	if err != nil {
		err = fmt.Errorf("entrypoint: %w", err)
		return
	}
	if _, ok := files[entrypoint]; !ok {
		err = fmt.Errorf("entrypoint %q is not present in files", entrypoint)
		return
	}
	stable := map[string]any{"runtime": runtime, "entrypoint": entrypoint, "target": target, "output_kind": outputKind, "manifest": manifest, "files": files}
	b, _ := json.Marshal(stable)
	sum := sha256.Sum256(b)
	sourceHash = hex.EncodeToString(sum[:])
	return
}

func defaultProcedureEntrypoint(runtime string) string {
	switch runtime {
	case "python-3.13-media":
		return "render.py"
	case "go-1.25-media":
		return "main.go"
	case "web-canvas-1":
		return "index.html"
	default:
		return "render.ts"
	}
}

func safeProcedurePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("must be a non-empty relative path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "" {
			return "", errors.New("must not contain empty or parent segments")
		}
	}
	clean := path.Clean(value)
	if clean == "." || strings.HasPrefix(clean, "../") {
		return "", errors.New("escapes source root")
	}
	return clean, nil
}

func procedureFilesArg(raw any) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	var values map[string]any
	switch value := raw.(type) {
	case map[string]any:
		values = value
	case map[string]string:
		values = make(map[string]any, len(value))
		for k, v := range value {
			values[k] = v
		}
	default:
		return nil, errors.New("files must be an object mapping relative paths to UTF-8 source")
	}
	out := make(map[string]string, len(values))
	total := 0
	for name, rawContent := range values {
		safe, err := safeProcedurePath(name)
		if err != nil {
			return nil, fmt.Errorf("file %q: %w", name, err)
		}
		content, ok := rawContent.(string)
		if !ok {
			return nil, fmt.Errorf("file %q content must be a string", name)
		}
		if len(content) > maxProcedureFileBytes {
			return nil, fmt.Errorf("file %q exceeds %d bytes", name, maxProcedureFileBytes)
		}
		total += len(content)
		if total > maxProcedureSourceBytes {
			return nil, fmt.Errorf("procedure source exceeds %d bytes", maxProcedureSourceBytes)
		}
		out[safe] = content
	}
	return out, nil
}

func mapArg(args map[string]any, key string) map[string]any {
	if value, ok := args[key].(map[string]any); ok {
		return value
	}
	return nil
}

func (a *App) toolProcedureCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx)
	name := strings.TrimSpace(strArg(args, "name", ""))
	if pid == "" || name == "" {
		return nil, errors.New("project context and name required")
	}
	runtime, entrypoint, target, outputKind, manifest, files, sourceHash, err := normalizeProcedureSource(args)
	if err != nil {
		return nil, err
	}
	manifestJSON, _ := json.Marshal(manifest)
	filesJSON, _ := json.Marshal(files)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO procedures(project_id,name,description) VALUES(?,?,?)`, pid, name, strArg(args, "description", ""))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err = tx.Exec(`INSERT INTO procedure_revisions(procedure_id,revision,runtime,entrypoint,target,output_kind,manifest_json,files_json,source_hash) VALUES(?,1,?,?,?,?,?,?,?)`, id, runtime, entrypoint, target, outputKind, string(manifestJSON), string(filesJSON), sourceHash); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	procedure, revision, err := getProcedure(ctx.AppDB(), pid, id, 1)
	if err == nil {
		ctx.EmitWithProject("procedure.created", pid, map[string]any{"procedure_id": id, "revision": 1})
	}
	return map[string]any{"procedure": procedure, "revision": revision, "asset": procedureAssetExample(id, 1, outputKind)}, err
}

func (a *App) toolProcedureRevisionCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx)
	id := int64Arg(args, "id", 0)
	expected := intArg(args, "expected_revision", 0)
	if id <= 0 || expected <= 0 {
		return nil, errors.New("id and expected_revision required")
	}
	runtime, entrypoint, target, outputKind, manifest, files, sourceHash, err := normalizeProcedureSource(args)
	if err != nil {
		return nil, err
	}
	manifestJSON, _ := json.Marshal(manifest)
	filesJSON, _ := json.Marshal(files)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current int
	if err = tx.QueryRow(`SELECT latest_revision FROM procedures WHERE id=? AND project_id=?`, id, pid).Scan(&current); err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("procedure not found")
		}
		return nil, err
	}
	if current != expected {
		return nil, fmt.Errorf("procedure revision conflict: current %d, expected %d", current, expected)
	}
	next := current + 1
	if _, err = tx.Exec(`INSERT INTO procedure_revisions(procedure_id,revision,runtime,entrypoint,target,output_kind,manifest_json,files_json,source_hash) VALUES(?,?,?,?,?,?,?,?,?)`, id, next, runtime, entrypoint, target, outputKind, string(manifestJSON), string(filesJSON), sourceHash); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`UPDATE procedures SET latest_revision=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND latest_revision=?`, next, id, pid, expected)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("procedure revision conflict")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	procedure, revision, err := getProcedure(ctx.AppDB(), pid, id, next)
	if err == nil {
		ctx.EmitWithProject("procedure.revised", pid, map[string]any{"procedure_id": id, "revision": next})
	}
	return map[string]any{"procedure": procedure, "revision": revision}, err
}

func (a *App) toolProcedureGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	if id <= 0 {
		return nil, errors.New("id required")
	}
	procedure, revision, err := getProcedure(ctx.AppDB(), projectScope(ctx), id, intArg(args, "revision", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"procedure": procedure, "revision": revision, "asset": procedureAssetExample(id, revision.Revision, revision.OutputKind)}, nil
}

func (a *App) toolProcedureList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := intArg(args, "limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,name,description,latest_revision,created_at,updated_at FROM procedures WHERE project_id=? ORDER BY id DESC LIMIT ?`, projectScope(ctx), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	procedures := []ProcedureRecord{}
	for rows.Next() {
		var item ProcedureRecord
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Name, &item.Description, &item.LatestRevision, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		procedures = append(procedures, item)
	}
	return map[string]any{"procedures": procedures, "count": len(procedures)}, rows.Err()
}

func (a *App) toolProcedureValidate(_ *sdk.AppCtx, args map[string]any) (any, error) {
	runtime, entrypoint, target, outputKind, manifest, files, sourceHash, err := normalizeProcedureSource(args)
	if err != nil {
		return map[string]any{"valid": false, "errors": []string{err.Error()}}, nil
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return map[string]any{"valid": true, "schema": procedureSchemaVersion, "runtime": runtime, "entrypoint": entrypoint, "target": target, "output_kind": outputKind, "manifest": manifest, "files": paths, "source_hash": sourceHash}, nil
}

func getProcedure(db *sql.DB, pid string, id int64, revision int) (*ProcedureRecord, *ProcedureRevisionRecord, error) {
	var p ProcedureRecord
	if err := db.QueryRow(`SELECT id,project_id,name,description,latest_revision,created_at,updated_at FROM procedures WHERE id=? AND project_id=?`, id, pid).Scan(&p.ID, &p.ProjectID, &p.Name, &p.Description, &p.LatestRevision, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, errors.New("procedure not found")
		}
		return nil, nil, err
	}
	if revision <= 0 {
		revision = p.LatestRevision
	}
	var r ProcedureRevisionRecord
	var manifestJSON, filesJSON string
	if err := db.QueryRow(`SELECT procedure_id,revision,runtime,entrypoint,target,output_kind,manifest_json,files_json,source_hash,created_at FROM procedure_revisions WHERE procedure_id=? AND revision=?`, id, revision).Scan(&r.ProcedureID, &r.Revision, &r.Runtime, &r.Entrypoint, &r.Target, &r.OutputKind, &manifestJSON, &filesJSON, &r.SourceHash, &r.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, errors.New("procedure revision not found")
		}
		return nil, nil, err
	}
	_ = json.Unmarshal([]byte(manifestJSON), &r.Manifest)
	_ = json.Unmarshal([]byte(filesJSON), &r.Files)
	return &p, &r, nil
}

func procedureAssetExample(id int64, revision int, outputKind string) map[string]any {
	return map[string]any{"type": "procedural", "procedure": map[string]any{"procedure_id": id, "revision": revision, "target": "clip", "output_kind": outputKind, "parameters": map[string]any{}, "inputs": map[string]any{}}}
}

func validateProcedureReferences(db *sql.DB, projectID string, edit *Edit) []string {
	var issues []string
	if db == nil || edit == nil {
		return issues
	}
	for ti, track := range edit.Timeline.Tracks {
		for ci, clip := range track.Clips {
			binding := clip.Asset.Procedure
			if binding == nil {
				continue
			}
			_, revision, err := getProcedure(db, projectID, binding.ProcedureID, binding.Revision)
			if err != nil {
				issues = append(issues, fmt.Sprintf("track[%d].clip[%d]: procedure: %v", ti, ci, err))
				continue
			}
			if kind := strings.ToLower(strings.TrimSpace(binding.OutputKind)); kind != "" && kind != revision.OutputKind {
				issues = append(issues, fmt.Sprintf("track[%d].clip[%d]: procedure output_kind %q does not match revision %q", ti, ci, kind, revision.OutputKind))
			}
			if target := strings.ToLower(strings.TrimSpace(binding.Target)); target != "" && target != revision.Target {
				issues = append(issues, fmt.Sprintf("track[%d].clip[%d]: procedure target %q does not match revision %q", ti, ci, target, revision.Target))
			}
			declarations, _ := revision.Manifest["inputs"].(map[string]any)
			for name, raw := range declarations {
				declaration, _ := raw.(map[string]any)
				required, _ := declaration["required"].(bool)
				if required && binding.Inputs[name] == nil {
					issues = append(issues, fmt.Sprintf("track[%d].clip[%d]: required procedure input %q is not bound", ti, ci, name))
				}
			}
		}
	}
	return issues
}

func (a *App) handleProcedures(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	ctx := requestAppCtx(r)
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolProcedureList(ctx, map[string]any{"limit": 100})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResp(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProcedureSourceBytes+1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.toolProcedureCreate(ctx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResp(w, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleProcedureByID(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/procedure/"), "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid procedure id", http.StatusBadRequest)
		return
	}
	ctx := requestAppCtx(r)
	if r.Method == http.MethodGet && len(parts) == 1 {
		revision, _ := strconv.Atoi(r.URL.Query().Get("revision"))
		out, getErr := a.toolProcedureGet(ctx, map[string]any{"id": id, "revision": revision})
		if getErr != nil {
			http.Error(w, getErr.Error(), http.StatusNotFound)
			return
		}
		jsonResp(w, out)
		return
	}
	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "revisions" {
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProcedureSourceBytes+1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body["id"] = id
		out, createErr := a.toolProcedureRevisionCreate(ctx, body)
		if createErr != nil {
			http.Error(w, createErr.Error(), http.StatusBadRequest)
			return
		}
		jsonResp(w, out)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
