import { Fragment, useEffect, useState, type ReactNode } from "react";
import { activityRoute, activityStatusLabel as statusLabel, activityStatusTone as statusTone, activityTime as relativeTime, useInvocationFeed, type ActivityAPI as Api, type ActivityFilter, type FunctionInvocationSummary } from "./activity";

interface FunctionRef { id: number; name: string; }

const filters: Array<[ActivityFilter, string]> = [
  ["all", "All"],
  ["errors", "Errors"],
  ["running", "Running"],
  ["ok", "Successful"],
];

export function FunctionActivityView({
  api,
  functions,
  refreshToken,
  renderInvocation,
}: {
  api: Api;
  functions: FunctionRef[];
  refreshToken: number;
  renderInvocation: (id: number) => ReactNode;
}) {
  const [initial] = useState(() => activityRoute(window.location.search));
  const [filter, setFilter] = useState<ActivityFilter>(initial.status);
  const [functionId, setFunctionId] = useState("");
  const [live, setLive] = useState(true);
  const [expanded, setExpanded] = useState<number | null>(initial.invocationId || null);
  const { page, loading, paging, error, refresh, loadMore } = useInvocationFeed(api, filter, functionId, 100, live, refreshToken);
  useEffect(() => { setExpanded(initial.invocationId || null); }, [filter, functionId]);
  const functionName = (inv: FunctionInvocationSummary) =>
    inv.function_name || functions.find((fn) => fn.id === inv.function_id)?.name || `Function #${inv.function_id}`;

  return (
    <section className="m-4 rounded-lg border border-border bg-bg-card" aria-label="Function logs">
      <div className="p-4 space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <div>
            <div className="flex items-center gap-2">
              <h2 className="font-medium text-text">Live logs</h2>
              <span className={`rounded px-1.5 py-0.5 text-[10px] font-semibold ${live && !error ? "bg-green/15 text-green" : "bg-border text-text-muted"}`}>{live ? error ? "RECONNECTING" : "LIVE" : "PAUSED"}</span>
            </div>
            <p className="mt-0.5 text-xs text-text-muted">Recent invocations from every function. Logs appear when a call finishes. Select a row to inspect output and errors.</p>
          </div>
          <button type="button" aria-pressed={live} className="ml-auto rounded border border-border px-2 py-1 text-xs text-text-muted hover:bg-bg-input" onClick={() => setLive(value => !value)}>{live ? "Pause live" : "Resume live"}</button>
          <button type="button" className=" rounded border border-border px-2 py-1 text-xs text-text-muted hover:bg-bg-input" disabled={loading || paging} onClick={() => void refresh()}>Refresh</button>
        </div>
        <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Function log filters">
          {filters.map(([value, label]) => (
            <button key={value} type="button" aria-pressed={filter === value} onClick={() => setFilter(value)} className={`rounded border px-2 py-1 text-xs ${filter === value ? "border-accent/40 bg-accent/10 text-accent" : "border-border text-text-muted hover:bg-bg-input"}`}>{label}</button>
          ))}
          <select aria-label="Filter by function" className="ml-auto rounded border border-border bg-bg-input px-2 py-1 text-xs text-text" value={functionId} onChange={(event) => setFunctionId(event.target.value)}>
            <option value="">All functions</option>
            {functions.map((fn) => <option key={fn.id} value={String(fn.id)}>{fn.name}</option>)}
          </select>
        </div>
      </div>
      {initial.invocationId && expanded === initial.invocationId && !page?.invocations.some(row => row.id === initial.invocationId) && <div className="border-t border-border p-4"><div className="mb-2 flex items-center gap-2 text-xs text-text-muted">Invocation #{initial.invocationId}<button type="button" className="ml-auto text-accent" onClick={() => setExpanded(null)}>Close details</button></div>{renderInvocation(initial.invocationId)}</div>}
      {error && <p role="alert" className="border-t border-border p-4 text-xs text-red">{error}</p>}
      {loading && !page ? <p role="status" className="border-t border-border p-4 text-xs text-text-muted">Loading invocations…</p> : page && page.invocations.length === 0 ? <p className="border-t border-border p-4 text-xs text-text-muted">No invocations match these filters.</p> : page && (
        <div className="overflow-auto border-t border-border" style={{ maxHeight: "34rem" }} aria-live="polite">
          <table className="w-full text-xs text-left">
            <thead className="sticky top-0 z-10 bg-bg-input text-text-dim"><tr>
              <th className="px-3 py-2 font-normal">Function</th>
              <th className="px-3 py-2 font-normal">Started</th>
              <th className="px-3 py-2 font-normal">Trigger</th>
              <th className="px-3 py-2 font-normal">Duration</th>
              <th className="px-3 py-2 font-normal">Status</th>
            </tr></thead>
            <tbody>{page.invocations.map((inv) => <Fragment key={inv.id}>
              <tr key={inv.id} className="border-t border-border hover:bg-bg-input/50">
                <td className="max-w-[14rem] truncate px-3 py-2 font-medium text-text" title={functionName(inv)}><button type="button" className="text-accent hover:underline" aria-expanded={expanded === inv.id} aria-label={`Inspect invocation ${inv.id}`} onClick={() => setExpanded(expanded === inv.id ? null : inv.id)}>{functionName(inv)}</button><span className="ml-2 text-text-dim">#{inv.id}</span></td>
                <td className="whitespace-nowrap px-3 py-2 text-text-muted">{relativeTime(inv.started_at)}</td>
                <td className="px-3 py-2 text-text-dim">{inv.trigger_kind}</td>
                <td className="whitespace-nowrap px-3 py-2 text-text-muted">{inv.status === "running" ? "—" : `${inv.duration_ms} ms`}</td>
                <td className="px-3 py-2"><span className={`rounded px-1.5 py-0.5 text-[10px] ${statusTone(inv.status)}`}>{statusLabel(inv.status)}</span>{inv.error && <div className="mt-1 max-w-[20rem] truncate text-red" title={inv.error}>{inv.error}</div>}</td>
              </tr>
              {expanded === inv.id && <tr key={`${inv.id}-detail`} className="border-t border-border bg-bg-input/30"><td colSpan={5} className="px-3 py-2">{renderInvocation(inv.id)}</td></tr>}
            </Fragment>)}
            {page.next_cursor && <tr><td colSpan={5} className="p-3 text-center"><button type="button" className="text-xs text-accent" disabled={paging} onClick={() => void loadMore()}>{paging ? "Loading…" : "Load more"}</button></td></tr>}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
