package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools wires every tool to a handler that resolves the project,
// validates args, and delegates to the store + edit engine.
func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "repos_list",
			Description: "List repositories in this project. Args: archived?, q?.",
			InputSchema: schemaObject(map[string]any{
				"archived": map[string]any{"type": "boolean"},
				"q":        map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolReposList,
		},
		{
			Name: "repos_create",
			Description: "Create a repository. Args: name (required), framework? (blank | nextjs | static | go | python), " +
				"description?, slug?. Walks the template tree into the new repo's storage_root.",
			InputSchema: schemaObject(map[string]any{
				"name":        map[string]any{"type": "string"},
				"framework":   map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"slug":        map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolReposCreate,
		},
		{
			Name:        "repos_get",
			Description: "Get repository metadata + file count and total size. Args: slug.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolReposGet,
		},
		{
			Name:        "repos_archive",
			Description: "Archive a repository (files retained). Pass force=true to hard-delete files and row.",
			InputSchema: schemaObject(map[string]any{
				"slug":  map[string]any{"type": "string"},
				"force": map[string]any{"type": "boolean"},
			}, []string{"slug"}),
			Handler: a.toolReposArchive,
		},
		{
			Name:        "repos_set_deploy_hints",
			Description: "Set build_cmd / start_cmd / port / env_json on a repo. Any field omitted is left unchanged.",
			InputSchema: schemaObject(map[string]any{
				"slug":      map[string]any{"type": "string"},
				"build_cmd": map[string]any{"type": "string"},
				"start_cmd": map[string]any{"type": "string"},
				"port":      map[string]any{"type": "integer"},
				"env_json":  map[string]any{"type": "string"},
			}, []string{"slug"}),
			Handler: a.toolReposSetDeployHints,
		},
		{
			Name: "repos_export",
			Description: "Export a repo as a zip archive. Returns {slug, sha256, size, zip_b64} where zip_b64 is the " +
				"base64-encoded zip bytes — used by the deploy app over PlatformAPI.CallApp.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"},
			}, []string{"slug"}),
			Handler: a.toolReposExport,
		},
		{
			Name:        "code_list_files",
			Description: "List files in a repo. Args: slug, path? (sub-tree prefix), recursive? (default true).",
			InputSchema: schemaObject(map[string]any{
				"slug":      map[string]any{"type": "string"},
				"path":      map[string]any{"type": "string"},
				"recursive": map[string]any{"type": "boolean"},
			}, []string{"slug"}),
			Handler: a.toolListFiles,
		},
		{
			Name:        "code_glob",
			Description: `Find files by glob (e.g. "**/*.tsx", "app/**/*.ts"). Args: slug, pattern.`,
			InputSchema: schemaObject(map[string]any{
				"slug":    map[string]any{"type": "string"},
				"pattern": map[string]any{"type": "string"},
			}, []string{"slug", "pattern"}),
			Handler: a.toolGlob,
		},
		{
			Name: "code_grep",
			Description: "Search file contents. Args: slug, pattern, regex? (default false), path?, file_pattern?, " +
				"context? (lines of before/after), ignore_case?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"slug":         map[string]any{"type": "string"},
				"pattern":      map[string]any{"type": "string"},
				"regex":        map[string]any{"type": "boolean"},
				"path":         map[string]any{"type": "string"},
				"file_pattern": map[string]any{"type": "string"},
				"context":      map[string]any{"type": "integer"},
				"ignore_case":  map[string]any{"type": "boolean"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"slug", "pattern"}),
			Handler: a.toolGrep,
		},
		{
			Name: "code_read_file",
			Description: "Read a file with cat -n line numbers prefixed. Args: slug, path, offset? (1-indexed), " +
				"limit? (default 2000).",
			InputSchema: schemaObject(map[string]any{
				"slug":   map[string]any{"type": "string"},
				"path":   map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			}, []string{"slug", "path"}),
			Handler: a.toolReadFile,
		},
		{
			Name:        "code_write_file",
			Description: "Write or overwrite a file with full content. Args: slug, path, content. Creates parent folders.",
			InputSchema: schemaObject(map[string]any{
				"slug":    map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			}, []string{"slug", "path", "content"}),
			Handler: a.toolWriteFile,
		},
		{
			Name: "code_edit_file",
			Description: "Exact-string replacement. Args: slug, path, old_string, new_string, replace_all? (default false). " +
				"Errors if old_string is not unique unless replace_all is set.",
			InputSchema: schemaObject(map[string]any{
				"slug":        map[string]any{"type": "string"},
				"path":        map[string]any{"type": "string"},
				"old_string":  map[string]any{"type": "string"},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			}, []string{"slug", "path", "old_string", "new_string"}),
			Handler: a.toolEditFile,
		},
		{
			Name: "code_multi_edit",
			Description: "Apply multiple edits to one file atomically. Args: slug, path, edits (array of " +
				"{old_string, new_string, replace_all?}). Each edit operates on the state after the previous one. " +
				"If any edit fails uniqueness, none are applied.",
			InputSchema: schemaObject(map[string]any{
				"slug":  map[string]any{"type": "string"},
				"path":  map[string]any{"type": "string"},
				"edits": map[string]any{"type": "array"},
			}, []string{"slug", "path", "edits"}),
			Handler: a.toolMultiEdit,
		},
		{
			Name:        "code_rename_path",
			Description: "Move or rename a file or folder. Args: slug, from, to.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"},
				"from": map[string]any{"type": "string"},
				"to":   map[string]any{"type": "string"},
			}, []string{"slug", "from", "to"}),
			Handler: a.toolRename,
		},
		{
			Name:        "code_delete_file",
			Description: "Delete a file or folder. Args: slug, path.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"},
				"path": map[string]any{"type": "string"},
			}, []string{"slug", "path"}),
			Handler: a.toolDeleteFile,
		},
		{
			Name: "repos_mark_template",
			Description: "Mark a repo as a template so others can fork it. Args: slug, " +
				"scope? ('private' | 'project' | 'global', default 'private'), tagline?, icon?.",
			InputSchema: schemaObject(map[string]any{
				"slug":    map[string]any{"type": "string"},
				"scope":   map[string]any{"type": "string"},
				"tagline": map[string]any{"type": "string"},
				"icon":    map[string]any{"type": "string"},
			}, []string{"slug"}),
			Handler: a.toolMarkTemplate,
		},
		{
			Name:        "repos_unmark_template",
			Description: "Clear the template flag on a repo. Existing forks are unaffected.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolUnmarkTemplate,
		},
		{
			Name: "templates_list",
			Description: "List templates available in this project: user templates (private to project + " +
				"globally-shared) and the embedded system templates baked into the binary. " +
				"Args: include_embedded? (default true).",
			InputSchema: schemaObject(map[string]any{
				"include_embedded": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolTemplatesList,
		},
		{
			Name: "repos_fork",
			Description: "Create a new repo by copying every file from a source. Source is either a user " +
				"repo/template (from_slug) or an embedded template name (from_template). Args: name (required), " +
				"slug?, description?, from_slug? (mutually exclusive with from_template), from_template?.",
			InputSchema: schemaObject(map[string]any{
				"name":          map[string]any{"type": "string"},
				"slug":          map[string]any{"type": "string"},
				"description":   map[string]any{"type": "string"},
				"from_slug":     map[string]any{"type": "string"},
				"from_template": map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolReposFork,
		},
		{
			Name: "repos_import_github",
			Description: "Import a GitHub repository as a local code repo (snapshot via gzip tarball). " +
				"Requires the install to have a github connection bound to the 'github' role. " +
				"Args: owner (required), repo (required), ref? (default repo's HEAD), slug?, framework?.",
			InputSchema: schemaObject(map[string]any{
				"owner":     map[string]any{"type": "string"},
				"repo":      map[string]any{"type": "string"},
				"ref":       map[string]any{"type": "string"},
				"slug":      map[string]any{"type": "string"},
				"framework": map[string]any{"type": "string"},
			}, []string{"owner", "repo"}),
			Handler: a.toolReposImportGithub,
		},
		{
			Name: "repos_dev_start",
			Description: "Start a Replit-style dev process for a repo. Auto-detects framework " +
				"(nextjs / node / go / static) from the file tree, or accepts framework='blank' with run_cmd. " +
				"Spawns the framework's dev command (next dev / <pm> run dev / go run . / in-process FileServer) " +
				"with cwd set to the repo's storage_root, so edits via code_edit_file land directly where the " +
				"running framework's watcher sees them. Args: slug (required), framework?, run_cmd?, env_json?.",
			InputSchema: schemaObject(map[string]any{
				"slug":      map[string]any{"type": "string"},
				"framework": map[string]any{"type": "string"},
				"run_cmd":   map[string]any{"type": "string"},
				"env_json":  map[string]any{"type": "string"},
			}, []string{"slug"}),
			Handler: a.toolDevStart,
		},
		{
			Name:        "repos_dev_stop",
			Description: "Stop the dev process for a repo. SIGTERM the process group, then SIGKILL after 5s. Idempotent.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolDevStop,
		},
		{
			Name:        "repos_dev_status",
			Description: "Get the current dev run state for a repo (status, port, pid, framework, last error).",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolDevStatus,
		},
		{
			Name:        "repos_dev_logs",
			Description: "Tail the dev run's stdout/stderr log file. Args: slug, tail? (lines, default 200).",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"},
				"tail": map[string]any{"type": "integer"},
			}, []string{"slug"}),
			Handler: a.toolDevLogs,
		},
	}
}

// ─── repos_dev_* handlers ─────────────────────────────────────────

func (a *App) toolDevStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	repo, err := requireRepo(ctx, pid, slug)
	if err != nil {
		return nil, err
	}
	if a.dev == nil {
		return nil, errors.New("dev runtime not initialised")
	}
	dr, err := a.dev.startDevRun(ctx, startDevInput{
		ProjectID: pid,
		Repo:      repo,
		Framework: strArg(args, "framework"),
		RunCmd:    strArg(args, "run_cmd"),
		EnvJSON:   strArg(args, "env_json"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"dev_run": dr}, nil
}

func (a *App) toolDevStop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	repo, err := requireRepo(ctx, pid, slug)
	if err != nil {
		return nil, err
	}
	if a.dev == nil {
		return map[string]any{"stopped": true}, nil
	}
	if err := a.dev.stopDevRun(ctx, pid, repo.ID); err != nil {
		return nil, err
	}
	return map[string]any{"stopped": true}, nil
}

func (a *App) toolDevStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	repo, err := requireRepo(ctx, pid, slug)
	if err != nil {
		return nil, err
	}
	dr, err := dbGetDevRun(ctx.AppDB(), pid, repo.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"dev_run": dr}, nil
}

func (a *App) toolDevLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	repo, err := requireRepo(ctx, pid, slug)
	if err != nil {
		return nil, err
	}
	dr, err := dbGetDevRun(ctx.AppDB(), pid, repo.ID)
	if err != nil {
		return nil, err
	}
	if dr == nil || dr.LogPath == "" {
		return map[string]any{"log": "", "available": false}, nil
	}
	tail := intArg(args, "tail", 200)
	body, err := tailFile(dr.LogPath, tail)
	if err != nil {
		return nil, err
	}
	return map[string]any{"log": body, "available": true}, nil
}

// ─── repos_import_github handler ──────────────────────────────────

func (a *App) toolReposImportGithub(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	res, err := importGitHub(ctx, a.store, importGitHubInput{
		Owner:     strArg(args, "owner"),
		Repo:      strArg(args, "repo"),
		Ref:       strArg(args, "ref"),
		Slug:      strArg(args, "slug"),
		Framework: strArg(args, "framework"),
		ProjectID: pid,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"repository":    res.Repository,
		"file_count":    res.FileCount,
		"bytes_written": res.BytesWritten,
		"source_url":    res.SourceURL,
		"ref":           res.Ref,
	}, nil
}

// ─── Template / fork handlers ──────────────────────────────────────

func (a *App) toolMarkTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		return nil, errors.New("slug required")
	}
	r, err := dbSetTemplate(ctx.AppDB(), pid, slug, true,
		strArg(args, "scope"), strArg(args, "tagline"), strArg(args, "icon"))
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("template.marked", map[string]any{"slug": slug, "scope": r.TemplateScope})
	}
	return map[string]any{"repository": r}, nil
}

func (a *App) toolUnmarkTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		return nil, errors.New("slug required")
	}
	r, err := dbSetTemplate(ctx.AppDB(), pid, slug, false, "", "", "")
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("template.unmarked", map[string]any{"slug": slug})
	}
	return map[string]any{"repository": r}, nil
}

// TemplateEntry is the unified shape used by templates_list. `kind`
// distinguishes user templates (forkable via from_slug) from embedded
// ones (forkable via from_template).
type TemplateEntry struct {
	Kind      string `json:"kind"` // 'user' | 'embedded'
	Name      string `json:"name"`
	Slug      string `json:"slug"`              // user: repo slug; embedded: framework name
	Tagline   string `json:"tagline,omitempty"`
	Icon      string `json:"icon,omitempty"`
	Scope     string `json:"scope,omitempty"`   // user only
	FileCount int    `json:"file_count"`
	ProjectID string `json:"project_id,omitempty"` // user only
}

func (a *App) toolTemplatesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	includeEmbedded := true
	if v, ok := args["include_embedded"].(bool); ok {
		includeEmbedded = v
	}
	out := []TemplateEntry{}

	repos, err := dbListUserTemplates(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		files, _ := a.store.List(r.Slug, "", true)
		out = append(out, TemplateEntry{
			Kind: "user", Name: r.Name, Slug: r.Slug,
			Tagline: r.TemplateTagline, Icon: r.TemplateIcon,
			Scope: r.TemplateScope, FileCount: len(files),
			ProjectID: r.ProjectID,
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
	return map[string]any{"templates": out, "count": len(out)}, nil
}

func (a *App) toolReposFork(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	fromSlug := strArg(args, "from_slug")
	fromTemplate := strArg(args, "from_template")
	if (fromSlug == "") == (fromTemplate == "") {
		return nil, errors.New("exactly one of from_slug or from_template must be set")
	}

	// Resolve the source tree first so a missing source fails before we
	// allocate a new slug + disk root.
	var src treeReader
	var srcID, parentKind string
	if fromSlug != "" {
		parent, err := dbGetRepoBySlug(ctx.AppDB(), pid, fromSlug)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			// Fall back to globally-scoped templates from other projects —
			// the only cross-project read the fork path allows.
			gp, err := findGlobalTemplate(ctx.AppDB(), fromSlug)
			if err != nil {
				return nil, err
			}
			if gp == nil {
				return nil, fmt.Errorf("source repo %q not found in this project", fromSlug)
			}
			parent = gp
		}
		src = storeReader{s: a.store}
		srcID = parent.Slug
		parentKind = "user"
	} else {
		// embedded
		src = embeddedReader{}
		srcID = fromTemplate
		parentKind = "embedded"
	}

	in := CreateRepoInput{
		Name:        name,
		Slug:        strArg(args, "slug"),
		Description: strArg(args, "description"),
		Framework:   "blank", // forks always start as blank — files come from the source tree
	}
	r, err := dbCreateRepo(ctx.AppDB(), pid, in)
	if err != nil {
		return nil, err
	}
	if err := a.store.CreateRepo(r.Slug); err != nil {
		_ = dbHardDeleteRepo(ctx.AppDB(), pid, r.Slug)
		return nil, fmt.Errorf("create repo dir: %w", err)
	}
	count, err := fork(src, srcID, a.store, r.Slug)
	if err != nil {
		// Roll back so the user doesn't end up with a half-copied repo
		// that they then have to clean up manually.
		_ = a.store.DropRepo(r.Slug)
		_ = dbHardDeleteRepo(ctx.AppDB(), pid, r.Slug)
		return nil, fmt.Errorf("copy from %s: %w", parentKind, err)
	}
	_ = dbRecordFork(ctx.AppDB(), r.ID, srcID, parentKind)
	_ = dbRecordImport(ctx.AppDB(), r.ID, "fork:"+parentKind+":"+srcID)
	if ctx != nil {
		ctx.Emit("repo.added", map[string]any{
			"id": r.ID, "slug": r.Slug, "name": r.Name,
			"forked_from": map[string]any{"slug": srcID, "kind": parentKind},
		})
	}
	return map[string]any{"repository": r, "files_created": count,
		"forked_from": map[string]any{"slug": srcID, "kind": parentKind}}, nil
}

// findGlobalTemplate looks up a template by slug across all projects,
// returning it only if it's marked global. Used by repos_fork so users
// can fork shared templates without having to know which project owns
// them.
func findGlobalTemplate(db *sql.DB, slug string) (*Repo, error) {
	row := db.QueryRow(`SELECT `+repoColumns+` FROM repositories
		WHERE slug = ? AND is_template = 1 AND template_scope = 'global' AND archived_at IS NULL
		LIMIT 1`, slug)
	return scanRepoRow(row)
}

// ─── repos_* handlers ──────────────────────────────────────────────

func (a *App) toolReposList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	includeArchived := boolArg(args, "archived")
	q := strArg(args, "q")
	repos, err := dbListRepos(ctx.AppDB(), pid, includeArchived, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repositories": repos, "count": len(repos)}, nil
}

func (a *App) toolReposCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	in := CreateRepoInput{
		Name:        strArg(args, "name"),
		Slug:        strArg(args, "slug"),
		Description: strArg(args, "description"),
		Framework:   strArg(args, "framework"),
	}
	r, err := dbCreateRepo(ctx.AppDB(), pid, in)
	if err != nil {
		return nil, err
	}
	if err := a.store.CreateRepo(r.Slug); err != nil {
		// Roll back the row to keep DB and disk consistent.
		_ = dbHardDeleteRepo(ctx.AppDB(), pid, r.Slug)
		return nil, fmt.Errorf("create repo dir: %w", err)
	}
	count, err := applyTemplate(a.store, r.Slug, r.Framework)
	if err != nil {
		ctx.Logger().Warn("template apply failed", "slug", r.Slug, "framework", r.Framework, "err", err)
	}
	if count > 0 {
		_ = dbRecordImport(ctx.AppDB(), r.ID, "template:"+r.Framework)
	}
	if ctx != nil {
		ctx.Emit("repo.added", map[string]any{
			"id": r.ID, "slug": r.Slug, "name": r.Name, "framework": r.Framework,
		})
	}
	return map[string]any{"repository": r, "files_created": count}, nil
}

func (a *App) toolReposGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		return nil, errors.New("slug required")
	}
	r, err := dbGetRepoBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return map[string]any{"repository": nil, "found": false}, nil
	}
	files, _ := a.store.List(slug, "", true)
	totalSize, _ := a.store.TotalSize(slug)
	return map[string]any{
		"repository": r,
		"found":      true,
		"file_count": len(files),
		"total_size": totalSize,
	}, nil
}

func (a *App) toolReposArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		return nil, errors.New("slug required")
	}
	force := boolArg(args, "force")
	if force {
		if err := dbHardDeleteRepo(ctx.AppDB(), pid, slug); err != nil {
			return nil, err
		}
		if err := a.store.DropRepo(slug); err != nil {
			return nil, err
		}
		if ctx != nil {
			ctx.Emit("repo.deleted", map[string]any{"slug": slug})
		}
		return map[string]any{"slug": slug, "deleted": true}, nil
	}
	if err := dbArchiveRepo(ctx.AppDB(), pid, slug); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("repo.archived", map[string]any{"slug": slug})
	}
	return map[string]any{"slug": slug, "archived": true}, nil
}

func (a *App) toolReposSetDeployHints(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		return nil, errors.New("slug required")
	}
	h := DeployHints{}
	if v, ok := args["build_cmd"].(string); ok {
		h.BuildCmd = &v
	}
	if v, ok := args["start_cmd"].(string); ok {
		h.StartCmd = &v
	}
	if v, ok := args["port"].(float64); ok {
		p := int(v)
		h.Port = &p
	}
	if v, ok := args["env_json"].(string); ok {
		// Validate that it parses; we store the raw string but reject
		// obvious garbage so the deploy app doesn't have to.
		var probe any
		if err := json.Unmarshal([]byte(v), &probe); err != nil {
			return nil, fmt.Errorf("env_json is not valid JSON: %w", err)
		}
		h.EnvJSON = &v
	}
	r, err := dbSetDeployHints(ctx.AppDB(), pid, slug, h)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repository": r}, nil
}

func (a *App) toolReposExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		return nil, errors.New("slug required")
	}
	if _, err := requireRepoSlug(ctx, pid, slug); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := zipRepo(&buf, a.store, slug); err != nil {
		return nil, fmt.Errorf("zip repo: %w", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return map[string]any{
		"slug":    slug,
		"size":    buf.Len(),
		"sha256":  hex.EncodeToString(sum[:]),
		"zip_b64": base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// ─── code_* file handlers ──────────────────────────────────────────

func (a *App) toolListFiles(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	prefix := strArg(args, "path")
	if prefix != "" {
		clean, err := normalisePath(prefix)
		if err != nil {
			return nil, err
		}
		prefix = clean
	}
	recursive := true
	if v, ok := args["recursive"].(bool); ok {
		recursive = v
	}
	files, err := a.store.List(strArg(args, "slug"), prefix, recursive)
	if err != nil {
		return nil, err
	}
	return map[string]any{"files": files, "count": len(files)}, nil
}

func (a *App) toolGlob(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	pattern := strArg(args, "pattern")
	matches, err := globRepo(a.store, strArg(args, "slug"), pattern)
	if err != nil {
		return nil, err
	}
	return map[string]any{"paths": matches, "count": len(matches)}, nil
}

func (a *App) toolGrep(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	o := GrepOptions{
		Pattern:     strArg(args, "pattern"),
		Path:        strArg(args, "path"),
		FilePattern: strArg(args, "file_pattern"),
		Regex:       boolArg(args, "regex"),
		IgnoreCase:  boolArg(args, "ignore_case"),
		Context:     intArg(args, "context", 0),
		Limit:       intArg(args, "limit", 0),
	}
	if o.Path != "" {
		clean, err := normalisePath(o.Path)
		if err != nil {
			return nil, err
		}
		o.Path = clean
	}
	matches, err := grepRepo(a.store, strArg(args, "slug"), o)
	if err != nil {
		return nil, err
	}
	return map[string]any{"matches": matches, "count": len(matches)}, nil
}

func (a *App) toolReadFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	res, err := readWithLineNumbers(a.store, strArg(args, "slug"), rel,
		intArg(args, "offset", 0), intArg(args, "limit", 0))
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (a *App) toolWriteFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	content := strArg(args, "content")
	slug := strArg(args, "slug")
	meta, err := a.store.Write(slug, rel, []byte(content))
	if err != nil {
		return nil, err
	}
	emitFileChange(ctx, "file.changed", slug, rel)
	return map[string]any{"file": meta}, nil
}

// emitFileChange broadcasts a per-file mutation. Lightweight payload:
// the panel re-reads on every event anyway, so just send (slug, path).
func emitFileChange(ctx *sdk.AppCtx, topic, slug, path string) {
	if ctx == nil {
		return
	}
	ctx.Emit(topic, map[string]any{"slug": slug, "path": path})
}

func (a *App) toolEditFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	res, err := editFile(a.store, slug, rel,
		strArg(args, "old_string"), strArg(args, "new_string"), boolArg(args, "replace_all"))
	if err != nil {
		return nil, err
	}
	emitFileChange(ctx, "file.changed", slug, rel)
	return res, nil
}

func (a *App) toolMultiEdit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	rawEdits, ok := args["edits"].([]any)
	if !ok {
		return nil, errors.New("edits must be an array")
	}
	ops := make([]EditOp, 0, len(rawEdits))
	for i, raw := range rawEdits {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits[%d] must be an object", i)
		}
		op := EditOp{
			OldString: strArg(m, "old_string"),
			NewString: strArg(m, "new_string"),
		}
		if v, ok := m["replace_all"].(bool); ok {
			op.ReplaceAll = v
		}
		ops = append(ops, op)
	}
	slug := strArg(args, "slug")
	res, err := multiEditFile(a.store, slug, rel, ops)
	if err != nil {
		return nil, err
	}
	emitFileChange(ctx, "file.changed", slug, rel)
	return res, nil
}

func (a *App) toolRename(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	from, err := normalisePath(strArg(args, "from"))
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	to, err := normalisePath(strArg(args, "to"))
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	slug := strArg(args, "slug")
	moved, err := a.store.Move(slug, from, to)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("file.renamed", map[string]any{
			"slug": slug, "from": from, "to": to, "count": len(moved),
		})
	}
	return map[string]any{"moved": moved, "count": len(moved)}, nil
}

func (a *App) toolDeleteFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err := requireRepo(ctx, pid, strArg(args, "slug")); err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	// Delete a file or a tree — we don't know which without statting,
	// and DeleteTree handles both safely (RemoveAll on a single file
	// works, RemoveAll on a missing path is nil).
	slug := strArg(args, "slug")
	if err := a.store.DeleteTree(slug, rel); err != nil {
		return nil, err
	}
	emitFileChange(ctx, "file.deleted", slug, rel)
	return map[string]any{"path": rel, "deleted": true}, nil
}

// ─── Helpers ──────────────────────────────────────────────────────

func requireRepo(ctx *sdk.AppCtx, pid, slug string) (*Repo, error) {
	if slug == "" {
		return nil, errors.New("slug required")
	}
	r, err := dbGetRepoBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("repository %q not found in this project", slug)
	}
	return r, nil
}

// ─── Arg helpers (mirror storage's pattern) ───────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	return def
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
