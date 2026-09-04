package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools wires every tool to a handler that resolves the project,
// validates args, and delegates to the store + edit engine.
func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{
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
				"description?, slug?, workspace_image?. Walks the template tree into the new repo's storage_root.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"framework":       map[string]any{"type": "string"},
				"description":     map[string]any{"type": "string"},
				"slug":            map[string]any{"type": "string"},
				"workspace_image": map[string]any{"type": "string"},
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
			Name:        "repos_set_workspace_image",
			Description: "Set or clear the repository's preferred workspace image. Workspaces validates registry policy when the image is used. A changed image provisions a new linked workspace on the next workspace command only when the current workspace has no unapplied source changes; the prior workspace is retained. Args: slug, image (empty clears).",
			InputSchema: schemaObject(map[string]any{
				"slug":  map[string]any{"type": "string"},
				"image": map[string]any{"type": "string"},
			}, []string{"slug", "image"}),
			Handler: a.toolReposSetWorkspaceImage,
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
			Description: "List source files in a repo. Generated/vendor trees are skipped by default; pass include_generated=true to include them. Args: slug, path? (sub-tree prefix), recursive? (default true), include_generated?",
			InputSchema: schemaObject(map[string]any{
				"slug":              map[string]any{"type": "string"},
				"path":              map[string]any{"type": "string"},
				"recursive":         map[string]any{"type": "boolean"},
				"include_generated": map[string]any{"type": "boolean"},
			}, []string{"slug"}),
			Handler: a.toolListFiles,
		},
		{
			Name:        "code_glob",
			Description: `Find source files by glob (e.g. "**/*.tsx", "app/**/*.ts"). Generated/vendor trees are skipped by default. Args: slug, pattern, limit? (default 2000), include_generated?.`,
			InputSchema: schemaObject(map[string]any{
				"slug":              map[string]any{"type": "string"},
				"pattern":           map[string]any{"type": "string"},
				"limit":             map[string]any{"type": "integer"},
				"include_generated": map[string]any{"type": "boolean"},
			}, []string{"slug", "pattern"}),
			Handler: a.toolGlob,
		},
		{
			Name: "code_grep",
			Description: "Search file contents compactly. Defaults to output_mode=files and limit=50; use " +
				"output_mode=content only when matching lines are needed. Narrow with path, file_pattern, " +
				"matches_per_file, and small context. Generated/vendor trees are skipped unless include_generated=true.",
			InputSchema: schemaObject(map[string]any{
				"slug":              map[string]any{"type": "string"},
				"pattern":           map[string]any{"type": "string"},
				"regex":             map[string]any{"type": "boolean"},
				"path":              map[string]any{"type": "string"},
				"file_pattern":      map[string]any{"type": "string"},
				"output_mode":       map[string]any{"type": "string", "enum": []string{"files", "content", "count"}},
				"context":           map[string]any{"type": "integer"},
				"matches_per_file":  map[string]any{"type": "integer"},
				"ignore_case":       map[string]any{"type": "boolean"},
				"limit":             map[string]any{"type": "integer"},
				"include_generated": map[string]any{"type": "boolean"},
			}, []string{"slug", "pattern"}),
			Handler: a.toolGrep,
		},
		{
			Name: "code_read_file",
			Description: "Read a file with cat -n line numbers prefixed. Args: slug, path, offset? (1-indexed), " +
				"limit? (default 200, max 2000). Prefer small limits and use next_offset to continue.",
			InputSchema: schemaObject(map[string]any{
				"slug":   map[string]any{"type": "string"},
				"path":   map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			}, []string{"slug", "path"}),
			Handler: a.toolReadFile,
		},
		{
			Name: "code_read_excerpt",
			Description: "Read a targeted excerpt with line numbers instead of a whole file. Args: slug, path, " +
				"start_line?/end_line?, around?/before?/after?, tail?, limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"slug":       map[string]any{"type": "string"},
				"path":       map[string]any{"type": "string"},
				"start_line": map[string]any{"type": "integer"},
				"end_line":   map[string]any{"type": "integer"},
				"around":     map[string]any{"type": "integer"},
				"before":     map[string]any{"type": "integer"},
				"after":      map[string]any{"type": "integer"},
				"tail":       map[string]any{"type": "boolean"},
				"limit":      map[string]any{"type": "integer"},
			}, []string{"slug", "path"}),
			Handler: a.toolReadExcerpt,
		},
		{
			Name: "code_file_outline",
			Description: "Return a compact structural outline with line numbers for Markdown headings and common " +
				"code declarations. Use before reading large files. Args: slug, path, limit?.",
			InputSchema: schemaObject(map[string]any{
				"slug":  map[string]any{"type": "string"},
				"path":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, []string{"slug", "path"}),
			Handler: a.toolFileOutline,
		},
		{
			Name: "code_write_file",
			Description: "Write or overwrite a file with full content. Best for new files or simple overwrites; use " +
				"code_apply_patch for large existing-file rewrites. Args: slug, path, content.",
			InputSchema: schemaObject(map[string]any{
				"slug":    map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			}, []string{"slug", "path", "content"}),
			Handler: a.toolWriteFile,
		},
		{
			Name: "code_apply_patch",
			Description: "Apply a unified diff patch across one or more files. Use dry_run=true to preview. " +
				"Dry runs return patch_id; pass patch_id later to apply the exact reviewed patch without resending it. " +
				"Preferred for large existing-file rewrites instead of code_write_file full-content overwrites.",
			InputSchema: schemaObject(map[string]any{
				"slug":     map[string]any{"type": "string"},
				"patch":    map[string]any{"type": "string"},
				"patch_id": map[string]any{"type": "string"},
				"dry_run":  map[string]any{"type": "boolean"},
			}, []string{"slug"}),
			Handler: a.toolApplyPatch,
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
			Name: "issues_list",
			Description: "List native Code issues for a repo. Args: slug (required), state? " +
				"(open | closed | all; default open), status? (all | todo | triage | planned | in_progress | in_review | blocked | done), type?, priority?, assignee?, q?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"slug":     map[string]any{"type": "string"},
				"state":    map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string"},
				"type":     map[string]any{"type": "string"},
				"priority": map[string]any{"type": "string"},
				"assignee": map[string]any{"type": "string"},
				"q":        map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}, []string{"slug"}),
			Handler: a.toolIssuesList,
		},
		{
			Name: "issues_search",
			Description: "Search native Code issues across the current project, optionally scoped to one repo. Args: repo?, state? " +
				"(open | closed | all; default all), status? (all | todo | triage | planned | in_progress | in_review | blocked | done), type?, priority?, assignee?, q?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"repo":     map[string]any{"type": "string"},
				"state":    map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string"},
				"type":     map[string]any{"type": "string"},
				"priority": map[string]any{"type": "string"},
				"assignee": map[string]any{"type": "string"},
				"q":        map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolIssuesSearch,
		},
		{
			Name:        "issues_get",
			Description: "Get a native Code issue with comments, links, and activity. Args: slug, number.",
			InputSchema: schemaObject(map[string]any{
				"slug":   map[string]any{"type": "string"},
				"number": map[string]any{"type": "integer"},
			}, []string{"slug", "number"}),
			Handler: a.toolIssuesGet,
		},
		{
			Name: "issues_claim",
			Description: "Atomically claim an open Code issue before starting work. The claimant is derived from the authenticated agent or delegated user; it cannot be supplied in arguments. " +
				"If another caller already owns the claim, returns success=false with the current claim instead of replacing it. Args: slug, number.",
			InputSchema: schemaObject(map[string]any{
				"slug":   map[string]any{"type": "string"},
				"number": map[string]any{"type": "integer"},
			}, []string{"slug", "number"}),
			HandlerCtx: a.toolIssuesClaim,
		},
		{
			Name: "issues_release",
			Description: "Release the authenticated caller's claim on a Code issue. Does not release another caller's claim. " +
				"An in-progress issue returns to todo; closing an issue releases its claim automatically. Args: slug, number.",
			InputSchema: schemaObject(map[string]any{
				"slug":   map[string]any{"type": "string"},
				"number": map[string]any{"type": "integer"},
			}, []string{"slug", "number"}),
			HandlerCtx: a.toolIssuesRelease,
		},
		{
			Name: "issues_create",
			Description: "Create a native Code issue. Args: slug, title, body?, type? " +
				"(bug | feature | task | chore), priority? (low | medium | high | urgent), assignee?, created_by?, link_path?, line_start?, line_end?.",
			InputSchema: schemaObject(map[string]any{
				"slug":       map[string]any{"type": "string"},
				"title":      map[string]any{"type": "string"},
				"body":       map[string]any{"type": "string"},
				"type":       map[string]any{"type": "string"},
				"priority":   map[string]any{"type": "string"},
				"assignee":   map[string]any{"type": "string"},
				"created_by": map[string]any{"type": "string"},
				"link_path":  map[string]any{"type": "string"},
				"line_start": map[string]any{"type": "integer"},
				"line_end":   map[string]any{"type": "integer"},
			}, []string{"slug", "title"}),
			Handler: a.toolIssuesCreate,
		},
		{
			Name:        "issues_update",
			Description: "Update a native Code issue. Args: slug, number, title?, body?, type?, status? (workflow), state? (open|closed), state_reason?, priority?, assignee?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug":         map[string]any{"type": "string"},
				"number":       map[string]any{"type": "integer"},
				"title":        map[string]any{"type": "string"},
				"body":         map[string]any{"type": "string"},
				"type":         map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string"},
				"state":        map[string]any{"type": "string"},
				"state_reason": map[string]any{"type": "string"},
				"priority":     map[string]any{"type": "string"},
				"assignee":     map[string]any{"type": "string"},
				"actor":        map[string]any{"type": "string"},
			}, []string{"slug", "number"}),
			Handler: a.toolIssuesUpdate,
		},
		{
			Name:        "issues_comment",
			Description: "Add a comment to a native Code issue. Args: slug, number, body, author?.",
			InputSchema: schemaObject(map[string]any{
				"slug":   map[string]any{"type": "string"},
				"number": map[string]any{"type": "integer"},
				"body":   map[string]any{"type": "string"},
				"author": map[string]any{"type": "string"},
			}, []string{"slug", "number", "body"}),
			Handler: a.toolIssuesComment,
		},
		{
			Name:        "issues_close",
			Description: "Close a native Code issue. Args: slug, number, state_reason? (completed | not_planned; default completed), resolution?, actor?. Sets state=closed, status=done, and adds the resolution as a comment when provided.",
			InputSchema: schemaObject(map[string]any{
				"slug":         map[string]any{"type": "string"},
				"number":       map[string]any{"type": "integer"},
				"state_reason": map[string]any{"type": "string"},
				"resolution":   map[string]any{"type": "string"},
				"actor":        map[string]any{"type": "string"},
			}, []string{"slug", "number"}),
			Handler: a.toolIssuesClose,
		},
		{
			Name:        "issues_reopen",
			Description: "Reopen a native Code issue. Args: slug, number, actor?. Sets state=open, clears state_reason, and moves workflow status to todo.",
			InputSchema: schemaObject(map[string]any{
				"slug":   map[string]any{"type": "string"},
				"number": map[string]any{"type": "integer"},
				"actor":  map[string]any{"type": "string"},
			}, []string{"slug", "number"}),
			Handler: a.toolIssuesReopen,
		},
		{
			Name:        "issues_link_path",
			Description: "Link an issue to a repo path or line range. Args: slug, number, path, line_start?, line_end?, title?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug":       map[string]any{"type": "string"},
				"number":     map[string]any{"type": "integer"},
				"path":       map[string]any{"type": "string"},
				"line_start": map[string]any{"type": "integer"},
				"line_end":   map[string]any{"type": "integer"},
				"title":      map[string]any{"type": "string"},
				"actor":      map[string]any{"type": "string"},
			}, []string{"slug", "number", "path"}),
			Handler: a.toolIssuesLinkPath,
		},
		{
			Name: "repos_dev_start",
			Description: "Start a dev/test preview process for a repo. This is for testing while editing; use the Deploy app for production builds, releases, domains, and hosting. Auto-detects framework " +
				"(nextjs / node / go / static) from the file tree, or accepts framework='blank' with run_cmd. " +
				"For JS/Bun/Node repos with package.json, bootstraps dependencies before the command when node_modules is missing or dependency files changed. " +
				"Spawns the framework's dev command (next dev / <pm> run dev / go run . / in-process FileServer) " +
				"with cwd set to the repo's storage_root, so edits via code_edit_file land directly where the " +
				"running framework's watcher sees them. With expose=true, registers <slug>.<dev_base_hostname> " +
				"with server-native ingress so the dev process is reachable publicly via apteva-server's host router. " +
				"Args: slug (required), framework?, run_cmd?, env_json?, expose? (default false).",
			InputSchema: schemaObject(map[string]any{
				"slug":      map[string]any{"type": "string"},
				"framework": map[string]any{"type": "string"},
				"run_cmd":   map[string]any{"type": "string"},
				"env_json":  map[string]any{"type": "string"},
				"expose":    map[string]any{"type": "boolean"},
			}, []string{"slug"}),
			Handler: a.toolDevStart,
		},
		{
			Name: "repos_run_command",
			Description: "Run a finite repo command and wait for it to exit. runtime=local uses the Code sidecar; runtime=workspace creates or reuses an isolated Workspaces environment, installs dependencies when inputs change, safely synchronizes source, and preserves its cache. Use this for builds, tests, lint, typecheck, generators, and validation commands. " +
				"Do not use repos_dev_start for finite commands; repos_dev_start is only for long-running preview servers. " +
				"Returns structured status, runtime, workspace_id when applicable, exit_code, duration_ms, dependency_install_ran, and bounded logs. Args: slug, command, runtime? (local|workspace), profile? (go|bun|python|apteva), image? (allowlisted immutable workspace image override), env_json?, timeout_seconds? (default 300, max 1800), tail? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"slug":            map[string]any{"type": "string"},
				"command":         map[string]any{"type": "string"},
				"runtime":         map[string]any{"type": "string", "enum": []string{"local", "workspace"}},
				"profile":         map[string]any{"type": "string", "enum": []string{"go", "bun", "python", "apteva"}},
				"image":           map[string]any{"type": "string"},
				"env_json":        map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "integer"},
				"tail":            map[string]any{"type": "integer"},
			}, []string{"slug", "command"}),
			HandlerCtx: a.toolRunCommand,
		},
		{
			Name:        "repos_workspace_changes",
			Description: "Preview source changes produced in a repository's linked execution workspace. Returns added, modified, and deleted paths plus source revision digests and conflict state. Run this before applying workspace-generated changes. Args: slug.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			HandlerCtx:  a.toolWorkspaceChanges,
		},
		{
			Name:        "repos_workspace_apply",
			Description: "Apply a previously previewed workspace source revision back to Code. Requires expected_workspace_digest from repos_workspace_changes and refuses when Code changed after synchronization or preview. Git metadata and dependency/build caches are never replaced. Args: slug, expected_workspace_digest.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "expected_workspace_digest": map[string]any{"type": "string"},
			}, []string{"slug", "expected_workspace_digest"}),
			HandlerCtx: a.toolWorkspaceApply,
		},
		{
			Name:        "repos_workspace_destroy",
			Description: "Permanently destroy a repository's linked execution workspace and cached volumes, then unlink it from Code. Repository files in Code are retained. Requires confirm=true. Args: slug, confirm.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "confirm": map[string]any{"type": "boolean"},
			}, []string{"slug", "confirm"}),
			HandlerCtx: a.toolWorkspaceDestroy,
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
	return append(tools, a.gitMCPTools()...)
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
	emitDevEvent(ctx, repo, dr)
	// Optional public exposure via server-native ingress. Best-effort: a
	// failure here doesn't roll back the dev run — the user can fix
	// DNS/config and call again. Cert issuance is handled by the
	// server for exact hostnames once DNS points at Apteva ingress.
	exposeResult := map[string]any{"requested": false}
	if boolArg(args, "expose") && dr != nil && dr.Status != "stopped" && dr.Port > 0 {
		hostname, err := exposeDevRun(ctx, repo.Slug, dr.Port)
		if err != nil {
			exposeResult = map[string]any{"requested": true, "registered": false, "error": err.Error()}
		} else {
			exposeResult = map[string]any{"requested": true, "registered": true, "hostname": hostname}
		}
	}
	return map[string]any{"dev_run": dr, "expose": exposeResult}, nil
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
	dr, _ := dbGetDevRun(ctx.AppDB(), pid, repo.ID)
	emitDevEvent(ctx, repo, dr)
	// Best-effort route cleanup — if the user passed expose=true on
	// start, we registered <slug>.<dev_base_hostname>. Drop it now
	// regardless of whether expose was requested; UnexposeIngress is
	// idempotent on a missing hostname.
	_ = unexposeDevRun(ctx, repo.Slug)
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

func (a *App) toolRunCommand(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	repo, err := requireRepo(ctx, pid, slug)
	if err != nil {
		return nil, err
	}
	runtime := strings.ToLower(strings.TrimSpace(strArg(args, "runtime")))
	if runtime == "workspace" {
		release, err := a.commands.acquire(callCtx, repo.ID)
		if err != nil {
			return nil, err
		}
		defer release()
		prep, err := a.prepareExecutionWorkspace(callCtx, ctx, repo, strArg(args, "profile"), strArg(args, "image"))
		if err != nil {
			return nil, err
		}
		return a.runWorkspaceCommand(callCtx, ctx, prep, repoCommandInput{
			Command: strArg(args, "command"), EnvJSON: strArg(args, "env_json"),
			TimeoutSeconds: intArg(args, "timeout_seconds", 300), TailLines: intArg(args, "tail", 200),
		})
	}
	if strArg(args, "image") != "" {
		return nil, errors.New("image requires runtime=workspace")
	}
	if runtime != "" && runtime != "local" {
		return nil, errors.New("runtime must be local or workspace")
	}
	pp, ok := a.storeFor(repo).(FileStoreLocalPath)
	if !ok {
		return nil, errors.New("repo command runner requires a local filesystem store")
	}
	srcDir := pp.RepoPath(repo.Slug)
	res, err := a.runRepoCommand(repo, srcDir, repoCommandInput{
		Command:        strArg(args, "command"),
		EnvJSON:        strArg(args, "env_json"),
		TimeoutSeconds: intArg(args, "timeout_seconds", 300),
		TailLines:      intArg(args, "tail", 200),
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (a *App) toolWorkspaceChanges(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	preview, _, _, _, err := a.workspaceChanges(ctx, repo)
	return preview, err
}

func (a *App) toolWorkspaceApply(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	result, err := a.applyWorkspaceChanges(ctx, repo, strArg(args, "expected_workspace_digest"))
	if err != nil {
		return nil, err
	}
	ctx.Emit("repo.workspace.applied", map[string]any{"id": repo.ID, "slug": repo.Slug, "workspace_id": result.WorkspaceID, "changes": len(result.Changes)})
	return result, nil
}

func (a *App) toolWorkspaceDestroy(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolArg(args, "confirm") {
		return nil, errors.New("confirm=true is required because workspace destruction permanently deletes its cached volumes")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	release, err := a.commands.acquire(callCtx, repo.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	link, err := dbGetRepoWorkspace(ctx.AppDB(), repo.ProjectID, repo.ID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return map[string]any{"slug": repo.Slug, "destroyed": false, "workspace_id": ""}, nil
	}
	var destroyed map[string]any
	if err := ctx.PlatformAPI().CallAppResult(workspacesAppName, "workspace_destroy", map[string]any{
		"workspace_id": link.WorkspaceID,
		"confirm":      true,
	}, &destroyed); err != nil {
		return nil, fmt.Errorf("destroy execution workspace: %w", err)
	}
	if err := dbDeleteRepoWorkspace(ctx.AppDB(), repo.ProjectID, repo.ID); err != nil {
		return nil, err
	}
	ctx.Emit("repo.workspace.destroyed", map[string]any{"id": repo.ID, "slug": repo.Slug, "workspace_id": link.WorkspaceID})
	return map[string]any{"slug": repo.Slug, "destroyed": true, "workspace_id": link.WorkspaceID}, nil
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

// ─── issues_* handlers ────────────────────────────────────────────

func (a *App) toolIssuesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, repo, err := issueRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status")
	if status == "" {
		status = "all"
	}
	issues, err := dbListIssues(ctx.AppDB(), pid, repo.ID, IssueListOptions{
		State:    strArg(args, "state"),
		Status:   status,
		Type:     strArg(args, "type"),
		Priority: strArg(args, "priority"),
		Assignee: strArg(args, "assignee"),
		Q:        strArg(args, "q"),
		Limit:    intArg(args, "limit", 100),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"issues": issues, "count": len(issues)}, nil
}

func (a *App) toolIssuesSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status")
	if status == "" {
		status = "all"
	}
	state := strArg(args, "state")
	if state == "" {
		state = "all"
	}
	issues, err := dbSearchIssues(ctx.AppDB(), pid, IssueListOptions{
		State:    state,
		Status:   status,
		Type:     strArg(args, "type"),
		Priority: strArg(args, "priority"),
		Assignee: strArg(args, "assignee"),
		RepoSlug: strArg(args, "repo"),
		Q:        strArg(args, "q"),
		Limit:    intArg(args, "limit", 100),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"issues": issues, "count": len(issues)}, nil
}

func (a *App) toolIssuesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, repo, err := issueRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	detail, err := dbGetIssueDetail(ctx.AppDB(), pid, repo.ID, intArg(args, "number", 0))
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return map[string]any{"issue": nil, "found": false}, nil
	}
	return map[string]any{"issue": detail.Issue, "comments": detail.Comments, "links": detail.Links, "events": detail.Events, "found": true}, nil
}

func issueClaimant(callCtx context.Context, app *sdk.AppCtx) (string, string, error) {
	caller := sdk.CallerFrom(callCtx)
	if caller == nil {
		return "", "", errors.New("issue claims require authenticated caller identity")
	}
	if caller.AgentID > 0 {
		owner := fmt.Sprintf("agent:%d", caller.AgentID)
		label := fmt.Sprintf("Agent %d", caller.AgentID)
		if agent, err := app.GetAgent(caller.AgentID); err == nil && agent != nil && agent.Name != "" {
			label = agent.Name
		}
		return owner, label, nil
	}
	if caller.SubjectType != "" && caller.SubjectID != "" {
		owner := "user:" + caller.SubjectType + ":" + caller.SubjectID
		label := caller.SubjectEmail
		if label == "" {
			label = caller.SubjectID
		}
		return owner, label, nil
	}
	return "", "", errors.New("issue claims are available to authenticated agents and users")
}

func (a *App) toolIssuesClaim(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, issue, err := issueFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	owner, label, err := issueClaimant(callCtx, ctx)
	if err != nil {
		return nil, err
	}
	outcome, err := dbClaimIssue(ctx.AppDB(), issue.ID, owner, label)
	if err != nil {
		return nil, err
	}
	if outcome.Changed {
		emitIssueEvent(ctx, "issue.claimed", repo, outcome.Issue)
	}
	return outcome, nil
}

func (a *App) toolIssuesRelease(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, issue, err := issueFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	owner, label, err := issueClaimant(callCtx, ctx)
	if err != nil {
		return nil, err
	}
	outcome, err := dbReleaseIssueClaim(ctx.AppDB(), issue.ID, owner, label)
	if err != nil {
		return nil, err
	}
	if outcome.Changed {
		emitIssueEvent(ctx, "issue.claim_released", repo, outcome.Issue)
	}
	return outcome, nil
}

func (a *App) toolIssuesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, repo, err := issueRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	iss, err := dbCreateIssue(ctx.AppDB(), pid, repo, IssueCreateInput{
		Title:     strArg(args, "title"),
		Body:      strArg(args, "body"),
		Type:      strArg(args, "type"),
		Priority:  strArg(args, "priority"),
		Assignee:  strArg(args, "assignee"),
		CreatedBy: strArg(args, "created_by"),
	})
	if err != nil {
		return nil, err
	}
	if p := strArg(args, "link_path"); p != "" {
		if _, err := issuePathLink(ctx.AppDB(), iss, p, intArg(args, "line_start", 0), intArg(args, "line_end", 0), "", strArg(args, "created_by")); err != nil {
			return nil, err
		}
	}
	emitIssueEvent(ctx, "issue.created", repo, iss)
	return map[string]any{"issue": iss}, nil
}

func (a *App) toolIssuesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, iss, err := issueFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	patch := IssuePatch{Actor: strArg(args, "actor")}
	if v, ok := args["title"].(string); ok {
		patch.Title = &v
	}
	if v, ok := args["body"].(string); ok {
		patch.Body = &v
	}
	if v, ok := args["type"].(string); ok {
		patch.Type = &v
	}
	if v, ok := args["status"].(string); ok {
		patch.Status = &v
	}
	if v, ok := args["state"].(string); ok {
		patch.State = &v
	}
	if v, ok := args["state_reason"].(string); ok {
		patch.StateReason = &v
	}
	if v, ok := args["priority"].(string); ok {
		patch.Priority = &v
	}
	if v, ok := args["assignee"].(string); ok {
		patch.Assignee = &v
	}
	updated, err := dbUpdateIssue(ctx.AppDB(), iss, patch)
	if err != nil {
		return nil, err
	}
	emitIssueEvent(ctx, "issue.updated", repo, updated)
	return map[string]any{"issue": updated}, nil
}

func (a *App) toolIssuesComment(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, iss, err := issueFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	comment, err := dbAddIssueComment(ctx.AppDB(), iss.ID, strArg(args, "author"), strArg(args, "body"))
	if err != nil {
		return nil, err
	}
	refreshed, _ := dbGetIssueByID(ctx.AppDB(), iss.ID)
	emitIssueEvent(ctx, "issue.commented", repo, refreshed)
	return map[string]any{"comment": comment, "issue": refreshed}, nil
}

func (a *App) toolIssuesClose(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, iss, err := issueFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	actor := strArg(args, "actor")
	if res := strArg(args, "resolution"); res != "" {
		if _, err := dbAddIssueComment(ctx.AppDB(), iss.ID, actor, res); err != nil {
			return nil, err
		}
	}
	state := issueStateClosed
	status := issueStatusDone
	reason := strArg(args, "state_reason")
	if reason == "" {
		reason = issueReasonCompleted
	}
	updated, err := dbUpdateIssue(ctx.AppDB(), iss, IssuePatch{State: &state, StateReason: &reason, Status: &status, Actor: actor})
	if err != nil {
		return nil, err
	}
	emitIssueEvent(ctx, "issue.closed", repo, updated)
	return map[string]any{"issue": updated}, nil
}

func (a *App) toolIssuesReopen(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, iss, err := issueFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	state := issueStateOpen
	status := issueStatusTodo
	reason := ""
	updated, err := dbUpdateIssue(ctx.AppDB(), iss, IssuePatch{State: &state, StateReason: &reason, Status: &status, Actor: strArg(args, "actor")})
	if err != nil {
		return nil, err
	}
	emitIssueEvent(ctx, "issue.reopened", repo, updated)
	return map[string]any{"issue": updated}, nil
}

func (a *App) toolIssuesLinkPath(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, iss, err := issueFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	link, err := issuePathLink(ctx.AppDB(), iss, strArg(args, "path"), intArg(args, "line_start", 0), intArg(args, "line_end", 0), strArg(args, "title"), strArg(args, "actor"))
	if err != nil {
		return nil, err
	}
	refreshed, _ := dbGetIssueByID(ctx.AppDB(), iss.ID)
	emitIssueEvent(ctx, "issue.linked", repo, refreshed)
	return map[string]any{"link": link, "issue": refreshed}, nil
}

func issueRepoFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, *Repo, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return "", nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	return pid, repo, err
}

func issueFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, *Repo, *Issue, error) {
	pid, repo, err := issueRepoFromArgs(ctx, args)
	if err != nil {
		return "", nil, nil, err
	}
	n := intArg(args, "number", 0)
	if n <= 0 {
		return "", nil, nil, errors.New("number required")
	}
	iss, err := dbGetIssueByNumber(ctx.AppDB(), pid, repo.ID, n)
	if err != nil {
		return "", nil, nil, err
	}
	if iss == nil {
		return "", nil, nil, fmt.Errorf("issue #%d not found", n)
	}
	return pid, repo, iss, nil
}

func issuePathLink(db *sql.DB, iss *Issue, rawPath string, lineStart, lineEnd int, title, actor string) (*IssueLink, error) {
	rel, err := normalisePath(rawPath)
	if err != nil {
		return nil, err
	}
	target := rel
	data := map[string]any{"path": rel}
	if lineStart > 0 {
		data["line_start"] = lineStart
		target = fmt.Sprintf("%s:%d", rel, lineStart)
	}
	if lineEnd > 0 {
		data["line_end"] = lineEnd
	}
	if title == "" {
		title = target
	}
	return dbAddIssueLink(db, iss.ID, "path", target, title, data, actor)
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
	Slug      string `json:"slug"` // user: repo slug; embedded: framework name
	Tagline   string `json:"tagline,omitempty"`
	Icon      string `json:"icon,omitempty"`
	Scope     string `json:"scope,omitempty"` // user only
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
		files, _ := a.storeFor(r).List(r.Slug, "", true)
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
		src = storeReader{s: a.storeFor(parent)}
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
	dstStore := a.storeFor(r)
	if err := dstStore.CreateRepo(r.Slug); err != nil {
		_ = dbHardDeleteRepo(ctx.AppDB(), pid, r.Slug)
		return nil, fmt.Errorf("create repo dir: %w", err)
	}
	count, err := fork(src, srcID, dstStore, r.Slug)
	if err != nil {
		// Roll back so the user doesn't end up with a half-copied repo
		// that they then have to clean up manually.
		_ = dstStore.DropRepo(r.Slug)
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
		Name:           strArg(args, "name"),
		Slug:           strArg(args, "slug"),
		Description:    strArg(args, "description"),
		Framework:      strArg(args, "framework"),
		WorkspaceImage: strArg(args, "workspace_image"),
	}
	r, err := dbCreateRepo(ctx.AppDB(), pid, in)
	if err != nil {
		return nil, err
	}
	repoStore := a.storeFor(r)
	if err := repoStore.CreateRepo(r.Slug); err != nil {
		// Roll back the row to keep DB and disk consistent.
		_ = dbHardDeleteRepo(ctx.AppDB(), pid, r.Slug)
		return nil, fmt.Errorf("create repo dir: %w", err)
	}
	count, err := applyTemplate(repoStore, r.Slug, r.Framework)
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
	repoStore := a.storeFor(r)
	files, _ := repoStore.List(slug, "", true)
	totalSize, _ := repoStore.TotalSize(slug)
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
		if err := a.hardDeleteRepo(ctx.AppDB(), pid, slug); err != nil {
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
	if ctx != nil {
		ctx.Emit("repo.updated", map[string]any{
			"id": r.ID, "slug": r.Slug, "name": r.Name, "framework": r.Framework,
		})
	}
	return map[string]any{"repository": r}, nil
}

func (a *App) toolReposSetWorkspaceImage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		return nil, errors.New("slug required")
	}
	r, err := dbSetWorkspaceImage(ctx.AppDB(), pid, slug, strArg(args, "image"))
	if err != nil {
		return nil, err
	}
	ctx.Emit("repo.updated", map[string]any{
		"id": r.ID, "slug": r.Slug, "name": r.Name, "framework": r.Framework,
	})
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
	repo, err := requireRepo(ctx, pid, slug)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := zipRepo(&buf, a.storeFor(repo), slug); err != nil {
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
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
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
	files, err := listSourceFiles(a.storeFor(repo), strArg(args, "slug"), prefix, recursive, boolArg(args, "include_generated"))
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
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	pattern := strArg(args, "pattern")
	matches, err := globRepoWithOptions(a.storeFor(repo), strArg(args, "slug"), pattern,
		boolArg(args, "include_generated"), intArg(args, "limit", 2000))
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
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	o := GrepOptions{
		Pattern:          strArg(args, "pattern"),
		Path:             strArg(args, "path"),
		FilePattern:      strArg(args, "file_pattern"),
		OutputMode:       strArg(args, "output_mode"),
		Regex:            boolArg(args, "regex"),
		IgnoreCase:       boolArg(args, "ignore_case"),
		Context:          intArg(args, "context", 0),
		Limit:            intArg(args, "limit", 0),
		MatchesPerFile:   intArg(args, "matches_per_file", 0),
		IncludeGenerated: boolArg(args, "include_generated"),
	}
	if o.Path != "" {
		clean, err := normalisePath(o.Path)
		if err != nil {
			return nil, err
		}
		o.Path = clean
	}
	res, err := grepRepo(a.storeFor(repo), strArg(args, "slug"), o)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (a *App) toolReadFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	res, err := readWithLineNumbers(a.storeFor(repo), strArg(args, "slug"), rel,
		intArg(args, "offset", 0), intArg(args, "limit", 0))
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (a *App) toolReadExcerpt(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	return readExcerpt(a.storeFor(repo), strArg(args, "slug"), rel, ExcerptOptions{
		StartLine: intArg(args, "start_line", 0),
		EndLine:   intArg(args, "end_line", 0),
		Around:    intArg(args, "around", 0),
		Before:    intArg(args, "before", 0),
		After:     intArg(args, "after", 0),
		Limit:     intArg(args, "limit", 0),
		Tail:      boolArg(args, "tail"),
	})
}

func (a *App) toolFileOutline(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	return fileOutline(a.storeFor(repo), strArg(args, "slug"), rel, intArg(args, "limit", 0))
}

func (a *App) toolWriteFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	content := strArg(args, "content")
	slug := strArg(args, "slug")
	meta, err := a.storeFor(repo).Write(slug, rel, []byte(content))
	if err != nil {
		return nil, err
	}
	emitFileChange(ctx, "file.changed", slug, rel)
	return map[string]any{"file": meta}, nil
}

func (a *App) toolApplyPatch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	repo, err := requireRepo(ctx, pid, slug)
	if err != nil {
		return nil, err
	}
	patch := strArg(args, "patch")
	if patchID := strArg(args, "patch_id"); patchID != "" {
		if boolArg(args, "dry_run") {
			return nil, errors.New("patch_id applies an existing dry run; omit dry_run or set it false")
		}
		var err error
		patch, err = loadPatchPreview(patchID, repoStoreKey(repo))
		if err != nil {
			return nil, err
		}
	}
	if patch == "" {
		return nil, errors.New("patch or patch_id required")
	}
	res, err := applyUnifiedPatch(a.storeFor(repo), repoStoreKey(repo), patch, boolArg(args, "dry_run"))
	if err != nil {
		return nil, err
	}
	if res.Applied {
		for _, f := range res.ChangedFiles {
			topic := "file.changed"
			if f.Deleted {
				topic = "file.deleted"
			}
			emitFileChange(ctx, topic, slug, f.Path)
		}
	}
	return res, nil
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
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	res, err := editFile(a.storeFor(repo), slug, rel,
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
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
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
	res, err := multiEditFile(a.storeFor(repo), slug, rel, ops)
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
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
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
	moved, err := a.storeFor(repo).Move(slug, from, to)
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
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
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
	if err := a.storeFor(repo).DeleteTree(slug, rel); err != nil {
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
