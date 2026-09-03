import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  projectId: string;
  installId: number;
}

interface Command {
  id: string;
  display_command: string;
  working_directory: string;
  actor_label: string;
  status: string;
  exit_code?: number;
  output_truncated?: boolean;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

interface Workspace {
  id: string;
  name: string;
  purpose: string;
  profile: string;
  image: string;
  workload_id: string;
  lifecycle_status: string;
  activity_status: string;
  status: string;
  runtime_status: string;
  health_status: string;
  host_label: string;
  network_policy: string;
  cpu: number;
  memory_mb: number;
  consumer_app: string;
  owner_agent_id?: number;
  owner_thread_id?: string;
  owner_label: string;
  resource_kind?: string;
  resource_id?: string;
  repo_label?: string;
  branch_label?: string;
  origin_label?: string;
  origin_href?: string;
  dirty_state: string;
  unpushed_state: string;
  last_error: string;
  created_at: string;
  updated_at: string;
  last_activity_at: string;
  expires_at: string;
  delete_at: string;
  storage_bytes?: number;
  current_command?: Command;
}

interface Runtime {
  id: string;
  name: string;
  image: string;
  status: string;
  health_status: string;
  public_url?: string;
  resources?: { cpu?: number; memory_mb?: number };
  ports?: Array<{ protocol: string; container_port: number; host_port: number; bind_addr: string }>;
  volumes?: Array<{ name: string; mount_path: string; size_bytes?: number }>;
}

interface Activity {
  id: number;
  event_type: string;
  actor_label: string;
  summary: string;
  created_at: string;
}

interface Detail {
  workspace: Workspace;
  runtime?: Runtime;
  commands: Command[];
  activity: Activity[];
  destroy_risk?: string;
  runtime_error?: string;
}

interface Profile {
  key: string;
  label: string;
  image: string;
  available: boolean;
}

type Tab = "overview" | "commands" | "runtime" | "activity";

const API = "/api/apps/workspaces/api";

async function request<T>(projectId: string, installId: number, path: string, init?: RequestInit): Promise<T> {
  const separator = path.includes("?") ? "&" : "?";
  const scope = new URLSearchParams({ project_id: projectId, install_id: String(installId) });
  const response = await fetch(`${API}${path}${separator}${scope.toString()}`, {
    credentials: "same-origin",
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
  });
  if (!response.ok) throw new Error((await response.text().catch(() => "")) || `${response.status}`);
  return (await response.json()) as T;
}

function relativeTime(value: string): string {
  const time = new Date(value).getTime();
  if (!Number.isFinite(time)) return "—";
  const seconds = Math.round((time - Date.now()) / 1000);
  const abs = Math.abs(seconds);
  if (abs < 60) return seconds >= 0 ? "in <1m" : "just now";
  const minutes = Math.round(abs / 60);
  if (minutes < 60) return seconds >= 0 ? `in ${minutes}m` : `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return seconds >= 0 ? `in ${hours}h` : `${hours}h ago`;
  const days = Math.round(hours / 24);
  return seconds >= 0 ? `in ${days}d` : `${days}d ago`;
}

function exactTime(value: string): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : value;
}

function duration(command: Command): string {
  if (!command.started_at) return "—";
  const start = new Date(command.started_at).getTime();
  const end = command.finished_at ? new Date(command.finished_at).getTime() : Date.now();
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "—";
  const seconds = Math.max(0, Math.round((end - start) / 1000));
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

function formatBytes(value?: number): string {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let current = value;
  let index = 0;
  while (current >= 1024 && index < units.length - 1) {
    current /= 1024;
    index += 1;
  }
  return `${current >= 10 || index === 0 ? current.toFixed(0) : current.toFixed(1)} ${units[index]}`;
}

function statusLabel(status: string): string {
  return status ? status.replaceAll("_", " ") : "unknown";
}

function commandMark(status: string): string {
  if (status === "succeeded") return "✓";
  if (status === "failed" || status === "timed_out") return "×";
  if (status === "cancelled") return "○";
  if (status === "running" || status === "queued" || status === "cancelling") return "●";
  return "·";
}

export default function WorkspacesPanel({ projectId, installId }: NativePanelProps) {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [summary, setSummary] = useState<Record<string, number>>({});
  const [selectedId, setSelectedId] = useState("");
  const [detail, setDetail] = useState<Detail | null>(null);
  const [tab, setTab] = useState<Tab>("overview");
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [defaultProfile, setDefaultProfile] = useState("go");
  const [showCreate, setShowCreate] = useState(false);
  const [showDestroy, setShowDestroy] = useState(false);
  const [logs, setLogs] = useState<{ command: Command; body: string } | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [commandText, setCommandText] = useState("");
  const [create, setCreate] = useState({ name: "", purpose: "", profile: "go", ttl_minutes: 120 });

  const loadList = useCallback(async () => {
    try {
      const response = await request<{ workspaces: Workspace[]; summary: Record<string, number> }>(projectId, installId, "/workspaces");
      setWorkspaces(response.workspaces || []);
      setSummary(response.summary || {});
      setError("");
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [projectId, installId]);

  const loadDetail = useCallback(async (id: string, quiet = false) => {
    if (!id) return;
    try {
      const response = await request<Detail>(projectId, installId, `/workspaces/${encodeURIComponent(id)}`);
      setDetail(response);
      if (!quiet) setError("");
    } catch (reason) {
      if (!quiet) setError((reason as Error).message);
    }
  }, [projectId, installId]);

  useEffect(() => {
    loadList();
    request<{ profiles: Profile[]; default: string }>(projectId, installId, "/profiles")
      .then((response) => {
        setProfiles(response.profiles || []);
        setDefaultProfile(response.default || "go");
        setCreate((current) => ({ ...current, profile: response.default || "go" }));
      })
      .catch(() => undefined);
    const timer = window.setInterval(loadList, 5000);
    return () => window.clearInterval(timer);
  }, [loadList]);

  useEffect(() => {
    if (!selectedId) return;
    loadDetail(selectedId);
    const timer = window.setInterval(() => loadDetail(selectedId, true), 2500);
    return () => window.clearInterval(timer);
  }, [selectedId, loadDetail]);

  const selected = detail?.workspace;
  const activeCommand = useMemo(
    () => detail?.commands?.find((command) => ["queued", "running", "cancelling"].includes(command.status)),
    [detail],
  );

  const createWorkspace = async () => {
    setBusy("create");
    setError("");
    try {
      const response = await request<{ workspace: Workspace }>(projectId, installId, "/workspaces", { method: "POST", body: JSON.stringify(create) });
      setShowCreate(false);
      setCreate({ name: "", purpose: "", profile: defaultProfile, ttl_minutes: 120 });
      await loadList();
      setSelectedId(response.workspace.id);
      setTab("overview");
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy("");
    }
  };

  const workspaceAction = async (action: "stop" | "resume") => {
    if (!selected) return;
    setBusy(action);
    setError("");
    try {
      await request(projectId, installId, `/workspaces/${encodeURIComponent(selected.id)}/${action}`, { method: "POST" });
      await Promise.all([loadDetail(selected.id), loadList()]);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy("");
    }
  };

  const extend = async (ttl = 120) => {
    if (!selected) return;
    setBusy("extend");
    setError("");
    try {
      await request(projectId, installId, `/workspaces/${encodeURIComponent(selected.id)}/extend`, { method: "POST", body: JSON.stringify({ ttl_minutes: ttl }) });
      await Promise.all([loadDetail(selected.id), loadList()]);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy("");
    }
  };

  const destroy = async () => {
    if (!selected) return;
    setBusy("destroy");
    setError("");
    try {
      await request(projectId, installId, `/workspaces/${encodeURIComponent(selected.id)}?confirm=1`, { method: "DELETE" });
      setShowDestroy(false);
      setSelectedId("");
      setDetail(null);
      await loadList();
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy("");
    }
  };

  const startCommand = async () => {
    if (!selected || !commandText.trim()) return;
    setBusy("command");
    setError("");
    try {
      await request(projectId, installId, `/workspaces/${encodeURIComponent(selected.id)}/commands`, {
        method: "POST",
        body: JSON.stringify({ shell_command: commandText.trim() }),
      });
      setCommandText("");
      await Promise.all([loadDetail(selected.id), loadList()]);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy("");
    }
  };

  const cancelCommand = async (command: Command) => {
    if (!selected) return;
    setBusy(`cancel:${command.id}`);
    setError("");
    try {
      await request(projectId, installId, `/workspaces/${encodeURIComponent(selected.id)}/commands/${encodeURIComponent(command.id)}/cancel`, { method: "POST" });
      await loadDetail(selected.id);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy("");
    }
  };

  const openLogs = async (command: Command) => {
    if (!selected) return;
    setBusy(`logs:${command.id}`);
    try {
      const response = await request<{ logs: string }>(projectId, installId, `/workspaces/${encodeURIComponent(selected.id)}/commands/${encodeURIComponent(command.id)}/logs?tail=1000`);
      setLogs({ command, body: response.logs || "" });
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="ws-root">
      <style>{CSS}</style>
      {!selectedId ? (
        <>
          <header className="ws-header">
            <div>
              <h1>Workspaces</h1>
              <p>Development environments, commands, ownership and expiry</p>
            </div>
            <button className="ws-primary" onClick={() => setShowCreate(true)}>+ New workspace</button>
          </header>
          {error && <div className="ws-error">{error}</div>}
          <section className="ws-summary">
            <Summary label="Running" value={summary.running || 0} tone="green" />
            <Summary label="Busy" value={summary.busy || 0} tone="blue" />
            <Summary label="Suspended" value={summary.suspended || 0} tone="amber" />
            <Summary label="Failed" value={summary.failed || 0} tone="red" />
            <div className="ws-summary-resource"><span>Tracked capacity</span><b>{workspaces.reduce((sum, workspace) => sum + (workspace.cpu || 0), 0)} CPU · {Math.round(workspaces.reduce((sum, workspace) => sum + (workspace.memory_mb || 0), 0) / 1024)} GB</b></div>
          </section>
          <main className="ws-list-shell">
            <div className="ws-table ws-table-head">
              <span>Name</span><span>Created by</span><span>Owner</span><span>Status</span><span>Command</span><span>Expires</span>
            </div>
            {workspaces.length === 0 ? (
              <div className="ws-empty"><b>No workspaces are running</b><span>Create an approved local Docker workspace for an agent or operator.</span></div>
            ) : workspaces.map((workspace) => (
              <button className="ws-table ws-table-row" key={workspace.id} onClick={() => setSelectedId(workspace.id)}>
                <span className="ws-name"><b>{workspace.name}</b><small>{workspace.repo_label || workspace.purpose || workspace.id}</small></span>
                <span>{workspace.consumer_app}</span>
                <span>{workspace.owner_label || "Unassigned"}</span>
                <span><Status value={workspace.status} /></span>
                <span className="ws-command-cell">{workspace.current_command?.display_command || "—"}</span>
                <span className={new Date(workspace.expires_at).getTime() < Date.now() ? "ws-red" : ""}>{relativeTime(workspace.expires_at)}</span>
              </button>
            ))}
          </main>
        </>
      ) : !selected ? (
        <div className="ws-loading">Loading workspace…</div>
      ) : (
        <>
          <header className="ws-header ws-detail-header">
            <button className="ws-back" onClick={() => { setSelectedId(""); setDetail(null); }}>← Workspaces</button>
            <div className="ws-detail-title"><h1>{selected.name}</h1><Status value={selected.status} /></div>
            <div className="ws-header-actions">
              {selected.origin_href && <a className="ws-button" href={selected.origin_href}>{selected.origin_label ? `Open in ${selected.origin_label}` : "Open originating app"}</a>}
              <button className="ws-button" disabled={!!busy} onClick={() => extend(120)}>Extend 2h</button>
              {selected.lifecycle_status === "running" ? (
                <button className="ws-button" disabled={!!busy} onClick={() => workspaceAction("stop")}>Stop</button>
              ) : selected.lifecycle_status === "suspended" || selected.lifecycle_status === "failed" ? (
                <button className="ws-button" disabled={!!busy} onClick={() => workspaceAction("resume")}>Resume</button>
              ) : null}
              <button className="ws-danger-outline" disabled={!!busy} onClick={() => setShowDestroy(true)}>Destroy</button>
            </div>
          </header>
          {error && <div className="ws-error">{error}</div>}
          <nav className="ws-tabs">
            {(["overview", "commands", "runtime", "activity"] as Tab[]).map((value) => (
              <button key={value} data-active={tab === value} onClick={() => setTab(value)}>{value}</button>
            ))}
          </nav>
          <main className="ws-detail-main">
            {tab === "overview" && <Overview workspace={selected} runtime={detail?.runtime} runtimeError={detail?.runtime_error} />}
            {tab === "commands" && (
              <section className="ws-card ws-command-card">
                <div className="ws-card-head"><div><h2>Terminal</h2><p>Commands share one persistent PTY shell. The current directory, exported variables, installed tools, files, and services carry into later commands.</p></div></div>
                <div className="ws-command-compose">
                  <span className="ws-prompt">$</span>
                  <input value={commandText} onChange={(event) => setCommandText(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") startCommand(); }} placeholder="go test ./..." disabled={selected.lifecycle_status !== "running" || !!activeCommand} />
                  <button className="ws-primary" disabled={!commandText.trim() || !!activeCommand || selected.lifecycle_status !== "running" || !!busy} onClick={startCommand}>{busy === "command" ? "Starting…" : "Run"}</button>
                </div>
                {activeCommand && <div className="ws-active-note">One command is active. Cancel it before starting another.</div>}
                <div className="ws-command-list">
                  {(detail?.commands || []).length === 0 ? <div className="ws-empty compact">No commands yet.</div> : detail?.commands.map((command) => (
                    <div className="ws-command-row" key={command.id}>
                      <button className={`ws-command-mark status-${command.status}`} onClick={() => openLogs(command)}>{commandMark(command.status)}</button>
                      <button className="ws-command-main" onClick={() => openLogs(command)}><code>{command.display_command}</code><small>{command.actor_label || "Unknown actor"} · {duration(command)} · {statusLabel(command.status)}{command.exit_code !== undefined ? ` · exit ${command.exit_code}` : ""}</small></button>
                      {["queued", "running", "cancelling"].includes(command.status) && <button className="ws-danger-link" disabled={!!busy} onClick={() => cancelCommand(command)}>Cancel</button>}
                    </div>
                  ))}
                </div>
              </section>
            )}
            {tab === "runtime" && <RuntimeView workspace={selected} runtime={detail?.runtime} />}
            {tab === "activity" && <ActivityView rows={detail?.activity || []} />}
          </main>
        </>
      )}

      {showCreate && (
        <div className="ws-modal-backdrop" onMouseDown={() => !busy && setShowCreate(false)}>
          <div className="ws-modal" onMouseDown={(event) => event.stopPropagation()}>
            <div className="ws-modal-head"><div><h2>New workspace</h2><p>Local Docker · approved profiles only</p></div><button className="ws-close" onClick={() => setShowCreate(false)}>×</button></div>
            <label>Name<input autoFocus value={create.name} onChange={(event) => setCreate({ ...create, name: event.target.value })} placeholder="code-apteva-42" /></label>
            <label>Purpose<input value={create.purpose} onChange={(event) => setCreate({ ...create, purpose: event.target.value })} placeholder="Develop apteva/apps" /></label>
            <div className="ws-form-grid">
              <label>Runtime profile<select value={create.profile} onChange={(event) => setCreate({ ...create, profile: event.target.value })}>{profiles.filter((profile) => profile.available).map((profile) => <option key={profile.key} value={profile.key}>{profile.label}</option>)}</select></label>
              <label>TTL (minutes)<input type="number" min={1} max={480} value={create.ttl_minutes} onChange={(event) => setCreate({ ...create, ttl_minutes: Number(event.target.value) })} /></label>
            </div>
            <div className="ws-profile-note">Image: <code>{profiles.find((profile) => profile.key === create.profile)?.image || "Not configured"}</code><br />Network policy: isolated Docker network with outbound access; no host bind mounts.</div>
            <div className="ws-modal-actions"><button className="ws-button" onClick={() => setShowCreate(false)}>Cancel</button><button className="ws-primary" disabled={!create.name.trim() || !!busy} onClick={createWorkspace}>{busy === "create" ? "Provisioning…" : "Create workspace"}</button></div>
          </div>
        </div>
      )}

      {showDestroy && selected && (
        <div className="ws-modal-backdrop" onMouseDown={() => !busy && setShowDestroy(false)}>
          <div className="ws-modal ws-destroy-modal" onMouseDown={(event) => event.stopPropagation()}>
            <div className="ws-modal-head"><div><h2>Destroy {selected.name}?</h2><p>This permanently removes the container and both volumes.</p></div><button className="ws-close" onClick={() => setShowDestroy(false)}>×</button></div>
            <div className="ws-warning"><b>Source safety</b><span>{detail?.destroy_risk || "Workspaces cannot verify uncommitted or unpushed Git changes."}</span></div>
            <div className="ws-modal-actions"><button className="ws-button" onClick={() => setShowDestroy(false)}>Cancel</button><button className="ws-danger" disabled={!!busy} onClick={destroy}>{busy === "destroy" ? "Destroying…" : "Destroy permanently"}</button></div>
          </div>
        </div>
      )}

      {logs && (
        <div className="ws-log-drawer">
          <div className="ws-log-head"><div><b>{logs.command.display_command}</b><span>{statusLabel(logs.command.status)} · {duration(logs.command)}</span></div><button className="ws-close" onClick={() => setLogs(null)}>×</button></div>
          <pre>{logs.body || "(no output)"}</pre>
          {logs.command.output_truncated && <div className="ws-truncated">Output was truncated by the runtime retention limit.</div>}
        </div>
      )}
    </div>
  );
}

function Summary({ label, value, tone }: { label: string; value: number; tone: string }) {
  return <div className="ws-summary-item"><i className={tone} /><span>{label}</span><b>{value}</b></div>;
}

function Status({ value }: { value: string }) {
  return <span className={`ws-status ws-status-${value || "unknown"}`}><i />{statusLabel(value)}</span>;
}

function Overview({ workspace, runtime, runtimeError }: { workspace: Workspace; runtime?: Runtime; runtimeError?: string }) {
  return (
    <div className="ws-overview-grid">
      <section className="ws-card">
        <div className="ws-card-head"><div><h2>Overview</h2><p>{workspace.purpose || "No purpose supplied"}</p></div></div>
        <dl className="ws-kv">
          <Row label="Created by" value={workspace.consumer_app} />
          <Row label="Owner" value={workspace.owner_label || "Unassigned"} sub={workspace.owner_thread_id} />
          <Row label="Repository" value={workspace.repo_label || "Not linked"} sub={workspace.branch_label} mono />
          <Row label="Profile" value={workspace.profile} sub={workspace.image} mono />
          <Row label="Host" value={workspace.host_label} sub={workspace.workload_id} mono />
          <Row label="Created" value={exactTime(workspace.created_at)} />
          <Row label="Expires" value={`${relativeTime(workspace.expires_at)} · ${exactTime(workspace.expires_at)}`} />
          <Row label="Auto-delete" value={`${relativeTime(workspace.delete_at)} · ${exactTime(workspace.delete_at)}`} />
        </dl>
        {(workspace.last_error || runtimeError) && <div className="ws-inline-error">{workspace.last_error || runtimeError}</div>}
      </section>
      <section className="ws-card">
        <div className="ws-card-head"><div><h2>Safety and resources</h2><p>The applied runtime boundary, not raw Docker controls.</p></div></div>
        <dl className="ws-kv">
          <Row label="Runtime" value={runtime?.status || workspace.runtime_status || "Unknown"} sub={runtime?.health_status || workspace.health_status} />
          <Row label="Limits" value={`${workspace.cpu} CPU · ${workspace.memory_mb} MB RAM`} />
          <Row label="Storage" value={formatBytes(workspace.storage_bytes)} sub="/workspace + /cache" />
          <Row label="Network" value={workspace.network_policy} sub="Outbound enabled · inbound ports explicit" />
          <Row label="Uncommitted" value={workspace.dirty_state} />
          <Row label="Unpushed" value={workspace.unpushed_state} />
        </dl>
      </section>
    </div>
  );
}

function RuntimeView({ workspace, runtime }: { workspace: Workspace; runtime?: Runtime }) {
  return (
    <div className="ws-overview-grid">
      <section className="ws-card">
        <div className="ws-card-head"><div><h2>Runtime</h2><p>Containers workload information</p></div></div>
        <dl className="ws-kv">
          <Row label="Workload" value={workspace.workload_id || "Not provisioned"} mono />
          <Row label="Status" value={runtime?.status || workspace.runtime_status || "Unknown"} sub={runtime?.health_status || workspace.health_status} />
          <Row label="Image" value={runtime?.image || workspace.image} mono />
          <Row label="Host" value={workspace.host_label} />
          <Row label="Limits" value={`${runtime?.resources?.cpu || workspace.cpu} CPU · ${runtime?.resources?.memory_mb || workspace.memory_mb} MB`} />
          <Row label="Network" value={workspace.network_policy} />
        </dl>
      </section>
      <section className="ws-card">
        <div className="ws-card-head"><div><h2>Files and storage</h2><p>Durable volumes shared by every command</p></div></div>
        <div className="ws-volume-list">
          {(runtime?.volumes || [{ name: "workspace", mount_path: "/workspace" }, { name: "cache", mount_path: "/cache" }]).map((volume) => (
            <div key={volume.name}><b>{volume.name}</b><code>{volume.mount_path}</code><span>{formatBytes(volume.size_bytes)}</span></div>
          ))}
        </div>
        <div className="ws-profile-note">Archive import/export exists for originating apps. A file editor and Git actions belong in Code.</div>
      </section>
    </div>
  );
}

function ActivityView({ rows }: { rows: Activity[] }) {
  return (
    <section className="ws-card">
      <div className="ws-card-head"><div><h2>Activity</h2><p>Workspace lifecycle and command audit trail</p></div></div>
      <div className="ws-timeline">
        {rows.length === 0 ? <div className="ws-empty compact">No activity yet.</div> : rows.map((row) => (
          <div key={row.id}><time>{new Date(row.created_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</time><i /><span><b>{row.summary}</b><small>{row.actor_label || row.event_type}</small></span></div>
        ))}
      </div>
    </section>
  );
}

function Row({ label, value, sub, mono = false }: { label: string; value: string; sub?: string; mono?: boolean }) {
  return <div><dt>{label}</dt><dd className={mono ? "mono" : ""}>{value}{sub && <small>{sub}</small>}</dd></div>;
}

const CSS = `
.ws-root,.ws-root *{box-sizing:border-box}.ws-root{height:100%;min-height:0;display:flex;flex-direction:column;background:var(--color-bg,#0b0d10);color:var(--color-text,#e7ebef);font-family:var(--font-base,Inter,ui-sans-serif,sans-serif);overflow:hidden}.ws-root button,.ws-root input,.ws-root select{font:inherit}.ws-header{min-height:72px;display:flex;align-items:center;justify-content:space-between;gap:16px;padding:14px 22px;border-bottom:1px solid var(--color-border,#292e35);background:var(--color-bg,#0b0d10)}.ws-header h1{margin:0;font-size:19px;font-weight:650}.ws-header p,.ws-card-head p,.ws-modal-head p{margin:4px 0 0;color:var(--color-text-muted,#89939f);font-size:11px}.ws-primary,.ws-danger,.ws-button,.ws-danger-outline{border-radius:6px;padding:7px 11px;font-size:11px;cursor:pointer;text-decoration:none}.ws-primary{border:1px solid var(--color-accent,#5bc0eb);background:var(--color-accent,#5bc0eb);color:#071016;font-weight:700}.ws-button{border:1px solid var(--color-border,#303640);background:transparent;color:var(--color-text,#e7ebef)}.ws-danger{border:1px solid var(--color-red,#ef5f67);background:var(--color-red,#ef5f67);color:white;font-weight:650}.ws-danger-outline{border:1px solid color-mix(in srgb,var(--color-red,#ef5f67) 65%,transparent);background:transparent;color:var(--color-red,#ef5f67)}.ws-root button:disabled{opacity:.42;cursor:not-allowed}.ws-error{margin:10px 22px 0;padding:9px 11px;border:1px solid color-mix(in srgb,var(--color-red,#ef5f67) 38%,transparent);border-radius:6px;background:color-mix(in srgb,var(--color-red,#ef5f67) 10%,transparent);color:var(--color-red,#ef5f67);font-size:11px;white-space:pre-wrap}.ws-summary{display:flex;align-items:center;gap:0;padding:12px 22px;border-bottom:1px solid var(--color-border,#292e35)}.ws-summary-item{display:flex;align-items:center;gap:7px;padding-right:18px;margin-right:18px;border-right:1px solid var(--color-border,#292e35);font-size:11px;color:var(--color-text-muted,#89939f)}.ws-summary-item i{width:7px;height:7px;border-radius:50%;background:#6b7280}.ws-summary-item i.green{background:#2fc77b}.ws-summary-item i.blue{background:#5bc0eb}.ws-summary-item i.amber{background:#e7a93d}.ws-summary-item i.red{background:#ef5f67}.ws-summary-item b{color:var(--color-text,#e7ebef);font-size:13px}.ws-summary-resource{margin-left:auto;display:flex;gap:9px;font-size:10px;color:var(--color-text-muted,#89939f)}.ws-summary-resource b{color:var(--color-text,#e7ebef);font-weight:550}.ws-list-shell{margin:18px 22px;min-height:0;overflow:auto;border:1px solid var(--color-border,#292e35);border-radius:7px;background:var(--color-bg-card,#11151a)}.ws-table{display:grid;grid-template-columns:minmax(190px,1.5fr) minmax(90px,.7fr) minmax(110px,.8fr) 100px minmax(150px,1fr) 90px;gap:12px;align-items:center;padding:11px 14px}.ws-table-head{position:sticky;top:0;z-index:2;background:var(--color-bg-input,#171c22);color:var(--color-text-dim,#697481);font-size:9px;text-transform:uppercase;letter-spacing:.08em;border-bottom:1px solid var(--color-border,#292e35)}.ws-table-row{width:100%;border:0;border-bottom:1px solid var(--color-border,#292e35);background:transparent;color:var(--color-text-muted,#a3abb5);text-align:left;font-size:11px;cursor:pointer}.ws-table-row:hover{background:var(--color-bg-input,#171c22)}.ws-table-row:last-child{border-bottom:0}.ws-name{min-width:0;display:flex;flex-direction:column}.ws-name b{font-size:12px;color:var(--color-text,#e7ebef);font-weight:600}.ws-name small{margin-top:3px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--color-text-dim,#697481)}.ws-command-cell{font-family:var(--font-mono-fixed,ui-monospace,monospace);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.ws-status{display:inline-flex;align-items:center;gap:6px;text-transform:capitalize;font-size:10px;color:var(--color-text-muted,#a3abb5)}.ws-status i{width:7px;height:7px;border-radius:50%;background:#6b7280}.ws-status-running i{background:#2fc77b}.ws-status-busy i{background:#5bc0eb;box-shadow:0 0 0 3px color-mix(in srgb,#5bc0eb 15%,transparent)}.ws-status-provisioning i{background:#5bc0eb}.ws-status-suspended i,.ws-status-expired i{background:#e7a93d}.ws-status-failed i{background:#ef5f67}.ws-red{color:var(--color-red,#ef5f67)!important}.ws-empty{min-height:180px;display:flex;align-items:center;justify-content:center;flex-direction:column;gap:6px;color:var(--color-text-muted,#89939f);font-size:11px}.ws-empty b{color:var(--color-text,#e7ebef);font-size:13px}.ws-empty.compact{min-height:80px}.ws-loading{display:grid;place-items:center;flex:1;color:var(--color-text-muted,#89939f);font-size:12px}.ws-detail-header{justify-content:flex-start}.ws-back{border:0;background:transparent;color:var(--color-text-muted,#89939f);cursor:pointer;font-size:11px;padding:6px 4px}.ws-detail-title{display:flex;align-items:center;gap:12px}.ws-detail-title h1{font-size:16px}.ws-header-actions{margin-left:auto;display:flex;align-items:center;gap:7px}.ws-tabs{height:42px;display:flex;align-items:flex-end;gap:18px;padding:0 22px;border-bottom:1px solid var(--color-border,#292e35)}.ws-tabs button{height:42px;border:0;border-bottom:2px solid transparent;background:transparent;color:var(--color-text-muted,#89939f);padding:0 1px;text-transform:capitalize;font-size:11px;cursor:pointer}.ws-tabs button[data-active=true]{border-bottom-color:var(--color-accent,#5bc0eb);color:var(--color-text,#e7ebef)}.ws-detail-main{flex:1;min-height:0;overflow:auto;padding:18px 22px}.ws-overview-grid{display:grid;grid-template-columns:minmax(360px,1fr) minmax(320px,.75fr);gap:14px}.ws-card{border:1px solid var(--color-border,#292e35);border-radius:7px;background:var(--color-bg-card,#11151a);overflow:hidden}.ws-card-head{padding:14px 16px;border-bottom:1px solid var(--color-border,#292e35)}.ws-card-head h2,.ws-modal-head h2{margin:0;font-size:13px;font-weight:650}.ws-kv{margin:0;padding:5px 16px 12px}.ws-kv>div{display:grid;grid-template-columns:120px minmax(0,1fr);gap:12px;padding:10px 0;border-bottom:1px solid color-mix(in srgb,var(--color-border,#292e35) 65%,transparent)}.ws-kv>div:last-child{border:0}.ws-kv dt{color:var(--color-text-dim,#697481);font-size:10px}.ws-kv dd{margin:0;font-size:11px;overflow-wrap:anywhere}.ws-kv dd.mono{font-family:var(--font-mono-fixed,ui-monospace,monospace)}.ws-kv dd small{display:block;margin-top:3px;color:var(--color-text-muted,#89939f);font:9px var(--font-mono-fixed,ui-monospace,monospace)}.ws-inline-error{margin:0 16px 14px;padding:9px;background:color-mix(in srgb,var(--color-red,#ef5f67) 10%,transparent);color:var(--color-red,#ef5f67);border-radius:5px;font-size:10px}.ws-command-card{max-width:1100px}.ws-command-compose{display:grid;grid-template-columns:auto minmax(0,1fr) auto;gap:8px;align-items:center;padding:12px 14px;border-bottom:1px solid var(--color-border,#292e35);background:#090c0f}.ws-prompt{font:12px var(--font-mono-fixed,ui-monospace,monospace);color:var(--color-accent,#5bc0eb)}.ws-command-compose input,.ws-modal input,.ws-modal select{width:100%;border:1px solid var(--color-border,#303640);border-radius:5px;background:var(--color-bg-input,#171c22);color:var(--color-text,#e7ebef);padding:8px 9px;outline:none;font-size:11px}.ws-command-compose input{border:0;background:transparent;font-family:var(--font-mono-fixed,ui-monospace,monospace)}.ws-command-compose input:focus,.ws-modal input:focus,.ws-modal select:focus{border-color:var(--color-accent,#5bc0eb)}.ws-active-note{padding:8px 14px;background:color-mix(in srgb,var(--color-accent,#5bc0eb) 8%,transparent);color:var(--color-text-muted,#89939f);font-size:10px}.ws-command-row{display:grid;grid-template-columns:28px minmax(0,1fr) auto;align-items:center;gap:8px;padding:10px 14px;border-bottom:1px solid var(--color-border,#292e35)}.ws-command-row:last-child{border:0}.ws-command-mark,.ws-command-main{border:0;background:transparent;cursor:pointer}.ws-command-mark{font-size:14px;color:var(--color-text-muted,#89939f)}.ws-command-mark.status-succeeded{color:#2fc77b}.ws-command-mark.status-failed,.ws-command-mark.status-timed_out{color:#ef5f67}.ws-command-mark.status-running,.ws-command-mark.status-queued{color:#5bc0eb}.ws-command-main{text-align:left;min-width:0;color:var(--color-text,#e7ebef)}.ws-command-main code{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font:11px var(--font-mono-fixed,ui-monospace,monospace)}.ws-command-main small{display:block;margin-top:4px;color:var(--color-text-dim,#697481);font-size:9px}.ws-danger-link{border:0;background:transparent;color:var(--color-red,#ef5f67);font-size:10px;cursor:pointer}.ws-volume-list{padding:8px 16px}.ws-volume-list>div{display:grid;grid-template-columns:100px minmax(0,1fr) auto;gap:10px;padding:11px 0;border-bottom:1px solid var(--color-border,#292e35);font-size:10px}.ws-volume-list>div:last-child{border:0}.ws-volume-list code{color:var(--color-text-muted,#89939f)}.ws-volume-list span{color:var(--color-text-muted,#89939f)}.ws-profile-note{margin:8px 16px 15px;padding:10px;border:1px solid var(--color-border,#303640);border-radius:5px;background:var(--color-bg-input,#171c22);color:var(--color-text-muted,#89939f);font-size:10px;line-height:1.55}.ws-profile-note code{color:var(--color-text,#e7ebef);font-family:var(--font-mono-fixed,ui-monospace,monospace)}.ws-timeline{padding:10px 16px}.ws-timeline>div{display:grid;grid-template-columns:48px 12px minmax(0,1fr);gap:9px;min-height:52px}.ws-timeline time{font:9px var(--font-mono-fixed,ui-monospace,monospace);color:var(--color-text-dim,#697481);padding-top:2px}.ws-timeline i{position:relative;width:7px;height:7px;margin-top:3px;border-radius:50%;background:var(--color-accent,#5bc0eb)}.ws-timeline i:after{content:"";position:absolute;left:3px;top:10px;width:1px;height:35px;background:var(--color-border,#303640)}.ws-timeline>div:last-child i:after{display:none}.ws-timeline span b{display:block;font-size:10px;font-weight:550}.ws-timeline span small{display:block;margin-top:4px;color:var(--color-text-dim,#697481);font-size:9px}.ws-modal-backdrop{position:fixed;inset:0;z-index:90;display:grid;place-items:center;padding:20px;background:rgba(0,0,0,.68)}.ws-modal{width:min(100%,510px);padding:16px;border:1px solid var(--color-border,#303640);border-radius:8px;background:var(--color-bg-card,#11151a);box-shadow:0 24px 80px rgba(0,0,0,.5)}.ws-modal-head{display:flex;justify-content:space-between;gap:15px;margin-bottom:15px}.ws-close{border:0;background:transparent;color:var(--color-text-muted,#89939f);font-size:20px;cursor:pointer}.ws-modal label{display:flex;flex-direction:column;gap:6px;margin-bottom:12px;color:var(--color-text-muted,#89939f);font-size:10px}.ws-form-grid{display:grid;grid-template-columns:1fr 150px;gap:10px}.ws-modal-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:16px}.ws-warning{display:flex;flex-direction:column;gap:6px;padding:12px;border:1px solid color-mix(in srgb,var(--color-red,#ef5f67) 35%,transparent);border-radius:6px;background:color-mix(in srgb,var(--color-red,#ef5f67) 8%,transparent);font-size:10px}.ws-warning span{color:var(--color-text-muted,#89939f);line-height:1.5}.ws-log-drawer{position:fixed;z-index:100;right:0;top:0;bottom:0;width:min(720px,80vw);display:flex;flex-direction:column;border-left:1px solid var(--color-border,#303640);background:#080a0c;box-shadow:-20px 0 60px rgba(0,0,0,.45)}.ws-log-head{display:flex;justify-content:space-between;gap:12px;padding:13px 15px;border-bottom:1px solid var(--color-border,#303640);background:var(--color-bg-card,#11151a)}.ws-log-head b{display:block;max-width:620px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font:11px var(--font-mono-fixed,ui-monospace,monospace)}.ws-log-head span{display:block;margin-top:4px;color:var(--color-text-muted,#89939f);font-size:9px}.ws-log-drawer pre{flex:1;margin:0;padding:15px;overflow:auto;white-space:pre-wrap;color:#d6dde4;font:10px/1.55 var(--font-mono-fixed,ui-monospace,monospace)}.ws-truncated{padding:8px 15px;border-top:1px solid var(--color-border,#303640);color:#e7a93d;font-size:9px}@media(max-width:900px){.ws-table{grid-template-columns:minmax(170px,1fr) 100px 90px}.ws-table>*:nth-child(2),.ws-table>*:nth-child(3),.ws-table>*:nth-child(5){display:none}.ws-summary-item{padding-right:10px;margin-right:10px}.ws-summary-resource{display:none}.ws-overview-grid{grid-template-columns:1fr}.ws-header-actions{overflow-x:auto}.ws-log-drawer{width:94vw}}@media(max-width:620px){.ws-header{align-items:flex-start;flex-wrap:wrap}.ws-detail-header{display:grid;grid-template-columns:1fr}.ws-header-actions{margin-left:0;width:100%}.ws-summary{overflow-x:auto}.ws-summary-item{white-space:nowrap}.ws-detail-main,.ws-list-shell{margin-left:12px;margin-right:12px}.ws-detail-main{padding-left:0;padding-right:0}.ws-form-grid{grid-template-columns:1fr}.ws-tabs{padding:0 12px}}
`;
