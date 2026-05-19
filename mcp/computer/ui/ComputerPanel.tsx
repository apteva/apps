// ComputerPanel — operator-facing dashboard panel for the computer
// app. Two columns: Browsers list (left) + selected-session details
// (right). View-only — chat injection lived here in v0.2.x but was
// removed in v0.3.1 since the panel can't actually talk to the agent
// loop today; the dashboard's own chat surface is the right place
// for that.
//
// Data source: this sidecar's own GET /api/sessions endpoint backed
// by the in-memory session registry. No more apteva-server endpoints
// (those were always speculative — see the v0.2.x comment block that
// referenced "endpoints added by a separate server-side PR" that
// never landed). Live screencast also stayed unbuilt; for v0.3.x the
// "view" button just deep-links to the backend's DevTools URL so
// operators can attach a real Chrome DevTools window.

import { useCallback, useEffect, useState } from "react";
import { Card, CardHeader, StatusDot, StatusPill, DataList } from "@apteva/ui-kit";

interface SessionRow {
  session_id: string;
  backend: "local" | "browserbase" | "steel" | string;
  current_url: string;
  debug_url?: string;
  opened_at: string;
  last_used_at: string;
  // width/height aren't reported by browser_list today (they're a
  // backend-side display property), but kept optional for forward
  // compat; LivePreview uses 16:10 as a fallback.
  width?: number;
  height?: number;
}

interface ListResponse {
  sessions?: SessionRow[];
  error?: string;
}

// /api/apps/<name>/<route> is the platform's proxy prefix; the
// sidecar registers Pattern:"/sessions" and the dashboard fetches
// /api/apps/computer/sessions to reach it.
const SESSIONS_URL = "/api/apps/computer/sessions";
const POLL_MS = 4000;

const BACKEND_LABEL: Record<string, string> = {
  local: "Local Chrome",
  browserbase: "Browserbase",
  steel: "Steel",
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

  // Auto-select first row when nothing's chosen and we just got data.
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
        gridTemplateColumns: "300px 1fr",
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
      <SessionDetail session={sel} onClose={onClose} />
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

// ─── Browsers list ──────────────────────────────────────────────────

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
            <button
              onClick={onOpen}
              className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
              title="Open a new browser session"
              style={{
                padding: "3px 8px",
                borderRadius: "5px",
                fontSize: "12px",
                fontWeight: 500,
                cursor: "pointer",
                display: "inline-flex",
                alignItems: "center",
                gap: "4px",
              }}
            >
              <PlusIcon /> New
            </button>
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
            <button
              onClick={onOpen}
              className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
              style={{
                padding: "5px 12px",
                borderRadius: "6px",
                fontSize: "12px",
                fontWeight: 500,
                cursor: "pointer",
                display: "inline-flex",
                alignItems: "center",
                gap: "4px",
              }}
            >
              <PlusIcon /> Open browser session
            </button>
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
  let host = "";
  try {
    host = new URL(row.current_url).host;
  } catch {
    host = row.current_url || "—";
  }
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
          <span>·</span>
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

// ─── Session detail pane ───────────────────────────────────────────

function SessionDetail({
  session,
  onClose,
}: {
  session: SessionRow | null;
  onClose: (id: string) => void;
}) {
  if (!session) {
    return (
      <Card>
        <CardHeader title="Session" />
        <div
          className="text-text-muted"
          style={{
            padding: "32px 16px",
            textAlign: "center",
            fontSize: "13px",
          }}
        >
          Select a browser session on the left.
        </div>
      </Card>
    );
  }

  let host = "";
  try {
    host = new URL(session.current_url).host;
  } catch {
    host = session.current_url || "session";
  }

  return (
    <Card>
      <CardHeader
        title={host}
        right={<StatusPill variant="neutral" label={BACKEND_LABEL[session.backend] ?? session.backend} />}
      />
      <div style={{ padding: "0 16px 16px" }}>
        <LivePreview session={session} />
        <DataList
          items={[
            { label: "Session ID", value: session.session_id },
            { label: "Current URL", value: session.current_url || "—" },
            { label: "Opened", value: formatTime(session.opened_at) },
            { label: "Last used", value: formatTime(session.last_used_at) },
          ]}
        />
        <div
          style={{
            marginTop: "12px",
            display: "flex",
            gap: "8px",
            flexWrap: "wrap",
          }}
        >
          {session.debug_url && (
            <a
              href={session.debug_url}
              target="_blank"
              rel="noreferrer"
              className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
              style={{
                display: "inline-flex",
                alignItems: "center",
                gap: "6px",
                padding: "6px 12px",
                borderRadius: "6px",
                fontSize: "13px",
                fontWeight: 500,
              }}
            >
              <ExternalIcon />
              Open in DevTools
            </a>
          )}
          <button
            onClick={() => onClose(session.session_id)}
            className="border border-border bg-bg text-text-muted hover:bg-bg-subtle"
            style={{
              display: "inline-flex",
              alignItems: "center",
              gap: "6px",
              padding: "6px 12px",
              borderRadius: "6px",
              fontSize: "13px",
              fontWeight: 500,
              cursor: "pointer",
            }}
          >
            <XIcon /> Close session
          </button>
        </div>
        {!session.debug_url && (
          <div
            className="text-text-muted"
            style={{ fontSize: "12px", marginTop: "10px" }}
          >
            This backend doesn't expose a debug URL. Use{" "}
            <code>browser_screenshot</code> to inspect the page.
          </div>
        )}
      </div>
    </Card>
  );
}

function OpenSessionModal({
  onClose,
  onOpened,
}: {
  onClose: () => void;
  onOpened: (sessionID: string) => void;
}) {
  const [url, setUrl] = useState("https://");
  const [backend, setBackend] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const body: Record<string, any> = {};
      if (url && url !== "https://") body.url = url;
      if (backend) body.backend = backend;
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
          width: "420px",
          maxWidth: "90vw",
          padding: "20px",
          borderRadius: "8px",
        }}
      >
        <h2 style={{ fontSize: "16px", fontWeight: 600, marginBottom: "12px" }}>
          New browser session
        </h2>
        <div style={{ display: "grid", gap: "10px" }}>
          <label style={{ fontSize: "12px" }} className="text-text-muted">
            URL (optional — open blank if omitted)
            <input
              type="url"
              autoFocus
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className="border border-border bg-bg text-text"
              style={{
                width: "100%",
                marginTop: "4px",
                padding: "6px 10px",
                borderRadius: "6px",
                fontSize: "13px",
              }}
            />
          </label>
          <label style={{ fontSize: "12px" }} className="text-text-muted">
            Backend
            <select
              value={backend}
              onChange={(e) => setBackend(e.target.value)}
              className="border border-border bg-bg text-text"
              style={{
                width: "100%",
                marginTop: "4px",
                padding: "6px 10px",
                borderRadius: "6px",
                fontSize: "13px",
              }}
            >
              <option value="">Default (server env)</option>
              <option value="local">Local Chrome</option>
              <option value="browserbase">Browserbase</option>
              <option value="steel">Steel</option>
            </select>
          </label>
        </div>
        {err && (
          <div
            style={{ marginTop: "10px", fontSize: "12px", color: "#dc2626" }}
          >
            {err}
          </div>
        )}
        <div
          style={{
            marginTop: "16px",
            display: "flex",
            justifyContent: "flex-end",
            gap: "8px",
          }}
        >
          <button
            onClick={onClose}
            disabled={busy}
            className="border border-border bg-bg text-text-muted hover:bg-bg-subtle"
            style={{
              padding: "6px 12px",
              borderRadius: "6px",
              fontSize: "13px",
              cursor: "pointer",
            }}
          >
            Cancel
          </button>
          <button
            onClick={submit}
            disabled={busy}
            className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
            style={{
              padding: "6px 12px",
              borderRadius: "6px",
              fontSize: "13px",
              fontWeight: 500,
              cursor: busy ? "wait" : "pointer",
              opacity: busy ? 0.6 : 1,
            }}
          >
            {busy ? "Opening…" : "Open"}
          </button>
        </div>
      </div>
    </div>
  );
}

function PlusIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M12 5v14" />
      <path d="M5 12h14" />
    </svg>
  );
}

function XIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  );
}

// LivePreview — cheap "live" view of the session's current page by
// polling /sessions/{id}/screenshot every ~800ms with a cache-busting
// query string. Not as smooth as CDP screencast but works for every
// backend (local Chrome, Browserbase, Steel) without WebSocket
// plumbing, and degrades gracefully if Screenshot errors (placeholder
// kept, next tick retries).
function LivePreview({ session }: { session: SessionRow }) {
  const [tick, setTick] = useState(0);
  const [stale, setStale] = useState(false);
  const url = `${SESSIONS_URL}/${encodeURIComponent(session.session_id)}/screenshot?t=${tick}`;
  const aspectW = session.width && session.height ? session.width : 16;
  const aspectH = session.width && session.height ? session.height : 10;

  useEffect(() => {
    setStale(false);
    setTick((n) => n + 1);
    const t = setInterval(() => setTick((n) => n + 1), 800);
    return () => clearInterval(t);
  }, [session.session_id]);

  return (
    <div
      className="border border-border bg-bg-subtle"
      style={{
        position: "relative",
        width: "100%",
        aspectRatio: `${aspectW} / ${aspectH}`,
        borderRadius: "6px",
        overflow: "hidden",
        marginBottom: "12px",
      }}
    >
      <img
        key={session.session_id}
        src={url}
        alt="live"
        onLoad={() => setStale(false)}
        onError={() => setStale(true)}
        style={{
          width: "100%",
          height: "100%",
          objectFit: "contain",
          display: "block",
        }}
      />
      {stale && (
        <div
          className="text-text-muted"
          style={{
            position: "absolute",
            inset: 0,
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            fontSize: "12px",
            background: "rgba(0,0,0,0.05)",
          }}
        >
          waiting for next frame…
        </div>
      )}
    </div>
  );
}

function ExternalIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M15 3h6v6" />
      <path d="M10 14 21 3" />
      <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
    </svg>
  );
}

function formatTime(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleString();
  } catch {
    return iso;
  }
}
