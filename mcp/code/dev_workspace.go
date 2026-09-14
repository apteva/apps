package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const workspacePreviewPort = 3000

func workspacePreviewName(repo *Repo) string {
	base := slugify(repo.Slug)
	if len(base) > 48 {
		base = base[:48]
	}
	return fmt.Sprintf("%s preview %x", base, time.Now().UnixNano())
}

func workspacePreviewConnected(app *sdk.AppCtx) (bool, error) {
	if app == nil || app.PlatformAPI() == nil {
		return false, nil
	}
	identity, err := app.PlatformAPI().WhoAmI()
	if err != nil {
		return false, fmt.Errorf("cannot resolve execution binding: %w", err)
	}
	if identity == nil {
		return false, errors.New("cannot resolve execution binding: missing install identity")
	}
	bound := identity.Bindings[workspacesAppName]
	return bound != nil && fmt.Sprint(bound) != "0" && fmt.Sprint(bound) != "", nil
}

type workspacePreviewWire struct {
	WorkspaceID string `json:"workspace_id"`
	Status      string `json:"status"`
	HostPort    int    `json:"host_port"`
	Error       string `json:"error"`
}

// Select commands without probing the Code host's PATH or node_modules. All
// installs and commands execute inside the selected workspace profile.
func workspacePreviewCommand(srcDir, framework, override string) ([]string, error) {
	if strings.TrimSpace(override) != "" {
		return []string{"/bin/sh", "-c", override}, nil
	}
	switch framework {
	case "node", "nextjs":
		script := "dev"
		if !hasScript(srcDir, script) {
			script = "start"
		}
		if !hasScript(srcDir, script) {
			if file := findBunRunScript(srcDir); file != "" {
				return []string{"bun", "--bun", "run", file}, nil
			}
			return nil, errors.New("no dev/start script; set run_cmd for the workspace preview")
		}
		args := devNodePortArgs(srcDir, "bun", []string{"run", script}, workspacePreviewPort)
		for i := 2; i < len(args)-1; i++ {
			if args[i] == "--host" {
				args[i+1] = "0.0.0.0"
			}
		}
		if framework == "nextjs" {
			args = append(args, "--hostname", "0.0.0.0", "--port", "3000")
		}
		return append([]string{"bun", "--bun"}, args...), nil
	case "go":
		return []string{"go", "run", "."}, nil
	case "static":
		return []string{"bun", "-e", `const root=process.cwd(); const {resolve}=require("node:path"); Bun.serve({hostname:"0.0.0.0",port:3000,async fetch(r){let p;try{p=resolve(root,"."+decodeURIComponent(new URL(r.url).pathname));}catch{return new Response("Bad path",{status:400})}if(p!==root&&!p.startsWith(root+"/"))return new Response("Forbidden",{status:403}); if(p===root||p.endsWith("/"))p+="/index.html";let f=Bun.file(p);if(!await f.exists())f=Bun.file(root+"/index.html");return new Response(f)}})`}, nil
	default:
		return nil, fmt.Errorf("framework %q needs run_cmd for a workspace preview; listen on 0.0.0.0:$PORT", framework)
	}
}
func (s *devSupervisor) startWorkspacePreview(callCtx context.Context, app *sdk.AppCtx, in startDevInput, srcDir, framework string) (dr *DevRun, err error) {
	if app.PlatformAPI() == nil || s.app == nil {
		return nil, errors.New("Workspaces is connected but its platform API is unavailable")
	}
	argv, err := workspacePreviewCommand(srcDir, framework, in.RunCmd)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.app.snapshotRepoSource(in.Repo, nil, nil)
	if err != nil {
		return nil, err
	}
	envList, err := workspaceEnv(in.EnvJSON)
	if err != nil {
		return nil, err
	}
	env := map[string]string{"PORT": "3000", "HOST": "0.0.0.0", "HOSTNAME": "0.0.0.0"}
	for _, pair := range envList {
		key, value, _ := strings.Cut(pair, "=")
		env[key] = value
	}
	// The published port is the preview contract, not a caller override.
	env["PORT"] = "3000"
	env["HOST"] = "0.0.0.0"
	env["HOSTNAME"] = "0.0.0.0"
	plan, _ := dependencyPlan(snapshot)
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	command := "exec " + strings.Join(quoted, " ")
	if plan != "" {
		command = plan + " && " + command
	}
	dr, err = dbUpsertDevRun(app.AppDB(), DevRun{ProjectID: in.ProjectID, RepoID: in.Repo.ID, Runner: workspacesAppName, Framework: framework, RunCmd: in.RunCmd, Status: "starting", StartedAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return nil, err
	}
	var created struct {
		Workspace workspaceWire `json:"workspace"`
	}
	input := map[string]any{"name": workspacePreviewName(in.Repo), "purpose": "Live preview for Code repository " + in.Repo.Slug, "profile": sourceProfile(in.Repo, snapshot), "resource_kind": "code.preview", "resource_id": fmt.Sprint(in.Repo.ID), "repo_label": in.Repo.Slug, "preview_port": workspacePreviewPort, "source_archive_base64": snapshot.Archive, "source_digest": snapshot.Digest, "source_paths": snapshot.Paths}
	if in.Repo.WorkspaceImage != "" {
		input["image"] = in.Repo.WorkspaceImage
	}
	if caller := sdk.CallerFrom(callCtx); caller != nil {
		input["owner_agent_id"] = caller.AgentID
		input["owner_thread_id"] = caller.ThreadID
	}
	record := dr
	defer func() {
		if err != nil {
			status := "crashed"
			if errors.Is(err, context.Canceled) {
				status = "stopped"
			}
			if created.Workspace.ID != "" {
				record.WorkspaceID = created.Workspace.ID
				if stopErr := stopWorkspacePreview(app, record); stopErr != nil {
					status = "crashed"
					err = fmt.Errorf("%w; preview cleanup failed: %v", err, stopErr)
				}
			}
			_ = dbUpdateDevRun(app.AppDB(), record.ID, map[string]any{"status": status, "workspace_id": record.WorkspaceID, "error": err.Error()})
		}
	}()
	if err = app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_create_for_resource", input, &created); err != nil {
		return dr, fmt.Errorf("create Docker preview workspace: %w", err)
	}
	if created.Workspace.ID == "" {
		return dr, errors.New("Workspaces returned no preview workspace id")
	}
	dr.WorkspaceID = created.Workspace.ID
	if err = dbUpdateDevRun(app.AppDB(), dr.ID, map[string]any{"workspace_id": dr.WorkspaceID, "workspace_source_digest": snapshot.Digest}); err != nil {
		return dr, err
	}
	if err = callCtx.Err(); err != nil {
		return dr, err
	}
	var out struct {
		Preview *workspacePreviewWire `json:"preview"`
	}
	if err = app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_preview_start", map[string]any{"workspace_id": dr.WorkspaceID, "argv": []string{"/bin/sh", "-c", command}, "env": env}, &out); err != nil {
		return dr, err
	}
	if out.Preview == nil {
		return dr, errors.New("Workspaces returned no preview")
	}
	if err = callCtx.Err(); err != nil {
		return dr, err
	}
	if err = dbUpdateDevRun(app.AppDB(), dr.ID, map[string]any{"status": out.Preview.Status, "port": out.Preview.HostPort, "error": out.Preview.Error}); err != nil {
		return dr, err
	}
	return dbGetDevRun(app.AppDB(), in.ProjectID, in.Repo.ID)
}
func stopWorkspacePreview(app *sdk.AppCtx, dr *DevRun) error {
	if dr.WorkspaceID == "" {
		return nil
	}
	if app.PlatformAPI() == nil {
		return errors.New("platform unavailable; Docker preview was not stopped")
	}
	var out map[string]any
	return app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_preview_stop", map[string]any{"workspace_id": dr.WorkspaceID}, &out)
}
func (a *App) refreshWorkspacePreview(c context.Context, app *sdk.AppCtx, repo *Repo, dr *DevRun) (*DevRun, error) {
	release, err := a.commands.acquire(c, repo.ID)
	if err != nil {
		return dr, err
	}
	defer release()
	dr, err = dbGetDevRun(app.AppDB(), repo.ProjectID, repo.ID)
	if err != nil || dr == nil {
		return dr, err
	}
	if dr.Runner != workspacesAppName || dr.WorkspaceID == "" || (dr.Status != "live" && dr.Status != "starting") {
		return dr, nil
	}
	var out struct {
		Preview *workspacePreviewWire `json:"preview"`
	}
	if app.PlatformAPI() == nil {
		return dr, errors.New("platform unavailable; cannot refresh Docker preview")
	}
	if err = app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_preview_get", map[string]any{"workspace_id": dr.WorkspaceID}, &out); err != nil {
		return dr, err
	}
	if out.Preview == nil {
		_ = dbUpdateDevRun(app.AppDB(), dr.ID, map[string]any{"status": "crashed", "error": "workspace preview was not started; run again"})
		return dbGetDevRun(app.AppDB(), repo.ProjectID, repo.ID)
	}
	syncError := ""
	if out.Preview.Status == "live" {
		snapshot, e := a.snapshotRepoSource(repo, nil, nil)
		if e == nil && snapshot.Digest != dr.WorkspaceSourceDigest {
			e = a.syncExecutionWorkspace(app, &RepoWorkspace{WorkspaceID: dr.WorkspaceID, SourceDigest: dr.WorkspaceSourceDigest}, snapshot)
			if e == nil {
				_ = dbUpdateDevRun(app.AppDB(), dr.ID, map[string]any{"workspace_source_digest": snapshot.Digest})
			}
		}
		if e != nil {
			syncError = "Source sync: " + e.Error()
		}
	}
	errorText := out.Preview.Error
	if syncError != "" {
		errorText = syncError
	}
	// A Stop may have arrived during the RPC. Never resurrect its terminal state.
	_, err = app.AppDB().Exec(`UPDATE dev_runs SET status=?,port=?,error=? WHERE id=? AND workspace_id=? AND status IN ('starting','live')`, out.Preview.Status, out.Preview.HostPort, errorText, dr.ID, dr.WorkspaceID)
	if err != nil {
		return dr, err
	}
	return dbGetDevRun(app.AppDB(), repo.ProjectID, repo.ID)
}
func (a *App) reconcileWorkspacePreviews(c context.Context, app *sdk.AppCtx) error {
	runs, err := dbListLiveDevRuns(app.AppDB())
	if err != nil {
		return err
	}
	var errs []error
	for _, dr := range runs {
		if dr.Runner != workspacesAppName {
			continue
		}
		repo, e := dbGetRepoByID(app.AppDB(), dr.ProjectID, dr.RepoID)
		if e == nil && repo != nil {
			_, e = a.refreshWorkspacePreview(c, app, repo, dr)
		}
		if e != nil {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}
func workspacePreviewLogs(app *sdk.AppCtx, dr *DevRun, tail int) (string, error) {
	if dr.WorkspaceID == "" {
		return "Preparing Docker workspace…\n", nil
	}
	if app.PlatformAPI() == nil {
		return "", errors.New("platform unavailable")
	}
	var out struct {
		Logs string `json:"logs"`
	}
	err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_preview_logs", map[string]any{"workspace_id": dr.WorkspaceID, "tail": tail}, &out)
	return out.Logs, err
}
func (a *App) httpWorkspacePreviewLogs(w http.ResponseWriter, r *http.Request, dr *DevRun) {
	if r.URL.Query().Get("follow") != "1" {
		body, err := workspacePreviewLogs(globalCtx, dr, atoiOr(r.URL.Query().Get("tail"), 200))
		if err != nil {
			httpErr(w, 502, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	previous := ""
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		body, err := workspacePreviewLogs(globalCtx, dr, 300)
		if err != nil {
			writeSSEEvent(w, "log-error", err.Error())
			flusher.Flush()
			return
		}
		if body != previous {
			if strings.HasPrefix(body, previous) {
				writeSSE(w, strings.TrimPrefix(body, previous))
			} else {
				writeSSEEvent(w, "reset", "")
				writeSSE(w, body)
			}
			previous = body
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		current, e := dbGetDevRun(globalCtx.AppDB(), dr.ProjectID, dr.RepoID)
		if e != nil || current == nil {
			return
		}
		if current.WorkspaceID != dr.WorkspaceID {
			writeSSEEvent(w, "reset", "")
			previous = ""
		}
		dr = current
	}
}
