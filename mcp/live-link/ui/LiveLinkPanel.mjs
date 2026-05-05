// LiveLinkPanel — single-toggle UI for the live-link app.
//
//   ┌───────────────────────────────────────────┐
//   │ Live Link                                 │
//   │                                           │
//   │  ●  Live      [https://foo.tryclou…] [⧉]  │
//   │                                  [Stop]    │
//   │                                           │
//   │  History                                  │
//   │   ✓ 14:02 → 14:38 · foo.trycloudflare…    │
//   │   ✕ 13:10 → 13:11 · failed: binary missing │
//   └───────────────────────────────────────────┘
//
// Hand-authored .mjs (no build step) so v0.1 reviews are diff-friendly.
// Imports React + jsx-runtime via the dashboard's importmap (same
// React instance as the host).

import { useCallback, useEffect, useState } from "react";
import { jsx, jsxs, Fragment } from "react/jsx-runtime";

const API = "/api/apps/live-link";

// ─── tiny SSE hook (same shape as BackupPanel uses) ─────────────────

function useAppEvents(app, projectId, onEvent) {
  useEffect(() => {
    if (!app || !projectId) return;
    let lastSeq = 0;
    let es = null;
    let cancelled = false;
    let reconnectTimer = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data);
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          onEvent(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) clearTimeout(reconnectTimer);
          reconnectTimer = setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId, onEvent]);
}

// ─── small formatting helpers ───────────────────────────────────────

function fmtTime(iso) {
  if (!iso) return "—";
  try { return new Date(iso).toLocaleString(); } catch { return iso; }
}

function fmtDuration(start, end) {
  if (!start || !end) return "—";
  try {
    const ms = new Date(end).getTime() - new Date(start).getTime();
    if (ms < 1000) return `${ms} ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
    if (ms < 3_600_000) return `${Math.round(ms / 60_000)} min`;
    return `${(ms / 3_600_000).toFixed(1)} h`;
  } catch { return "—"; }
}

function statusDot(status) {
  const color =
    status === "running" ? "#0a0" :
    status === "failed"  ? "#b00" :
    status === "stopped" ? "#888" :
    /* orphaned */         "#aa0";
  return jsx("span", {
    style: {
      display: "inline-block",
      width: 8, height: 8, borderRadius: 4,
      background: color, marginRight: 6,
    },
  });
}

// ─── main component ────────────────────────────────────────────────

function LiveLinkPanel({ projectId, installId }) {
  const [status, setStatus] = useState({ status: "idle", public_url: "", target_url: "", resolved_target: "" });
  const [runs, setRuns] = useState([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(null); // 'start' | 'stop' | null

  const qs = useCallback(
    () => new URLSearchParams({ project_id: projectId, install_id: String(installId) }).toString(),
    [projectId, installId],
  );

  const api = useCallback(async (method, path, body) => {
    const res = await fetch(`${API}${path}?${qs()}`, {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [qs]);

  const refresh = useCallback(async () => {
    try {
      const [s, r] = await Promise.all([
        api("GET", "/status"),
        api("GET", "/runs"),
      ]);
      setStatus(s);
      setRuns(r.runs || []);
      setError("");
    } catch (e) {
      setError("Error: " + e.message);
    }
  }, [api]);

  useEffect(() => { refresh(); }, [refresh]);
  // Re-poll every 2s while a tunnel is starting (URL not yet assigned)
  // since the SSE event could be missed and we don't want a stale
  // "starting…" pill forever.
  useEffect(() => {
    if (status.status !== "running" || status.public_url) return;
    const t = setInterval(refresh, 2000);
    return () => clearInterval(t);
  }, [status.status, status.public_url, refresh]);

  useAppEvents("live-link", projectId, refresh);

  const onStart = async () => {
    setBusy("start"); setError("");
    try { await api("POST", "/start", {}); await refresh(); }
    catch (e) { setError("Start failed: " + e.message); }
    finally { setBusy(null); }
  };
  const onStop = async () => {
    setBusy("stop"); setError("");
    try { await api("POST", "/stop", {}); await refresh(); }
    catch (e) { setError("Stop failed: " + e.message); }
    finally { setBusy(null); }
  };
  const onReinstall = async () => {
    if (!window.confirm("Download the latest cloudflared release? The current binary will be replaced.")) return;
    setBusy("install"); setError("");
    try { await api("POST", "/install", {}); }
    catch (e) { setError("Reinstall failed: " + e.message); }
    finally { setBusy(null); }
  };
  const copyURL = async () => {
    if (!status.public_url) return;
    try { await navigator.clipboard.writeText(status.public_url); } catch {}
  };

  const isRunning = status.status === "running";
  const isStarting = isRunning && !status.public_url;

  return jsxs("div", {
    className: "live-link-panel",
    style: { maxWidth: 640 },
    children: [
      jsxs("header", {
        style: { marginBottom: 24 },
        children: [
          jsx("h2", { children: "Live Link" }),
          jsx("div", {
            style: { fontSize: 13, color: "#666", marginTop: 4 },
            children: "Give this Apteva instance a public HTTPS URL via Cloudflare Quick Tunnel.",
          }),
          error && jsx("div", { style: { color: "#b00", marginTop: 8 }, children: error }),
        ],
      }),

      // ─── Main toggle / URL display ───────────────────────────────
      jsxs("section", {
        style: { padding: 16, background: "#f6f8fa", borderRadius: 8, marginBottom: 24 },
        children: [
          jsxs("div", {
            style: { display: "flex", alignItems: "center", gap: 12 },
            children: [
              statusDot(status.status === "idle" ? "stopped" : status.status),
              jsx("strong", {
                children:
                  isStarting ? "Starting tunnel…" :
                  isRunning  ? "Live" :
                  status.status === "failed" ? "Failed" :
                  "Off",
              }),
              jsx("div", { style: { flex: 1 } }),
              !isRunning && jsx("button", {
                onClick: onStart,
                disabled: busy !== null,
                title: "First click may download cloudflared (~30MB).",
                children: busy === "start" ? "Starting…" : "Go live",
              }),
              isRunning && jsx("button", {
                onClick: onStop,
                disabled: busy === "stop",
                children: busy === "stop" ? "Stopping…" : "Stop",
              }),
            ],
          }),

          isRunning && status.public_url && jsxs("div", {
            style: {
              marginTop: 12, padding: "8px 12px",
              background: "#fff", border: "1px solid #ddd", borderRadius: 6,
              display: "flex", alignItems: "center", gap: 8, fontFamily: "ui-monospace, monospace",
            },
            children: [
              jsx("a", {
                href: status.public_url, target: "_blank", rel: "noreferrer",
                style: { flex: 1, textDecoration: "none", color: "#06c", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" },
                children: status.public_url,
              }),
              jsx("button", {
                onClick: copyURL,
                title: "Copy",
                children: "Copy",
              }),
            ],
          }),

          isStarting && jsx("div", {
            style: { marginTop: 12, fontSize: 12, color: "#666" },
            children: "Cloudflared usually assigns a URL within a few seconds.",
          }),

          status.status === "failed" && status.last_error && jsx("div", {
            style: { marginTop: 12, fontSize: 13, color: "#b00" },
            children: status.last_error,
          }),

          jsxs("div", {
            style: {
              marginTop: 12, fontSize: 12, color: "#888",
              display: "flex", justifyContent: "space-between", gap: 8,
            },
            children: [
              jsxs("div", {
                children: [
                  "Forwards to ",
                  jsx("code", { children: status.target_url || status.resolved_target || "—" }),
                ],
              }),
              !isRunning && jsx("button", {
                onClick: onReinstall,
                disabled: busy !== null,
                style: {
                  background: "transparent", border: "none",
                  color: "#888", cursor: "pointer", padding: 0,
                  textDecoration: "underline", fontSize: 12,
                },
                children: busy === "install" ? "Downloading…" : "Reinstall cloudflared",
              }),
            ],
          }),
        ],
      }),

      // ─── History ─────────────────────────────────────────────────
      jsxs("section", {
        children: [
          jsx("h3", { children: "History" }),
          runs.length === 0 && jsx("div", {
            style: { color: "#888", fontStyle: "italic", padding: "8px 0" },
            children: "No tunnel runs yet.",
          }),
          ...runs.map((r) => jsxs("div", {
            style: {
              display: "flex", justifyContent: "space-between",
              alignItems: "center", padding: "8px 0", borderBottom: "1px solid #eee",
              fontSize: 13,
            },
            children: [
              jsxs("div", {
                children: [
                  statusDot(r.status),
                  fmtTime(r.started_at),
                  r.finished_at && jsxs(Fragment, {
                    children: [" → ", fmtTime(r.finished_at), " · ", fmtDuration(r.started_at, r.finished_at)],
                  }),
                  r.public_url && jsxs("div", {
                    style: { fontSize: 12, color: "#666", marginTop: 2, fontFamily: "ui-monospace, monospace" },
                    children: [r.public_url],
                  }),
                  r.exit_reason && r.status !== "stopped" && jsx("div", {
                    style: { fontSize: 12, color: "#b00", marginTop: 2 },
                    children: r.exit_reason,
                  }),
                ],
              }),
            ],
          }, r.id)),
        ],
      }),
    ],
  });
}

export { LiveLinkPanel as default };
