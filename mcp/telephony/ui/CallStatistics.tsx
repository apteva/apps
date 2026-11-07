import { useEffect, useMemo, useRef, useState } from "react";
import type { AudioDashboardHost } from "./audio-dashboard";
import CallDiagnostics from "./CallDiagnostics";
import {
  comparison,
  defaultStatisticsFilters,
  duration,
  percent,
  statisticsURL,
  type StatisticsFilters,
  type StatisticsResponse,
  type StatisticsSelection,
} from "./call-statistics";
const control =
  "rounded border border-border bg-bg px-3 py-2 text-sm text-text";
function useStatistics(
  host: AudioDashboardHost,
  filters: StatisticsFilters,
  selection: StatisticsSelection,
  focus: string,
  cursor: string,
) {
  const [state, setState] = useState<{
    key: string;
    data: StatisticsResponse | null;
    error: string;
    loading: boolean;
  }>({ key: "", data: null, error: "", loading: true });
  const key = JSON.stringify([
    host.appName,
    host.installId,
    host.projectId,
    filters,
    selection,
    focus,
    cursor,
  ]);
  const reload = useRef<() => void>(() => {});
  useEffect(() => {
    let disposed = false,
      busy = false,
      denied = false,
      failures = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let request: AbortController | undefined;
    setState({ key, data: null, error: "", loading: true });
    const load = async (manual = false) => {
      if (disposed || busy || document.hidden || (denied && !manual)) return;
      if (timer) clearTimeout(timer);
      busy = true;
      request = new AbortController();
      const ctrl = request;
      const timeout = setTimeout(() => ctrl.abort("timeout"), 12000);
      setState((s) => ({ ...s, loading: true }));
      try {
        const response = await fetch(
          statisticsURL(host, filters, selection, focus, cursor),
          { credentials: "same-origin", signal: ctrl.signal },
        );
        if (disposed) return;
        if (!response.ok) {
          if ([401, 403, 404].includes(response.status)) {
            denied = true;
            setState((s) => ({ ...s, data: null }));
          }
          throw new Error(
            response.status === 403
              ? "Operator access is required."
              : `Statistics unavailable (HTTP ${response.status}).`,
          );
        }
        const data = (await response.json()) as StatisticsResponse;
        if (disposed) return;
        if (!data.totals || !Array.isArray(data.buckets))
          throw new Error("Invalid statistics response.");
        setState({ key, data, error: "", loading: false });
        failures = 0;
        denied = false;
      } catch (e) {
        if (!disposed) {
          failures++;
          setState((s) => ({
            ...s,
            error: ctrl.signal.aborted
              ? "Statistics timed out. Previous results may be stale."
              : (e as Error).message,
            loading: false,
          }));
        }
      } finally {
        clearTimeout(timeout);
        busy = false;
        if (!disposed) {
          setState((s) => ({ ...s, loading: false }));
          if (!denied && !document.hidden)
            timer = setTimeout(
              () => void load(),
              failures
                ? Math.min(60000, 10000 * 2 ** Math.min(failures, 3))
                : 30000,
            );
        }
      }
    };
    const visibility = () => {
      if (timer) clearTimeout(timer);
      if (!document.hidden) void load();
    };
    reload.current = () => void load(true);
    void load();
    document.addEventListener("visibilitychange", visibility);
    return () => {
      disposed = true;
      if (timer) clearTimeout(timer);
      request?.abort("unmount");
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [key]);
  return {
    ...(state.key === key
      ? state
      : { key, data: null, error: "", loading: true }),
    reload: () => reload.current(),
  };
}
export default function CallStatistics(host: AudioDashboardHost) {
  const [draft, setDraft] = useState<StatisticsFilters>(
      defaultStatisticsFilters,
    ),
    [filters, setFilters] = useState<StatisticsFilters>(
      defaultStatisticsFilters,
    ),
    [selection, setSelection] = useState<StatisticsSelection>(null),
    [focus, setFocus] = useState(""),
    [cursor, setCursor] = useState(""),
    [pages, setPages] = useState<string[]>([]),
    [selected, setSelected] = useState(""),
    [validation, setValidation] = useState("");
  const { data, error, loading, reload } = useStatistics(
    host,
    filters,
    selection,
    focus,
    cursor,
  );
  const resetList = () => {
    setCursor("");
    setPages([]);
    setSelected("");
  };
  const apply = (next: StatisticsFilters) => {
    try {
      statisticsURL(host, next, null, "", "");
      setFilters(next);
      setDraft(next);
      setSelection(null);
      resetList();
      setValidation("");
    } catch (e) {
      setValidation((e as Error).message);
    }
  };
  const pick = (key: "provider" | "adviser" | "number", value: string) =>
    apply({ ...filters, [key]: value || "__unassigned__" });
  const chartMax = Math.max(1, ...(data?.buckets.map((b) => b.calls) || []));
  const table = useRef<HTMLDivElement>(null);
  const chooseBucket = (from: string, until: string) => {
    if (!data) return;
    setSelection({ from, until, anchor: Date.parse(data.until) });
    resetList();
    table.current?.scrollIntoView({ block: "nearest" });
  };
  const exportData = () => {
    if (!data) return;
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }),
    );
    const a = document.createElement("a");
    a.href = url;
    a.download = "telephony-statistics.json";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const periodLabel = useMemo(
    () =>
      data
        ? `${new Date(data.from).toLocaleString()} – ${new Date(data.until).toLocaleString()}`
        : "",
    [data?.from, data?.until],
  );
  const input = (
    key:
      "provider" | "adviser" | "number" | "direction" | "threshold" | "range",
    label: string,
    options: { value: string; label: string }[],
  ) => (
    <label className="min-w-0 text-xs text-text-muted">
      {label}
      <select
        aria-label={label}
        className={`${control} mt-1 block w-full`}
        value={draft[key]}
        onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
  const groups = (key: "provider" | "adviser" | "number") => {
    const options = (data?.breakdowns[key].items ?? []).map((g) => ({
      value: g.key || "__unassigned__",
      label: g.label || "Unassigned",
    }));
    if (draft[key] && !options.some((o) => o.value === draft[key]))
      options.push({ value: draft[key], label: draft[key] });
    return [{ value: "", label: "All" }, ...options];
  };
  return (
    <section
      className="min-h-0 flex-1 overflow-auto bg-bg p-4 space-y-5"
      aria-label="Call statistics"
    >
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold">Statistics</h1>
          <p className="mt-1 text-xs text-text-muted">
            Call activity and reported audio quality ·{" "}
            {data?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone}
          </p>
        </div>
        <div className="flex gap-2">
          <button className={control} disabled={loading} onClick={reload}>
            Refresh statistics
          </button>
          <button className={control} disabled={!data} onClick={exportData}>
            Export statistics
          </button>
        </div>
      </header>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          apply(draft);
        }}
        className="rounded-xl border border-border bg-bg-card p-4 space-y-3"
      >
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {input("range", "Period", [
            { value: "today", label: "Today" },
            { value: "24h", label: "Last 24 hours" },
            { value: "7d", label: "Last 7 days" },
            { value: "custom", label: "Custom dates" },
          ])}
          {input("direction", "Call direction", [
            { value: "", label: "Inbound and outbound" },
            { value: "inbound", label: "Inbound" },
            { value: "outbound", label: "Outbound" },
          ])}
          {input("provider", "Carrier", groups("provider"))}
          {input("adviser", "Adviser", groups("adviser"))}
          {input("number", "Phone number", groups("number"))}
          {input("threshold", "Audio duration threshold", [
            { value: "20", label: "20 ms — sensitive" },
            { value: "200", label: "200 ms — standard" },
            { value: "1000", label: "1 second — major" },
          ])}
          {draft.range === "custom"
            ? (["from", "until"] as const).map((k) => (
                <label className="text-xs text-text-muted" key={k}>
                  {k === "from" ? "From" : "Until"}
                  <input
                    className={`${control} mt-1 block w-full`}
                    aria-label={
                      k === "from" ? "Statistics from" : "Statistics until"
                    }
                    type="datetime-local"
                    value={draft[k]}
                    onChange={(e) =>
                      setDraft({ ...draft, [k]: e.target.value })
                    }
                  />
                </label>
              ))
            : null}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <button
            className="rounded bg-accent px-4 py-2 text-sm text-bg"
            type="submit"
          >
            Apply filters
          </button>
          <button
            type="button"
            className="text-xs text-text-muted underline"
            onClick={() => {
              setFocus("");
              apply(defaultStatisticsFilters);
            }}
          >
            Reset filters
          </button>
          <span className="text-xs text-text-dim">
            Refreshes every 30 seconds while visible.
          </span>
        </div>
      </form>
      {validation || error ? (
        <p
          role="alert"
          className="rounded border border-error/30 p-3 text-sm text-error"
        >
          {validation || error}
        </p>
      ) : null}
      {data ? (
        <>
          <p className="text-xs text-text-muted">
            {periodLabel} · Calls started in this period; lifetime measurements
            as of {new Date(data.generated_at).toLocaleTimeString()}. Previous
            comparison uses the immediately preceding, equal-length period.
          </p>
          {data.pending_reports > 0 ? (
            <p className="rounded bg-warn/10 p-3 text-sm text-warn">
              {data.pending_reports} calls awaiting audio indexing. Call counts
              are complete; audio rates are provisional.
            </p>
          ) : null}
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            {[
              [
                "Calls",
                String(data.totals.calls),
                comparison(data.totals.calls, data.previous.calls),
              ],
              [
                "Answered",
                String(data.totals.answered),
                `${data.totals.active} active · ${data.totals.missed} missed inbound · ${data.totals.failed} failed`,
              ],
              [
                "Connected time",
                duration(data.totals.talk_seconds),
                `${duration(data.totals.average_seconds)} average · ${data.totals.duration_calls ?? data.totals.answered} calls with duration`,
              ],
              [
                "Calls with audio problems",
                percent(data.totals.problem_percent),
                `${data.totals.problems} / ${data.totals.observed} observed · ${comparison(data.totals.problem_percent, data.previous.problem_percent, true)}`,
              ],
              [
                "Telemetry coverage",
                percent(data.totals.coverage_percent),
                `${data.totals.calls - data.totals.observed} calls without telemetry`,
              ],
              [
                "SIP send discard rate",
                percent(data.totals.send_discard_percent),
                `${data.totals.send_rate_calls} calls with paired sent/discard counters`,
              ],
            ].map(([label, value, detail]) => (
              <div
                key={label}
                className="rounded-xl border border-border bg-bg-card p-4"
              >
                <p className="text-xs text-text-muted">{label}</p>
                <p className="mt-2 text-2xl font-semibold tabular-nums">
                  {value}
                </p>
                <p className="mt-2 text-xs text-text-dim">{detail}</p>
              </div>
            ))}
          </div>
          <div className="rounded-xl border border-border bg-bg-card p-4">
            <h2 className="text-sm font-semibold">Call activity</h2>
            <p className="mt-1 text-xs text-text-muted">
              Click an interval to inspect its calls. Blue: observed without a
              matching problem · amber: matching audio problem · grey: no
              telemetry.
            </p>
            <div
              role="group"
              aria-label="Call activity histogram"
              className="mt-4 flex gap-1 overflow-x-auto border-b border-border pb-2"
              style={{ height: 200 }}
            >
              {data.buckets.map((b) => (
                <button
                  key={b.from}
                  type="button"
                  onClick={() => chooseBucket(b.from, b.until)}
                  aria-pressed={selection?.from === b.from}
                  aria-label={`${new Date(b.from).toLocaleString()} to ${new Date(b.until).toLocaleString()}: ${b.calls} calls, ${b.problems} with problems`}
                  title={`${new Date(b.from).toLocaleString()} – ${new Date(b.until).toLocaleString()} · ${b.calls} calls · ${b.problems} problems`}
                  className="flex flex-1 flex-col justify-end rounded px-1 text-center hover:bg-bg-subtle focus-visible:outline-accent"
                  style={{
                    minWidth: 36,
                    border:
                      selection?.from === b.from
                        ? "1px solid var(--accent)"
                        : "1px solid transparent",
                  }}
                >
                  <span className="text-xs tabular-nums text-text-muted">
                    {b.calls}
                  </span>
                  <span
                    className="mt-1 flex w-full flex-col justify-end overflow-hidden rounded-sm"
                    style={{ height: Math.max(2, (b.calls / chartMax) * 130) }}
                  >
                    <span
                      style={{
                        height: `${b.calls ? ((b.calls - b.observed) / b.calls) * 100 : 0}%`,
                        background: "var(--text-dim)",
                      }}
                    />
                    <span
                      style={{
                        height: `${b.calls ? (b.problems / b.calls) * 100 : 0}%`,
                        background: "var(--warn)",
                      }}
                    />
                    <span
                      style={{
                        height: `${b.calls ? ((b.observed - b.problems) / b.calls) * 100 : 0}%`,
                        background: "var(--accent)",
                      }}
                    />
                  </span>
                  <span className="mt-2 whitespace-nowrap text-xs text-text-dim">
                    {data.buckets.length <= 32 &&
                    Date.parse(b.until) - Date.parse(b.from) > 3600000
                      ? new Date(b.from).toLocaleDateString(undefined, {
                          month: "short",
                          day: "numeric",
                        })
                      : new Date(b.from).toLocaleTimeString(undefined, {
                          hour: "2-digit",
                          minute: "2-digit",
                        })}
                  </span>
                </button>
              ))}
            </div>
            {!data.totals.calls ? (
              <p className="mt-3 text-sm text-text-muted">
                No calls started in this period.
              </p>
            ) : null}
          </div>
          <div className="grid gap-4 lg:grid-cols-2">
            <div className="rounded-xl border border-border bg-bg-card p-4">
              <h2 className="text-sm font-semibold">Audio-loss indicators</h2>
              <p className="mt-1 text-xs text-text-muted">
                Separate observation boundaries; never add these rows together.
              </p>
              <dl className="mt-3 divide-y divide-border">
                {[
                  [
                    "SIP receive buffer discard",
                    duration(data.totals.sip_discard_ms / 1000),
                    "sip",
                  ],
                  [
                    "SIP concealment",
                    duration(data.totals.sip_concealed_ms / 1000),
                    "sip",
                  ],
                  [
                    "Browser playback discard",
                    duration(data.totals.browser_discard_ms / 1000),
                    "browser",
                  ],
                  [
                    "Browser replacement audio",
                    duration(data.totals.browser_concealed_ms / 1000),
                    "webrtc",
                  ],
                  [
                    "Microphone capture discard",
                    duration(data.totals.capture_discard_ms / 1000),
                    "capture",
                  ],
                  [
                    "Server WebRTC ingress discard",
                    duration(data.totals.ingress_discard_ms / 1000),
                    "ingress",
                  ],
                  [
                    "Observed playback gaps",
                    duration(data.totals.playback_gap_ms / 1000),
                    "playback_gap",
                  ],
                  [
                    "Carrier send discard",
                    duration(data.totals.send_discard_ms / 1000),
                    "send",
                  ],
                  [
                    "Missing SIP sequences",
                    String(data.totals.sip_missing_packets),
                    "sip",
                  ],
                  [
                    "Browser packets lost",
                    String(data.totals.browser_lost_packets),
                    "webrtc",
                  ],
                ].map(([label, value, coverage]) => (
                  <div
                    key={label}
                    className="flex justify-between gap-4 py-2 text-xs"
                  >
                    <dt className="text-text-muted">{label}</dt>
                    <dd className="tabular-nums">
                      {data.totals.metric_calls?.[coverage]
                        ? `${value} · ${data.totals.metric_calls[coverage]} calls`
                        : "—"}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
            <div className="rounded-xl border border-border bg-bg-card p-4">
              <h2 className="text-sm font-semibold">How to read these rates</h2>
              <div className="mt-3 space-y-3 text-sm text-text-muted">
                <p>
                  Problem rate = calls with a qualifying observation ÷ calls
                  with telemetry. It does not measure spoken words lost.
                </p>
                <p>
                  A qualifying observation is at least {filters.threshold} ms of
                  discard, concealment or playback gaps at any one boundary, or
                  a reported packet loss / transport error. Intentional mute
                  time is excluded.
                </p>
                <p>
                  SIP send discard rate = discarded audio ÷ (successfully sent +
                  discarded audio), using counters from the same send pacer.
                  Legacy calls without both counters are excluded from that
                  rate.
                </p>
                <p>
                  Metrics may include silence. Reporting-session counters can
                  reset. Retained counters are not a complete
                  audible-interruption record; calls without telemetry remain
                  unknown.
                </p>
              </div>
            </div>
          </div>
          <div className="grid gap-3 lg:grid-cols-3">
            {(["adviser", "number", "provider"] as const).map((key) => (
              <div
                key={key}
                className="rounded-xl border border-border bg-bg-card p-4"
              >
                <h2 className="text-sm font-semibold">
                  By {key === "provider" ? "carrier" : key}
                </h2>
                <p className="mt-1 text-xs text-text-dim">
                  Calls · affected / observed
                </p>
                <div className="mt-2 space-y-1">
                  {data.breakdowns[key].items.slice(0, 10).map((g) => (
                    <button
                      key={g.key}
                      type="button"
                      onClick={() => pick(key, g.key)}
                      className="flex w-full items-center justify-between gap-2 rounded p-2 text-left text-xs hover:bg-bg-subtle"
                    >
                      <span className="min-w-0 truncate">
                        {g.label || "Unassigned"}
                      </span>
                      <span className="shrink-0 tabular-nums">
                        {g.calls} · {percent(g.problem_percent)}
                      </span>
                    </button>
                  ))}
                </div>
                {data.breakdowns[key].total_groups > 10 ? (
                  <p className="mt-2 text-xs text-text-dim">
                    Top 10 of {data.breakdowns[key].total_groups}. First 50
                    groups available in filters; exported totals cover all
                    groups.
                  </p>
                ) : null}
              </div>
            ))}
          </div>
          <div
            ref={table}
            className="rounded-xl border border-border bg-bg-card p-4 space-y-3"
          >
            <div className="flex flex-wrap justify-between gap-3">
              <div>
                <h2 className="text-sm font-semibold">Calls in selection</h2>
                <p className="mt-1 text-xs text-text-muted">
                  {data.matching_calls} calls ·{" "}
                  {selection
                    ? `${new Date(selection.from).toLocaleString()} – ${new Date(selection.until).toLocaleString()}`
                    : "Whole period"}
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                <select
                  aria-label="Calls to inspect"
                  className={control}
                  value={focus}
                  onChange={(e) => {
                    setFocus(e.target.value);
                    resetList();
                  }}
                >
                  <option value="">All calls</option>
                  <option value="problems">Audio problems</option>
                  <option value="unobserved">No telemetry</option>
                  <option value="missed">Missed inbound</option>
                  <option value="failed">Failed</option>
                </select>
                {selection ? (
                  <button
                    className={control}
                    onClick={() => {
                      setSelection(null);
                      resetList();
                    }}
                  >
                    Clear chart selection
                  </button>
                ) : null}
              </div>
            </div>
            {data.calls.map((call) => (
              <article
                key={call.call_id}
                className="border-t border-border pt-2"
              >
                <button
                  type="button"
                  aria-expanded={selected === call.call_id}
                  onClick={() =>
                    setSelected(selected === call.call_id ? "" : call.call_id)
                  }
                  className="flex w-full flex-wrap items-center justify-between gap-2 rounded p-2 text-left text-sm hover:bg-bg-subtle"
                >
                  <span>
                    {call.from || "Unknown"} → {call.to || "Unknown"}
                    <span className="ml-2 text-xs text-text-dim">
                      {call.direction} · {call.status}
                    </span>
                  </span>
                  <span
                    className={`text-xs ${call.problem ? "text-warn" : "text-text-muted"}`}
                  >
                    {!call.observed
                      ? "No telemetry"
                      : call.problem
                        ? "Audio problem observed"
                        : "No matching problem observed"}{" "}
                    · {duration(call.talk_seconds)}
                  </span>
                  <span className="text-xs text-text-dim">
                    {new Date(call.started_at).toLocaleString()}
                  </span>
                </button>
                {selected === call.call_id ? (
                  <CallDiagnostics host={host} callId={call.call_id} />
                ) : null}
              </article>
            ))}
            {!data.calls.length ? (
              <p className="py-3 text-sm text-text-muted">
                No calls match this selection.
              </p>
            ) : null}
            <div className="flex gap-2">
              <button
                className={control}
                disabled={!pages.length}
                onClick={() => {
                  setCursor(pages.at(-1)!);
                  setPages(pages.slice(0, -1));
                  setSelected("");
                }}
              >
                Previous calls
              </button>
              <button
                className={control}
                disabled={!data.next_cursor}
                onClick={() => {
                  setPages([...pages, cursor]);
                  setCursor(data.next_cursor);
                  setSelected("");
                }}
              >
                Next calls
              </button>
            </div>
          </div>
        </>
      ) : !error ? (
        <p role="status" className="py-10 text-center text-sm text-text-muted">
          Loading statistics…
        </p>
      ) : null}
    </section>
  );
}
