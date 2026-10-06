import { useEffect, useMemo, useRef, useState } from "react";

import { useAppEvents } from "./lib/useAppEvents";

const API = "/api/apps/tables";

export interface TablesDiagnosticsWidgetProps {
  appName?: string;
  installId: number;
  projectId: string;
  instanceId?: number;
  compact?: boolean;
}

interface Diagnostic {
  id: number;
  recorded_at: string;
  operation: string;
  query_id: string;
  outcome: "ok" | "error" | "timeout" | "canceled" | string;
  stage?: string;
  deadline_source?: string;
  total_ms: number;
  sql_ms: number;
  read_queue_ms: number;
  rows_returned: number;
  error_type?: string;
  sqlite_error_code?: number;
}

interface DiagnosticsResponse {
  diagnostics: Diagnostic[];
  total: number;
  error_count: number;
  slow_count: number;
  has_more: boolean;
}

function apiURL(
  projectId: string,
  installId: number,
  outcome: string,
  limit: number,
): string {
  const url = new URL(`${API}/diagnostics`, window.location.origin);
  url.searchParams.set("project_id", projectId);
  url.searchParams.set("install_id", String(installId));
  url.searchParams.set("limit", String(limit));
  if (outcome) url.searchParams.set("outcome", outcome);
  return url.pathname + url.search;
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleTimeString([], {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      });
}

function percentile(values: number[], fraction: number): number {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[
    Math.min(sorted.length - 1, Math.ceil(sorted.length * fraction) - 1)
  ];
}

function diagnosticDetails(item: Diagnostic): string {
  return [
    `query ${item.query_id}`,
    item.stage && `stage ${item.stage}`,
    item.read_queue_ms > 0 && `queue ${item.read_queue_ms} ms`,
    item.error_type,
  ]
    .filter(Boolean)
    .join(" · ");
}

function outcomeClass(outcome: string): string {
  if (outcome === "ok")
    return "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400";
  if (outcome === "timeout")
    return "bg-amber-500/15 text-amber-600 dark:text-amber-400";
  if (outcome === "canceled")
    return "bg-sky-500/15 text-sky-600 dark:text-sky-400";
  return "bg-red/10 text-red";
}

export default function TablesDiagnosticsWidget({
  projectId,
  installId,
  compact = true,
}: TablesDiagnosticsWidgetProps) {
  const [data, setData] = useState<DiagnosticsResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [outcome, setOutcome] = useState("");
  const [limit, setLimit] = useState(10);
  const invalidate = useRef<() => void>(() => {});
  useAppEvents("tables", projectId, (event) => {
    if (event.project_id !== projectId) return;
    if (
      event.topic === "table.refresh" ||
      (event.topic === "diagnostics.recorded" && event.install_id === installId)
    ) {
      invalidate.current();
    }
  });
  useEffect(() => {
    let disposed = false;
    let running = false;
    let dirty = false;
    let failures = 0;
    let timer: number | undefined;
    const controller = new AbortController();
    setData(null);
    setError("");
    setLoading(!!projectId);
    const schedule = (delay = 250) => {
      if (disposed || !projectId) return;
      dirty = true;
      // A fixed deadline coalesces bursts without postponing indefinitely.
      if (!running && timer === undefined) {
        timer = window.setTimeout(() => {
          timer = undefined;
          void load();
        }, delay);
      }
    };
    const load = async () => {
      if (disposed || !projectId) return;
      running = true;
      dirty = false;
      try {
        const response = await fetch(
          apiURL(projectId, installId, outcome, limit),
          {
            credentials: "same-origin",
            signal: controller.signal,
          },
        );
        if (!response.ok) throw new Error(await response.text());
        const next = (await response.json()) as DiagnosticsResponse;
        if (!disposed) {
          setData(next);
          setError("");
          failures = 0;
        }
      } catch (caught) {
        if (!disposed) {
          setError((caught as Error).message || "Could not load diagnostics");
          failures++;
        }
      } finally {
        running = false;
        if (!disposed) {
          setLoading(false);
          if (dirty) schedule();
          else if (failures > 0 && failures <= 3)
            schedule(1000 * 2 ** (failures - 1));
        }
      }
    };
    const recover = () => {
      failures = 0;
      schedule();
    };
    const connected = (event: Event) => {
      if ((event as CustomEvent).detail?.projectId === projectId) recover();
    };
    const visible = () => {
      if (document.visibilityState !== "hidden") recover();
    };
    invalidate.current = recover;
    window.addEventListener("apteva:app-events-connected", connected);
    window.addEventListener("focus", recover);
    document.addEventListener("visibilitychange", visible);
    void load();
    return () => {
      disposed = true;
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
      invalidate.current = () => {};
      window.removeEventListener("apteva:app-events-connected", connected);
      window.removeEventListener("focus", recover);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [installId, projectId, outcome, limit]);

  const records = (data?.diagnostics ?? []).slice(0, limit);
  const p95 = useMemo(
    () =>
      percentile(
        records.map((item) => item.total_ms),
        0.95,
      ),
    [records],
  );
  const shell = compact
    ? "flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card"
    : "min-h-full overflow-auto bg-bg p-4";
  return (
    <section className={shell} aria-label="Tables diagnostics">
      <header
        className={`flex items-start justify-between gap-3 border-b border-border ${compact ? "px-4 py-3" : "bg-bg-card px-4 py-3"}`}
      >
        <div>
          <p className="text-[11px] uppercase tracking-[0.16em] text-text-dim">
            Operations
          </p>
          <h1
            className={`${compact ? "text-sm" : "mt-0.5 text-lg"} font-semibold text-text`}
          >
            Read diagnostics
          </h1>
          <p className="mt-1 text-xs text-text-dim">
            Slow reads and database failures, with sensitive query data
            redacted.
          </p>
        </div>
      </header>
      <div className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-2 text-xs text-text-muted">
        <label className="flex items-center gap-2">
          Show
          <select
            aria-label="Diagnostic outcome"
            value={outcome}
            onChange={(event) => setOutcome(event.target.value)}
            className="min-w-0 rounded border border-border bg-bg-card px-2 py-1 text-text"
          >
            <option value="">All diagnostics</option>
            <option value="error">Errors only</option>
            <option value="timeout">Timeouts only</option>
            <option value="canceled">Canceled only</option>
          </select>
        </label>
        <label className="flex items-center gap-2">
          Rows
          <select
            aria-label="Diagnostic row limit"
            value={limit}
            onChange={(event) => setLimit(Number(event.target.value))}
            className="rounded border border-border bg-bg-card px-2 py-1 text-text"
          >
            {[10, 25, 50].map((count) => (
              <option key={count} value={count}>
                {count}
              </option>
            ))}
          </select>
        </label>
      </div>
      {error && (
        <div
          role="alert"
          className="border-b border-red/30 bg-red/10 px-4 py-2 text-xs text-red"
        >
          {error}
        </div>
      )}
      {loading && !data ? (
        <p className="p-4 text-xs text-text-dim">Loading diagnostics…</p>
      ) : (
        <div
          className={`${compact ? "min-h-0 flex-1 overflow-auto" : "mx-auto max-w-5xl"} p-3`}
        >
          <div
            className="grid gap-2"
            style={{
              gridTemplateColumns: compact
                ? "repeat(2, minmax(0, 1fr))"
                : "repeat(auto-fit, minmax(min(100%, 180px), 1fr))",
            }}
          >
            <Metric label="Recorded" value={String(data?.total ?? 0)} />
            <Metric
              label="Failures"
              value={String(data?.error_count ?? 0)}
              tone={(data?.error_count ?? 0) > 0 ? "error" : "neutral"}
            />
            <Metric
              label="Slow"
              value={String(data?.slow_count ?? 0)}
              tone={(data?.slow_count ?? 0) > 0 ? "warn" : "neutral"}
            />
            <Metric label="Recent p95" value={`${Math.round(p95)} ms`} />
          </div>
          <p className="mt-3 text-[10px] text-text-dim">
            Showing {records.length} of {data?.total ?? 0}{" "}
            {outcome ? "matching " : ""}records · latest first
          </p>
          {records.length === 0 ? (
            <div className="mt-4 rounded-lg border border-dashed border-border p-6 text-center text-xs text-text-dim">
              {outcome
                ? "No matching diagnostics recorded."
                : "No slow or failed reads recorded."}
            </div>
          ) : (
            <div className="mt-3 overflow-hidden rounded-lg border border-border bg-bg-card">
              <div
                className="grid gap-3 border-b border-border px-3 py-2 text-[10px] uppercase tracking-wide text-text-dim"
                style={{ gridTemplateColumns: "minmax(0, 1fr) auto auto" }}
              >
                <span>Operation</span>
                <span>Duration</span>
                <span>Outcome</span>
              </div>
              <div className="divide-y divide-border">
                {records.map((item) => (
                  <div
                    key={item.id}
                    className="grid items-center gap-3 px-3 py-2 text-xs"
                    style={{ gridTemplateColumns: "minmax(0, 1fr) auto auto" }}
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="truncate font-mono text-text">
                          {item.operation}
                        </span>
                        <span className="shrink-0 whitespace-nowrap text-[10px] text-text-dim">
                          {formatTime(item.recorded_at)}
                        </span>
                      </div>
                      <div
                        className="mt-0.5 truncate text-[10px] text-text-dim"
                        title={diagnosticDetails(item)}
                      >
                        {diagnosticDetails(item)}
                      </div>
                    </div>
                    <span className="whitespace-nowrap font-mono tabular-nums text-text">
                      {item.total_ms} ms
                    </span>
                    <span
                      className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${outcomeClass(item.outcome)}`}
                    >
                      {item.outcome}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function Metric({
  label,
  value,
  tone = "neutral",
}: {
  label: string;
  value: string;
  tone?: "neutral" | "error" | "warn";
}) {
  const valueClass =
    tone === "error"
      ? "text-red"
      : tone === "warn"
        ? "text-amber-600 dark:text-amber-400"
        : "text-text";
  return (
    <div className="rounded-lg border border-border bg-bg-card px-3 py-2.5">
      <div className="text-[10px] uppercase tracking-wide text-text-dim">
        {label}
      </div>
      <div className={`mt-1 font-mono text-lg font-semibold ${valueClass}`}>
        {value}
      </div>
    </div>
  );
}
