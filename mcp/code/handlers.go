package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// REST surface — mirror of the MCP tools, used by the SPA and curl.
// The path scheme is:
//
//   /api/repos                                  collection
//   /api/repos/<slug>                           one repo
//   /api/repos/<slug>/tree                      file tree
//   /api/repos/<slug>/files/<path>              read/write/delete one file
//   /api/repos/<slug>/edit                      POST {path, old, new, replace_all}
//   /api/repos/<slug>/multi-edit                POST {path, edits[]}
//   /api/repos/<slug>/move                      POST {from, to}
//   /api/repos/<slug>/grep                      POST {pattern, ...}
//   /api/repos/<slug>/glob                      POST {pattern}
//   /api/repos/<slug>/import                    POST multipart zip OR {url}
//   /api/repos/<slug>/export                    GET zip stream

func (a *App) handleReposCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListRepos(w, r)
	case http.MethodPost:
		a.httpCreateRepo(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleRepoItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/repos/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "slug required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	slug := parts[0]
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}

	switch {
	case tail == "":
		a.httpRepoMeta(w, r, slug)
	case tail == "tree":
		a.httpRepoTree(w, r, slug)
	case strings.HasPrefix(tail, "files/"):
		path := strings.TrimPrefix(tail, "files/")
		a.httpRepoFile(w, r, slug, path)
	case tail == "edit":
		a.httpRepoEdit(w, r, slug)
	case tail == "multi-edit":
		a.httpRepoMultiEdit(w, r, slug)
	case tail == "move":
		a.httpRepoMove(w, r, slug)
	case tail == "glob":
		a.httpRepoGlob(w, r, slug)
	case tail == "grep":
		a.httpRepoGrep(w, r, slug)
	case tail == "import":
		a.httpRepoImport(w, r, slug)
	case tail == "export":
		a.httpRepoExport(w, r, slug)
	case tail == "mark-template":
		a.httpRepoMarkTemplate(w, r, slug)
	case tail == "unmark-template":
		a.httpRepoUnmarkTemplate(w, r, slug)
	case tail == "fork":
		a.httpRepoFork(w, r, slug)
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

// ─── Collection ────────────────────────────────────────────────────

func (a *App) httpListRepos(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	includeArchived := r.URL.Query().Get("archived") == "1" || r.URL.Query().Get("archived") == "true"
	q := r.URL.Query().Get("q")
	repos, err := dbListRepos(globalCtx.AppDB(), pid, includeArchived, q)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"repositories": repos, "count": len(repos)})
}

func (a *App) httpCreateRepo(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Framework   string `json:"framework"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	repo, err := dbCreateRepo(globalCtx.AppDB(), pid, CreateRepoInput{
		Name:        body.Name,
		Slug:        body.Slug,
		Framework:   body.Framework,
		Description: body.Description,
	})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.store.CreateRepo(repo.Slug); err != nil {
		_ = dbHardDeleteRepo(globalCtx.AppDB(), pid, repo.Slug)
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	count, err := applyTemplate(a.store, repo.Slug, repo.Framework)
	if err != nil {
		globalCtx.Logger().Warn("template apply failed", "slug", repo.Slug, "err", err)
	}
	if count > 0 {
		_ = dbRecordImport(globalCtx.AppDB(), repo.ID, "template:"+repo.Framework)
	}
	if globalCtx != nil {
		globalCtx.Emit("repo.added", map[string]any{
			"id": repo.ID, "slug": repo.Slug, "name": repo.Name, "framework": repo.Framework,
		})
	}
	httpJSON(w, map[string]any{"repository": repo, "files_created": count})
}

// ─── One repo ─────────────────────────────────────────────────────

func (a *App) httpRepoMeta(w http.ResponseWriter, r *http.Request, slug string) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		repo, err := dbGetRepoBySlug(globalCtx.AppDB(), pid, slug)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if repo == nil {
			httpErr(w, http.StatusNotFound, "repo not found")
			return
		}
		files, _ := a.store.List(slug, "", true)
		size, _ := a.store.TotalSize(slug)
		httpJSON(w, map[string]any{
			"repository": repo, "file_count": len(files), "total_size": size,
		})
	case http.MethodPatch:
		var body struct {
			Name        *string `json:"name"`
			Description *string `json:"description"`
			BuildCmd    *string `json:"build_cmd"`
			StartCmd    *string `json:"start_cmd"`
			Port        *int    `json:"port"`
			EnvJSON     *string `json:"env_json"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Name != nil || body.Description != nil {
			if _, err := dbPatchRepo(globalCtx.AppDB(), pid, slug, body.Name, body.Description); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if body.BuildCmd != nil || body.StartCmd != nil || body.Port != nil || body.EnvJSON != nil {
			h := DeployHints{BuildCmd: body.BuildCmd, StartCmd: body.StartCmd, Port: body.Port, EnvJSON: body.EnvJSON}
			if _, err := dbSetDeployHints(globalCtx.AppDB(), pid, slug, h); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		repo, _ := dbGetRepoBySlug(globalCtx.AppDB(), pid, slug)
		httpJSON(w, map[string]any{"repository": repo})
	case http.MethodDelete:
		force := r.URL.Query().Get("force") == "1"
		if force {
			_ = dbHardDeleteRepo(globalCtx.AppDB(), pid, slug)
			_ = a.store.DropRepo(slug)
			if globalCtx != nil {
				globalCtx.Emit("repo.deleted", map[string]any{"slug": slug})
			}
			httpJSON(w, map[string]any{"slug": slug, "deleted": true})
			return
		}
		_ = dbArchiveRepo(globalCtx.AppDB(), pid, slug)
		if globalCtx != nil {
			globalCtx.Emit("repo.archived", map[string]any{"slug": slug})
		}
		httpJSON(w, map[string]any{"slug": slug, "archived": true})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH, or DELETE")
	}
}

// ─── Tree ─────────────────────────────────────────────────────────

func (a *App) httpRepoTree(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := requireRepoSlug(globalCtx, pid, slug); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	files, err := a.store.List(slug, "", true)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"files": files, "count": len(files)})
}

// ─── Per-file ─────────────────────────────────────────────────────

func (a *App) httpRepoFile(w http.ResponseWriter, r *http.Request, slug, rawPath string) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := requireRepoSlug(globalCtx, pid, slug); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	rel, err := normalisePath(rawPath)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		body, err := a.store.Read(slug, rel)
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		// Lines + line numbers if ?annotated=1, else raw bytes.
		if r.URL.Query().Get("annotated") == "1" {
			res, err := readWithLineNumbers(a.store, slug, rel,
				atoiOr(r.URL.Query().Get("offset"), 0),
				atoiOr(r.URL.Query().Get("limit"), 0))
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, res)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		meta, err := a.store.Write(slug, rel, body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		emitFileChange(globalCtx, "file.changed", slug, rel)
		httpJSON(w, map[string]any{"file": meta})

	case http.MethodDelete:
		if err := a.store.DeleteTree(slug, rel); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		emitFileChange(globalCtx, "file.deleted", slug, rel)
		httpJSON(w, map[string]any{"path": rel, "deleted": true})

	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PUT, or DELETE")
	}
}

// ─── Edit / multi-edit ────────────────────────────────────────────

func (a *App) httpRepoEdit(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := requireRepoSlug(globalCtx, pid, slug); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	rel, err := normalisePath(body.Path)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := editFile(a.store, slug, rel, body.OldString, body.NewString, body.ReplaceAll)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitFileChange(globalCtx, "file.changed", slug, rel)
	httpJSON(w, res)
}

func (a *App) httpRepoMultiEdit(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := requireRepoSlug(globalCtx, pid, slug); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Path  string   `json:"path"`
		Edits []EditOp `json:"edits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	rel, err := normalisePath(body.Path)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := multiEditFile(a.store, slug, rel, body.Edits)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitFileChange(globalCtx, "file.changed", slug, rel)
	httpJSON(w, res)
}

// ─── Move / glob / grep ───────────────────────────────────────────

func (a *App) httpRepoMove(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := requireRepoSlug(globalCtx, pid, slug); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct{ From, To string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	from, err := normalisePath(body.From)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "from: "+err.Error())
		return
	}
	to, err := normalisePath(body.To)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "to: "+err.Error())
		return
	}
	moved, err := a.store.Move(slug, from, to)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if globalCtx != nil {
		globalCtx.Emit("file.renamed", map[string]any{
			"slug": slug, "from": from, "to": to, "count": len(moved),
		})
	}
	httpJSON(w, map[string]any{"moved": moved, "count": len(moved)})
}

func (a *App) httpRepoGlob(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct{ Pattern string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	matches, err := globRepo(a.store, slug, body.Pattern)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"paths": matches, "count": len(matches)})
}

func (a *App) httpRepoGrep(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Pattern     string `json:"pattern"`
		Path        string `json:"path"`
		FilePattern string `json:"file_pattern"`
		Regex       bool   `json:"regex"`
		IgnoreCase  bool   `json:"ignore_case"`
		Context     int    `json:"context"`
		Limit       int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	o := GrepOptions{
		Pattern:     body.Pattern,
		Path:        body.Path,
		FilePattern: body.FilePattern,
		Regex:       body.Regex,
		IgnoreCase:  body.IgnoreCase,
		Context:     body.Context,
		Limit:       body.Limit,
	}
	if o.Path != "" {
		clean, err := normalisePath(o.Path)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		o.Path = clean
	}
	matches, err := grepRepo(a.store, slug, o)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"matches": matches, "count": len(matches)})
}

// ─── Import / export ──────────────────────────────────────────────

func (a *App) httpRepoExport(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := requireRepoSlug(globalCtx, pid, slug); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := writeZip(w, a.store, slug); err != nil {
		// Headers already written; the client gets a truncated zip
		// which they'll catch via CRC. Log + move on.
		globalCtx.Logger().Warn("export zip", "slug", slug, "err", err)
	}
}

func (a *App) httpRepoImport(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := requireRepoSlug(globalCtx, pid, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		httpErr(w, http.StatusBadRequest, "not a valid zip")
		return
	}
	count, err := readZipInto(a.store, slug, zr)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = dbRecordImport(globalCtx.AppDB(), repo.ID, "zip")
	httpJSON(w, map[string]any{"files_imported": count})
}

// ─── Helpers ──────────────────────────────────────────────────────

func requireRepoSlug(_ any, pid, slug string) (*Repo, error) {
	if globalCtx == nil {
		return nil, errors.New("not mounted")
	}
	if slug == "" {
		return nil, errors.New("slug required")
	}
	r, err := dbGetRepoBySlug(globalCtx.AppDB(), pid, slug)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("repository %q not found", slug)
	}
	return r, nil
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// ─── Templates / fork ─────────────────────────────────────────────

func (a *App) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	includeEmbedded := true
	if v := r.URL.Query().Get("include_embedded"); v == "0" || v == "false" {
		includeEmbedded = false
	}
	out := []TemplateEntry{}
	repos, err := dbListUserTemplates(globalCtx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, repo := range repos {
		files, _ := a.store.List(repo.Slug, "", true)
		out = append(out, TemplateEntry{
			Kind: "user", Name: repo.Name, Slug: repo.Slug,
			Tagline: repo.TemplateTagline, Icon: repo.TemplateIcon,
			Scope: repo.TemplateScope, FileCount: len(files),
			ProjectID: repo.ProjectID,
		})
	}
	if includeEmbedded {
		for _, name := range embeddedTemplateNames() {
			paths, _ := embeddedReader{}.ListPaths(name)
			out = append(out, TemplateEntry{
				Kind: "embedded", Name: name, Slug: name,
				Tagline: "Built-in " + name + " starter", FileCount: len(paths),
			})
		}
	}
	httpJSON(w, map[string]any{"templates": out, "count": len(out)})
}

func (a *App) httpRepoMarkTemplate(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Scope   string `json:"scope"`
		Tagline string `json:"tagline"`
		Icon    string `json:"icon"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	repo, err := dbSetTemplate(globalCtx.AppDB(), pid, slug, true, body.Scope, body.Tagline, body.Icon)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if globalCtx != nil {
		globalCtx.Emit("template.marked", map[string]any{"slug": slug, "scope": repo.TemplateScope})
	}
	httpJSON(w, map[string]any{"repository": repo})
}

func (a *App) httpRepoUnmarkTemplate(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := dbSetTemplate(globalCtx.AppDB(), pid, slug, false, "", "", "")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if globalCtx != nil {
		globalCtx.Emit("template.unmarked", map[string]any{"slug": slug})
	}
	httpJSON(w, map[string]any{"repository": repo})
}

func (a *App) httpRepoFork(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == "" {
		httpErr(w, http.StatusBadRequest, "name required")
		return
	}
	res, err := a.toolReposFork(globalCtx, map[string]any{
		"name":        body.Name,
		"slug":        body.Slug,
		"description": body.Description,
		"from_slug":   slug,
		"_project_id": pid,
	})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, res)
}

// ─── GitHub import (panel-driven) ─────────────────────────────────

// POST /api/github/import — body: {owner, repo, ref?, slug?, framework?}
//
// Same logic as the repos_import_github MCP tool. Surfaced as a REST
// route so the panel can call it without going through the agent's
// MCP gateway.
func (a *App) handleGithubImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Owner     string `json:"owner"`
		Repo      string `json:"repo"`
		Ref       string `json:"ref"`
		Slug      string `json:"slug"`
		Framework string `json:"framework"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	res, err := importGitHub(globalCtx, a.store, importGitHubInput{
		Owner:     body.Owner,
		Repo:      body.Repo,
		Ref:       body.Ref,
		Slug:      body.Slug,
		Framework: body.Framework,
		ProjectID: pid,
	})
	if err != nil {
		// Map a known set of error strings to status codes the panel
		// can branch on. Unmatched falls through to 500.
		msg := err.Error()
		switch {
		case strings.Contains(msg, "github not connected"):
			httpErr(w, http.StatusFailedDependency, msg)
		case strings.Contains(msg, "owner and repo are required"):
			httpErr(w, http.StatusBadRequest, msg)
		case strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "duplicate"):
			httpErr(w, http.StatusConflict, msg)
		default:
			httpErr(w, http.StatusInternalServerError, msg)
		}
		return
	}
	httpJSON(w, map[string]any{
		"repository":    res.Repository,
		"file_count":    res.FileCount,
		"bytes_written": res.BytesWritten,
		"source_url":    res.SourceURL,
		"ref":           res.Ref,
	})
}

// GET /api/github/repos — calls list_repos via the bound github
// integration so the panel's import dialog can render a picker. Pass
// query params through (sort, per_page, etc.) so panel-side filters
// reach GitHub directly.
func (a *App) handleGithubReposList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	bound := globalCtx.IntegrationFor("github")
	if bound == nil || bound.ConnectionID == 0 {
		httpErr(w, http.StatusFailedDependency, "github not connected: bind a github connection on this install first")
		return
	}
	input := map[string]any{}
	if v := r.URL.Query().Get("sort"); v != "" {
		input["sort"] = v
	}
	if v := r.URL.Query().Get("per_page"); v != "" {
		input["per_page"] = v
	} else {
		input["per_page"] = "100"
	}
	if v := r.URL.Query().Get("page"); v != "" {
		input["page"] = v
	}
	res, err := globalCtx.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID,
		bound.ToolFor("list_repos"),
		input,
	)
	if err != nil {
		httpErr(w, http.StatusBadGateway, "list_repos: "+err.Error())
		return
	}
	if !res.Success {
		httpErr(w, http.StatusBadGateway, fmt.Sprintf("github list_repos status=%d", res.Status))
		return
	}
	// Pass the upstream payload through verbatim — a JSON array of
	// repo objects with the fields the panel needs (name, full_name,
	// default_branch, language, private, pushed_at, …).
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Data)
}
