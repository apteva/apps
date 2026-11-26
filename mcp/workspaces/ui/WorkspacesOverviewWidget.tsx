import { useEffect, useMemo, useState } from "react";
import {
  ActivityBar,
  ActivityIcon,
  ActivityLine,
  EmptyIcon,
  StatusDot,
  WidgetHostProps,
  Workspace,
  activityDetail,
  activitySummary,
  ago,
  apiRequest,
  boolSetting,
  enumSetting,
  intSetting,
  needsAttention,
  ownerOf,
  panelLink,
  timeLeft,
  useWidgetData,
  widgetCSS,
} from "./widgetShared";

const SHOW = ["active", "attention", "all"] as const;
const HOSTS = ["", "local", "remote", "machine"] as const;
const KINDS = ["all", "commands", "changes"] as const;

interface ListResponse {
  workspaces: Workspace[];
}
interface ActivityResponse {
  activity: ActivityLine[];
}

const samples: { workspaces: Workspace[]; activity: ActivityLine[] } = {
  workspaces: [
    { id: "wsp_a", name: "api-fix", status: "busy", lifecycle_status: "running", host_label: "Local Docker", owner_label: "Builder agent", expires_at: new Date(Date.now() + 95 * 60000).toISOString(), current_command: { id: "c1", display_command: "go test ./...", status: "running", created_at: "" }, activity_buckets: [0, 0, 1, 0, 2, 4, 3, 0, 1, 5, 6, 2] },
    { id: "wsp_b", name: "site-preview", status: "running", lifecycle_status: "running", host_label: "Instance #2", owner_label: "Code", expires_at: new Date(Date.now() + 10 * 60000).toISOString(), preview: { status: "live", url: "http://127.0.0.1:49152/" }, activity_buckets: [1, 0, 0, 0, 0, 1, 0, 0, 0, 2, 0, 1] },
  ],
  activity: [
    { workspace_id: "wsp_a", workspace_name: "api-fix", event_type: "command.failed", category: "command", severity: "error", actor_kind: "runtime", actor_label: "Containers", summary: "Command failed: go test ./...", count: 1, data: { exit_code: 1, duration_ms: 42000 }, first_at: new Date(Date.now() - 120000).toISOString(), created_at: new Date(Date.now() - 120000).toISOString() },
    { workspace_id: "wsp_a", workspace_name: "api-fix", event_type: "file.edited", category: "file", actor_kind: "agent", actor_label: "Builder agent", summary: "Changed 7 files", count: 7, first_at: new Date(Date.now() - 300000).toISOString(), created_at: new Date(Date.now() - 180000).toISOString() },
    { workspace_id: "wsp_b", workspace_name: "site-preview", event_type: "preview.live", category: "preview", actor_kind: "runtime", actor_label: "Containers", summary: "Preview live on port 3000", count: 1, first_at: new Date(Date.now() - 540000).toISOString(), created_at: new Date(Date.now() - 540000).toISOString() },
  ],
};

export default function WorkspacesOverviewWidget(props: WidgetHostProps) {
  const settings = props.widgetSettings;
  const full = props.widgetSize === "full";
  const show = enumSetting(settings, "show", SHOW, "active");
  const host = enumSetting(settings, "host", HOSTS, "");
  const includeTest = boolSetting(settings, "include_test", false);
  const maxRows = intSetting(settings, "max_rows", 6, 3, 15);
  const showActivity = boolSetting(settings, "show_activity", true);
  const kinds = enumSetting(settings, "activity_kinds", KINDS, "all");
  const activityLimit = intSetting(settings, "activity_limit", full ? 10 : 5, 3, 15);
  const [now, setNow] = useState(Date.now());
  const [busy, setBusy] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [actionError, setActionError] = useState("");
  const live = !props.preview && !!props.projectId;

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30000);
    return () => window.clearInterval(timer);
  }, []);

  const listKey = JSON.stringify({ host, includeTest, full });
  const list = useWidgetData<ListResponse>(
    props,
    listKey,
    () =>
      apiRequest<ListResponse>(props, "/workspaces", {
        kind: includeTest ? "" : "dev",
        host,
        include: full ? "activity_buckets" : "",
      }),
    live,
  );
  const activity = useWidgetData<ActivityResponse>(
    props,
    JSON.stringify({ kinds, includeTest, activityLimit }),
    () => apiRequest<ActivityResponse>(props, "/activity", { limit: String(activityLimit), kinds, include_test: includeTest ? "1" : "" }),
    live && showActivity,
  );

  const workspaces = props.preview ? samples.workspaces : list.data?.workspaces || [];
  const lines = props.preview ? samples.activity : activity.data?.activity || [];
  const counts = useMemo(() => {
    const out = { running: 0, busy: 0, stopped: 0, failed: 0 };
    for (const workspace of workspaces) {
      if (workspace.status === "busy") out.busy++;
      else if (workspace.status === "running") out.running++;
      else if (workspace.status === "failed") out.failed++;
      else if (workspace.status === "suspended" || workspace.status === "expired") out.stopped++;
    }
    return out;
  }, [workspaces]);
  const rows = useMemo(() => {
    const filtered = workspaces.filter((workspace) => {
      if (show === "attention") return needsAttention(workspace, now);
      if (show === "active") return workspace.status === "running" || workspace.status === "busy" || workspace.status === "provisioning" || needsAttention(workspace, now);
      return true;
    });
    // Busy first, then attention, then most recently active.
    const rank = (w: Workspace) => (w.status === "busy" ? 0 : needsAttention(w, now) ? 1 : w.status === "running" ? 2 : 3);
    return filtered
      .sort((a, b) => rank(a) - rank(b) || String(b.last_activity_at || "").localeCompare(String(a.last_activity_at || "")))
      .slice(0, maxRows);
  }, [workspaces, show, maxRows, now]);

  const refresh = async () => {
    setRefreshing(true);
    try {
      await Promise.all([list.reload(), showActivity ? activity.reload() : Promise.resolve()]);
    } finally {
      setRefreshing(false);
    }
  };

  const act = async (workspace: Workspace, action: "extend" | "stop") => {
    setBusy(workspace.id + action);
    setActionError("");
    try {
      await apiRequest(props, `/workspaces/${encodeURIComponent(workspace.id)}/${action}`, {}, {
        method: "POST",
        body: JSON.stringify(action === "extend" ? { ttl_minutes: 60 } : {}),
      });
      await list.reload();
    } catch (reason) {
      setActionError(`${workspace.name}: ${(reason as Error).message}`);
    } finally {
      setBusy("");
    }
  };

  if (!props.projectId && !props.preview) {
    return (
      <section className="wsw">
        <style>{widgetCSS}</style>
        <div className="wsw-card wsw-empty">Select a project to see its workspaces.</div>
      </section>
    );
  }

  const error = list.error || (showActivity ? activity.error : "") || actionError;
  const emptyLabel = show === "attention" ? "Nothing needs attention." : show === "active" ? "No workspace is running." : "No workspaces yet.";
  return (
    <section className="wsw" aria-label="Workspaces">
      <style>{widgetCSS}</style>
      <header className="wsw-head">
        <div>
          <h2><a href={panelLink(props)}>Workspaces</a></h2>
          <p>{show === "attention" ? "Failed, expiring or crashed previews" : "What is running, who owns it, and when it expires"}</p>
        </div>
        <button type="button" className="wsw-btn" onClick={() => void refresh()} disabled={refreshing} aria-label="Refresh workspaces">
          {refreshing ? "Refreshing…" : "Refresh"}
        </button>
      </header>
      <div className="wsw-card wsw-counts" aria-label="Workspace counts">
        <Count label="Running" value={counts.running} />
        <Count label="Busy" value={counts.busy} tone={counts.busy ? "wsw-warn" : ""} />
        <Count label="Stopped" value={counts.stopped} />
        <Count label="Failed" value={counts.failed} tone={counts.failed ? "wsw-error" : ""} />
      </div>
      {error && <div className="wsw-banner" role="alert">{error}</div>}
      {rows.length === 0 && list.loaded && !list.error && (
        <div className="wsw-card wsw-empty">
          <EmptyIcon />
          <span>{emptyLabel}</span>
        </div>
      )}
      {rows.length === 0 && !list.loaded && !props.preview && <div className="wsw-card wsw-empty wsw-dim">Loading workspaces…</div>}
      {rows.length > 0 && (
        <div className="wsw-cards">
          {rows.map((workspace) => {
            const left = timeLeft(workspace.expires_at, now);
            const running = workspace.lifecycle_status === "running";
            const previewLive = workspace.preview?.status === "live" && workspace.preview.url;
            const detail = workspace.status === "failed"
              ? <span className="wsw-error wsw-trunc">{workspace.last_error || "Failed"}</span>
              : workspace.current_command
                ? <span className="wsw-mono wsw-trunc">{workspace.current_command.display_command}</span>
                : workspace.preview?.status === "crashed"
                  ? <span className="wsw-error wsw-trunc">Preview crashed{workspace.preview.error ? `: ${workspace.preview.error}` : ""}</span>
                  : <span className="wsw-dim wsw-trunc">{workspace.purpose || (workspace.status === "busy" ? "Running a command" : workspace.status)}</span>;
            return (
              <article key={workspace.id} className="wsw-card wsw-ws-card">
                <a className="wsw-ws-top" href={panelLink(props, workspace.id)} title={`Open ${workspace.name}`}>
                  <StatusDot status={workspace.status} />
                  <span className="wsw-main">
                    <span className="wsw-name" style={{ maxWidth: "100%" }}>{workspace.name}</span>
                    <span className="wsw-muted wsw-trunc">{ownerOf(workspace)} · {workspace.host_label}</span>
                  </span>
                  {full && <ActivityBar buckets={workspace.activity_buckets} />}
                </a>
                <div className="wsw-line">{detail}</div>
                {full && workspace.branch_label && <div className="wsw-dim wsw-trunc">{workspace.repo_label ? `${workspace.repo_label}@` : ""}{workspace.branch_label}</div>}
                <div className="wsw-ws-foot">
                  {previewLive && <a className="wsw-pill wsw-link" href={workspace.preview!.url} target="_blank" rel="noreferrer">Preview</a>}
                  {running && left.label && <span className={`wsw-pill ${left.soon ? "wsw-warn" : ""}`}>{left.label}</span>}
                  {running && !props.preview && left.soon && (
                    <button type="button" className="wsw-btn" disabled={!!busy} onClick={() => void act(workspace, "extend")} aria-label={`Keep ${workspace.name} running for another hour`} title="Set the time left to one hour">
                      +60m
                    </button>
                  )}
                  {running && !props.preview && full && (
                    <button type="button" className="wsw-btn" disabled={!!busy} onClick={() => void act(workspace, "stop")} aria-label={`Stop ${workspace.name}`}>
                      Stop
                    </button>
                  )}
                </div>
              </article>
            );
          })}
        </div>
      )}
      {showActivity && (
        <div className="wsw-card">
          <h3>Recent activity</h3>
          {lines.length === 0 && (activity.loaded || props.preview) && !activity.error && <div className="wsw-dim">No activity yet.</div>}
          {lines.length === 0 && !activity.loaded && !props.preview && <div className="wsw-dim">Loading activity…</div>}
          <ul className="wsw-list">
            {lines.map((line, index) => {
              const extra = activityDetail(line);
              return (
                <li key={`${line.workspace_id}-${line.created_at}-${index}`}>
                  <a
                    className={`wsw-act ${line.severity || ""}`}
                    href={panelLink(props, line.workspace_id, "activity")}
                    title={`${line.workspace_name}: ${line.summary} (${line.actor_label || line.actor_kind})`}
                  >
                    <span className="wsw-icon"><ActivityIcon category={line.category} /></span>
                    <span className="wsw-ws">{line.workspace_name}</span>
                    <span className="wsw-trunc">
                      <span className="wsw-what">{activitySummary(line)}</span>
                      {extra && <span className="wsw-dim"> · {extra}</span>}
                      {full && line.actor_label && <span className="wsw-dim"> · {line.actor_label}</span>}
                    </span>
                    <span className="wsw-when">{ago(line.created_at, now)}</span>
                  </a>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </section>
  );
}

function Count({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return (
    <div className="wsw-count">
      <span>{label}</span>
      <strong className={tone}>{value}</strong>
    </div>
  );
}
