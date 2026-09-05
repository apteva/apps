// ComputerPanel - operator-facing dashboard panel for app-owned browser sessions.
// The UI is a thin HTTP client over the same browser_session and computer_use
// handlers exposed through MCP.

import { useCallback, useEffect, useRef, useState } from "react";
import { Card, CardHeader, StatusDot, StatusPill, DataList } from "@apteva/ui-kit";
import Hls from "hls.js";
import { usePollingRefresh } from "./refresh";

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}

function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(app: string, projectId: string, fn: (ev: AppEventEnvelope<T>) => void): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

interface NativePanelProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  instanceId?: number;
}

interface SessionRow {
  session_id: string;
  backend_session_id?: string;
  backend: "local" | "browserbase" | "steel" | string;
  status?: "active" | "closed" | "reaped" | "failed" | "interrupted" | string;
  recording_supported?: boolean;
  recording_status?: "recording" | "processing" | "ready" | "unavailable" | "unsupported" | "failed" | string;
  context_id?: string;
  app_context_id?: string;
  context_name?: string;
  persist?: boolean;
  timeout_seconds?: number;
  provider_expires_at?: string;
  current_url: string;
  debug_url?: string;
  stream_url?: string;
  active_tab_id?: string;
  tabs?: BrowserTab[];
  tab_count?: number;
  opened_at: string;
  last_used_at: string;
  closed_at?: string;
  width?: number;
  height?: number;
  proxy?: SessionProxyState;
  usage?: SessionUsageState;
}

interface SessionProxyState {
  mode: "auto" | "direct" | "managed" | "profile" | string;
  provider?: string;
  profile_id?: string;
  profile_name?: string;
  country?: string;
  sticky_scope?: "rotating" | "session" | "context" | string;
}

interface SessionUsageState {
	status: "ready" | "unsupported" | "unavailable" | string;
	proxy_bytes?: number;
	measured_at?: string;
}

interface RecordingStream {
  id: string;
  start_ms?: number;
  end_ms?: number;
  playlist_url: string;
}

interface RecordingMetadata {
  session_id: string;
  backend: string;
  status: "recording" | "processing" | "ready" | "unavailable" | "unsupported" | "failed" | string;
  recording_supported: boolean;
  streams: RecordingStream[];
  message?: string;
  error?: string;
}

interface BrowserTab {
  tab_id: string;
  url: string;
  title?: string;
  active?: boolean;
  opener_tab_id?: string;
}

interface ListResponse {
  sessions?: SessionRow[];
  error?: string;
}

interface ContextRow {
  id: string;
  name: string;
  backend: "local" | "browserbase" | "steel" | "browser-engine" | string;
  provider_context_id?: string;
  persist_default: boolean;
  auto_created: boolean;
  metadata_json?: string;
  created_at: string;
  updated_at: string;
  last_used_at?: string;
}

interface ContextListResponse {
  contexts?: ContextRow[];
  error?: string;
}

interface ComputerSettings {
  default_backend: "local" | "browserbase" | "steel" | "browser-engine" | "service" | string;
  lock_backend: boolean;
  default_proxy_mode: "auto" | "direct" | "managed" | "profile" | string;
  default_proxy_profile_id?: string;
  lock_proxy_policy: boolean;
}

interface ProxyProfile {
  id: string;
  name: string;
  provider_slug: string;
  connection_id: number;
  external_ref?: string;
  pool_type: string;
  protocol: "http" | "https" | "socks5" | string;
  default_country?: string;
  sticky_scope: "rotating" | "session" | "context" | string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

interface ProxyConnection {
  connection_id: number;
  provider_slug: string;
  name: string;
  default?: boolean;
}

interface ProxyProfilesResponse {
  profiles?: ProxyProfile[];
  connections?: ProxyConnection[];
  error?: string;
}

interface ProxyResource {
  id: string;
  name: string;
}

interface SettingsResponse {
  settings?: ComputerSettings;
  error?: string;
}

interface ComputerEventData extends Partial<SessionRow> {
  id?: string;
  default_backend?: string;
  lock_backend?: boolean;
  default_proxy_mode?: string;
  default_proxy_profile_id?: string;
  lock_proxy_policy?: boolean;
}

type OpenMode = "open" | "resume" | "context" | "create_context";
type ProxyMode = "" | "auto" | "direct" | "managed" | "profile";

const SESSIONS_URL = "/api/apps/computer/sessions";
const CONTEXTS_URL = "/api/apps/computer/contexts";
const SETTINGS_URL = "/api/apps/computer/settings";
const PROXY_PROFILES_URL = "/api/apps/computer/proxy-profiles";
const PROXY_RESOURCES_URL = "/api/apps/computer/proxy-resources";
const POLL_MS = 4000;

const BACKEND_LABEL: Record<string, string> = {
  local: "Local Chrome",
  browserbase: "Browserbase",
  steel: "Steel",
  "browser-engine": "Browser Engine",
  service: "Browser Service",
};

const backendOptions = [
  { value: "local", label: "Local Chrome" },
  { value: "browserbase", label: "Browserbase" },
  { value: "steel", label: "Steel" },
  { value: "browser-engine", label: "Browser Engine" },
  { value: "service", label: "Browser Service" },
] as const;

function appURL(path: string, projectId?: string, extra?: Record<string, string | number | boolean | undefined>) {
  const url = new URL(path, window.location.origin);
  if (projectId) url.searchParams.set("project_id", projectId);
  for (const [key, value] of Object.entries(extra ?? {})) {
    if (value !== undefined && value !== "") url.searchParams.set(key, String(value));
  }
  return `${url.pathname}${url.search}`;
}

export default function ComputerPanel(props: NativePanelProps) {
 return <ComputerPanelContent key={props.projectId ?? ""} {...props}/>;
}

function ComputerPanelContent({ projectId }: NativePanelProps) {
 const settingsGeneration = useRef(0);
  const [rows, setRows] = useState<SessionRow[]>([]);
  const [loadedProject, setLoadedProject] = useState<string | null>(null);
  const pendingSelection = useRef<string | null>(null);
  const [contexts, setContexts] = useState<ContextRow[]>([]);
  const [settings, setSettings] = useState<ComputerSettings>({
    default_backend: "local",
    lock_backend: false,
    default_proxy_mode: "auto",
    lock_proxy_policy: false,
  });
  const [proxyProfiles, setProxyProfiles] = useState<ProxyProfile[]>([]);
  const [proxyConnections, setProxyConnections] = useState<ProxyConnection[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(() => {
    const query = new URLSearchParams(window.location.search);
    return query.get("instance") || query.get("session_id");
  });
  const [eventPreviewTick, setEventPreviewTick] = useState(0);
  const [nowTick, setNowTick] = useState(Date.now());
  const [showOpen, setShowOpen] = useState(false);
  const [pendingClose, setPendingClose] = useState<string | null>(null);
  const [pendingContextDelete, setPendingContextDelete] = useState<string | null>(null);
  const [closedSession, setClosedSession] = useState<SessionRow | null>(null);
  const [showAddProxy, setShowAddProxy] = useState(false);
  const [pendingProxyDelete, setPendingProxyDelete] = useState<string | null>(null);

  const refresh = usePollingRefresh(async (signal) => {
    const settingsVersion = settingsGeneration.current;
    try {
      const [sessionsRes, contextsRes, settingsRes, proxiesRes] = await Promise.all([
        fetch(appURL(SESSIONS_URL, projectId), { credentials: "include", signal }),
        fetch(appURL(CONTEXTS_URL, projectId), { credentials: "include", signal }),
        fetch(appURL(SETTINGS_URL, projectId), { credentials: "include", signal }),
        fetch(appURL(PROXY_PROFILES_URL, projectId), { credentials: "include", signal }),
      ]);
      if (!sessionsRes.ok) throw new Error(`sessions HTTP ${sessionsRes.status}`);
      if (!contextsRes.ok) throw new Error(`contexts HTTP ${contextsRes.status}`);
      if (!settingsRes.ok) throw new Error(`settings HTTP ${settingsRes.status}`);
      if (!proxiesRes.ok) throw new Error(`proxy profiles HTTP ${proxiesRes.status}`);
      const body = (await sessionsRes.json()) as ListResponse;
      const contextBody = (await contextsRes.json()) as ContextListResponse;
      const settingsBody = (await settingsRes.json()) as SettingsResponse;
      const proxiesBody = (await proxiesRes.json()) as ProxyProfilesResponse;
      if (body.error) throw new Error(body.error);
      if (contextBody.error) throw new Error(contextBody.error);
      if (settingsBody.error) throw new Error(settingsBody.error);
      if (proxiesBody.error) throw new Error(proxiesBody.error);
      if (signal.aborted) return;
      const nextRows = body.sessions ?? [];
      const requested = pendingSelection.current ?? selected;
      if (requested && !nextRows.some((row) => row.session_id === requested)) {
        const detail = await fetch(appURL(`${SESSIONS_URL}/${encodeURIComponent(requested)}/presentation`, projectId), { credentials: "include", signal });
        if (detail.ok) {
          const presentation = await detail.json();
          if (signal.aborted) return;
          if (presentation.session) nextRows.push(presentation.session);
        }
      }
      if (signal.aborted) return;
      if (pendingSelection.current && pendingSelection.current !== requested) return;
      pendingSelection.current = null;
      setRows(nextRows);
      setLoadedProject(projectId ?? "");
      setContexts(contextBody.contexts ?? []);
      if (settingsBody.settings && settingsVersion === settingsGeneration.current) setSettings(settingsBody.settings);
      setProxyProfiles(proxiesBody.profiles ?? []);
      setProxyConnections(proxiesBody.connections ?? []);
      setErr(null);
    } catch (e: any) {
      if (!signal.aborted) setErr(String(e?.message ?? e));
    }
  }, projectId, POLL_MS);


  useEffect(() => {
    const t = setInterval(() => setNowTick(Date.now()), 60_000);
    return () => clearInterval(t);
  }, []);

  useAppEvents<ComputerEventData>("computer", projectId, (ev) => {
    switch (ev.topic) {
      case "session.opened":
        if (ev.data?.session_id && closedSession?.session_id === ev.data.session_id) setClosedSession(null);
        if (!selected && ev.data?.session_id) setSelected(ev.data.session_id);
        void refresh();
        break;
      case "session.action":
        if (ev.data?.session_id === selected) setEventPreviewTick((n) => n + 1);
        void refresh();
        break;
      case "session.closed":
      case "session.reaped":
        if (ev.data?.session_id) {
          const active = rows.find((row) => row.session_id === ev.data.session_id);
          if (active || selected === ev.data.session_id) {
            setClosedSession({
              ...(active ?? closedSession ?? ({} as SessionRow)),
              ...ev.data,
              session_id: ev.data.session_id,
              backend: ev.data.backend ?? active?.backend ?? closedSession?.backend ?? "",
              current_url: ev.data.current_url ?? active?.current_url ?? closedSession?.current_url ?? "",
              opened_at: ev.data.opened_at ?? active?.opened_at ?? closedSession?.opened_at ?? ev.time,
              last_used_at: ev.data.last_used_at ?? active?.last_used_at ?? closedSession?.last_used_at ?? ev.time,
              status: ev.data.status ?? (ev.topic === "session.reaped" ? "reaped" : "closed"),
              recording_status: ev.data.recording_status ?? "processing",
            });
            setSelected(ev.data.session_id);
          }
        }
        void refresh();
        break;
      case "recording.ready":
      case "recording.failed":
        if (ev.data?.session_id && closedSession?.session_id === ev.data.session_id) {
          setClosedSession((current) => current ? { ...current, recording_status: ev.data.recording_status } : current);
        }
        setEventPreviewTick((n) => n + 1);
        break;
      case "context.created":
      case "context.updated":
      case "context.deleted":
      case "settings.updated":
        void refresh();
        break;
    }
  });

  useEffect(() => {
    if (loadedProject !== (projectId ?? "") || pendingSelection.current) return;
    if (!selected && rows.length > 0) setSelected(rows[0].session_id);
    if (selected && !rows.some((r) => r.session_id === selected) && closedSession?.session_id !== selected) {
      setSelected(rows[0]?.session_id ?? null);
    }
  }, [rows, selected, closedSession, loadedProject, projectId]);

  useEffect(() => {
    setRows([]); setContexts([]); setClosedSession(null); setLoadedProject(null);
    const query = new URLSearchParams(window.location.search);
    const requested = query.get("instance") || query.get("session_id");
    pendingSelection.current = requested;
    setSelected(requested);
  }, [projectId]);

  const onClose = useCallback(
    async (id: string) => {
      const active = rows.find((row) => row.session_id === id);
      const r = await fetch(appURL(`${SESSIONS_URL}/${encodeURIComponent(id)}`, projectId), {
        method: "DELETE",
        credentials: "include",
      });
      if (!r.ok) {
        setErr(`close failed: HTTP ${r.status}`);
        return;
      }
      const body = await r.json();
      if (active) {
        setClosedSession({
          ...active,
          status: body.status ?? "closed",
          recording_status: body.recording_status ?? (active.recording_supported ? "processing" : "unsupported"),
          usage: body.usage ?? active.usage,
          closed_at: new Date().toISOString(),
        });
        setSelected(id);
      }
      void refresh();
    },
    [projectId, refresh, rows],
  );

  const sel = rows.find((r) => r.session_id === selected) ?? (closedSession?.session_id === selected ? closedSession : null);
  const closeTarget = rows.find((r) => r.session_id === pendingClose) ?? null;
  const contextDeleteTarget = contexts.find((c) => c.id === pendingContextDelete) ?? null;
  const proxyDeleteTarget = proxyProfiles.find((profile) => profile.id === pendingProxyDelete) ?? null;

  const onDeleteContext = useCallback(
    async (id: string) => {
      const r = await fetch(appURL(`${CONTEXTS_URL}/${encodeURIComponent(id)}`, projectId), {
        method: "DELETE",
        credentials: "include",
      });
      if (!r.ok) {
        setErr(`delete context failed: HTTP ${r.status}`);
        return;
      }
      void refresh();
    },
    [projectId, refresh],
  );

  const updateSettings = useCallback(
    async (patch: Partial<ComputerSettings>) => {
      const generation = ++settingsGeneration.current;
      setSettings((previous) => ({ ...previous, ...patch }));
      try {
        const r = await fetch(appURL(SETTINGS_URL, projectId), {
          method: "PATCH",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(patch),
        });
        const body = (await r.json()) as SettingsResponse;
        if (!r.ok || body.error) throw new Error(body.error ?? `HTTP ${r.status}`);
        if (generation === settingsGeneration.current) {
          if (body.settings) setSettings(body.settings);
          setErr(null);
        }
      } catch (e: any) {
        if (generation === settingsGeneration.current) setErr(`settings failed: ${String(e?.message ?? e)}`);
      } finally {
        settingsGeneration.current++;
        void refresh();
      }
    },
    [projectId, refresh],
  );

  const onDeleteProxy = useCallback(
    async (id: string) => {
      const r = await fetch(appURL(`${PROXY_PROFILES_URL}/${encodeURIComponent(id)}`, projectId), {
        method: "DELETE",
        credentials: "include",
      });
      const body = await r.json();
      if (!r.ok || body.error) {
        setErr(`delete proxy profile failed: ${body.error ?? `HTTP ${r.status}`}`);
        return;
      }
      void refresh();
    },
    [projectId, refresh],
  );

  return (
    <div className="computer-panel-layout bg-bg">
      <style>{computerPanelLayoutCSS}</style>
      <BrowsersList
        rows={rows}
        err={err}
        selected={selected}
        now={nowTick}
        onSelect={setSelected}
        onClose={setPendingClose}
        onOpen={() => setShowOpen(true)}
        contexts={contexts}
        onDeleteContext={setPendingContextDelete}
        settings={settings}
        onUpdateSettings={updateSettings}
        proxyProfiles={proxyProfiles}
        onAddProxy={() => setShowAddProxy(true)}
        onDeleteProxy={setPendingProxyDelete}
      />
      <SessionDetail
        key={`${projectId ?? ""}:${selected ?? ""}`}
        session={sel}
        projectId={projectId}
        now={nowTick}
        onClose={setPendingClose}
        onRefresh={refresh}
        externalRefreshKey={eventPreviewTick}
      />
      {showOpen && (
        <OpenSessionModal
          onClose={() => setShowOpen(false)}
          contexts={contexts}
          settings={settings}
          proxyProfiles={proxyProfiles.filter((profile) => profile.enabled)}
          projectId={projectId}
          onOpened={(newID) => {
            setShowOpen(false);
            pendingSelection.current = newID;
            setSelected(newID);
            void refresh();
          }}
        />
      )}
      {showAddProxy && (
        <AddProxyProfileModal
          onClose={() => setShowAddProxy(false)}
          projectId={projectId}
          connections={proxyConnections}
          onCreated={() => {
            setShowAddProxy(false);
            void refresh();
          }}
        />
      )}
      {closeTarget && (
        <ConfirmModal
          title="Close Browser Session"
          body={`Close ${hostFor(closeTarget.current_url)}? The app session will be removed and the backend browser will be released.`}
          confirmLabel="Close session"
          busyLabel="Closing..."
          onCancel={() => setPendingClose(null)}
          onConfirm={async () => {
            await onClose(closeTarget.session_id);
            setPendingClose(null);
          }}
        />
      )}
      {contextDeleteTarget && (
        <ConfirmModal
          title="Delete Browser Context"
          body={`Delete ${contextDeleteTarget.name} from the app catalog? Provider state is left intact.`}
          confirmLabel="Delete context"
          busyLabel="Deleting..."
          onCancel={() => setPendingContextDelete(null)}
          onConfirm={async () => {
            await onDeleteContext(contextDeleteTarget.id);
            setPendingContextDelete(null);
          }}
        />
      )}
      {proxyDeleteTarget && (
        <ConfirmModal
          title="Delete Proxy Profile"
          body={`Delete ${proxyDeleteTarget.name}? Existing session history keeps only its safe routing summary.`}
          confirmLabel="Delete profile"
          busyLabel="Deleting..."
          onCancel={() => setPendingProxyDelete(null)}
          onConfirm={async () => {
            await onDeleteProxy(proxyDeleteTarget.id);
            setPendingProxyDelete(null);
          }}
        />
      )}
    </div>
  );
}

function BrowsersList({
  rows,
  err,
  selected,
  now,
  onSelect,
  onClose,
  onOpen,
  contexts,
  onDeleteContext,
  settings,
  onUpdateSettings,
  proxyProfiles,
  onAddProxy,
  onDeleteProxy,
}: {
  rows: SessionRow[];
  err: string | null;
  selected: string | null;
  now: number;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
  onOpen: () => void;
  contexts: ContextRow[];
  onDeleteContext: (id: string) => void;
  settings: ComputerSettings;
  onUpdateSettings: (patch: Partial<ComputerSettings>) => void;
  proxyProfiles: ProxyProfile[];
  onAddProxy: () => void;
  onDeleteProxy: (id: string) => void;
}) {
  const activeRows = rows.filter((row) => !row.status || row.status === "active");
  const pastRows = rows.filter((row) => row.status && row.status !== "active");

  return (
    <Card fullWidth className="overflow-hidden flex flex-col h-full min-h-0">
      <CardHeader
        title="Browsers"
        right={
          <div style={{ display: "flex", gap: "6px", alignItems: "center" }}>
            <StatusDot variant={err ? "error" : "live"}>{err ? "Error" : "Connected"}</StatusDot>
            <IconButton onClick={onOpen} title="Open session">
              <PlusIcon /> New
            </IconButton>
          </div>
        }
      />
      <div className="flex-1 overflow-y-auto" style={{ padding: "0 12px 12px" }}>
        {err && (
          <p className="text-text-muted" style={{ fontSize: "12px", padding: "8px 4px" }}>
            {err}
          </p>
        )}
        {!err && activeRows.length === 0 && (
          <div
            className="text-text-muted"
            style={{ fontSize: "12px", padding: "12px 4px", textAlign: "center" }}
          >
            <p style={{ marginBottom: "8px" }}>No active sessions.</p>
            <IconButton onClick={onOpen} title="Open session">
              <PlusIcon /> Open
            </IconButton>
          </div>
        )}
        <ul style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
          {activeRows.map((r) => (
            <BrowserListItem
              key={r.session_id}
              row={r}
              selected={r.session_id === selected}
              now={now}
              onSelect={() => onSelect(r.session_id)}
              onClose={() => onClose(r.session_id)}
            />
          ))}
        </ul>
        {pastRows.length > 0 && (
          <div className="border-t border-border" style={{ marginTop: "12px", paddingTop: "10px" }}>
            <div
              className="text-text-muted"
              style={{ fontSize: "11px", fontWeight: 600, textTransform: "uppercase", marginBottom: "8px" }}
            >
              Past sessions
            </div>
            <ul style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
              {pastRows.map((row) => (
                <BrowserListItem
                  key={row.session_id}
                  row={row}
                  selected={row.session_id === selected}
                  now={now}
                  onSelect={() => onSelect(row.session_id)}
                />
              ))}
            </ul>
          </div>
        )}
        <div className="border-t border-border" style={{ marginTop: "12px", paddingTop: "10px" }}>
          <div
            className="text-text-muted"
            style={{ fontSize: "11px", fontWeight: 600, textTransform: "uppercase", marginBottom: "8px" }}
          >
            Provider
          </div>
          <select
            value={settings.default_backend}
            onChange={(e) => onUpdateSettings({ default_backend: e.target.value })}
            className="border border-border bg-bg text-text"
            style={inputStyle}
            title="Default provider"
          >
            <option value="local">Local Chrome</option>
            <option value="browserbase">Browserbase</option>
            <option value="steel">Steel</option>
            <option value="browser-engine">Browser Engine</option>
            <option value="service">Browser Service</option>
          </select>
          <label
            className="text-text-muted"
            style={{ display: "flex", gap: "8px", alignItems: "center", marginTop: "8px", fontSize: "12px" }}
            title="Reject explicit session requests for any other provider"
          >
            <input
              type="checkbox"
              checked={settings.lock_backend}
              onChange={(e) => onUpdateSettings({ lock_backend: e.target.checked })}
            />
            Force this provider for agents
          </label>
        </div>
        <div className="border-t border-border" style={{ marginTop: "12px", paddingTop: "10px" }}>
          <div
            className="text-text-muted"
            style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "8px", fontSize: "11px", fontWeight: 600, textTransform: "uppercase", marginBottom: "8px" }}
          >
            <span>Proxy routing</span>
            <button
              type="button"
              onClick={onAddProxy}
              className="text-text-muted hover:text-text"
              style={{ background: "transparent", border: 0, padding: 0, cursor: "pointer", fontSize: "11px", textTransform: "none" }}
            >
              + Add profile
            </button>
          </div>
          <select
            value={settings.default_proxy_mode}
            onChange={(e) => {
              const mode = e.target.value;
              const patch: Partial<ComputerSettings> = { default_proxy_mode: mode };
              if (mode === "profile" && !settings.default_proxy_profile_id && proxyProfiles[0]) {
                patch.default_proxy_profile_id = proxyProfiles[0].id;
              }
              onUpdateSettings(patch);
            }}
            className="border border-border bg-bg text-text"
            style={inputStyle}
            title="Default proxy policy"
          >
            <option value="auto">Provider default</option>
            <option value="direct">Direct / no proxy</option>
            <option value="managed">Backend managed proxy</option>
            <option value="profile" disabled={proxyProfiles.length === 0}>Configured profile</option>
          </select>
          {settings.default_proxy_mode === "profile" && (
            <select
              value={settings.default_proxy_profile_id ?? ""}
              onChange={(e) => onUpdateSettings({ default_proxy_profile_id: e.target.value })}
              className="border border-border bg-bg text-text"
              style={{ ...inputStyle, marginTop: "7px" }}
              title="Default proxy profile"
            >
              <option value="">Select profile</option>
              {proxyProfiles.filter((profile) => profile.enabled).map((profile) => (
                <option key={profile.id} value={profile.id}>{profile.name}</option>
              ))}
            </select>
          )}
          <label
            className="text-text-muted"
            style={{ display: "flex", gap: "8px", alignItems: "center", marginTop: "8px", fontSize: "12px" }}
            title="Ignore per-session proxy overrides from agents"
          >
            <input
              type="checkbox"
              checked={settings.lock_proxy_policy}
              onChange={(e) => onUpdateSettings({ lock_proxy_policy: e.target.checked })}
            />
            Force this proxy policy for agents
          </label>
          {proxyProfiles.length === 0 ? (
            <div className="text-text-muted" style={{ fontSize: "11px", padding: "8px 2px 2px" }}>
              No external proxy profiles configured.
            </div>
          ) : (
            <ul style={{ display: "flex", flexDirection: "column", gap: "5px", marginTop: "9px" }}>
              {proxyProfiles.map((profile) => (
                <li key={profile.id} className="border border-border bg-bg-subtle" style={{ borderRadius: "6px", padding: "7px 9px" }}>
                  <div style={{ display: "flex", justifyContent: "space-between", gap: "8px", alignItems: "center" }}>
                    <span style={{ minWidth: 0 }}>
                      <span style={{ display: "block", fontSize: "12px", fontWeight: 500, overflow: "hidden", textOverflow: "ellipsis" }}>{profile.name}</span>
                      <span className="text-text-muted" style={{ display: "block", fontSize: "10px" }}>
                        {profile.provider_slug} · {profile.protocol} · {profile.default_country || "any country"} · {profile.sticky_scope}
                      </span>
                    </span>
                    <button
                      type="button"
                      onClick={() => onDeleteProxy(profile.id)}
                      title="Delete proxy profile"
                      className="text-text-muted hover:text-text"
                      style={{ background: "transparent", border: 0, padding: "1px", cursor: "pointer", display: "inline-flex" }}
                    >
                      <XIcon />
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="border-t border-border" style={{ marginTop: "12px", paddingTop: "10px" }}>
          <div
            className="text-text-muted"
            style={{ fontSize: "11px", fontWeight: 600, textTransform: "uppercase", marginBottom: "6px" }}
          >
            Contexts
          </div>
          {contexts.length === 0 ? (
            <div className="text-text-muted" style={{ fontSize: "12px", padding: "4px" }}>
              No saved contexts.
            </div>
          ) : (
            <ul style={{ display: "flex", flexDirection: "column", gap: "5px" }}>
              {contexts.slice(0, 8).map((ctx) => (
                <li key={ctx.id} className="border border-border bg-bg-subtle" style={{ borderRadius: "6px", padding: "7px 9px" }}>
                  <div style={{ display: "flex", justifyContent: "space-between", gap: "8px" }}>
                    <span style={{ fontSize: "12px", fontWeight: 500 }}>{ctx.name}</span>
                    <span style={{ display: "inline-flex", gap: "6px", alignItems: "center" }}>
                      <span className="text-text-muted" style={{ fontSize: "11px" }}>
                        {BACKEND_LABEL[ctx.backend] ?? ctx.backend}
                      </span>
                      <button
                        type="button"
                        onClick={() => onDeleteContext(ctx.id)}
                        title="Delete context"
                        className="text-text-muted hover:text-text"
                        style={{ background: "transparent", border: 0, padding: "1px", cursor: "pointer", display: "inline-flex" }}
                      >
                        <XIcon />
                      </button>
                    </span>
                  </div>
                  <div
                    className="text-text-muted"
                    style={{
                      marginTop: "2px",
                      fontSize: "11px",
                      fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {ctx.provider_context_id || "pending first persisted session"}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </Card>
  );
}

function BrowserListItem({
  row,
  selected,
  now,
  onSelect,
  onClose,
}: {
  row: SessionRow;
  selected: boolean;
  now: number;
  onSelect: () => void;
  onClose?: () => void;
}) {
  const isActive = !row.status || row.status === "active";
  const host = hostFor(row.current_url);
  const contextLabel = row.context_name || row.app_context_id || row.context_id || "";
  const openedAgo = relativeAge(row.opened_at, now);
  const lastUsedAgo = relativeAge(row.last_used_at, now);
  const viewport = row.width && row.height ? `${row.width}x${row.height}` : "";
  const providerLife = providerLifetimeLabel(row, now);
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        className={
          "w-full text-left border " +
          (selected
            ? "border-accent bg-bg-subtle text-text"
            : "border-border text-text hover:bg-bg-subtle")
        }
        style={{
          padding: "9px 10px",
          borderRadius: "6px",
          cursor: "pointer",
          display: "block",
        }}
      >
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: "8px",
            marginBottom: "2px",
          }}
        >
          <span
            style={{
              fontSize: "12px",
              fontWeight: 500,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            }}
          >
            {host}
          </span>
          <div style={{ display: "flex", alignItems: "center", gap: "4px" }}>
            <StatusPill variant={isActive ? "success" : "neutral"}>{row.status || "active"}</StatusPill>
            {isActive && onClose && (
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onClose();
                }}
                title="Close session"
                className="text-text-muted hover:text-text"
                style={{
                  background: "transparent",
                  border: 0,
                  padding: "2px 4px",
                  borderRadius: "4px",
                  cursor: "pointer",
                  display: "inline-flex",
                }}
              >
                <XIcon />
              </button>
            )}
          </div>
        </div>
        <div
          className="text-text-muted"
          style={{
            fontSize: "11px",
            display: "flex",
            gap: "6px",
            alignItems: "center",
            minWidth: 0,
          }}
        >
          <span>{BACKEND_LABEL[row.backend] ?? row.backend}</span>
          <span>|</span>
          <span
            style={{
              fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            }}
          >
            {row.session_id}
          </span>
        </div>
        <div
          className="text-text-muted"
          style={{
            marginTop: "6px",
            display: "grid",
            gridTemplateColumns: "auto 1fr",
            columnGap: "8px",
            rowGap: "2px",
            fontSize: "11px",
            lineHeight: 1.35,
          }}
        >
          <span>Opened</span>
          <span className="text-text" title={formatTime(row.opened_at)}>
            {openedAgo}
          </span>
          <span>Context</span>
          <span
            className={contextLabel ? "text-text" : "text-text-muted"}
            title={contextLabel || "No saved context"}
            style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 }}
          >
            {contextLabel || "none"}
            {isActive && contextLabel && row.persist === false ? " (read-only)" : ""}
          </span>
          <span>{isActive ? "Last used" : "Closed"}</span>
          <span title={formatTime(isActive ? row.last_used_at : row.closed_at || row.last_used_at)}>
            {isActive ? lastUsedAgo : relativeAge(row.closed_at || row.last_used_at, now)}
            {isActive && viewport ? ` | ${viewport}` : ""}
            {isActive && row.tab_count && row.tab_count > 1 ? ` | ${row.tab_count} tabs` : ""}
          </span>
          <span>{isActive ? "Provider" : "Recording"}</span>
          <span title={isActive && row.provider_expires_at ? formatTime(row.provider_expires_at) : undefined}>
            {isActive ? providerLife : recordingStatusLabel(row.recording_status ?? "unavailable")}
          </span>
        </div>
      </button>
    </li>
  );
}

function TabStrip({
  session,
  busy,
  onSwitch,
  onClose,
}: {
  session: SessionRow;
  busy: boolean;
  onSwitch: (tabID: string) => Promise<void>;
  onClose: (tabID: string) => Promise<void>;
}) {
  const tabs = session.tabs ?? [];
  if (tabs.length <= 1) {
    return null;
  }
  const activeID = session.active_tab_id || tabs.find((t) => t.active)?.tab_id || "";
  return (
    <div className="border border-border bg-bg-subtle" style={{ borderRadius: "6px", padding: "6px", overflowX: "auto" }}>
      <div style={{ display: "flex", gap: "6px", minWidth: "max-content" }}>
        {tabs.map((tab) => {
          const active = tab.tab_id === activeID || tab.active;
          const label = tab.title || hostFor(tab.url) || "Untitled";
          return (
            <div
              key={tab.tab_id}
              className={active ? "border border-accent bg-bg text-text" : "border border-border bg-bg text-text-muted"}
              style={{
                display: "inline-flex",
                alignItems: "center",
                gap: "6px",
                maxWidth: "220px",
                padding: "5px 7px",
                borderRadius: "6px",
                fontSize: "12px",
              }}
              title={tab.url || tab.tab_id}
            >
              <button
                type="button"
                disabled={busy || active}
                onClick={() => onSwitch(tab.tab_id)}
                className={active ? "text-text" : "text-text-muted hover:text-text"}
                style={{
                  border: 0,
                  background: "transparent",
                  padding: 0,
                  cursor: busy || active ? "default" : "pointer",
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  whiteSpace: "nowrap",
                  maxWidth: "170px",
                  fontWeight: active ? 600 : 500,
                }}
              >
                {label}
              </button>
              <button
                type="button"
                disabled={busy || tabs.length <= 1}
                onClick={() => onClose(tab.tab_id)}
                title="Close tab"
                className="text-text-muted hover:text-text"
                style={{
                  border: 0,
                  background: "transparent",
                  padding: "1px",
                  cursor: busy || tabs.length <= 1 ? "not-allowed" : "pointer",
                  display: "inline-flex",
                }}
              >
                <XIcon />
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function SessionDetail({
  session,
  projectId,
  now,
  onClose,
  onRefresh,
  externalRefreshKey,
}: {
  session: SessionRow | null;
  projectId?: string;
  now: number;
  onClose: (id: string) => void;
  onRefresh: () => Promise<void>;
  externalRefreshKey: number;
}) {
  const [tick, setTick] = useState(0);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [typeText, setTypeText] = useState("");
  const [keyText, setKeyText] = useState("Enter");
  const [manualX, setManualX] = useState("");
  const [manualY, setManualY] = useState("");
  const [embedLive, setEmbedLive] = useState(false);

  useEffect(() => {
    setErr(null);
    setBusy(null);
    setTick((n) => n + 1);
  }, [session?.session_id]);

  useEffect(() => {
    if (!session) return;
    setTick((n) => n + 1);
  }, [externalRefreshKey, session?.session_id]);

  if (!session) {
    return (
      <Card fullWidth className="h-full min-h-0 flex flex-col">
        <CardHeader title="Session" />
        <div
          className="text-text-muted"
          style={{
            flex: 1,
            minHeight: 0,
            padding: "32px 16px",
            textAlign: "center",
            fontSize: "13px",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
          }}
        >
          Select a browser session on the left.
        </div>
      </Card>
    );
  }

  const sendUse = async (action: string, body: Record<string, any> = {}) => {
    setBusy(action);
    setErr(null);
    try {
      const res = await fetch(appURL(`${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/use`, projectId), {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action, ...body }),
      });
      const j = await res.json();
      if (!res.ok || j.error) throw new Error(j.error ?? `HTTP ${res.status}`);
      setTick((n) => n + 1);
      await onRefresh();
      return j;
    } catch (e: any) {
      setErr(String(e?.message ?? e));
      return null;
    } finally {
      setBusy(null);
    }
  };

  const sendCoordinate = (action: "click" | "double_click", x: number, y: number) =>
    sendUse(action, { coordinate: `${Math.round(x)},${Math.round(y)}` });

  const host = hostFor(session.current_url);
  const currentURL = session.current_url || "-";
  const viewport = `${session.width ?? 0} x ${session.height ?? 0}`;
  const providerLife = providerLifetimeLabel(session, now);
  const isActive = !session.status || session.status === "active";

  if (!isActive) {
    return (
      <RecordingSessionDetail
        session={session}
        projectId={projectId}
        externalRefreshKey={externalRefreshKey}
      />
    );
  }

  return (
    <Card fullWidth className="h-full min-h-0 flex flex-col overflow-hidden">
      <CardHeader
        title={host || "Session"}
        right={
          <div style={{ display: "flex", gap: "6px", alignItems: "center" }}>
            {session.recording_supported && (
              <StatusPill variant="neutral">{recordingStatusLabel(session.recording_status ?? "recording")}</StatusPill>
            )}
            <StatusPill variant="neutral">{BACKEND_LABEL[session.backend] ?? session.backend}</StatusPill>
          </div>
        }
      />
      <div
        style={{
          flex: 1,
          minHeight: 0,
          overflowY: "auto",
          padding: "0 16px 16px",
          display: "flex",
          flexDirection: "column",
          gap: "12px",
        }}
      >
        <TabStrip
          session={session}
          busy={Boolean(busy)}
          onSwitch={async (tabID) => {
            setBusy("switch_tab");
            setErr(null);
            try {
              const res = await fetch(appURL(`${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/tabs/${encodeURIComponent(tabID)}/switch`, projectId), {
                method: "POST",
                credentials: "include",
              });
              const j = await res.json();
              if (!res.ok || j.error) throw new Error(j.error ?? `HTTP ${res.status}`);
              setTick((n) => n + 1);
              await onRefresh();
            } catch (e: any) {
              setErr(String(e?.message ?? e));
            } finally {
              setBusy(null);
            }
          }}
          onClose={async (tabID) => {
            setBusy("close_tab");
            setErr(null);
            try {
              const res = await fetch(appURL(`${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/tabs/${encodeURIComponent(tabID)}`, projectId), {
                method: "DELETE",
                credentials: "include",
              });
              const j = await res.json();
              if (!res.ok || j.error) throw new Error(j.error ?? `HTTP ${res.status}`);
              setTick((n) => n + 1);
              await onRefresh();
            } catch (e: any) {
              setErr(String(e?.message ?? e));
            } finally {
              setBusy(null);
            }
          }}
        />

        <div style={{ flex: "0 0 auto", minHeight: 0 }}>
          {session.stream_url && embedLive ? (
            <div className="border border-border bg-bg-subtle" style={browserViewportFrameStyle}>
              <iframe
                src={session.stream_url}
                title="Live browser stream"
                style={{ width: "100%", height: "100%", border: 0, display: "block" }}
              />
            </div>
          ) : (
            <InteractivePreview
              session={session}
              projectId={projectId}
              tick={tick}
              busy={Boolean(busy)}
              onClickPoint={(x, y) => sendCoordinate("click", x, y)}
            />
          )}
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: "8px", flexWrap: "wrap", flex: "0 0 auto" }}>
          {session.stream_url && (
            <IconButton onClick={() => setEmbedLive((v) => !v)} title="Toggle stream">
              <MonitorIcon /> {embedLive ? "Snapshot" : "Stream"}
            </IconButton>
          )}
          {session.debug_url && (
            <a
              href={session.debug_url}
              target="_blank"
              rel="noreferrer"
              className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
              style={linkButtonStyle}
            >
              <ExternalIcon /> Live link
            </a>
          )}
          <IconButton onClick={() => sendUse("screenshot")} disabled={Boolean(busy)} title="Refresh">
            <RefreshIcon /> Refresh
          </IconButton>
          <IconButton onClick={() => onClose(session.session_id)} title="Close session">
            <XIcon /> Close
          </IconButton>
        </div>

        <div
          className="border border-border bg-bg-subtle"
          style={{ borderRadius: "6px", padding: "10px", display: "flex", flexDirection: "column", gap: "10px", flex: "0 0 auto" }}
        >
          <div style={{ display: "flex", alignItems: "center", flexWrap: "wrap", gap: "8px" }}>
            <input
              value={typeText}
              onChange={(e) => setTypeText(e.target.value)}
              className="border border-border bg-bg text-text"
              style={{ ...inputStyle, flex: "1 1 280px", minWidth: 0 }}
              placeholder="Text"
            />
            <IconButton
              disabled={!typeText || Boolean(busy)}
              onClick={() => sendUse("type", { text: typeText })}
              title="Type text"
            >
              Type
            </IconButton>
          </div>
          <div style={{ display: "flex", alignItems: "center", flexWrap: "wrap", gap: "8px" }}>
            <input
              value={keyText}
              onChange={(e) => setKeyText(e.target.value)}
              className="border border-border bg-bg text-text"
              style={{ ...inputStyle, flex: "1 1 240px", minWidth: 0 }}
              placeholder="Key"
            />
            <IconButton disabled={!keyText || Boolean(busy)} onClick={() => sendUse("key", { key: keyText })} title="Send key">
              Key
            </IconButton>
            <IconButton disabled={Boolean(busy)} onClick={() => sendUse("key", { key: "Backspace" })} title="Backspace">
              Backspace
            </IconButton>
            <IconButton disabled={Boolean(busy)} onClick={() => sendUse("key", { key: "Delete" })} title="Delete">
              Delete
            </IconButton>
            <IconButton disabled={Boolean(busy)} onClick={() => sendUse("scroll", { direction: "up", amount: 550 })} title="Scroll up">
              Up
            </IconButton>
            <IconButton disabled={Boolean(busy)} onClick={() => sendUse("scroll", { direction: "down", amount: 550 })} title="Scroll down">
              Down
            </IconButton>
            <IconButton disabled={Boolean(busy)} onClick={() => sendUse("wait", { duration: 1000 })} title="Wait">
              Wait
            </IconButton>
          </div>
          <div style={{ display: "flex", alignItems: "center", flexWrap: "wrap", gap: "8px" }}>
            <input
              value={manualX}
              onChange={(e) => setManualX(e.target.value)}
              className="border border-border bg-bg text-text"
              style={{ ...inputStyle, flex: "1 1 180px", minWidth: 0 }}
              placeholder="X"
              inputMode="numeric"
            />
            <input
              value={manualY}
              onChange={(e) => setManualY(e.target.value)}
              className="border border-border bg-bg text-text"
              style={{ ...inputStyle, flex: "1 1 180px", minWidth: 0 }}
              placeholder="Y"
              inputMode="numeric"
            />
            <IconButton
              disabled={!manualX || !manualY || Boolean(busy)}
              onClick={() => sendCoordinate("click", Number(manualX), Number(manualY))}
              title="Click coordinate"
            >
              Click
            </IconButton>
            <IconButton
              disabled={!manualX || !manualY || Boolean(busy)}
              onClick={() => sendCoordinate("double_click", Number(manualX), Number(manualY))}
              title="Double click coordinate"
            >
              Double
            </IconButton>
          </div>
          <div style={actionStatusStyle}>
            {err ? (
              <span style={{ color: "#dc2626" }}>{err}</span>
            ) : busy ? (
              <span className="text-text-muted">{busy}...</span>
            ) : (
              <span aria-hidden>&nbsp;</span>
            )}
          </div>
        </div>

        <DataList
          items={[
            { label: "App session", value: session.session_id },
            { label: "Backend session", value: session.backend_session_id || "-" },
            { label: "App context", value: session.context_name || session.app_context_id || "-" },
            { label: "Provider context", value: session.context_id || "-" },
            { label: "Proxy", value: sessionProxyLabel(session.proxy) },
            { label: "Recording", value: recordingStatusLabel(session.recording_status ?? (session.recording_supported ? "recording" : "unsupported")) },
            { label: "Persist changes", value: session.persist ? "yes" : "no" },
            { label: "Provider timeout", value: session.timeout_seconds ? formatDurationSeconds(session.timeout_seconds) : "provider default" },
            { label: "Provider expires", value: session.provider_expires_at ? `${providerLife} (${formatTime(session.provider_expires_at)})` : providerLife },
            { label: "Current URL", value: currentURL },
            { label: "Viewport", value: viewport },
            { label: "Opened", value: formatTime(session.opened_at) },
            { label: "Last used", value: formatTime(session.last_used_at) },
          ]}
        />
      </div>
    </Card>
  );
}

function RecordingSessionDetail({
  session,
  projectId,
  externalRefreshKey,
}: {
  session: SessionRow;
  projectId?: string;
  externalRefreshKey: number;
}) {
  const [metadata, setMetadata] = useState<RecordingMetadata | null>(null);
  const [selectedStream, setSelectedStream] = useState("");
  const [err, setErr] = useState<string | null>(null);

  const refreshRecording = usePollingRefresh(async (signal) => {
    try {
      const response = await fetch(
        appURL(`${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/recording`, projectId),
        { credentials: "include", signal },
      );
      const body = (await response.json()) as RecordingMetadata;
      if (!response.ok || body.error) throw new Error(body.error ?? `HTTP ${response.status}`);
      if(signal.aborted) return;
      setMetadata(body);
      setSelectedStream((current) => {
        if (body.streams.some((stream) => stream.id === current)) return current;
        return body.streams[0]?.id ?? "";
      });
      setErr(null);
    } catch (e: any) {
      if(!signal.aborted) setErr(String(e?.message ?? e));
    }
  }, `${projectId ?? ""}:${session.session_id}`, (metadata?.status ?? session.recording_status) === "processing" || (metadata?.status ?? session.recording_status) === "recording" ? POLL_MS : 0);

  useEffect(() => { setMetadata(null); setSelectedStream(""); setErr(null); }, [projectId, session.session_id]);
  useEffect(() => { void refreshRecording(); }, [externalRefreshKey, refreshRecording]);

  const status = metadata?.status ?? session.recording_status ?? (session.recording_supported ? "processing" : "unsupported");
  const streams = metadata?.streams ?? [];
  const stream = streams.find((item) => item.id === selectedStream) ?? streams[0];
  const host = hostFor(session.current_url);

  return (
    <Card fullWidth className="h-full min-h-0 flex flex-col overflow-hidden">
      <CardHeader
        title={host || "Recording"}
        right={
          <div style={{ display: "flex", gap: "6px", alignItems: "center" }}>
            <StatusPill variant="neutral">{recordingStatusLabel(status)}</StatusPill>
            <StatusPill variant="neutral">{BACKEND_LABEL[session.backend] ?? session.backend}</StatusPill>
          </div>
        }
      />
      <div
        style={{
          flex: 1,
          minHeight: 0,
          overflowY: "auto",
          padding: "0 16px 16px",
          display: "flex",
          flexDirection: "column",
          gap: "12px",
        }}
      >
        <div style={{ flex: "0 0 auto", minHeight: 0, display: "flex", flexDirection: "column", gap: "8px" }}>
          {streams.length > 1 && (
            <div role="tablist" aria-label="Recording streams" style={{ display: "flex", gap: "4px", flexWrap: "wrap" }}>
              {streams.map((item, index) => (
                <button
                  key={item.id}
                  type="button"
                  role="tab"
                  aria-selected={item.id === stream?.id}
                  onClick={() => setSelectedStream(item.id)}
                  className={item.id === stream?.id ? "border-accent bg-bg-subtle text-text" : "border-border bg-bg text-text-muted"}
                  style={{ borderWidth: "1px", borderStyle: "solid", borderRadius: "5px", padding: "5px 9px", fontSize: "12px", cursor: "pointer" }}
                >
                  Tab {index + 1}
                </button>
              ))}
            </div>
          )}
          {status === "ready" && stream ? (
            <RecordingPlayer
              key={`${session.session_id}:${stream.id}`}
              stream={stream}
            />
          ) : (
            <div
              className="border border-border bg-bg-subtle text-text-muted"
              style={{ ...browserViewportFrameStyle, display: "flex", alignItems: "center", justifyContent: "center", gap: "8px", fontSize: "13px" }}
            >
              {status === "processing" || status === "recording" ? <StatusDot variant="active">{null}</StatusDot> : <StatusDot variant={status === "failed" ? "error" : "muted"}>{null}</StatusDot>}
              {err || metadata?.message || recordingStatusLabel(status)}
            </div>
          )}
        </div>
        <DataList
          items={[
            { label: "App session", value: session.session_id },
            { label: "Backend session", value: session.backend_session_id || "-" },
            { label: "Recording", value: recordingStatusLabel(status) },
            { label: "Proxy traffic", value: sessionUsageLabel(session.usage) },
            { label: "Current URL", value: session.current_url || "-" },
            { label: "Opened", value: formatTime(session.opened_at) },
            { label: "Closed", value: session.closed_at ? formatTime(session.closed_at) : "-" },
          ]}
        />
      </div>
    </Card>
  );
}

function RecordingPlayer({ stream }: { stream: RecordingStream }) {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    let hls: Hls | null = null;
    setErr(null);

    if (Hls.isSupported()) {
      hls = new Hls({ enableWorker: true });
      hls.loadSource(stream.playlist_url);
      hls.attachMedia(video);
      hls.on(Hls.Events.ERROR, (_event, data) => {
        if (data.fatal) setErr(data.details || "Playback failed");
      });
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = stream.playlist_url;
      video.load();
    } else {
      setErr("HLS playback is unavailable in this browser");
    }

    return () => {
      hls?.destroy();
      video.removeAttribute("src");
      video.load();
    };
  }, [stream.playlist_url]);

  return (
    <div
      className="border border-border bg-black"
      style={{
        ...browserViewportFrameStyle,
        position: "relative",
      }}
    >
      <video
        ref={videoRef}
        controls
        playsInline
        preload="metadata"
        style={{ width: "100%", height: "100%", display: "block", objectFit: "contain" }}
      />
      {err && <div className="text-text-muted" style={previewOverlayStyle}>{err}</div>}
    </div>
  );
}

function InteractivePreview({
  session,
  projectId,
  tick,
  busy,
  onClickPoint,
}: {
  session: SessionRow;
  projectId?: string;
  tick: number;
  busy: boolean;
  onClickPoint: (x: number, y: number) => void;
}) {
  const imgRef = useRef<HTMLImageElement | null>(null);
  const [stale, setStale] = useState(false);
  const src = appURL(`${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/screenshot`, projectId, { t: tick });

  useEffect(() => {
    setStale(false);
  }, [session.session_id, tick]);

  const clickImage = (e: any) => {
    if (busy) return;
    const img = imgRef.current;
    if (!img) return;
    const rect = img.getBoundingClientRect();
    const naturalW = img.naturalWidth || session.width || rect.width;
    const naturalH = img.naturalHeight || session.height || rect.height;
    const scale = Math.min(rect.width / naturalW, rect.height / naturalH);
    const renderedW = naturalW * scale;
    const renderedH = naturalH * scale;
    const offsetX = (rect.width - renderedW) / 2;
    const offsetY = (rect.height - renderedH) / 2;
    const localX = e.clientX - rect.left - offsetX;
    const localY = e.clientY - rect.top - offsetY;
    if (localX < 0 || localY < 0 || localX > renderedW || localY > renderedH) return;
    const x = (localX / renderedW) * naturalW;
    const y = (localY / renderedH) * naturalH;
    onClickPoint(x, y);
  };

  return (
    <div
      className="border border-border bg-bg-subtle"
      style={{
        ...browserViewportFrameStyle,
        position: "relative",
      }}
    >
      <img
        ref={imgRef}
        src={src}
        alt="browser"
        onClick={clickImage}
        onLoad={() => setStale(false)}
        onError={() => setStale(true)}
        style={{
          width: "100%",
          height: "100%",
          objectFit: "contain",
          display: "block",
          cursor: busy ? "wait" : "crosshair",
          userSelect: "none",
        }}
        draggable={false}
      />
      {stale && (
        <div className="text-text-muted" style={previewOverlayStyle}>
          waiting for next frame...
        </div>
      )}
    </div>
  );
}

function OpenSessionModal({
  onClose,
  contexts,
  settings,
  proxyProfiles,
  projectId,
  onOpened,
}: {
  onClose: () => void;
  contexts: ContextRow[];
  settings: ComputerSettings;
  proxyProfiles: ProxyProfile[];
  projectId?: string;
  onOpened: (sessionID: string) => void;
}) {
  const [mode, setMode] = useState<OpenMode>("open");
  const [url, setUrl] = useState("https://");
  const [backend, setBackend] = useState("");
  const [contextID, setContextID] = useState("");
  const [contextName, setContextName] = useState("");
  const [providerContextID, setProviderContextID] = useState("");
  const [persist, setPersist] = useState(true);
  const [backendSessionID, setBackendSessionID] = useState("");
  const [timeout, setTimeoutValue] = useState("");
  const [proxyMode, setProxyMode] = useState<ProxyMode>("");
  const [proxyProfile, setProxyProfile] = useState("");
  const [proxyCountry, setProxyCountry] = useState("");
  const [proxySticky, setProxySticky] = useState("");
  const [width, setWidth] = useState("1600");
  const [height, setHeight] = useState("800");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const body: Record<string, any> = {
        action: mode === "resume" ? "resume" : "open",
      };
      if (url && url !== "https://") body.url = url;
      if (backend) body.backend = backend;
      if (contextID && mode === "context") body.context_id = contextID;
      if (contextID && mode === "open") body.provider_context_id = contextID;
      if (contextName && mode === "create_context") {
        body.context_name = contextName;
        body.auto_create_context = true;
      }
      if (providerContextID && mode !== "resume") body.provider_context_id = providerContextID;
      if (mode === "resume" && backendSessionID) body.backend_session_id = backendSessionID;
      if (mode !== "resume") body.persist = persist;
      if (timeout) body.timeout = Number(timeout);
      if (proxyMode) body.proxy_mode = proxyMode;
      if (proxyProfile) body.proxy_profile = proxyProfile;
      if (proxyCountry) body.proxy_country = proxyCountry.toUpperCase();
      if (proxySticky) body.proxy_sticky = proxySticky;
      const viewport: Record<string, number> = {};
      if (width) viewport.width = Number(width);
      if (height) viewport.height = Number(height);
      if (viewport.width || viewport.height) body.viewport = viewport;

      const res = await fetch(appURL(SESSIONS_URL, projectId), {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const j = await res.json();
      if (!res.ok || j.error) {
        setErr(j.error ?? `HTTP ${res.status}`);
        return;
      }
      onOpened(String(j.session_id ?? ""));
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      onClick={onClose}
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,0.4)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        zIndex: 50,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="bg-bg border border-border text-text"
        style={{
          width: "560px",
          maxWidth: "94vw",
          maxHeight: "90vh",
          overflow: "auto",
          padding: "18px",
          borderRadius: "8px",
        }}
      >
        <h2 style={{ fontSize: "16px", fontWeight: 600, marginBottom: "12px" }}>
          Browser session
        </h2>
        <div style={{ display: "flex", gap: "6px", marginBottom: "12px" }}>
          <ModeButton active={mode === "open"} onClick={() => setMode("open")}>Open</ModeButton>
          <ModeButton active={mode === "context"} onClick={() => setMode("context")}>Context</ModeButton>
          <ModeButton active={mode === "create_context"} onClick={() => setMode("create_context")}>Save</ModeButton>
          <ModeButton active={mode === "resume"} onClick={() => setMode("resume")}>Resume</ModeButton>
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "10px" }}>
          <Field label="Backend">
            <select value={backend} onChange={(e) => setBackend(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle}>
              <option value="">App default ({BACKEND_LABEL[settings.default_backend] ?? settings.default_backend})</option>
              {backendOptions.map((opt) => (
                <option
                  key={opt.value}
                  value={opt.value}
                  disabled={settings.lock_backend && opt.value !== settings.default_backend}
                >
                  {opt.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label="URL">
            <input value={url} onChange={(e) => setUrl(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
          </Field>
          {mode === "resume" ? (
            <Field label="Backend session ID">
              <input value={backendSessionID} onChange={(e) => setBackendSessionID(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
            </Field>
          ) : mode === "context" ? (
            <Field label="Saved context">
              <select
                value={contextID}
                onChange={(e) => {
                  const id = e.target.value;
                  setContextID(id);
                  const ctx = contexts.find((c) => c.id === id);
                  if (ctx) {
                    setBackend(ctx.backend);
                    setPersist(ctx.persist_default);
                  }
                }}
                className="border border-border bg-bg text-text"
                style={inputStyle}
              >
                <option value="">Select context</option>
                {contexts.map((ctx) => (
                  <option key={ctx.id} value={ctx.id}>
                    {ctx.name} - {BACKEND_LABEL[ctx.backend] ?? ctx.backend}
                  </option>
                ))}
              </select>
            </Field>
          ) : mode === "create_context" ? (
            <Field label="Context name">
              <input value={contextName} onChange={(e) => setContextName(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
            </Field>
          ) : (
            <Field label="Provider context ID">
              <input value={contextID} onChange={(e) => setContextID(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
            </Field>
          )}
          {mode === "create_context" && (
            <Field label="Import provider context">
              <input value={providerContextID} onChange={(e) => setProviderContextID(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
            </Field>
          )}
          <Field label="Timeout seconds">
            <input value={timeout} onChange={(e) => setTimeoutValue(e.target.value)} inputMode="numeric" className="border border-border bg-bg text-text" style={inputStyle} />
          </Field>
          <Field label="Viewport width">
            <input value={width} onChange={(e) => setWidth(e.target.value)} inputMode="numeric" className="border border-border bg-bg text-text" style={inputStyle} />
          </Field>
          <Field label="Viewport height">
            <input value={height} onChange={(e) => setHeight(e.target.value)} inputMode="numeric" className="border border-border bg-bg text-text" style={inputStyle} />
          </Field>
          <Field label="Proxy policy">
            <select
              value={proxyMode}
              onChange={(e) => {
                const next = e.target.value as ProxyMode;
                setProxyMode(next);
                if (next !== "profile") setProxyProfile("");
                if (next === "direct" || next === "auto") {
                  setProxyCountry("");
                  setProxySticky("");
                }
              }}
              className="border border-border bg-bg text-text"
              style={inputStyle}
              disabled={settings.lock_proxy_policy}
            >
              <option value="">App default ({proxyModeLabel(settings.default_proxy_mode)})</option>
              <option value="auto">Provider default</option>
              <option value="direct">Direct / no proxy</option>
              <option value="managed">Backend managed proxy</option>
              <option value="profile" disabled={proxyProfiles.length === 0}>Configured profile</option>
            </select>
          </Field>
          {proxyMode === "profile" && (
            <Field label="Proxy profile">
              <select value={proxyProfile} onChange={(e) => setProxyProfile(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle}>
                <option value="">Select profile</option>
                {proxyProfiles.map((profile) => (
                  <option key={profile.id} value={profile.id}>{profile.name}</option>
                ))}
              </select>
            </Field>
          )}
          {(proxyMode === "managed" || proxyMode === "profile") && (
            <Field label="Proxy country">
              <input value={proxyCountry} onChange={(e) => setProxyCountry(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} placeholder="US" maxLength={2} />
            </Field>
          )}
          {proxyMode === "profile" && (
            <Field label="Proxy stickiness">
              <select value={proxySticky} onChange={(e) => setProxySticky(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle}>
                <option value="">Profile default</option>
                <option value="rotating">Rotate requests</option>
                <option value="session">Keep for session</option>
                <option value="context">Keep for context</option>
              </select>
            </Field>
          )}
        </div>
        {mode !== "resume" && (
          <div style={{ display: "grid", gap: "8px", marginTop: "10px", fontSize: "13px" }}>
            <label style={{ display: "flex", gap: "8px", alignItems: "center" }}>
              <input type="checkbox" checked={persist} onChange={(e) => setPersist(e.target.checked)} />
              Persist context changes
            </label>
          </div>
        )}
        {err && <div style={{ marginTop: "10px", fontSize: "12px", color: "#dc2626" }}>{err}</div>}
        <div style={{ marginTop: "16px", display: "flex", justifyContent: "flex-end", gap: "8px" }}>
          <IconButton onClick={onClose} disabled={busy} title="Cancel">Cancel</IconButton>
          <IconButton onClick={submit} disabled={busy} title="Open session">
            {busy ? "Opening..." : mode === "resume" ? "Resume" : mode === "create_context" ? "Save and open" : "Open"}
          </IconButton>
        </div>
      </div>
    </div>
  );
}

function AddProxyProfileModal({
  onClose,
  projectId,
  connections,
  onCreated,
}: {
  onClose: () => void;
  projectId?: string;
  connections: ProxyConnection[];
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [connectionID, setConnectionID] = useState(() => String(connections.find((connection) => connection.default)?.connection_id ?? connections[0]?.connection_id ?? ""));
  const [resourceRef, setResourceRef] = useState("");
  const [protocol, setProtocol] = useState("http");
  const [country, setCountry] = useState("");
  const [sticky, setSticky] = useState("session");
  const [resources, setResources] = useState<ProxyResource[]>([]);
  const [resourcesLoading, setResourcesLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const selectedConnection = connections.find((connection) => String(connection.connection_id) === connectionID);
  const providerSlug = selectedConnection?.provider_slug ?? "";
  const resourceRequired = providerSlug === "dataimpulse" || providerSlug === "iproyal" || providerSlug === "proxy-cheap";

  useEffect(() => {
    if (providerSlug === "proxy-cheap" && protocol === "socks5") setProtocol("http");
  }, [providerSlug, protocol]);

  useEffect(() => {
    if (!connectionID) {
      setResources([]);
      return;
    }
    let cancelled = false;
    setResourcesLoading(true);
    fetch(appURL(PROXY_RESOURCES_URL, projectId, { connection_id: connectionID }), { credentials: "include" })
      .then(async (response) => {
        const body = await response.json();
        if (!response.ok || body.error) throw new Error(body.error ?? `HTTP ${response.status}`);
        if (!cancelled) setResources(body.resources ?? []);
      })
      .catch(() => {
        if (!cancelled) setResources([]);
      })
      .finally(() => {
        if (!cancelled) setResourcesLoading(false);
      });
    return () => { cancelled = true; };
  }, [connectionID, projectId]);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      if (!selectedConnection) throw new Error("Select a connected proxy provider");
      const res = await fetch(appURL(PROXY_PROFILES_URL, projectId), {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name,
          provider_slug: selectedConnection.provider_slug,
          connection_id: selectedConnection.connection_id,
          external_ref: resourceRef,
          pool_type: "residential",
          protocol,
          default_country: country.toUpperCase(),
          sticky_scope: sticky,
        }),
      });
      const body = await res.json();
      if (!res.ok || body.error) throw new Error(body.error ?? `HTTP ${res.status}`);
      onCreated();
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      onClick={onClose}
      style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,0.4)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 50 }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="bg-bg border border-border text-text"
        style={{ width: "500px", maxWidth: "94vw", padding: "18px", borderRadius: "8px" }}
      >
        <h2 style={{ fontSize: "16px", fontWeight: 600, marginBottom: "5px" }}>Add proxy profile</h2>
        <p className="text-text-muted" style={{ fontSize: "12px", marginBottom: "14px" }}>
          Computer stores only this routing metadata. Credentials stay in the connected provider integration.
        </p>
        {connections.length === 0 ? (
          <div className="border border-border bg-bg-subtle text-text-muted" style={{ borderRadius: "6px", padding: "12px", fontSize: "12px" }}>
            Connect and bind DataImpulse, IPRoyal, or Proxy-Cheap to Computer first.
          </div>
        ) : (
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "10px" }}>
            <Field label="Profile name">
              <input value={name} onChange={(e) => setName(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} placeholder="US research" />
            </Field>
            <Field label="Provider connection">
              <select value={connectionID} onChange={(e) => { setConnectionID(e.target.value); setResourceRef(""); }} className="border border-border bg-bg text-text" style={inputStyle}>
                {connections.map((connection) => (
                  <option key={connection.connection_id} value={connection.connection_id}>{connection.name} ({connection.provider_slug})</option>
                ))}
              </select>
            </Field>
            <Field label={proxyResourceLabel(providerSlug)}>
              <input value={resourceRef} onChange={(e) => setResourceRef(e.target.value)} list="computer-proxy-resources" inputMode={providerSlug === "dataimpulse" ? "numeric" : "text"} className="border border-border bg-bg text-text" style={inputStyle} placeholder={resourcesLoading ? "Loading..." : proxyResourcePlaceholder(providerSlug)} />
              <datalist id="computer-proxy-resources">
                {resources.map((resource) => <option key={resource.id} value={resource.id}>{resource.name}</option>)}
              </datalist>
            </Field>
            <Field label="Protocol">
              <select value={protocol} onChange={(e) => setProtocol(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle}>
                <option value="http">HTTP</option>
                <option value="https">HTTPS</option>
                {providerSlug !== "proxy-cheap" && <option value="socks5">SOCKS5</option>}
              </select>
            </Field>
            <Field label="Default country">
              <input value={country} onChange={(e) => setCountry(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} placeholder="Any" maxLength={2} />
            </Field>
            <Field label="Default stickiness">
              <select value={sticky} onChange={(e) => setSticky(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle}>
                <option value="rotating">Rotate requests</option>
                <option value="session">Keep for session</option>
                <option value="context">Keep for context</option>
              </select>
            </Field>
          </div>
        )}
        {err && <div style={{ marginTop: "10px", fontSize: "12px", color: "#dc2626" }}>{err}</div>}
        <div style={{ marginTop: "16px", display: "flex", justifyContent: "flex-end", gap: "8px" }}>
          <IconButton onClick={onClose} disabled={busy} title="Cancel">Cancel</IconButton>
          <IconButton onClick={submit} disabled={busy || connections.length === 0 || !name.trim() || (resourceRequired && !resourceRef.trim())} title="Create proxy profile">
            {busy ? "Creating..." : "Create profile"}
          </IconButton>
        </div>
      </div>
    </div>
  );
}

function proxyResourceLabel(provider: string): string {
  switch (provider) {
    case "dataimpulse": return "Sub-user ID";
    case "iproyal": return "Sub-user hash";
    case "proxy-cheap": return "Order ID";
    default: return "Provider resource";
  }
}

function proxyResourcePlaceholder(provider: string): string {
  switch (provider) {
    case "dataimpulse": return "12345";
    case "iproyal": return "Residential sub-user hash";
    case "proxy-cheap": return "Rotating residential order ID";
    default: return "Provider resource ID";
  }
}

function ConfirmModal({
  title,
  body,
  confirmLabel,
  busyLabel,
  onCancel,
  onConfirm,
}: {
  title: string;
  body: string;
  confirmLabel: string;
  busyLabel: string;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const confirm = async () => {
    setBusy(true);
    setErr(null);
    try {
      await onConfirm();
    } catch (e: any) {
      setErr(String(e?.message ?? e));
      setBusy(false);
    }
  };

  return (
    <div
      onClick={busy ? undefined : onCancel}
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,0.4)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        zIndex: 60,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="bg-bg border border-border text-text"
        role="dialog"
        aria-modal="true"
        aria-labelledby="computer-confirm-title"
        style={{
          width: "380px",
          maxWidth: "92vw",
          padding: "18px",
          borderRadius: "8px",
          boxShadow: "0 18px 50px rgba(0,0,0,0.25)",
        }}
      >
        <h2 id="computer-confirm-title" style={{ fontSize: "16px", fontWeight: 600, marginBottom: "8px" }}>
          {title}
        </h2>
        <p className="text-text-muted" style={{ fontSize: "13px", lineHeight: 1.5, margin: 0 }}>
          {body}
        </p>
        {err && <div style={{ marginTop: "10px", fontSize: "12px", color: "#dc2626" }}>{err}</div>}
        <div style={{ marginTop: "16px", display: "flex", justifyContent: "flex-end", gap: "8px" }}>
          <IconButton onClick={onCancel} disabled={busy} title="Cancel">Cancel</IconButton>
          <DangerButton onClick={confirm} disabled={busy} title={confirmLabel}>
            {busy ? busyLabel : confirmLabel}
          </DangerButton>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: any }) {
  return (
    <label style={{ fontSize: "12px" }} className="text-text-muted">
      {label}
      <div style={{ marginTop: "4px" }}>{children}</div>
    </label>
  );
}

function ModeButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: any }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={active ? "border border-accent bg-bg-subtle text-text" : "border border-border bg-bg text-text-muted hover:bg-bg-subtle"}
      style={{ padding: "5px 10px", borderRadius: "6px", fontSize: "13px", cursor: "pointer" }}
    >
      {children}
    </button>
  );
}

function IconButton({
  onClick,
  disabled,
  title,
  children,
}: {
  onClick: () => void;
  disabled?: boolean;
  title: string;
  children: any;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
      style={{
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        flex: "0 0 auto",
        gap: "6px",
        height: "32px",
        padding: "6px 10px",
        borderRadius: "6px",
        fontSize: "13px",
        fontWeight: 500,
        lineHeight: 1,
        whiteSpace: "nowrap",
        boxSizing: "border-box",
        cursor: disabled ? "not-allowed" : "pointer",
        opacity: disabled ? 0.55 : 1,
      }}
    >
      {children}
    </button>
  );
}

function DangerButton({
  onClick,
  disabled,
  title,
  children,
}: {
  onClick: () => void;
  disabled?: boolean;
  title: string;
  children: any;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      style={{
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        gap: "6px",
        padding: "6px 10px",
        borderRadius: "6px",
        border: "1px solid #b91c1c",
        background: "#dc2626",
        color: "white",
        fontSize: "13px",
        fontWeight: 600,
        cursor: disabled ? "not-allowed" : "pointer",
        opacity: disabled ? 0.65 : 1,
      }}
    >
      {children}
    </button>
  );
}

const inputStyle = {
  width: "100%",
  padding: "6px 8px",
  borderRadius: "6px",
  fontSize: "13px",
} as const;

const linkButtonStyle = {
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  flex: "0 0 auto",
  gap: "6px",
  height: "32px",
  padding: "6px 10px",
  borderRadius: "6px",
  fontSize: "13px",
  fontWeight: 500,
  lineHeight: 1,
  whiteSpace: "nowrap",
  boxSizing: "border-box",
} as const;

const browserViewportStyle = {
  width: "100%",
  height: "100%",
  minHeight: 0,
  borderRadius: "6px",
  overflow: "hidden",
} as const;

const browserViewportFrameStyle = {
  ...browserViewportStyle,
  height: "clamp(480px, min(68vh, 50vw), 820px)",
  minHeight: "480px",
} as const;

const computerPanelLayoutCSS = `
  .computer-panel-layout {
    display: grid;
    grid-template-columns: minmax(260px, 300px) minmax(0, 1fr);
    gap: 12px;
    width: 100%;
    height: 100%;
    min-height: 720px;
    min-width: 0;
    box-sizing: border-box;
    overflow: hidden;
    padding: 12px;
  }

  @media (max-width: 900px) {
    .computer-panel-layout {
      grid-template-columns: minmax(0, 1fr);
      height: auto;
      overflow: visible;
    }
  }
`;

const actionStatusStyle = {
  minHeight: "16px",
  fontSize: "12px",
  lineHeight: "16px",
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
} as const;

const previewOverlayStyle = {
  position: "absolute",
  inset: 0,
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  fontSize: "12px",
  background: "rgba(0,0,0,0.05)",
} as const;

function hostFor(raw: string): string {
  try {
    return new URL(raw).host;
  } catch {
    return raw || "-";
  }
}

function recordingStatusLabel(status: string): string {
  switch (status) {
    case "recording": return "Recording";
    case "processing": return "Processing recording";
    case "ready": return "Recording ready";
    case "unavailable": return "Recording unavailable";
    case "unsupported": return "Recording unsupported";
    case "failed": return "Recording failed";
    default: return status || "Recording unavailable";
  }
}

function PlusIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M12 5v14" />
      <path d="M5 12h14" />
    </svg>
  );
}

function XIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  );
}

function ExternalIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M15 3h6v6" />
      <path d="M10 14 21 3" />
      <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
    </svg>
  );
}

function RefreshIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16" />
      <path d="M3 21v-5h5" />
      <path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8" />
      <path d="M16 8h5V3" />
    </svg>
  );
}

function MonitorIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <rect width="20" height="14" x="2" y="3" rx="2" />
      <path d="M8 21h8" />
      <path d="M12 17v4" />
    </svg>
  );
}

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

function providerLifetimeLabel(row: SessionRow, now: number): string {
  if (row.provider_expires_at) return relativeUntil(row.provider_expires_at, now);
  if (row.timeout_seconds) return formatDurationSeconds(row.timeout_seconds);
  return row.backend === "browserbase" ? "provider default" : "not tracked";
}

function proxyModeLabel(mode?: string): string {
  switch (mode) {
    case "direct": return "direct";
    case "managed": return "managed";
    case "profile": return "configured profile";
    default: return "provider default";
  }
}

function sessionProxyLabel(proxy?: SessionProxyState): string {
  if (!proxy?.mode) return "provider default";
  if (proxy.mode === "profile") {
    const details = [proxy.profile_name || proxy.provider, proxy.country, proxy.sticky_scope].filter(Boolean);
    return details.length > 0 ? details.join(" · ") : "configured profile";
  }
  if (proxy.mode === "managed" && proxy.country) return `managed · ${proxy.country}`;
  return proxyModeLabel(proxy.mode);
}

function sessionUsageLabel(usage?: SessionUsageState): string {
  if (!usage?.status) return "-";
  if (usage.status === "unsupported") return "Unsupported";
  if (usage.status === "unavailable") return "Unavailable";
  if (usage.status !== "ready" || usage.proxy_bytes == null) return usage.status;
  return formatProxyBytes(usage.proxy_bytes);
}

function formatProxyBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "-";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  if (unit === 0) return `${Math.round(value)} ${units[unit]}`;
  const digits = value >= 100 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits)} ${units[unit]}`;
}

function relativeUntil(iso: string | undefined, now: number): string {
  if (!iso) return "-";
  const then = Date.parse(iso);
  if (!Number.isFinite(then)) return "-";
  const seconds = Math.floor((then - now) / 1000);
  if (seconds <= 0) return "expired";
  if (seconds < 60) return `${seconds}s left`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m left`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m left`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h left`;
}

function formatDurationSeconds(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "-";
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}

function relativeAge(iso: string, now: number): string {
  const then = Date.parse(iso);
  if (!Number.isFinite(then)) return "-";
  const seconds = Math.max(0, Math.floor((now - then) / 1000));
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h ago`;
}
