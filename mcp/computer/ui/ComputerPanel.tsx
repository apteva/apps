// ComputerPanel - operator-facing dashboard panel for app-owned browser sessions.
// The UI is a thin HTTP client over the same browser_session and computer_use
// handlers exposed through MCP.

import { useCallback, useEffect, useRef, useState } from "react";
import { Card, CardHeader, StatusDot, StatusPill, DataList } from "@apteva/ui-kit";

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
  context_id?: string;
  app_context_id?: string;
  context_name?: string;
  persist?: boolean;
  keep_alive?: boolean;
  timeout_seconds?: number;
  provider_expires_at?: string;
  current_url: string;
  debug_url?: string;
  stream_url?: string;
  opened_at: string;
  last_used_at: string;
  width?: number;
  height?: number;
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
}

interface SettingsResponse {
  settings?: ComputerSettings;
  error?: string;
}

interface ComputerEventData {
  session_id?: string;
  id?: string;
  default_backend?: string;
  lock_backend?: boolean;
}

type OpenMode = "open" | "resume" | "context" | "create_context";
type ProxyMode = "" | "true" | "false";

const SESSIONS_URL = "/api/apps/computer/sessions";
const CONTEXTS_URL = "/api/apps/computer/contexts";
const SETTINGS_URL = "/api/apps/computer/settings";
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

export default function ComputerPanel({ projectId }: NativePanelProps) {
  const [rows, setRows] = useState<SessionRow[]>([]);
  const [contexts, setContexts] = useState<ContextRow[]>([]);
  const [settings, setSettings] = useState<ComputerSettings>({ default_backend: "local", lock_backend: false });
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [eventPreviewTick, setEventPreviewTick] = useState(0);
  const [nowTick, setNowTick] = useState(Date.now());
  const [showOpen, setShowOpen] = useState(false);
  const [pendingClose, setPendingClose] = useState<string | null>(null);
  const [pendingContextDelete, setPendingContextDelete] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [sessionsRes, contextsRes, settingsRes] = await Promise.all([
        fetch(appURL(SESSIONS_URL, projectId), { credentials: "include" }),
        fetch(appURL(CONTEXTS_URL, projectId), { credentials: "include" }),
        fetch(appURL(SETTINGS_URL, projectId), { credentials: "include" }),
      ]);
      if (!sessionsRes.ok) throw new Error(`sessions HTTP ${sessionsRes.status}`);
      if (!contextsRes.ok) throw new Error(`contexts HTTP ${contextsRes.status}`);
      if (!settingsRes.ok) throw new Error(`settings HTTP ${settingsRes.status}`);
      const body = (await sessionsRes.json()) as ListResponse;
      const contextBody = (await contextsRes.json()) as ContextListResponse;
      const settingsBody = (await settingsRes.json()) as SettingsResponse;
      if (body.error) throw new Error(body.error);
      if (contextBody.error) throw new Error(contextBody.error);
      if (settingsBody.error) throw new Error(settingsBody.error);
      setRows(body.sessions ?? []);
      setContexts(contextBody.contexts ?? []);
      if (settingsBody.settings) setSettings(settingsBody.settings);
      setErr(null);
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    }
  }, [projectId]);

  useEffect(() => {
    void refresh();
    const t = setInterval(refresh, POLL_MS);
    return () => clearInterval(t);
  }, [refresh]);

  useEffect(() => {
    const t = setInterval(() => setNowTick(Date.now()), 60_000);
    return () => clearInterval(t);
  }, []);

  useAppEvents<ComputerEventData>("computer", projectId, (ev) => {
    switch (ev.topic) {
      case "session.opened":
        if (!selected && ev.data?.session_id) setSelected(ev.data.session_id);
        void refresh();
        break;
      case "session.action":
        if (ev.data?.session_id === selected) setEventPreviewTick((n) => n + 1);
        void refresh();
        break;
      case "session.closed":
      case "session.reaped":
      case "context.created":
      case "context.updated":
      case "context.deleted":
      case "settings.updated":
        void refresh();
        break;
    }
  });

  useEffect(() => {
    if (!selected && rows.length > 0) setSelected(rows[0].session_id);
    if (selected && !rows.some((r) => r.session_id === selected)) {
      setSelected(rows[0]?.session_id ?? null);
    }
  }, [rows, selected]);

  const onClose = useCallback(
    async (id: string) => {
      const r = await fetch(appURL(`${SESSIONS_URL}/${encodeURIComponent(id)}`, projectId), {
        method: "DELETE",
        credentials: "include",
      });
      if (!r.ok) {
        setErr(`close failed: HTTP ${r.status}`);
        return;
      }
      void refresh();
    },
    [projectId, refresh],
  );

  const sel = rows.find((r) => r.session_id === selected) ?? null;
  const closeTarget = rows.find((r) => r.session_id === pendingClose) ?? null;
  const contextDeleteTarget = contexts.find((c) => c.id === pendingContextDelete) ?? null;

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
      const next = { ...settings, ...patch };
      setSettings(next);
      try {
        const r = await fetch(appURL(SETTINGS_URL, projectId), {
          method: "PATCH",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(patch),
        });
        const body = (await r.json()) as SettingsResponse;
        if (!r.ok || body.error) throw new Error(body.error ?? `HTTP ${r.status}`);
        if (body.settings) setSettings(body.settings);
        setErr(null);
      } catch (e: any) {
        setErr(`settings failed: ${String(e?.message ?? e)}`);
        void refresh();
      }
    },
    [projectId, refresh, settings],
  );

  return (
    <div
      className="bg-bg"
      style={{
        display: "grid",
        gridTemplateColumns: "320px minmax(0, 1fr)",
        gap: "12px",
        width: "100%",
        height: "100%",
        minHeight: "680px",
        minWidth: 0,
        boxSizing: "border-box",
        overflow: "hidden",
        padding: "12px",
      }}
    >
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
      />
      <SessionDetail
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
          projectId={projectId}
          onOpened={(newID) => {
            setShowOpen(false);
            setSelected(newID);
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
}) {
  return (
    <Card fullWidth className="overflow-hidden flex flex-col h-full min-h-0">
      <CardHeader
        title="Browsers"
        right={
          <div style={{ display: "flex", gap: "6px", alignItems: "center" }}>
            <StatusDot variant={err ? "error" : "success"} />
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
        {!err && rows.length === 0 && (
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
          {rows.map((r) => (
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
  onClose: () => void;
}) {
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
            <StatusPill variant="success" label="active" />
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
            {contextLabel && row.persist === false ? " (read-only)" : ""}
          </span>
          <span>Last used</span>
          <span title={formatTime(row.last_used_at)}>
            {lastUsedAgo}
            {viewport ? ` | ${viewport}` : ""}
          </span>
          <span>Keep alive</span>
          <span className={row.keep_alive ? "text-text" : "text-text-muted"}>{row.keep_alive ? "yes" : "no"}</span>
          <span>Provider</span>
          <span title={row.provider_expires_at ? formatTime(row.provider_expires_at) : "Provider default timeout"}>
            {providerLife}
          </span>
        </div>
      </button>
    </li>
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

  return (
    <Card fullWidth className="h-full min-h-0 flex flex-col overflow-hidden">
      <CardHeader
        title={host || "Session"}
        right={<StatusPill variant="neutral" label={BACKEND_LABEL[session.backend] ?? session.backend} />}
      />
      <div
        style={{
          flex: 1,
          minHeight: 0,
          padding: "0 16px 16px",
          display: "grid",
          gridTemplateRows: "minmax(360px, 1fr) auto auto auto",
          gap: "12px",
        }}
      >
        <div style={{ minHeight: 0 }}>
          {session.stream_url && embedLive ? (
            <div className="border border-border bg-bg-subtle" style={browserViewportStyle}>
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

        <div style={{ display: "flex", gap: "8px", flexWrap: "wrap" }}>
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
          style={{ borderRadius: "6px", padding: "10px", display: "grid", gap: "10px" }}
        >
          <div style={{ display: "grid", gridTemplateColumns: "1fr auto", gap: "8px" }}>
            <input
              value={typeText}
              onChange={(e) => setTypeText(e.target.value)}
              className="border border-border bg-bg text-text"
              style={inputStyle}
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
          <div style={{ display: "grid", gridTemplateColumns: "1fr auto auto auto auto auto auto", gap: "8px" }}>
            <input
              value={keyText}
              onChange={(e) => setKeyText(e.target.value)}
              className="border border-border bg-bg text-text"
              style={inputStyle}
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
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr auto auto", gap: "8px" }}>
            <input
              value={manualX}
              onChange={(e) => setManualX(e.target.value)}
              className="border border-border bg-bg text-text"
              style={inputStyle}
              placeholder="X"
              inputMode="numeric"
            />
            <input
              value={manualY}
              onChange={(e) => setManualY(e.target.value)}
              className="border border-border bg-bg text-text"
              style={inputStyle}
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
            { label: "Persist changes", value: session.persist ? "yes" : "no" },
            { label: "Keep alive", value: session.keep_alive ? "yes" : "no" },
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
        ...browserViewportStyle,
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
  projectId,
  onOpened,
}: {
  onClose: () => void;
  contexts: ContextRow[];
  settings: ComputerSettings;
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
  const [keepAlive, setKeepAlive] = useState(false);
  const [proxy, setProxy] = useState<ProxyMode>("");
  const [proxyCountry, setProxyCountry] = useState("");
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
      if (mode !== "resume" && keepAlive) body.keep_alive = true;
      if (proxy) body.proxy = proxy === "true";
      if (proxyCountry) body.proxy_country = proxyCountry.toUpperCase();
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
          <Field label="Proxy">
            <select value={proxy} onChange={(e) => setProxy(e.target.value as ProxyMode)} className="border border-border bg-bg text-text" style={inputStyle}>
              <option value="">Default</option>
              <option value="true">On</option>
              <option value="false">Off</option>
            </select>
          </Field>
          <Field label="Proxy country">
            <input value={proxyCountry} onChange={(e) => setProxyCountry(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} placeholder="US" />
          </Field>
        </div>
        {mode !== "resume" && (
          <div style={{ display: "grid", gap: "8px", marginTop: "10px", fontSize: "13px" }}>
            <label style={{ display: "flex", gap: "8px", alignItems: "center" }}>
              <input type="checkbox" checked={persist} onChange={(e) => setPersist(e.target.checked)} />
              Persist context changes
            </label>
            <label style={{ display: "flex", gap: "8px", alignItems: "center" }}>
              <input type="checkbox" checked={keepAlive} onChange={(e) => setKeepAlive(e.target.checked)} />
              Keep Browserbase session alive after disconnect
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
        gap: "6px",
        padding: "6px 10px",
        borderRadius: "6px",
        fontSize: "13px",
        fontWeight: 500,
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
  gap: "6px",
  padding: "6px 10px",
  borderRadius: "6px",
  fontSize: "13px",
  fontWeight: 500,
} as const;

const browserViewportStyle = {
  width: "100%",
  height: "100%",
  minHeight: "360px",
  borderRadius: "6px",
  overflow: "hidden",
} as const;

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
