package main

// Previews use their own Containers execution, independent of the command PTY.
// The execution and its published loopback port survive Workspaces/Code restarts;
// the workspace TTL remains the upper bound on the preview's lifetime.
//
// On an Instances host the port is published on that host's loopback, so the
// preview URL is an Instances SSH tunnel to it. Instances keeps tunnels in
// memory only; every preview refresh reopens (or reuses) it, so a restart of
// Instances heals on the next preview_get or reconcile pass.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Preview struct {
	WorkspaceID   string `json:"workspace_id"`
	ExecutionID   string `json:"execution_id"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	InstanceID    int64  `json:"instance_id,omitempty"`
	TunnelPort    int    `json:"tunnel_port,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	StartedAt     string `json:"started_at"`
	// URL is the platform path people open in their browser: Workspaces
	// proxies it to the preview (handlePreviewProxy). LocalURL is the
	// loopback address on the machine running Workspaces.
	URL      string `json:"url,omitempty"`
	LocalURL string `json:"local_url,omitempty"`
	// AccessToken is the secret part of URL. A new one is made each time the
	// preview starts, so a link stops working when its preview is restarted.
	AccessToken string `json:"-"`
}

func getPreview(db *sql.DB, id string) (*Preview, error) {
	p := &Preview{}
	err := db.QueryRow(`SELECT workspace_id,execution_id,container_port,host_port,instance_id,tunnel_port,status,error,started_at,access_token FROM workspace_previews WHERE workspace_id=?`, id).Scan(&p.WorkspaceID, &p.ExecutionID, &p.ContainerPort, &p.HostPort, &p.InstanceID, &p.TunnelPort, &p.Status, &p.Error, &p.StartedAt, &p.AccessToken)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.setURL()
	return p, nil
}

// setURL points at the tunnel for remote previews and the published port
// for local ones. A remote preview without an open tunnel has no URL.
func (p *Preview) setURL() {
	p.URL, p.LocalURL = "", ""
	if port := p.upstreamPort(); port > 0 {
		p.LocalURL = "http://127.0.0.1:" + strconv.Itoa(port) + "/"
		if p.AccessToken != "" {
			p.URL = previewPublicPath(p.WorkspaceID, p.AccessToken) + "/"
		}
	}
}

// upstreamPort is the loopback port that reaches the preview from here: the
// published port, or the Instances tunnel for a remote host.
func (p *Preview) upstreamPort() int {
	if p.InstanceID > 0 {
		return p.TunnelPort
	}
	return p.HostPort
}

// previewPublicPath is the platform path of a workspace's preview, without a
// trailing slash. The _install form routes every request (including the
// preview's own assets) to this install without a project_id query.
func previewPublicPath(workspaceID, token string) string {
	base := "/api/apps/workspaces"
	if id := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID")); id != "" && id != "0" {
		base += "/_install/" + id
	}
	return base + "/p/" + workspaceID + "/" + token
}

func savePreview(db *sql.DB, p *Preview) error {
	_, err := db.Exec(`INSERT INTO workspace_previews(workspace_id,execution_id,container_port,host_port,instance_id,tunnel_port,status,error,started_at,access_token) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET execution_id=excluded.execution_id,container_port=excluded.container_port,host_port=excluded.host_port,instance_id=excluded.instance_id,tunnel_port=excluded.tunnel_port,status=excluded.status,error=excluded.error,started_at=excluded.started_at,access_token=excluded.access_token`, p.WorkspaceID, p.ExecutionID, p.ContainerPort, p.HostPort, p.InstanceID, p.TunnelPort, p.Status, p.Error, p.StartedAt, p.AccessToken)
	p.setURL()
	return err
}
func (a *App) previewTools() []sdk.Tool {
	id := map[string]any{"workspace_id": strSchema()}
	return []sdk.Tool{
		{Name: "workspace_preview_start", Description: "Start a persistent HTTP preview server (argv) and get a link people can open: url is a path on this Apteva server that proxies to the preview, opens without an Apteva sign-in, changes on each start, and serves pages sandboxed (no cookies or localStorage); local_url is the loopback address on the machine itself. Local workspaces can use any port: pass port (default the default_preview_port setting, 3000) and have the server listen on it. Docker workspaces use the preview_port chosen at create. The server should bind 0.0.0.0 and use relative asset paths: it is served under a path prefix (sent as X-Forwarded-Prefix). Runs until stopped or the workspace TTL expires; returns before readiness.", InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "argv": map[string]any{"type": "array", "items": strSchema()}, "env": map[string]any{"type": "object", "additionalProperties": strSchema()}, "port": map[string]any{"type": "integer", "description": "Port the server listens on. Local workspaces only; Docker workspaces keep their preview_port."}}, []string{"workspace_id", "argv"}), HandlerCtx: a.toolPreviewStart},
		{Name: "workspace_preview_get", Description: "Fetch preview readiness (starting, live, crashed, stopped), its url (open in a browser) and local_url.", InputSchema: schemaObject(id, []string{"workspace_id"}), HandlerCtx: a.toolPreviewGet},
		{Name: "workspace_preview_logs", Description: "Tail bounded container preview logs.", InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "tail": intSchema()}, []string{"workspace_id"}), HandlerCtx: a.toolPreviewLogs},
		{Name: "workspace_preview_stop", Description: "Stop the preview. keep_workspace_running (default true for agents, false for app callers) keeps the workspace container up; otherwise the container stops with its volumes retained.", InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "keep_workspace_running": boolSchema()}, []string{"workspace_id"}), HandlerCtx: a.toolPreviewStop},
	}
}
func previewContext(c context.Context, app *sdk.AppCtx, args map[string]any) (Actor, *Workspace, error) {
	actor, err := actorFrom(c, app)
	if err != nil {
		return actor, nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err == nil {
		err = requireDevWorkspace(w, "preview")
	}
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
	if err = runtimeCall(app, "containers_get", map[string]any{"workload_id": w.WorkloadID}, &runtime); err != nil {
		return nil, err
	}
	p := &Preview{WorkspaceID: w.ID, InstanceID: workloadTarget(runtime.Workload), Status: "starting", StartedAt: nowUTC(), AccessToken: randomHex(16)}
	for _, port := range runtime.Workload.Ports {
		if port.Protocol == "tcp" && port.BindAddr == "127.0.0.1" && port.HostPort > 0 {
			p.ContainerPort = port.ContainerPort
			p.HostPort = port.HostPort
			break
		}
	}
	requested := intArg(args, "port", 0)
	if requested < 0 || requested > 65535 {
		return nil, errors.New("port must be 1-65535")
	}
	if w.Driver == driverLocal {
		// No port mapping on the local driver: any port can be used, so the
		// preview port is just recorded on the workload.
		port := requested
		if port == 0 {
			port = firstPositive(p.ContainerPort, configInt(app, "default_preview_port", 3000))
		}
		if port != p.ContainerPort {
			if err = runtimeCall(app, "containers_port_set", map[string]any{"workload_id": w.WorkloadID, "container_port": port}, &runtime); err != nil {
				return nil, err
			}
		}
		p.ContainerPort, p.HostPort = port, port
	} else if p.HostPort == 0 {
		return nil, errors.New("this Docker workspace has no preview port: Docker publishes ports when the container is created, so create a new workspace with preview_port")
	} else if requested > 0 && requested != p.ContainerPort {
		return nil, fmt.Errorf("this Docker workspace publishes port %d, set at create; listen on %d, or create a new workspace with preview_port %d", p.ContainerPort, p.ContainerPort, requested)
	}
	if p.InstanceID > 0 {
		if p.TunnelPort, err = openPreviewTunnel(app, p.InstanceID, p.HostPort); err != nil {
			return nil, err
		}
	}
	if err = savePreview(app.AppDB(), p); err != nil {
		return nil, err
	}
	var out executionResponse
	err = runtimeCall(app, "containers_exec_start", map[string]any{"workload_id": w.WorkloadID, "argv": argv, "env": args["env"], "working_directory": "/workspace", "umask": "022", "timeout_s": ttl, "idempotency_key": "preview-" + w.ID + "-" + p.StartedAt}, &out)
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
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "preview.started", actor, "Container preview starting", map[string]any{"execution_id": p.ExecutionID, "port": p.HostPort, "instance_id": p.InstanceID, "tunnel_port": p.TunnelPort})
	emitPreviewChanged(app, w, p)
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
	previous := p.Status
	var out executionResponse
	if p.ExecutionID == "" {
		p.Status = "crashed"
		p.Error = "preview start interrupted before execution was recorded"
	} else {
		if err = runtimeCall(app, "containers_exec_get", map[string]any{"execution_id": p.ExecutionID}, &out); err != nil {
			return p, err
		}
		if commandTerminal(out.Execution.Status) {
			p.Status = "crashed"
			p.Error = firstNonEmpty(out.Execution.Error, fmt.Sprintf("preview exited (%s, exit code %v)", out.Execution.Status, out.Execution.ExitCode))
		} else {
			var e error
			if p.InstanceID > 0 {
				// Reuses the open tunnel; reopens it after an Instances restart.
				var port int
				if port, e = openPreviewTunnel(app, p.InstanceID, p.HostPort); e == nil {
					p.TunnelPort = port
					p.setURL()
				} else {
					p.Error = "preview tunnel: " + e.Error()
				}
			}
			var resp *http.Response
			if e == nil {
				resp, e = previewHTTPClient.Get(p.LocalURL)
			}
			if e == nil {
				resp.Body.Close()
				p.Status = "live"
				p.Error = ""
			} else if p.Status == "starting" && time.Since(parseTime(p.StartedAt)) > 10*time.Minute {
				var cancelled executionResponse
				if err = runtimeCall(app, "containers_exec_cancel", map[string]any{"execution_id": p.ExecutionID}, &cancelled); err != nil {
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
	if p.Status != previous {
		runtimeActor := Actor{Kind: "runtime", Label: runtimeActorLabel(w)}
		if p.Status == "live" {
			_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "preview.live", runtimeActor, fmt.Sprintf("Preview live on port %d", p.ContainerPort), map[string]any{"url": p.URL})
		} else if p.Status == "crashed" {
			_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "preview.crashed", runtimeActor, "Preview crashed: "+p.Error, nil)
		}
		emitPreviewChanged(app, w, p)
	}
	return p, nil
}

// emitPreviewChanged lets widgets refresh when a preview starts, goes live,
// crashes or stops. Previews have no other app event.
func emitPreviewChanged(app *sdk.AppCtx, w *Workspace, p *Preview) {
	app.EmitWithProject("workspace.preview.changed", w.ProjectID, map[string]any{"workspace_id": w.ID, "status": p.Status, "url": p.URL})
}
func (a *App) cancelPreview(app *sdk.AppCtx, w *Workspace) error {
	p, err := getPreview(app.AppDB(), w.ID)
	if err != nil || p == nil {
		return err
	}
	if p.ExecutionID != "" && (p.Status == "starting" || p.Status == "live") {
		var out executionResponse
		if err = runtimeCall(app, "containers_exec_cancel", map[string]any{"execution_id": p.ExecutionID}, &out); err != nil {
			return err
		}
	}
	closeTunnel(app, p)
	wasActive := p.Status == "starting" || p.Status == "live"
	p.Status = "stopped"
	p.Error = ""
	if err = savePreview(app.AppDB(), p); err != nil {
		return err
	}
	if wasActive {
		emitPreviewChanged(app, w, p)
	}
	return nil
}

// openPreviewTunnel opens or reuses the Instances tunnel to a port published
// on the remote host's loopback and returns the local port.
func openPreviewTunnel(app *sdk.AppCtx, instanceID int64, hostPort int) (int, error) {
	var out struct {
		LocalPort int `json:"local_port"`
	}
	if err := app.PlatformAPI().CallAppResult("instances", "instance_open_tunnel", map[string]any{"id": instanceID, "target_port": hostPort}, &out); err != nil {
		return 0, fmt.Errorf("remote preview needs the Instances app: %w", err)
	}
	if out.LocalPort <= 0 {
		return 0, errors.New("Instances returned no tunnel port")
	}
	return out.LocalPort, nil
}

// closeTunnel is best effort: Instances drops all tunnels on restart anyway.
func closeTunnel(app *sdk.AppCtx, p *Preview) {
	if p == nil || p.InstanceID <= 0 || p.HostPort <= 0 || p.TunnelPort <= 0 {
		return
	}
	var out map[string]any
	_ = app.PlatformAPI().CallAppResult("instances", "instance_close_tunnel", map[string]any{"id": p.InstanceID, "target_port": p.HostPort}, &out)
	p.TunnelPort = 0
}

// closePreviewTunnel closes a destroyed workspace's tunnel.
func (a *App) closePreviewTunnel(app *sdk.AppCtx, w *Workspace) {
	p, err := getPreview(app.AppDB(), w.ID)
	if err != nil || p == nil || p.TunnelPort <= 0 {
		return
	}
	closeTunnel(app, p)
	_ = savePreview(app.AppDB(), p)
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
	err = runtimeCall(app, "containers_exec_logs", map[string]any{"execution_id": p.ExecutionID, "tail": intArg(args, "tail", 300)}, &out)
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
	// App callers (Code) own the whole preview workspace and expect Stop to
	// stop its container. Agents usually keep working in the same workspace,
	// so for them Stop only ends the preview process unless told otherwise.
	keep := actor.InstallID <= 0
	if _, ok := args["keep_workspace_running"]; ok {
		keep = boolArg(args, "keep_workspace_running")
	}
	if keep {
		unlock := a.lockWorkspace(w.ID)
		err = a.cancelPreview(app, w)
		unlock()
		if err != nil {
			return nil, err
		}
		_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "preview.stopped", actor, "Preview stopped; workspace still running", nil)
		p, _ := getPreview(app.AppDB(), w.ID)
		return map[string]any{"workspace": w, "preview": p}, nil
	}
	w, err = a.stopWorkspace(app, actor, w, "workspace.stopped")
	return map[string]any{"workspace": w}, err
}

func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}
