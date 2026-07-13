import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

const API = "/api/apps/web/runs";

function apiURL(projectId) {
  return projectId ? `${API}?project_id=${encodeURIComponent(projectId)}` : API;
}

function relativeTime(value) {
  const timestamp = new Date(value).getTime();
  if (!Number.isFinite(timestamp)) return value || "Unknown time";
  const seconds = Math.round((timestamp - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (Math.abs(seconds) < 60) return formatter.format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return formatter.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, "hour");
  return formatter.format(Math.round(hours / 24), "day");
}

function statusClass(status) {
  switch (status) {
    case "completed": return "border-success/40 bg-success/10 text-success";
    case "failed": return "border-red/40 bg-red/10 text-red";
    case "running": return "border-info/40 bg-info/10 text-info";
    default: return "border-border bg-bg-input text-text-muted";
  }
}

function WebPanel({ projectId } = {}) {
  const [runs, setRuns] = useState([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [statusFilter, setStatusFilter] = useState("all");
  const [kindFilter, setKindFilter] = useState("all");
  const requestId = useRef(0);

  const load = useCallback(async (initial = false) => {
    const currentRequest = ++requestId.current;
    if (initial) setLoading(true);
    else setRefreshing(true);
    setError("");
    try {
      const res = await fetch(apiURL(projectId), { credentials: "include" });
      const text = await res.text();
      let body = {};
      try { body = text ? JSON.parse(text) : {}; } catch { body = { error: text }; }
      if (!res.ok || body.error) throw new Error(body.error || `HTTP ${res.status}`);
      if (requestId.current === currentRequest) setRuns(body.runs || []);
    } catch (err) {
      if (requestId.current === currentRequest) setError(String(err?.message || err));
    } finally {
      if (requestId.current === currentRequest) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [projectId]);

  useEffect(() => { load(true); }, [load]);

  const hasRunning = runs.some((run) => run.status === "running");
  useEffect(() => {
    if (!hasRunning) return undefined;
    const timer = window.setInterval(() => load(false), 3000);
    return () => window.clearInterval(timer);
  }, [hasRunning, load]);

  const kinds = useMemo(() => Array.from(new Set(runs.map((run) => run.kind))).sort(), [runs]);
  const filteredRuns = useMemo(() => runs.filter((run) =>
    (statusFilter === "all" || run.status === statusFilter) &&
    (kindFilter === "all" || run.kind === kindFilter)
  ), [runs, statusFilter, kindFilter]);

  return React.createElement("div", { className: "h-full min-w-0 overflow-auto bg-bg text-text" },
    React.createElement("header", { className: "sticky top-0 z-10 flex flex-wrap items-center gap-3 border-b border-border bg-bg px-4 py-3" },
      React.createElement("div", { className: "mr-auto min-w-0" },
        React.createElement("h1", { className: "text-lg font-semibold" }, "Web"),
        React.createElement("p", { className: "text-xs text-text-muted" }, `${runs.length} recent run${runs.length === 1 ? "" : "s"}`)
      ),
      React.createElement("select", {
        value: kindFilter,
        onChange: (event) => setKindFilter(event.target.value),
        "aria-label": "Filter by run type",
        className: "min-w-32 rounded border border-border bg-bg-input px-2 py-1.5 text-sm"
      },
        React.createElement("option", { value: "all" }, "All types"),
        kinds.map((kind) => React.createElement("option", { key: kind, value: kind }, kind))
      ),
      React.createElement("select", {
        value: statusFilter,
        onChange: (event) => setStatusFilter(event.target.value),
        "aria-label": "Filter by status",
        className: "min-w-32 rounded border border-border bg-bg-input px-2 py-1.5 text-sm"
      },
        React.createElement("option", { value: "all" }, "All statuses"),
        React.createElement("option", { value: "running" }, "Running"),
        React.createElement("option", { value: "completed" }, "Completed"),
        React.createElement("option", { value: "failed" }, "Failed")
      ),
      React.createElement("button", {
        type: "button",
        onClick: () => load(false),
        disabled: refreshing,
        className: "rounded border border-border px-3 py-1.5 text-sm hover:bg-bg-input disabled:opacity-50"
      }, refreshing ? "Refreshing" : "Refresh")
    ),
    React.createElement("main", { className: "mx-auto w-full max-w-5xl p-4" },
      error && React.createElement("div", {
        role: "alert",
        className: "mb-3 rounded border border-red/40 bg-red/10 px-3 py-2 text-sm text-red break-words"
      }, error),
      loading ? React.createElement("div", { className: "py-12 text-center text-sm text-text-muted", "aria-live": "polite" }, "Loading runs") :
        filteredRuns.length === 0 ? React.createElement("div", { className: "py-12 text-center" },
          React.createElement("div", { className: "text-sm font-medium" }, runs.length ? "No runs match these filters" : "No web runs yet"),
          React.createElement("div", { className: "mt-1 text-xs text-text-muted" }, runs.length ? "Change a filter to see more runs." : "Web tool activity will appear here.")
        ) : React.createElement("div", { className: "grid gap-2" },
          filteredRuns.map((run) => React.createElement("article", {
            key: run.id,
            className: "rounded border border-border bg-bg-card px-3 py-3"
          },
            React.createElement("div", { className: "flex flex-wrap items-start gap-2" },
              React.createElement("div", { className: "min-w-0 flex-1" },
                React.createElement("div", { className: "text-sm font-medium capitalize" }, run.kind),
                run.summary && React.createElement("div", { className: "mt-1 text-sm text-text-muted break-words" }, run.summary)
              ),
              React.createElement("span", { className: `shrink-0 rounded border px-2 py-0.5 text-xs ${statusClass(run.status)}` }, run.status)
            ),
            React.createElement("div", { className: "mt-2 text-xs text-text-dim", title: run.created_at }, relativeTime(run.created_at)),
            run.error && React.createElement("div", { className: "mt-2 border-l-2 border-red pl-2 text-xs text-red break-words" }, run.error)
          ))
        )
    )
  );
}

export default WebPanel;

if (typeof document !== "undefined") {
  const el = document.getElementById("root");
  if (el) createRoot(el).render(React.createElement(WebPanel));
}
