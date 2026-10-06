package main

// Previews use their own Containers execution, independent of the command PTY.
// The execution and its published loopback port survive Workspaces/Code restarts;
// the workspace TTL remains the upper bound on the preview's lifetime.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Preview struct {
	WorkspaceID   string `json:"workspace_id"`
	ExecutionID   string `json:"execution_id"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	StartedAt     string `json:"started_at"`
	URL           string `json:"url,omitempty"`
}

func getPreview(db *sql.DB, id string) (*Preview, error) {
	p := &Preview{}
	err := db.QueryRow(`SELECT workspace_id,execution_id,container_port,host_port,status,error,started_at FROM workspace_previews WHERE workspace_id=?`, id).Scan(&p.WorkspaceID, &p.ExecutionID, &p.ContainerPort, &p.HostPort, &p.Status, &p.Error, &p.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.HostPort > 0 {
		p.URL = "http://127.0.0.1:" + strconv.Itoa(p.HostPort) + "/"
	}
	return p, nil
}
func savePreview(db *sql.DB, p *Preview) error {
	_, err := db.Exec(`INSERT INTO workspace_previews(workspace_id,execution_id,container_port,host_port,status,error,started_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET execution_id=excluded.execution_id,container_port=excluded.container_port,host_port=excluded.host_port,status=excluded.status,error=excluded.error,started_at=excluded.started_at`, p.WorkspaceID, p.ExecutionID, p.ContainerPort, p.HostPort, p.Status, p.Error, p.StartedAt)
	return err
}
func (a *App) previewTools() []sdk.Tool {
	id := map[string]any{"workspace_id": strSchema()}
	return []sdk.Tool{
		{Name: "workspace_preview_start", Description: "Start a persistent HTTP preview on a workspace provisioned with preview_port. Runs until stopped or workspace TTL expires; argv must bind 0.0.0.0. Returns before readiness.", Exposure: sdk.ToolExposureAppOnly, InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "argv": map[string]any{"type": "array", "items": strSchema()}, "env": map[string]any{"type": "object", "additionalProperties": strSchema()}}, []string{"workspace_id", "argv"}), HandlerCtx: a.toolPreviewStart},
		{Name: "workspace_preview_get", Description: "Fetch preview readiness, mapped URL and execution status.", Exposure: sdk.ToolExposureAppOnly, InputSchema: schemaObject(id, []string{"workspace_id"}), HandlerCtx: a.toolPreviewGet},
		{Name: "workspace_preview_logs", Description: "Tail bounded container preview logs.", Exposure: sdk.ToolExposureAppOnly, InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "tail": intSchema()}, []string{"workspace_id"}), HandlerCtx: a.toolPreviewLogs},
		{Name: "workspace_preview_stop", Description: "Stop the preview and its workspace container, retaining its volumes until workspace expiry.", Exposure: sdk.ToolExposureAppOnly, InputSchema: schemaObject(id, []string{"workspace_id"}), HandlerCtx: a.toolPreviewStop},
	}
}
func previewContext(c context.Context, app *sdk.AppCtx, args map[string]any) (Actor, *Workspace, error) {
	actor, err := actorFrom(c, app)
	if err != nil {
		return actor, nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	return actor, w, err
}
func (a *App) toolPreviewStart(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, w, err := previewContext(c, app, args)
	if err != nil {
		return nil, err
	}
	unlock := a.lockWorkspace(w.ID)
	defer unlock()
	w, err = requireWorkspaceForActor(app.AppDB(), actor, w.ID)
	if err != nil {
		return nil, err
	}
	if w.LifecycleStatus != statusRunning {
		return nil, errors.New("workspace must be running")
	}
	ttl := int(time.Until(parseTime(w.ExpiresAt)).Seconds())
	if ttl < 1 || ttl > 86400 {
		return nil, errors.New("workspace must have an active TTL of at most 24 hours")
	}
	old, err := getPreview(app.AppDB(), w.ID)
	if err != nil {
		return nil, err
	}
	if old != nil && (old.Status == "starting" || old.Status == "live") {
		return map[string]any{"preview": old}, nil
	}
	argv, _, _, _, err := normalizeCommand(map[string]any{"argv": args["argv"]}, 30)
	if err != nil {
		return nil, err
	}
	var runtime workloadResponse
	if err = app.PlatformAPI().CallAppResult("containers", "containers_get", map[string]any{"workload_id": w.WorkloadID}, &runtime); err != nil {
		return nil, err
	}
	if runtime.Workload.HostID != 0 || runtime.Workload.InstanceID != 0 {
		return nil, errors.New("preview requires a local Docker workspace")
	}
	p := &Preview{WorkspaceID: w.ID, Status: "starting", StartedAt: nowUTC()}
	for _, port := range runtime.Workload.Ports {
		if port.Protocol == "tcp" && port.BindAddr == "127.0.0.1" && port.HostPort > 0 {
			p.ContainerPort = port.ContainerPort
			p.HostPort = port.HostPort
			break
		}
	}
	if p.HostPort == 0 {
		return nil, errors.New("workspace has no published preview port; create it with preview_port using Containers >=0.5.0")
	}
	if err = savePreview(app.AppDB(), p); err != nil {
		return nil, err
	}
	var out executionResponse
	err = app.PlatformAPI().CallAppResult("containers", "containers_exec_start", map[string]any{"workload_id": w.WorkloadID, "argv": argv, "env": args["env"], "working_directory": "/workspace", "timeout_s": ttl, "idempotency_key": "preview-" + w.ID + "-" + p.StartedAt}, &out)
	if err != nil {
		p.Status = "crashed"
		p.Error = err.Error()
		_ = savePreview(app.AppDB(), p)
		return nil, err
	}
	p.ExecutionID = firstNonEmpty(out.Execution.ID, out.ExecutionID)
	if p.ExecutionID == "" {
		p.Status = "crashed"
		p.Error = "Containers returned an empty execution id"
	}
	if err = savePreview(app.AppDB(), p); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "preview.started", actor, "Container preview starting", map[string]any{"execution_id": p.ExecutionID, "port": p.HostPort})
	p, _ = getPreview(app.AppDB(), w.ID)
	return map[string]any{"preview": p}, nil
}

var previewHTTPClient = &http.Client{Timeout: time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

func (a *App) refreshPreview(app *sdk.AppCtx, w *Workspace) (*Preview, error) {
	unlock := a.lockWorkspace(w.ID)
	defer unlock()
	p, err := getPreview(app.AppDB(), w.ID)
	if err != nil || p == nil {
		return p, err
	}
	if p.Status != "starting" && p.Status != "live" {
		return p, nil
	}
	var out executionResponse
	if p.ExecutionID == "" {
		p.Status = "crashed"
		p.Error = "preview start interrupted before execution was recorded"
	} else {
		if err = app.PlatformAPI().CallAppResult("containers", "containers_exec_get", map[string]any{"execution_id": p.ExecutionID}, &out); err != nil {
			return p, err
		}
		if commandTerminal(out.Execution.Status) {
			p.Status = "crashed"
			p.Error = firstNonEmpty(out.Execution.Error, fmt.Sprintf("preview exited (%s, exit code %v)", out.Execution.Status, out.Execution.ExitCode))
		} else {
			resp, e := previewHTTPClient.Get(p.URL)
			if e == nil {
				resp.Body.Close()
				p.Status = "live"
				p.Error = ""
			} else if p.Status == "starting" && time.Since(parseTime(p.StartedAt)) > 10*time.Minute {
				var cancelled executionResponse
				if err = app.PlatformAPI().CallAppResult("containers", "containers_exec_cancel", map[string]any{"execution_id": p.ExecutionID}, &cancelled); err != nil {
					return p, err
				}
				p.Status = "crashed"
				p.Error = "preview did not listen on its published port within 10 minutes; check logs and bind 0.0.0.0"
			}
		}
	}
	if err = savePreview(app.AppDB(), p); err != nil {
		return p, err
	}
	return p, nil
}
func (a *App) cancelPreview(app *sdk.AppCtx, w *Workspace) error {
	p, err := getPreview(app.AppDB(), w.ID)
	if err != nil || p == nil {
		return err
	}
	if p.ExecutionID != "" && (p.Status == "starting" || p.Status == "live") {
		var out executionResponse
		if err = app.PlatformAPI().CallAppResult("containers", "containers_exec_cancel", map[string]any{"execution_id": p.ExecutionID}, &out); err != nil {
			return err
		}
	}
	p.Status = "stopped"
	p.Error = ""
	return savePreview(app.AppDB(), p)
}
func (a *App) toolPreviewGet(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, w, err := previewContext(c, app, args)
	if err != nil {
		return nil, err
	}
	p, err := a.refreshPreview(app, w)
	return map[string]any{"preview": p}, err
}
func (a *App) toolPreviewLogs(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, w, err := previewContext(c, app, args)
	if err != nil {
		return nil, err
	}
	p, err := getPreview(app.AppDB(), w.ID)
	if err != nil {
		return nil, err
	}
	if p == nil || p.ExecutionID == "" {
		return map[string]any{"logs": ""}, nil
	}
	var out executionLogsResponse
	err = app.PlatformAPI().CallAppResult("containers", "containers_exec_logs", map[string]any{"execution_id": p.ExecutionID, "tail": intArg(args, "tail", 300)}, &out)
	return out, err
}
func (a *App) toolPreviewStop(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, w, err := previewContext(c, app, args)
	if err != nil {
		return nil, err
	}
	// Expiry or an operator may have already destroyed this preview workspace.
	// Preserve idempotent Stop so Code can restart or delete its repository.
	if w.LifecycleStatus == statusDestroyed {
		return map[string]any{"workspace": w}, nil
	}
	w, err = a.stopWorkspace(app, actor, w, "workspace.stopped")
	return map[string]any{"workspace": w}, err
}
