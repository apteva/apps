import { subscribeAudioDashboardEvents, type AudioUpdatesMode } from "./audio-dashboard-events";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  audioDashboardURL,
  audioDashboardLink,
  audioIssues,
  audioStages,
  audioStateLabel,
  initialAudioFilters,
  metricLabel,
  type AudioDashboardHost,
  type AudioDashboardResponse,
  type AudioFilters,
  type AudioReport,
} from "./audio-dashboard";

export function useAudioDashboard(
  host: AudioDashboardHost,
  filters: AudioFilters,
  cursor = "",
  limit = 50,
  interval = 15000,
) {
  const [data, setData] = useState<AudioDashboardResponse | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [updatesMode, setUpdatesMode] = useState<AudioUpdatesMode>("connecting");
  const request = useRef<AbortController | null>(null);
  const latest = useRef(0);
  const load = useCallback(async () => {
    if (!host.projectId) return;
    request.current?.abort();
    const ctrl = new AbortController();
    request.current = ctrl;
    const generation = ++latest.current;
    setLoading(true);
    const timeout = window.setTimeout(() => ctrl.abort("timeout"), 12000);
    try {
      const url = audioDashboardURL(host, filters, cursor, limit);
      const response = await fetch(url, {
        credentials: "same-origin",
        signal: ctrl.signal,
      });
      if (!response.ok)
        throw new Error(
          `Audio health request failed (HTTP ${response.status}). ${response.status === 403 ? "Operator access is required." : ""}`,
        );
      const value = (await response.json()) as AudioDashboardResponse;
      if (generation !== latest.current) return;
      setData(value);
      setError("");
    } catch (e) {
      if (generation !== latest.current) return;
      if (ctrl.signal.aborted && ctrl.signal.reason !== "timeout") return;
      setError(
        ctrl.signal.reason === "timeout"
          ? "Audio health request timed out. Displayed data may be stale."
          : (e as Error).message,
      );
    } finally {
      window.clearTimeout(timeout);
      if (generation === latest.current) setLoading(false);
    }
  }, [host.appName, host.installId, host.projectId, filters, cursor, limit]);
  useEffect(() => {
    setData(null);
    setError("");
    void load();
    return () => {
      latest.current++;
      request.current?.abort();
    };
  }, [load]);
  const liveLoad = useRef(load);
  liveLoad.current = load;
  useEffect(() => {
    let pending: ReturnType<typeof setTimeout> | undefined;
    const unsubscribe = subscribeAudioDashboardEvents(host, () => {
      if (document.hidden || pending) return;
      pending = setTimeout(() => { pending = undefined; void liveLoad.current(); }, 300);
    }, setUpdatesMode);
    return () => { unsubscribe(); if (pending) clearTimeout(pending); };
  }, [host.appName, host.installId, host.projectId]);
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!document.hidden) void load();
    }, updatesMode === "fallback" ? interval : Math.max(interval, 60000));
    const resume = () => {
      if (!document.hidden) void load();
    };
    document.addEventListener("visibilitychange", resume);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [load, interval, updatesMode]);
  return { data, error, loading, updatesMode, reload: load };
}
export function AudioHealthCards({ data }: { data: AudioDashboardResponse }) {
  return (
    <div className="grid grid-cols-2 gap-2 md:grid-cols-5">
      {[
        ["Calls in window", data.totals.calls],
        ["Degraded now", data.totals.degraded],
        ["With observations", data.totals.affected],
        ["Active tracked", data.totals.active],
        ["No telemetry", data.totals.unobserved],
      ].map(([label, value]) => (
        <div key={label} className="rounded border border-border p-3">
          <div className="text-xs text-text-muted">{label}</div>
          <div
            className={`text-xl font-semibold tabular-nums ${label === "Degraded now" && Number(value) > 0 ? "text-error" : ""}`}
          >
            {value}
          </div>
        </div>
      ))}
    </div>
  );
}
const displayMS = (n: number | undefined) =>
  n === undefined ? "—" : `${Math.round(n)} ms`;
function AudioDetails({
  host,
  callId,
}: {
  host: AudioDashboardHost;
  callId: string;
}) {
  const [value, setValue] = useState<Record<string, unknown> | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    const ctrl = new AbortController();
    const q = new URLSearchParams({
      call_id: callId,
      project_id: host.projectId || "",
      install_id: String(host.installId || ""),
    });
    const timer = window.setTimeout(() => ctrl.abort(), 12000);
    void (async () => {
      try {
        const r = await fetch(
          `/api/apps/${encodeURIComponent(host.appName || "telephony")}/audio-health?${q}`,
          { credentials: "same-origin", signal: ctrl.signal },
        );
        if (!r.ok) throw new Error(`Diagnostics failed (HTTP ${r.status})`);
        setValue(await r.json());
      } catch (e) {
        if (!ctrl.signal.aborted) setError((e as Error).message);
        else if (ctrl.signal.reason?.name === "AbortError")
          setError("Diagnostics request timed out.");
      } finally {
        window.clearTimeout(timer);
      }
    })();
    return () => {
      window.clearTimeout(timer);
      ctrl.abort("unmount");
    };
  }, [host.appName, host.projectId, host.installId, callId]);
  if (error)
    return (
      <p role="alert" className="text-error">
        {error}
      </p>
    );
  return (
    <details className="mt-3">
      <summary className="cursor-pointer text-text-muted">
        Full recorded diagnostics: peer hash, session events, closes, timings
        and drops
      </summary>
      <pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-muted p-3 text-xs">
        {value ? JSON.stringify(value, null, 2) : "Loading diagnostics…"}
      </pre>
    </details>
  );
}
export function AudioHealthRow({
  report,
  host,
  expanded,
  onExpand,
}: {
  report: AudioReport;
  host: AudioDashboardHost;
  expanded: boolean;
  onExpand: () => void;
}) {
  return (
    <article className="rounded border border-border p-3">
      <button
        type="button"
        className="flex w-full flex-wrap items-center gap-x-4 gap-y-2 text-left"
        onClick={onExpand}
        aria-expanded={expanded}
      >
        <span
          className={`rounded px-2 py-1 text-xs ${report.state === "audio_degraded" ? "bg-error/10 text-error" : "bg-bg-muted text-text-muted"}`}
        >
          {audioStateLabel(report.state)}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-medium">
            {report.adviser_label}
          </span>
          <span className="text-xs text-text-muted">
            {report.provider} · {report.from} → {report.to}
          </span>
        </span>
        <span className="text-xs tabular-nums text-text-muted">
          Dropped {displayMS(report.metrics.playback_dropped_ms)} · RTT{" "}
          {displayMS(report.metrics.max_rtt_ms)} · reconnects{" "}
          {report.metrics.reconnects || 0}
        </span>
        <span className="text-xs text-text-dim">
          {new Date(report.observed_at).toLocaleString()} {expanded ? "▾" : "▸"}
        </span>
      </button>
      <div className="mt-2 flex flex-wrap gap-1">
        {report.issues.map((issue) => (
          <span
            key={issue}
            className="rounded border border-border px-2 py-0.5 text-xs text-text-muted"
          >
            {audioIssues[issue as keyof typeof audioIssues] || issue}
          </span>
        ))}
      </div>
      {expanded ? (
        <div className="mt-3 border-t border-border pt-3">
          <p className="break-all text-xs text-text-muted">
            Call {report.call_id} · {report.status}
          </p>
          <div className="my-3 grid gap-2 md:grid-cols-3">
            {Object.entries(report.health.stages || {}).map(([stage, h]) => (
              <div key={stage} className="rounded bg-bg-muted p-2 text-xs">
                <strong>
                  {audioStages[stage as keyof typeof audioStages] || stage}
                </strong>
                <div>
                  {!report.active
                    ? "Recorded"
                    : report.state === "stale"
                      ? "Stale report"
                      : h.state}{" "}
                  {h.signal ? `· ${h.signal}` : ""}
                </div>
                {h.last_bad_at ? (
                  <div className="text-text-dim">
                    Last impairment: {new Date(h.last_bad_at).toLocaleString()}
                  </div>
                ) : null}
              </div>
            ))}
          </div>
          <dl className="grid grid-cols-2 gap-2 text-xs md:grid-cols-3">
            {Object.entries(report.metrics).map(([key, n]) => (
              <div key={key}>
                <dt className="text-text-muted">{metricLabel[key] || key}</dt>
                <dd className="tabular-nums">{Math.round(n * 10) / 10}</dd>
              </div>
            ))}
          </dl>
          <AudioDetails host={host} callId={report.call_id} />
        </div>
      ) : null}
    </article>
  );
}
export default function AudioHealthView(props: AudioDashboardHost) {
  const [filters, setFilters] = useState(() =>
    initialAudioFilters(window.location.search),
  );
  const [cursor, setCursor] = useState("");
  const [previous, setPrevious] = useState<string[]>([]);
  const [expanded, setExpanded] = useState(
    () =>
      new URLSearchParams(window.location.search).get("audio_call_id") || "",
  );
  const { data, error, loading, reload } = useAudioDashboard(
    props,
    filters,
    cursor,
  );
  const update = (key: keyof AudioFilters, value: string) => {
    setFilters((f) => ({ ...f, [key]: value }));
    setCursor("");
    setPrevious([]);
  };
  const field = (
    label: string,
    key: keyof AudioFilters,
    options: { value: string; label: string }[],
  ) => (
    <label className="min-w-0 text-xs text-text-muted">
      {label}
      <select
        aria-label={label}
        value={filters[key]}
        onChange={(e) => update(key, e.target.value)}
        className="mt-1 block h-9 w-full rounded border border-border bg-bg px-2 text-text"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
  if (!props.projectId)
    return (
      <p className="p-5 text-sm text-text-muted">
        Select a project to view audio health.
      </p>
    );
  return (
    <section
      className="min-h-0 flex-1 overflow-auto p-4 space-y-4"
      aria-label="Audio health overview"
    >
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold">Audio health</h1>
          <p className="text-xs text-text-muted">
            Project-wide browser call observations. Auto-refresh every 15
            seconds.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void reload()}
          disabled={loading}
          className="rounded border border-border px-3 py-2 text-xs"
        >
          {loading ? "Refreshing…" : "Refresh audio health"}
        </button>
      </header>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {field("Time window", "range", [
          { value: "1h", label: "Last hour" },
          { value: "24h", label: "Last 24 hours" },
          { value: "7d", label: "Last 7 days" },
          { value: "custom", label: "Custom range" },
        ])}
        {field("Provider", "provider", [
          { value: "", label: "All providers" },
          ...(data?.facets.provider || []),
        ])}
        {field("Adviser", "adviser", [
          { value: "", label: "All advisers" },
          ...(data?.facets.adviser || []),
        ])}
        {field("Destination / team", "destination", [
          { value: "", label: "All destinations" },
          ...(data?.facets.destination || []),
        ])}
        {field("Audio direction", "stage", [
          { value: "", label: "All directions" },
          ...Object.entries(audioStages).map(([value, label]) => ({
            value,
            label,
          })),
        ])}
        {field("Issue / observation", "issue", [
          { value: "", label: "All observations" },
          ...Object.entries(audioIssues).map(([value, label]) => ({
            value,
            label,
          })),
        ])}
        {field("Show", "state", [
          { value: "", label: "All tracked calls" },
          { value: "issues", label: "Calls with observations" },
          { value: "degraded", label: "Degraded now" },
          { value: "active", label: "Active calls" },
          { value: "unobserved", label: "No telemetry" },
        ])}
        <label className="text-xs text-text-muted">
          Call, number or team
          <input
            aria-label="Call, number or team"
            value={filters.search}
            onChange={(e) => update("search", e.target.value)}
            maxLength={128}
            className="mt-1 block h-9 w-full rounded border border-border bg-bg px-2 text-text"
          />
        </label>
        {filters.range === "custom"
          ? (["from", "until"] as const).map((k) => (
              <label key={k} className="text-xs text-text-muted">
                {k === "from" ? "From" : "Until"}
                <input
                  aria-label={k === "from" ? "From" : "Until"}
                  type="datetime-local"
                  value={filters[k]}
                  onChange={(e) => update(k, e.target.value)}
                  className="mt-1 block h-9 rounded border border-border bg-bg px-2"
                />
              </label>
            ))
          : null}
      </div>
      <p className="text-xs text-text-dim">
        The adviser filter uses the current or last call owner; metrics are
        cumulative per call and may include previous owners. Observations
        include background-tab pauses and reconnections; they do not prove
        audible cuts. RTT is browser ↔ Telephony, not mouth-to-ear delay. Drop
        and sequence counters overlap at some boundaries and are shown
        separately.
      </p>
      {error ? (
        <div
          role="alert"
          className="rounded border border-error/40 p-3 text-sm text-error"
        >
          {error}
          {data ? (
            <span>
              {" "}
              Last successful update:{" "}
              {new Date(data.generated_at).toLocaleTimeString()}.
            </span>
          ) : null}
        </div>
      ) : null}
      {data ? (
        <>
          <AudioHealthCards data={data} />
          {data.pending_reports > 0 ? (
            <p className="text-xs text-warn">
              {data.pending_reports} reports awaiting background indexing.
              Counts may be incomplete until processing finishes.
            </p>
          ) : null}
          <div className="flex items-center justify-between text-xs text-text-muted">
            <span>
              {data.calls.length} shown · {data.totals.calls} match · updated{" "}
              {new Date(data.generated_at).toLocaleTimeString()}
            </span>
            <a href={audioDashboardLink(props, filters)} className="underline">
              Link to these filters
            </a>
          </div>
          <div className="space-y-2">
            {data.calls.map((report) => (
              <AudioHealthRow
                key={report.call_id}
                report={report}
                host={props}
                expanded={expanded === report.call_id}
                onExpand={() =>
                  setExpanded(expanded === report.call_id ? "" : report.call_id)
                }
              />
            ))}
          </div>
          {data.calls.length === 0 ? (
            <p className="rounded border border-border p-5 text-sm text-text-muted">
              No calls match these filters. No telemetry is not proof of healthy
              audio.
            </p>
          ) : null}
          <div className="flex gap-2">
            <button
              type="button"
              disabled={previous.length === 0}
              onClick={() => {
                setCursor(previous[previous.length - 1]);
                setPrevious(previous.slice(0, -1));
              }}
              className="rounded border border-border px-3 py-2 text-xs disabled:opacity-40"
            >
              Previous
            </button>
            <button
              type="button"
              disabled={!data.next_cursor}
              onClick={() => {
                setPrevious([...previous, cursor]);
                setCursor(data.next_cursor);
              }}
              className="rounded border border-border px-3 py-2 text-xs disabled:opacity-40"
            >
              Next
            </button>
          </div>
          <section className="rounded border border-border p-3">
            <h2 className="text-sm font-semibold">Recent project alerts</h2>
            <p className="mb-2 text-xs text-text-dim">
              Latest 20 events in this time window, filtered by provider and
              direction. Adviser, team and issue filters apply to the call list
              above.
            </p>
            {data.alerts.length === 0 ? (
              <p className="text-xs text-text-muted">
                No recorded alerts in this window.
              </p>
            ) : (
              data.alerts.map((a, i) => (
                <div
                  key={`${a.occurred_at}-${i}`}
                  className="border-t border-border py-2 text-xs"
                >
                  <strong>
                    {a.state === "recovered"
                      ? "Recovered"
                      : "Multiple calls degraded"}
                  </strong>{" "}
                  · {a.provider} ·{" "}
                  {audioStages[a.stage as keyof typeof audioStages] || a.stage}{" "}
                  · {a.call_count} calls ·{" "}
                  {new Date(a.occurred_at).toLocaleString()}
                  <div className="mt-1 flex flex-wrap gap-2">
                    {a.call_ids?.map((id) => (
                      <button
                        key={id}
                        type="button"
                        onClick={() => update("search", id)}
                        className="underline"
                      >
                        {id}
                      </button>
                    ))}
                  </div>
                </div>
              ))
            )}
          </section>
        </>
      ) : !error ? (
        <p className="text-sm text-text-muted">Loading audio health…</p>
      ) : null}
    </section>
  );
}
