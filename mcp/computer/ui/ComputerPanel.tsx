// ComputerPanel - operator-facing dashboard panel for app-owned browser sessions.
// The UI is a thin HTTP client over the same browser_session and computer_use
// handlers exposed through MCP.

import { useCallback, useEffect, useRef, useState } from "react";
import { Card, CardHeader, StatusDot, StatusPill, DataList } from "@apteva/ui-kit";

interface SessionRow {
  session_id: string;
  backend_session_id?: string;
  backend: "local" | "browserbase" | "steel" | string;
  context_id?: string;
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

type OpenMode = "open" | "resume" | "context";
type ProxyMode = "" | "true" | "false";

const SESSIONS_URL = "/api/apps/computer/sessions";
const POLL_MS = 4000;

const BACKEND_LABEL: Record<string, string> = {
  local: "Local Chrome",
  browserbase: "Browserbase",
  steel: "Steel",
  "browser-engine": "Browser Engine",
  service: "Browser Service",
};

export default function ComputerPanel() {
  const [rows, setRows] = useState<SessionRow[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [showOpen, setShowOpen] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const r = await fetch(SESSIONS_URL, { credentials: "include" });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const body = (await r.json()) as ListResponse;
      if (body.error) throw new Error(body.error);
      setRows(body.sessions ?? []);
      setErr(null);
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const t = setInterval(refresh, POLL_MS);
    return () => clearInterval(t);
  }, [refresh]);

  useEffect(() => {
    if (!selected && rows.length > 0) setSelected(rows[0].session_id);
    if (selected && !rows.some((r) => r.session_id === selected)) {
      setSelected(rows[0]?.session_id ?? null);
    }
  }, [rows, selected]);

  const onClose = useCallback(
    async (id: string) => {
      if (!confirm("Close this browser session?")) return;
      const r = await fetch(`${SESSIONS_URL}/${encodeURIComponent(id)}`, {
        method: "DELETE",
        credentials: "include",
      });
      if (!r.ok) {
        setErr(`close failed: HTTP ${r.status}`);
        return;
      }
      void refresh();
    },
    [refresh],
  );

  const sel = rows.find((r) => r.session_id === selected) ?? null;

  return (
    <div
      className="bg-bg"
      style={{
        display: "grid",
        gridTemplateColumns: "320px minmax(0, 1fr)",
        gap: "12px",
        height: "100%",
        padding: "12px",
      }}
    >
      <BrowsersList
        rows={rows}
        err={err}
        selected={selected}
        onSelect={setSelected}
        onClose={onClose}
        onOpen={() => setShowOpen(true)}
      />
      <SessionDetail session={sel} onClose={onClose} onRefresh={refresh} />
      {showOpen && (
        <OpenSessionModal
          onClose={() => setShowOpen(false)}
          onOpened={(newID) => {
            setShowOpen(false);
            setSelected(newID);
            void refresh();
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
  onSelect,
  onClose,
  onOpen,
}: {
  rows: SessionRow[];
  err: string | null;
  selected: string | null;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
  onOpen: () => void;
}) {
  return (
    <Card className="overflow-hidden flex flex-col">
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
              onSelect={() => onSelect(r.session_id)}
              onClose={() => onClose(r.session_id)}
            />
          ))}
        </ul>
      </div>
    </Card>
  );
}

function BrowserListItem({
  row,
  selected,
  onSelect,
  onClose,
}: {
  row: SessionRow;
  selected: boolean;
  onSelect: () => void;
  onClose: () => void;
}) {
  const host = hostFor(row.current_url);
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
          padding: "8px 10px",
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
      </button>
    </li>
  );
}

function SessionDetail({
  session,
  onClose,
  onRefresh,
}: {
  session: SessionRow | null;
  onClose: (id: string) => void;
  onRefresh: () => Promise<void>;
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

  if (!session) {
    return (
      <Card>
        <CardHeader title="Session" />
        <div
          className="text-text-muted"
          style={{ padding: "32px 16px", textAlign: "center", fontSize: "13px" }}
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
      const res = await fetch(`${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/use`, {
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

  return (
    <Card>
      <CardHeader
        title={host || "Session"}
        right={<StatusPill variant="neutral" label={BACKEND_LABEL[session.backend] ?? session.backend} />}
      />
      <div style={{ padding: "0 16px 16px", display: "grid", gap: "12px" }}>
        {session.stream_url && embedLive ? (
          <div
            className="border border-border bg-bg-subtle"
            style={{
              width: "100%",
              aspectRatio: `${session.width || 16} / ${session.height || 10}`,
              borderRadius: "6px",
              overflow: "hidden",
            }}
          >
            <iframe
              src={session.stream_url}
              title="Live browser stream"
              style={{ width: "100%", height: "100%", border: 0, display: "block" }}
            />
          </div>
        ) : (
          <InteractivePreview
            session={session}
            tick={tick}
            busy={Boolean(busy)}
            onClickPoint={(x, y) => sendCoordinate("click", x, y)}
          />
        )}

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
          <div style={{ display: "grid", gridTemplateColumns: "1fr auto auto auto auto", gap: "8px" }}>
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
          {err && <div style={{ color: "#dc2626", fontSize: "12px" }}>{err}</div>}
          {busy && <div className="text-text-muted" style={{ fontSize: "12px" }}>{busy}...</div>}
        </div>

        <DataList
          items={[
            { label: "App session", value: session.session_id },
            { label: "Backend session", value: session.backend_session_id || "-" },
            { label: "Context", value: session.context_id || "-" },
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
  tick,
  busy,
  onClickPoint,
}: {
  session: SessionRow;
  tick: number;
  busy: boolean;
  onClickPoint: (x: number, y: number) => void;
}) {
  const imgRef = useRef<HTMLImageElement | null>(null);
  const [stale, setStale] = useState(false);
  const src = `${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/screenshot?t=${tick}`;
  const aspectW = session.width && session.height ? session.width : 16;
  const aspectH = session.width && session.height ? session.height : 10;

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
    const x = ((e.clientX - rect.left) / rect.width) * naturalW;
    const y = ((e.clientY - rect.top) / rect.height) * naturalH;
    onClickPoint(x, y);
  };

  return (
    <div
      className="border border-border bg-bg-subtle"
      style={{
        position: "relative",
        width: "100%",
        aspectRatio: `${aspectW} / ${aspectH}`,
        borderRadius: "6px",
        overflow: "hidden",
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
  onOpened,
}: {
  onClose: () => void;
  onOpened: (sessionID: string) => void;
}) {
  const [mode, setMode] = useState<OpenMode>("open");
  const [url, setUrl] = useState("https://");
  const [backend, setBackend] = useState("");
  const [contextID, setContextID] = useState("");
  const [persist, setPersist] = useState(true);
  const [backendSessionID, setBackendSessionID] = useState("");
  const [timeout, setTimeoutValue] = useState("");
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
      if (contextID && mode !== "resume") body.context_id = contextID;
      if (mode === "resume" && backendSessionID) body.backend_session_id = backendSessionID;
      if (mode !== "resume") body.persist = persist;
      if (timeout) body.timeout = Number(timeout);
      if (proxy) body.proxy = proxy === "true";
      if (proxyCountry) body.proxy_country = proxyCountry.toUpperCase();
      const viewport: Record<string, number> = {};
      if (width) viewport.width = Number(width);
      if (height) viewport.height = Number(height);
      if (viewport.width || viewport.height) body.viewport = viewport;

      const res = await fetch(SESSIONS_URL, {
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
          <ModeButton active={mode === "resume"} onClick={() => setMode("resume")}>Resume</ModeButton>
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "10px" }}>
          <Field label="Backend">
            <select value={backend} onChange={(e) => setBackend(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle}>
              <option value="">Default</option>
              <option value="local">Local Chrome</option>
              <option value="browserbase">Browserbase</option>
              <option value="steel">Steel</option>
              <option value="browser-engine">Browser Engine</option>
              <option value="service">Browser Service</option>
            </select>
          </Field>
          <Field label="URL">
            <input value={url} onChange={(e) => setUrl(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
          </Field>
          {mode === "resume" ? (
            <Field label="Backend session ID">
              <input value={backendSessionID} onChange={(e) => setBackendSessionID(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
            </Field>
          ) : (
            <Field label="Context ID">
              <input value={contextID} onChange={(e) => setContextID(e.target.value)} className="border border-border bg-bg text-text" style={inputStyle} />
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
          <label style={{ display: "flex", gap: "8px", alignItems: "center", marginTop: "10px", fontSize: "13px" }}>
            <input type="checkbox" checked={persist} onChange={(e) => setPersist(e.target.checked)} />
            Persist context changes
          </label>
        )}
        {err && <div style={{ marginTop: "10px", fontSize: "12px", color: "#dc2626" }}>{err}</div>}
        <div style={{ marginTop: "16px", display: "flex", justifyContent: "flex-end", gap: "8px" }}>
          <IconButton onClick={onClose} disabled={busy} title="Cancel">Cancel</IconButton>
          <IconButton onClick={submit} disabled={busy} title="Open session">
            {busy ? "Opening..." : mode === "resume" ? "Resume" : "Open"}
          </IconButton>
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
