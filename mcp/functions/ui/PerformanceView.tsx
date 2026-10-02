import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";

type Api = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;

interface FunctionPerformance {
  function_id: number;
  function_name: string;
  runtime: string;
  calls: number;
  completed: number;
  running: number;
  errors: number;
  canceled: number;
  error_rate: number;
  calls_per_minute: number;
  avg_duration_ms: number | null;
  p95_duration_ms: number | null;
  max_duration_ms: number | null;
  avg_execution_ms: number | null;
  p95_execution_ms: number | null;
  avg_queue_ms: number | null;
  total_execution_ms: number;
}

interface PerformanceReport {
  since: string;
  until: string;
  functions: FunctionPerformance[];
  function_count: number;
  has_more: boolean;
  totals: { calls: number; completed: number; running: number; errors: number; canceled: number; total_execution_ms: number };
}

interface SlowCall {
  id: number;
  function_id: number;
  function_name: string;
  started_at: string;
  status: string;
  duration_ms: number;
  execution_ms: number;
  queue_ms: number;
}

interface SlowPage {
  since: string;
  until: string;
  invocations: SlowCall[];
  next_cursor: string;
}

const rankingOptions = [
  ["total_execution_ms", "Most execution time"],
  ["calls", "Most calls"],
  ["p95_execution_ms", "Slowest p95 execution"],
  ["p95_duration_ms", "Slowest p95 total time"],
  ["avg_execution_ms", "Slowest average execution"],
  ["avg_duration_ms", "Slowest average total time"],
  ["avg_queue_ms", "Longest queue waits"],
  ["errors", "Most errors"],
  ["error_rate", "Highest error rate"],
];

export function timingText(ms: number | null | undefined): string {
  if (ms == null) return "—";
  if (ms < 1000) return `${Math.round(ms).toLocaleString()} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(2)} s`;
  if (ms < 3600000) return `${(ms / 60000).toFixed(1)} min`;
  return `${(ms / 3600000).toFixed(1)} h`;
}

const control = "bg-bg-input border border-border rounded px-2 py-1 text-sm text-text";
const cell = "px-3 py-2 whitespace-nowrap";

export function PerformanceView({ api, functions, onSelect, renderInvocation }: {
  api: Api;
  functions: { id: number; name: string }[];
  onSelect: (id: number) => void;
  renderInvocation: (id: number) => ReactNode;
}) {
  const [tab, setTab] = useState<"functions" | "slow">("functions");
  const [window, setWindow] = useState("24h");
  const [functionId, setFunctionId] = useState("");
  const [ranking, setRanking] = useState("total_execution_ms");
  const [slowSort, setSlowSort] = useState("duration_ms");
  const [minMS, setMinMS] = useState("0");
  const [status, setStatus] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [report, setReport] = useState<PerformanceReport | null>(null);
  const [slow, setSlow] = useState<SlowPage | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [paging, setPaging] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);

  useEffect(() => {
    const request = ++generation.current;
    setReport(null);
    setSlow(null);
    setExpanded(null);
    setError("");
    setLoading(true);
    setPaging(false);
    const filters: Record<string, string> = { window, limit: "100" };
    if (functionId) filters.id = functionId;
    const load = async () => {
      try {
        if (tab === "functions") {
          const data = await api<PerformanceReport>("GET", "/performance", undefined, { ...filters, sort: ranking });
          if (generation.current === request) setReport(data);
        } else {
          const data = await api<SlowPage>("GET", "/invocations/slow", undefined, { ...filters, sort: slowSort, min_ms: minMS, status });
          if (generation.current === request) setSlow(data);
        }
      } catch (e) {
        if (generation.current === request) setError((e as Error).message);
      } finally {
        if (generation.current === request) setLoading(false);
      }
    };
    load();
    return () => { ++generation.current; };
  }, [api, tab, window, functionId, ranking, slowSort, minMS, status, refresh]);

  const more = async () => {
    if (!slow?.next_cursor || paging) return;
    const request = generation.current;
    setPaging(true);
    setError("");
    try {
      const filters: Record<string, string> = { window, sort: slowSort, min_ms: minMS, status, cursor: slow.next_cursor, limit: "100" };
      if (functionId) filters.id = functionId;
      const page = await api<SlowPage>("GET", "/invocations/slow", undefined, filters);
      if (generation.current === request) setSlow({ ...page, invocations: [...slow.invocations, ...page.invocations] });
    } catch (e) {
      if (generation.current === request) setError((e as Error).message);
    } finally {
      if (generation.current === request) setPaging(false);
    }
  };

  const showSlow = (id: number) => {
    setFunctionId(String(id));
    setTab("slow");
  };
  const sortHeader = (key: string, label: string, title?: string) => (
    <th className={cell} aria-sort={ranking === key ? "descending" : "none"}>
      <button type="button" title={title} onClick={() => setRanking(key)} className={ranking === key ? "text-accent font-semibold" : "font-normal hover:text-text"}>
        {label}{ranking === key ? " ↓" : ""}
      </button>
    </th>
  );
  const timestamp = report?.until || slow?.until;

  return (
    <section className="m-4 rounded-lg border border-border bg-bg-card" aria-label="Function performance">
      <div className="p-4 space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <h2 className="font-medium text-text">Performance</h2>
          <div className="flex gap-1" role="group" aria-label="Performance view">
            <button type="button" aria-pressed={tab === "functions"} onClick={() => setTab("functions")} className={`px-3 py-1 text-sm rounded ${tab === "functions" ? "bg-accent/15 text-accent" : "text-text-muted hover:bg-bg-input"}`}>Function ranking</button>
            <button type="button" aria-pressed={tab === "slow"} onClick={() => setTab("slow")} className={`px-3 py-1 text-sm rounded ${tab === "slow" ? "bg-accent/15 text-accent" : "text-text-muted hover:bg-bg-input"}`}>Slow calls</button>
          </div>
          <label className="ml-auto text-xs text-text-muted">Period <select aria-label="Performance period" className={control} value={window} onChange={e => setWindow(e.target.value)}>
            <option value="1h">Last hour</option><option value="6h">Last 6 hours</option><option value="24h">Last 24 hours</option><option value="7d">Last 7 days</option><option value="30d">Last 30 days</option>
          </select></label>
          <button type="button" className={control} disabled={loading || paging} onClick={() => setRefresh(n => n + 1)}>Refresh performance</button>
        </div>
        <p className="text-xs text-text-muted">Find busy functions by calls or total execution time. p95 is the time 95% of completed calls finish within. Execution includes downstream calls; total time adds preparation, queueing and worker startup.</p>
        <div className="flex flex-wrap items-center gap-3">
          <label className="text-xs text-text-muted">Function <select aria-label="Performance function" className={control} value={functionId} onChange={e => setFunctionId(e.target.value)}>
            <option value="">All functions</option>{functions.map(f => <option key={f.id} value={String(f.id)}>{f.name}</option>)}
          </select></label>
          {tab === "functions" ? <label className="text-xs text-text-muted">Rank by <select aria-label="Rank functions by" className={control} value={ranking} onChange={e => setRanking(e.target.value)}>
            {rankingOptions.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select></label> : <>
            <label className="text-xs text-text-muted">Sort by <select aria-label="Sort slow calls by" className={control} value={slowSort} onChange={e => setSlowSort(e.target.value)}>
              <option value="duration_ms">Total time</option><option value="execution_ms">Execution time</option><option value="queue_ms">Queue wait</option>
            </select></label>
            <label className="text-xs text-text-muted">Minimum <select aria-label="Minimum slow-call time" className={control} value={minMS} onChange={e => setMinMS(e.target.value)}>
              <option value="0">Any duration</option><option value="100">100 ms</option><option value="500">500 ms</option><option value="1000">1 second</option><option value="5000">5 seconds</option><option value="10000">10 seconds</option>
            </select></label>
            <label className="text-xs text-text-muted">Status <select aria-label="Slow-call status" className={control} value={status} onChange={e => setStatus(e.target.value)}>
              <option value="">All completed</option><option value="ok">Successful</option><option value="error">Error</option><option value="timeout">Function timeout</option><option value="upstream_timeout">Upstream timeout</option><option value="canceled">Caller canceled</option>
            </select></label>
          </>}
        </div>
        {loading && <p role="status" className="text-sm text-text-muted">Loading performance…</p>}
        {error && <p role="alert" className="text-sm text-red">{error}</p>}
        {report && <div className="grid grid-cols-2 md:grid-cols-4 gap-2 text-sm">
          {[["Calls", report.totals.calls.toLocaleString()], ["Completed / running", `${report.totals.completed.toLocaleString()} / ${report.totals.running.toLocaleString()}`], ["Errors / canceled", `${report.totals.errors.toLocaleString()} / ${report.totals.canceled.toLocaleString()}`], ["Total execution time", timingText(report.totals.total_execution_ms)]].map(([label, value]) =>
            <div key={label} className="border border-border rounded p-3"><div className="text-xs text-text-muted">{label}</div><strong className="text-text">{value}</strong></div>)}
        </div>}
      </div>
      {report && (report.functions.length ? <div className="overflow-auto" style={{ maxHeight: "32rem" }}>
        <table className="w-full text-xs text-left" aria-label="Function performance ranking">
          <thead className="text-text-dim bg-bg-input sticky top-0 z-10"><tr>
            <th className={cell}>Function</th>{sortHeader("calls", "Calls")}
            {sortHeader("avg_execution_ms", "Avg execution")}{sortHeader("p95_execution_ms", "p95 execution", "95% of completed calls have execution time at or below this value.")}
            {sortHeader("p95_duration_ms", "p95 total")}{sortHeader("avg_queue_ms", "Avg queue")}
            {sortHeader("total_execution_ms", "Total execution", "Summed wall time across completed calls, including downstream calls; not CPU time.")}
            {sortHeader("errors", "Errors")}<th className={cell}></th>
          </tr></thead>
          <tbody>{report.functions.map(f => <tr key={f.function_id} className="border-t border-border hover:bg-bg-input/30">
            <td className={cell}><button type="button" className="text-accent font-medium hover:underline" onClick={() => onSelect(f.function_id)}>{f.function_name}</button><div className="text-text-dim">{f.runtime} · {f.completed} completed{f.running ? ` · ${f.running} running` : ""}</div></td>
            <td className={cell}>{f.calls.toLocaleString()}<div className="text-text-dim">{f.calls_per_minute.toFixed(2)}/min</div></td>
            <td className={cell}>{timingText(f.avg_execution_ms)}</td><td className={cell}>{timingText(f.p95_execution_ms)}</td>
            <td className={cell}>{timingText(f.p95_duration_ms)}</td><td className={cell}>{timingText(f.avg_queue_ms)}</td>
            <td className={cell}>{timingText(f.total_execution_ms)}</td>
            <td className={`${cell} ${f.errors ? "text-red" : "text-text-muted"}`}>{f.errors.toLocaleString()}<div>{f.completed ? `${(f.error_rate * 100).toFixed(1)}%` : "—"}</div></td>
            <td className={cell}><button type="button" className="text-accent hover:underline" onClick={() => showSlow(f.function_id)} aria-label={`Slow calls for ${f.function_name}`}>Slow calls →</button></td>
          </tr>)}</tbody>
        </table>
      </div> : <p className="px-4 pb-4 text-sm text-text-muted">No recorded calls in this period. Try a longer period or another function.</p>)}
      {slow && (slow.invocations.length ? <div className="overflow-auto" style={{ maxHeight: "32rem" }}>
        <table className="w-full text-xs text-left" aria-label="Slow function calls">
          <thead className="text-text-dim bg-bg-input sticky top-0 z-10"><tr>
            <th className={cell}>Invocation / function</th><th className={cell}>Started</th><th className={cell}>Status</th>
            <th className={cell}>Total time{slowSort === "duration_ms" && " ↓"}</th><th className={cell}>Execution{slowSort === "execution_ms" && " ↓"}</th><th className={cell}>Queue{slowSort === "queue_ms" && " ↓"}</th><th className={cell}></th>
          </tr></thead>
          <tbody>{slow.invocations.map(call => <Fragment key={call.id}>
            <tr className="border-t border-border hover:bg-bg-input/30">
              <td className={cell}>#{call.id} <button type="button" className="text-accent hover:underline" onClick={() => onSelect(call.function_id)}>{call.function_name}</button></td>
              <td className={cell}>{new Date(call.started_at).toLocaleString()}</td><td className={`${cell} ${call.status !== "ok" && call.status !== "canceled" ? "text-red" : "text-text-muted"}`}>{call.status.replaceAll("_", " ")}</td>
              <td className={cell}>{timingText(call.duration_ms)}</td><td className={cell}>{timingText(call.execution_ms)}</td><td className={cell}>{timingText(call.queue_ms)}</td>
              <td className={cell}><button type="button" className="text-accent hover:underline" aria-expanded={expanded === call.id} aria-label={`Inspect invocation ${call.id}`} onClick={() => setExpanded(expanded === call.id ? null : call.id)}>{expanded === call.id ? "Close details" : "Inspect →"}</button></td>
            </tr>
            {expanded === call.id && <tr className="border-t border-border bg-bg-input/30"><td colSpan={7} className="p-3">{renderInvocation(call.id)}</td></tr>}
          </Fragment>)}</tbody>
        </table>
        {slow.next_cursor && <button type="button" className={`${control} m-4`} disabled={paging} onClick={more}>{paging ? "Loading…" : "Load more slow calls"}</button>}
      </div> : <p className="px-4 pb-4 text-sm text-text-muted">No completed calls match these filters. Try a longer period or lower the minimum time.</p>)}
      {timestamp && <div className="p-4 space-y-1 text-xs text-text-dim">
        {report?.has_more && <p>Showing the top {report.functions.length} of {report.function_count} functions. Totals cover all matching functions; use the function filter to inspect another.</p>}
        <p>Snapshot ending {new Date(timestamp).toLocaleString()}. Refresh to include new calls.</p>
        <p>Retained history only. p95 uses completed calls, including failures; running calls have no finalized latency. Older timing breakdowns may be zero when not recorded.</p>
      </div>}
    </section>
  );
}
