// GalleryPanel — operator-facing dashboard surface for the
// screenshots app. Lists captures, lets the operator trigger a fresh
// capture, opens any capture full-size in a new tab, and deletes.
//
// All data goes through this app's own JSON API
// (/api/apps/screenshots/screenshots…) which delegates to the same
// MCP tool functions the agent calls.

import { useCallback, useEffect, useMemo, useState } from "react";
import { Card, CardHeader, StatusPill } from "@apteva/ui-kit";

interface Row {
  id: number;
  url: string;
  final_url: string;
  label: string;
  width: number;
  height: number;
  backend: string;
  storage_id: number;
  captured_at: string;
}

interface ListResponse {
  screenshots?: Row[];
  error?: string;
}

interface GetResponse {
  url?: string;
  error?: string;
}

// /api/apps/<name>/<route> is the platform's proxy prefix; the
// sidecar registers Pattern:"/screenshots" and the dashboard fetches
// /api/apps/screenshots/screenshots to reach it.
const API_LIST = "/api/apps/screenshots/screenshots";

export default function GalleryPanel() {
  const [rows, setRows] = useState<Row[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [showCapture, setShowCapture] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const res = await fetch(API_LIST, { credentials: "include" });
      const body = (await res.json()) as ListResponse;
      if (!res.ok || body.error) {
        setErr(body.error ?? `HTTP ${res.status}`);
        return;
      }
      setRows(body.screenshots ?? []);
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const filtered = useMemo(() => {
    if (!search) return rows;
    const needle = search.toLowerCase();
    return rows.filter(
      (r) =>
        r.url.toLowerCase().includes(needle) ||
        (r.label ?? "").toLowerCase().includes(needle),
    );
  }, [rows, search]);

  const onDelete = useCallback(
    async (id: number) => {
      if (!confirm("Delete this screenshot?")) return;
      const res = await fetch(`${API_LIST}/${id}`, {
        method: "DELETE",
        credentials: "include",
      });
      if (!res.ok) {
        setErr(`Delete failed: HTTP ${res.status}`);
        return;
      }
      setRows((prev) => prev.filter((r) => r.id !== id));
    },
    [],
  );

  return (
    <div
      className="bg-bg text-text"
      style={{ padding: "16px", height: "100%", overflow: "auto" }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          marginBottom: "16px",
          gap: "12px",
          flexWrap: "wrap",
        }}
      >
        <div>
          <h1
            style={{ fontSize: "20px", fontWeight: 600, marginBottom: "2px" }}
          >
            Screenshots
          </h1>
          <p className="text-text-muted" style={{ fontSize: "13px" }}>
            {loading
              ? "Loading…"
              : `${filtered.length} capture${filtered.length === 1 ? "" : "s"}`}
          </p>
        </div>
        <div style={{ display: "flex", gap: "8px", alignItems: "center" }}>
          <input
            type="search"
            placeholder="Search URL or label…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="border border-border bg-bg text-text"
            style={{
              padding: "6px 10px",
              borderRadius: "6px",
              fontSize: "13px",
              width: "220px",
            }}
          />
          <button
            onClick={() => setShowCapture(true)}
            className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
            style={{
              padding: "6px 12px",
              borderRadius: "6px",
              fontSize: "13px",
              fontWeight: 500,
              cursor: "pointer",
              display: "inline-flex",
              alignItems: "center",
              gap: "6px",
            }}
          >
            <CameraIcon />
            Capture
          </button>
        </div>
      </div>

      {err && (
        <div
          className="border border-border bg-bg-subtle text-text"
          style={{
            padding: "12px",
            borderRadius: "6px",
            marginBottom: "12px",
            fontSize: "13px",
          }}
        >
          {err}
        </div>
      )}

      {filtered.length === 0 && !loading && !err ? (
        <EmptyState onCapture={() => setShowCapture(true)} />
      ) : (
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fill, minmax(240px, 1fr))",
            gap: "12px",
          }}
        >
          {filtered.map((r) => (
            <ScreenshotTile key={r.id} row={r} onDelete={onDelete} />
          ))}
        </div>
      )}

      {showCapture && (
        <CaptureModal
          onClose={() => setShowCapture(false)}
          onCaptured={() => {
            setShowCapture(false);
            void refresh();
          }}
        />
      )}
    </div>
  );
}

function ScreenshotTile({
  row,
  onDelete,
}: {
  row: Row;
  onDelete: (id: number) => void;
}) {
  const [imgURL, setImgURL] = useState<string | null>(null);
  const [hover, setHover] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const res = await fetch(`${API_LIST}/${row.id}`, {
          credentials: "include",
        });
        const body = (await res.json()) as GetResponse;
        if (!cancelled && res.ok && body.url) setImgURL(body.url);
      } catch {
        // ignore — tile renders without thumbnail
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [row.id]);

  let host = "";
  try {
    host = new URL(row.url).host;
  } catch {
    host = row.url;
  }

  return (
    <Card>
      <div
        style={{ position: "relative", lineHeight: 0 }}
        onMouseEnter={() => setHover(true)}
        onMouseLeave={() => setHover(false)}
      >
        {imgURL ? (
          <a href={imgURL} target="_blank" rel="noreferrer">
            <img
              src={imgURL}
              alt={row.label || host}
              style={{
                width: "100%",
                aspectRatio: `${row.width} / ${row.height}`,
                objectFit: "cover",
                display: "block",
                borderRadius: "4px",
              }}
            />
          </a>
        ) : (
          <div
            className="bg-bg-subtle"
            style={{
              width: "100%",
              aspectRatio: `${row.width} / ${row.height}`,
              borderRadius: "4px",
            }}
          />
        )}
        {hover && (
          <div
            style={{
              position: "absolute",
              top: "6px",
              right: "6px",
              display: "flex",
              gap: "4px",
            }}
          >
            <button
              onClick={() => onDelete(row.id)}
              title="Delete"
              style={{
                background: "rgba(0,0,0,0.6)",
                color: "white",
                border: 0,
                borderRadius: "4px",
                padding: "4px 6px",
                cursor: "pointer",
              }}
            >
              <TrashIcon />
            </button>
          </div>
        )}
      </div>
      <CardHeader
        title={row.label || host}
        right={<StatusPill variant="neutral" label={row.backend} />}
      />
      <div
        className="text-text-muted"
        style={{ fontSize: "12px", padding: "0 12px 12px" }}
      >
        <div
          style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
          title={row.final_url || row.url}
        >
          {row.final_url || row.url}
        </div>
        <div style={{ marginTop: "2px" }}>{formatTime(row.captured_at)}</div>
      </div>
    </Card>
  );
}

function EmptyState({ onCapture }: { onCapture: () => void }) {
  return (
    <div
      className="border border-border text-text-muted"
      style={{
        padding: "48px 16px",
        textAlign: "center",
        borderRadius: "8px",
        borderStyle: "dashed",
      }}
    >
      <div
        style={{
          display: "inline-flex",
          marginBottom: "12px",
          opacity: 0.6,
        }}
      >
        <CameraIcon size={32} />
      </div>
      <div style={{ fontSize: "14px", marginBottom: "12px" }}>
        No screenshots yet. The agent attaches screenshots automatically
        when it calls <code>screenshot_capture</code>, or you can
        trigger one here.
      </div>
      <button
        onClick={onCapture}
        className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
        style={{
          padding: "6px 14px",
          borderRadius: "6px",
          fontSize: "13px",
          cursor: "pointer",
        }}
      >
        Capture a URL
      </button>
    </div>
  );
}

function CaptureModal({
  onClose,
  onCaptured,
}: {
  onClose: () => void;
  onCaptured: () => void;
}) {
  const [url, setUrl] = useState("https://");
  const [label, setLabel] = useState("");
  const [backend, setBackend] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const body: Record<string, any> = { url };
      if (label) body.label = label;
      if (backend) body.backend = backend;
      const res = await fetch(API_LIST, {
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
      onCaptured();
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
          New screenshot
        </h2>
        <div style={{ display: "grid", gap: "10px" }}>
          <label style={{ fontSize: "12px" }} className="text-text-muted">
            URL
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
            Label (optional)
            <input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
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
            Backend (optional)
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
              <option value="">Default (from server env)</option>
              <option value="local">Local Chrome</option>
              <option value="browserbase">Browserbase</option>
              <option value="steel">Steel</option>
            </select>
          </label>
        </div>
        {err && (
          <div
            className="text-text-muted"
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
            disabled={busy || !url}
            className="border border-border bg-bg-subtle text-text hover:bg-bg-hover"
            style={{
              padding: "6px 12px",
              borderRadius: "6px",
              fontSize: "13px",
              fontWeight: 500,
              cursor: busy ? "wait" : "pointer",
              opacity: busy || !url ? 0.6 : 1,
            }}
          >
            {busy ? "Capturing…" : "Capture"}
          </button>
        </div>
      </div>
    </div>
  );
}

function CameraIcon({ size = 14 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z" />
      <circle cx="12" cy="13" r="3" />
    </svg>
  );
}

function TrashIcon() {
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
      <path d="M3 6h18" />
      <path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
      <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
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
