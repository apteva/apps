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
}

interface ListResponse {
  sessions?: SessionRow[];
  error?: string;
}

const SESSIONS_URL = "/api/sessions";
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
      />
      <SessionDetail session={sel} />
    </div>
  );
}

// ─── Browsers list ──────────────────────────────────────────────────

function BrowsersList({
  rows,
  err,
  selected,
  onSelect,
}: {
  rows: SessionRow[];
  err: string | null;
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <Card className="overflow-hidden flex flex-col">
      <CardHeader
        title="Browsers"
        right={<StatusDot variant={err ? "error" : "success"} />}
      />
      <div className="flex-1 overflow-y-auto" style={{ padding: "0 12px 12px" }}>
        {err && (
          <p className="text-text-muted" style={{ fontSize: "12px", padding: "8px 4px" }}>
            {err}
          </p>
        )}
        {!err && rows.length === 0 && (
          <p className="text-text-muted" style={{ fontSize: "12px", padding: "8px 4px" }}>
            No active sessions. Call <code>browser_open</code> to start one.
          </p>
        )}
        <ul style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
          {rows.map((r) => (
            <BrowserListItem
              key={r.session_id}
              row={r}
              selected={r.session_id === selected}
              onSelect={() => onSelect(r.session_id)}
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
}: {
  row: SessionRow;
  selected: boolean;
  onSelect: () => void;
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
          <StatusPill variant="success" label="active" />
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

function SessionDetail({ session }: { session: SessionRow | null }) {
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
        <DataList
          items={[
            { label: "Session ID", value: session.session_id },
            { label: "Current URL", value: session.current_url || "—" },
            { label: "Opened", value: formatTime(session.opened_at) },
            { label: "Last used", value: formatTime(session.last_used_at) },
          ]}
        />
        {session.debug_url ? (
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
              marginTop: "12px",
            }}
          >
            <ExternalIcon />
            Open in DevTools
          </a>
        ) : (
          <div
            className="text-text-muted"
            style={{ fontSize: "12px", marginTop: "12px" }}
          >
            This backend doesn't expose a debug URL. Use{" "}
            <code>browser_screenshot</code> to inspect the page.
          </div>
        )}
      </div>
    </Card>
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
