import { useCallback, useEffect, useRef, useState } from "react";

// Shared code for the Workspaces dashboard widgets. Both read the operator
// HTTP API the panel uses, refresh on the host's event revision, and poll
// every 30 seconds while visible (file edits and syncs emit no app event).

export interface WidgetHostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  agentId?: number;
  threadId?: string;
  slot?: string;
  eventRevision?: number;
  preview?: boolean;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

export interface Command {
  id: string;
  display_command: string;
  working_directory?: string;
  actor_label?: string;
  status: string;
  exit_code?: number | null;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

export interface Preview {
  status: string;
  url?: string;
  error?: string;
  container_port?: number;
}

export interface Workspace {
  id: string;
  name: string;
  purpose?: string;
  kind?: string;
  status: string;
  lifecycle_status: string;
  host_label: string;
  instance_id?: number;
  owner_label?: string;
  consumer_app?: string;
  owner_agent_id?: number;
  owner_thread_id?: string;
  repo_label?: string;
  branch_label?: string;
  last_error?: string;
  last_activity_at?: string;
  expires_at?: string;
  storage_bytes?: number;
  current_command?: Command;
  preview?: Preview;
  activity_buckets?: number[];
}

export interface ActivityLine {
  workspace_id: string;
  workspace_name: string;
  event_type: string;
  category: "command" | "file" | "source" | "preview" | "lifecycle";
  severity?: "error" | "warning";
  actor_kind: string;
  actor_label: string;
  summary: string;
  count: number;
  data?: { exit_code?: number | null; duration_ms?: number; url?: string };
  first_at: string;
  created_at: string;
}

export function apiURL(props: WidgetHostProps, path: string, params: Record<string, string> = {}): string {
  const query = new URLSearchParams();
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  for (const [key, value] of Object.entries(params)) if (value) query.set(key, value);
  return `/api/apps/${encodeURIComponent(props.appName || "workspaces")}/api${path}?${query}`;
}

export async function apiRequest<T>(props: WidgetHostProps, path: string, params: Record<string, string> = {}, init?: RequestInit): Promise<T> {
  const response = await fetch(apiURL(props, path, params), {
    credentials: "same-origin",
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
  });
  if (!response.ok) {
    const text = await response.text().catch(() => "");
    throw new Error(errorMessage(response.status, text));
  }
  return (await response.json()) as T;
}

// errorMessage shows what the server said. A 403 with no reason is the
// operator API itself refusing; anything else keeps the server's wording
// (for example "app install is scoped to another project").
export function errorMessage(status: number, body: string): string {
  let text = body.trim();
  if (text.startsWith("{")) {
    try {
      const parsed = JSON.parse(text) as { error?: unknown; message?: unknown };
      const value = parsed.error ?? parsed.message;
      if (typeof value === "string" && value.trim()) text = value.trim();
    } catch {
      // Not JSON; show it as is.
    }
  }
  if (text.length > 300) text = text.slice(0, 300) + "…";
  if (text) return text;
  return status === 403 ? "Operator access is required." : `Request failed (HTTP ${status}).`;
}

export function panelLink(props: WidgetHostProps, workspaceId?: string, tab?: string): string {
  const query = new URLSearchParams();
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  if (workspaceId) query.set("workspace_id", workspaceId);
  if (tab) query.set("tab", tab);
  return `/apps/${encodeURIComponent(props.appName || "workspaces")}/page?${query}`;
}

// useWidgetData reloads when key or the host's event revision changes, and
// every 30 s while the page is visible. Out-of-order responses are dropped.
// Background reloads keep the current data on screen: `loading` is true only
// until the first answer arrives, and `loaded` says whether one has.
export function useWidgetData<T>(props: WidgetHostProps, key: string, load: () => Promise<T>, enabled = true) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState(false);
  const generation = useRef(0);
  const loader = useRef(load);
  loader.current = load;
  const reload = useCallback(async () => {
    if (!enabled) return;
    const current = ++generation.current;
    try {
      const value = await loader.current();
      if (current !== generation.current) return;
      setData(value);
      setError("");
    } catch (reason) {
      if (current !== generation.current) return;
      setError((reason as Error).message || "Request failed.");
    } finally {
      if (current === generation.current) setLoaded(true);
    }
  }, [enabled]);
  const loading = enabled && !loaded;
  useEffect(() => {
    void reload();
  }, [reload, key, props.eventRevision]);
  useEffect(() => {
    if (!enabled) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void reload();
    }, 30000);
    return () => window.clearInterval(timer);
  }, [reload, enabled]);
  return { data, error, loading, loaded, reload };
}

export function intSetting(settings: Record<string, unknown> | undefined, key: string, fallback: number, min: number, max: number): number {
  const value = Number(settings?.[key] ?? fallback);
  return Number.isFinite(value) ? Math.max(min, Math.min(max, Math.round(value))) : fallback;
}

export function enumSetting<T extends string>(settings: Record<string, unknown> | undefined, key: string, values: readonly T[], fallback: T): T {
  const value = settings?.[key];
  return typeof value === "string" && (values as readonly string[]).includes(value) ? (value as T) : fallback;
}

export function boolSetting(settings: Record<string, unknown> | undefined, key: string, fallback: boolean): boolean {
  const value = settings?.[key];
  return typeof value === "boolean" ? value : fallback;
}

export function ago(value?: string, now = Date.now()): string {
  const time = value ? new Date(value).getTime() : NaN;
  if (!Number.isFinite(time)) return "";
  const seconds = Math.max(0, Math.round((now - time) / 1000));
  if (seconds < 60) return "now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  return hours < 48 ? `${hours}h` : `${Math.round(hours / 24)}d`;
}

export function timeLeft(value?: string, now = Date.now()): { label: string; soon: boolean } {
  const time = value ? new Date(value).getTime() : NaN;
  if (!Number.isFinite(time)) return { label: "", soon: false };
  const minutes = Math.round((time - now) / 60000);
  if (minutes <= 0) return { label: "expired", soon: true };
  const label = minutes < 60 ? `${minutes}m left` : `${Math.floor(minutes / 60)}h ${minutes % 60}m left`;
  return { label, soon: minutes <= 15 };
}

export function formatDuration(ms?: number | null): string {
  if (ms == null || !Number.isFinite(ms)) return "";
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function commandDuration(command: Command, now = Date.now()): string {
  const start = command.started_at ? new Date(command.started_at).getTime() : NaN;
  const end = command.finished_at ? new Date(command.finished_at).getTime() : now;
  return Number.isFinite(start) && Number.isFinite(end) ? formatDuration(end - start) : "";
}

export function ownerOf(workspace: Workspace): string {
  return workspace.owner_label || (workspace.consumer_app && workspace.consumer_app !== "workspaces" ? workspace.consumer_app : "") || "Operator";
}

export function needsAttention(workspace: Workspace, now = Date.now()): boolean {
  if (workspace.status === "failed" || workspace.preview?.status === "crashed") return true;
  return workspace.lifecycle_status === "running" && timeLeft(workspace.expires_at, now).soon;
}

export const statusTone: Record<string, string> = {
  running: "var(--color-green, #34d399)",
  busy: "var(--color-accent, #f59e0b)",
  provisioning: "var(--color-accent, #f59e0b)",
  suspended: "var(--color-text-dim, #6b7280)",
  expired: "var(--color-text-dim, #6b7280)",
  failed: "var(--color-red, #f87171)",
};

export function StatusDot({ status }: { status: string }) {
  return (
    <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true" className="wsw-dot">
      <circle cx="4" cy="4" r="4" fill={statusTone[status] || "var(--color-text-dim, #6b7280)"} />
    </svg>
  );
}

// Small stroke pictograms for activity lines (no emoji in app UI).
export function ActivityIcon({ category }: { category: ActivityLine["category"] }) {
  const common = { width: 14, height: 14, viewBox: "0 0 16 16", fill: "none", stroke: "currentColor", strokeWidth: 1.5, strokeLinecap: "round" as const, strokeLinejoin: "round" as const, "aria-hidden": true };
  switch (category) {
    case "command":
      return <svg {...common}><path d="M3 4l4 4-4 4" /><path d="M8.5 12H13" /></svg>;
    case "file":
      return <svg {...common}><path d="M10.5 2.5l3 3L6 13H3v-3z" /></svg>;
    case "source":
      return <svg {...common}><path d="M3 6a5 5 0 0 1 9-2l1 1" /><path d="M13 2v3h-3" /><path d="M13 10a5 5 0 0 1-9 2l-1-1" /><path d="M3 14v-3h3" /></svg>;
    case "preview":
      return <svg {...common}><circle cx="8" cy="8" r="5.5" /><path d="M2.5 8h11" /><path d="M8 2.5c1.6 1.6 1.6 9.4 0 11" /><path d="M8 2.5c-1.6 1.6-1.6 9.4 0 11" /></svg>;
    default:
      return <svg {...common}><rect x="2.5" y="3" width="11" height="9" rx="1.5" /><path d="M6 14h4" /></svg>;
  }
}

export function EmptyIcon() {
  return (
    <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="3" y="4" width="18" height="13" rx="2" />
      <path d="M7 9l3 2.5L7 14" />
      <path d="M12 14h4" />
      <path d="M9 20h6" />
    </svg>
  );
}

export function ActivityBar({ buckets }: { buckets?: number[] }) {
  const values = buckets && buckets.length ? buckets : new Array(12).fill(0);
  const max = Math.max(1, ...values);
  return (
    <svg className="wsw-bar" width="60" height="12" viewBox="0 0 60 12" role="img" aria-label={`Activity in the last hour: ${values.reduce((a, b) => a + b, 0)} events`}>
      {values.map((value, index) => (
        <rect
          key={index}
          x={index * 5}
          y={0}
          width={4}
          height={12}
          rx={1}
          fill={value > 0 ? "var(--color-accent, #f59e0b)" : "var(--color-border, #2a2f36)"}
          opacity={value > 0 ? 0.35 + 0.65 * (value / max) : 1}
        />
      ))}
    </svg>
  );
}

export function activityDetail(line: ActivityLine): string {
  if (line.category === "command" && line.event_type !== "command.started" && line.event_type !== "command.cancel_requested") {
    const parts: string[] = [];
    if (line.data?.exit_code != null) parts.push(`exit ${line.data.exit_code}`);
    const duration = formatDuration(line.data?.duration_ms);
    if (duration) parts.push(duration);
    return parts.join(" · ");
  }
  return "";
}

export function activitySummary(line: ActivityLine): string {
  // Strip the "Command succeeded: " style prefix; the icon and detail carry it.
  const match = /^Command (?:started|succeeded|failed|cancelled|timed_out): (.*)$/s.exec(line.summary);
  if (match) {
    const verb = line.event_type === "command.started" ? "running" : line.event_type === "command.succeeded" ? "ran" : line.event_type.replace("command.", "").replace("_", " ");
    return `${verb} ${match[1]}`;
  }
  return line.summary;
}

export const widgetCSS = `
.wsw{box-sizing:border-box;height:100%;min-height:0;display:flex;flex-direction:column;gap:8px;overflow:auto;color:var(--color-text,#e7ebef);font-family:var(--font-base,inherit);font-size:12px}
.wsw-card{border:1px solid var(--color-border,#2a2f36);border-radius:8px;background:var(--color-bg-card,#12161b);padding:10px 12px;min-width:0;flex-shrink:0}
.wsw-card>h3{margin:0 0 6px;color:var(--color-text-muted,#9aa4ae);font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:.04em;display:flex;align-items:center;gap:6px}
.wsw-cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:8px}
.wsw-ws-card{display:flex;flex-direction:column;gap:6px}
.wsw-ws-card:hover{border-color:var(--color-text-dim,#6b7280)}
.wsw-ws-top{display:flex;align-items:center;gap:8px;min-width:0}
.wsw-ws-foot{display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.wsw *{box-sizing:border-box}
.wsw a{color:inherit;text-decoration:none}
.wsw-head{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:2px 2px 0;flex-shrink:0}
.wsw-head h2{margin:0;font-size:13px;font-weight:600}
.wsw-head p{margin:2px 0 0;color:var(--color-text-muted,#9aa4ae);font-size:11px}
.wsw-btn{border:1px solid var(--color-border,#2a2f36);background:transparent;color:var(--color-text-muted,#9aa4ae);border-radius:6px;padding:3px 8px;font-size:11px;cursor:pointer;white-space:nowrap}
.wsw-btn:hover{color:var(--color-text,#e7ebef)}
.wsw-btn:disabled{opacity:.5;cursor:default}
.wsw-counts{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:6px}
.wsw-count{display:flex;flex-direction:column;gap:1px}
.wsw-count span{color:var(--color-text-muted,#9aa4ae);font-size:11px}
.wsw-count strong{font-size:17px;font-variant-numeric:tabular-nums}
.wsw-list{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:2px}
.wsw-row{display:flex;align-items:center;gap:8px;padding:5px 6px;border-radius:6px;min-width:0}
.wsw-row:hover{background:var(--color-bg-input,#1a1f26)}
.wsw-dot{flex-shrink:0}
.wsw-main{flex:1;min-width:0;display:flex;flex-direction:column;gap:1px}
.wsw-line{display:flex;align-items:baseline;gap:6px;min-width:0}
.wsw-name{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:55%}
.wsw-muted{color:var(--color-text-muted,#9aa4ae)}
.wsw-dim{color:var(--color-text-dim,#6b7280)}
.wsw-trunc{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0}
.wsw-mono{font-family:var(--font-mono-fixed,ui-monospace,monospace)}
.wsw-error{color:var(--color-red,#f87171)}
.wsw-warn{color:var(--color-accent,#f59e0b)}
.wsw-side{display:flex;align-items:center;gap:6px;flex-shrink:0}
.wsw-pill{border:1px solid var(--color-border,#2a2f36);border-radius:999px;padding:1px 7px;font-size:11px;color:var(--color-text-muted,#9aa4ae);white-space:nowrap}
.wsw-link{color:var(--color-accent,#f59e0b)!important}
.wsw-link:hover{text-decoration:underline!important}
.wsw-act{display:grid;grid-template-columns:14px minmax(0,auto) minmax(0,1fr) auto;align-items:center;gap:6px;padding:3px 6px;border-radius:6px}
.wsw-act:hover{background:var(--color-bg-input,#1a1f26)}
.wsw-act .wsw-icon{color:var(--color-text-muted,#9aa4ae);display:flex}
.wsw-act.error .wsw-icon,.wsw-act.error .wsw-what{color:var(--color-red,#f87171)}
.wsw-act.warning .wsw-icon{color:var(--color-accent,#f59e0b)}
.wsw-ws{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:110px}
.wsw-when{color:var(--color-text-dim,#6b7280);white-space:nowrap;font-variant-numeric:tabular-nums}
.wsw-empty{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:6px;padding:14px 12px;color:var(--color-text-muted,#9aa4ae);text-align:center}
.wsw-banner{flex-shrink:0;padding:6px 8px;border-radius:6px;border:1px solid var(--color-red,#f87171);color:var(--color-red,#f87171);font-size:11px;overflow-wrap:anywhere}
.wsw-output{margin:0;padding:8px;border-radius:6px;background:var(--color-bg,#0b0d10);border:1px solid var(--color-border,#2a2f36);font-family:var(--font-mono-fixed,ui-monospace,monospace);font-size:11px;line-height:1.45;white-space:pre-wrap;word-break:break-word;max-height:220px;overflow:auto}
.wsw-select{background:var(--color-bg-input,#1a1f26);color:var(--color-text,#e7ebef);border:1px solid var(--color-border,#2a2f36);border-radius:6px;padding:2px 6px;font-size:11px;max-width:160px}
.wsw-bar{flex-shrink:0}
`;
