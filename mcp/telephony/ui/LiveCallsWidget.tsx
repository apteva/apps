import { useEffect, useMemo, useState } from "react";
import type { AudioDashboardHost } from "./audio-dashboard";
import { callLifecycleTopics, formatDuration, freeSetting, stringSetting, useWidgetQuery } from "./widget-query";

interface LiveCall {
  call_id: string;
  direction: "inbound" | "outbound";
  status: string;
  phase: "ringing" | "in_progress" | "on_hold";
  number: string;
  remote: string;
  answered_by: "person" | "ai" | "";
  handler: string;
  started_at: string;
  answered_at?: string;
}
interface Answerer {
  id: string;
  name: string;
  kind: "browser" | "ai";
  state: "busy" | "connected" | "offline" | "available";
  calls: number;
  people: number;
}
interface LiveResponse {
  generated_at: string;
  calls: LiveCall[];
  truncated: boolean;
  summary: { ringing: number; in_progress: number; on_hold: number };
  answerers: Answerer[];
}

const phaseLabel = { ringing: "Ringing", in_progress: "In progress", on_hold: "On hold" } as const;
const phaseTone = {
  ringing: "border-warn/30 bg-warn/10 text-warn",
  in_progress: "border-success/30 bg-success/10 text-success",
  on_hold: "border-info/30 bg-info/10 text-info",
} as const;
const stateLabel = { busy: "On a call", connected: "Ready", offline: "Offline", available: "AI" } as const;
const stateDot = { busy: "var(--warn)", connected: "var(--success)", offline: "var(--text-dim)", available: "var(--accent)" } as const;

function Dot({ color }: { color: string }) {
  return <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true" className="shrink-0"><circle cx="4" cy="4" r="4" fill={color} /></svg>;
}
function DirectionIcon({ direction }: { direction: string }) {
  const inbound = direction === "inbound";
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-label={inbound ? "Inbound" : "Outbound"} role="img" className="shrink-0">
      <path d={inbound ? "M12 4 4 12M4 6v6h6" : "M4 12 12 4M6 4h6v6"} fill="none" stroke="var(--text-muted)" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

const liveInterval = 5000;
const control = "w-full min-w-0 rounded border border-border bg-bg p-2 text-xs text-text";

export default function LiveCallsWidget(props: AudioDashboardHost) {
  const preferredDirection = stringSetting(props.widgetSettings, "direction", ["", "inbound", "outbound"], "");
  const preferredNumber = freeSetting(props.widgetSettings, "number");
  const [direction, setDirection] = useState(preferredDirection);
  const [number, setNumber] = useState(preferredNumber);
  const [typed, setTyped] = useState(preferredNumber);
  useEffect(() => setDirection(preferredDirection), [preferredDirection]);
  useEffect(() => { setNumber(preferredNumber); setTyped(preferredNumber); }, [preferredNumber]);
  useEffect(() => {
    const timer = window.setTimeout(() => setNumber(typed), 400);
    return () => window.clearTimeout(timer);
  }, [typed]);
  const params = useMemo(() => ({ direction, number }), [direction, number]);
  const { data, error, loading, live, reload } = useWidgetQuery<LiveResponse>(props, "live", params, callLifecycleTopics, liveInterval);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  if (!props.projectId) {
    return <section className="rounded border border-border bg-bg-card p-4"><h2 className="text-sm font-semibold">Live calls</h2><p className="text-xs text-text-muted">Select a project to see live calls.</p></section>;
  }
  const humans = data?.answerers.filter((item) => item.kind === "browser") || [];
  const ready = humans.filter((item) => item.state === "connected").length;
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border p-3">
        <div>
          <h2 className="text-sm font-semibold">Live calls</h2>
          <p className="mt-1 text-xs text-text-muted">Calls ringing, in progress or on hold right now</p>
        </div>
        <button type="button" aria-label="Refresh live calls" onClick={() => void reload()} disabled={loading} className="rounded border border-border px-3 py-1.5 text-xs text-text-muted disabled:opacity-50">{loading ? "Refreshing…" : "Refresh"}</button>
      </header>

      <div className="grid grid-cols-3 gap-2 border-b border-border p-3">
        {(["ringing", "in_progress", "on_hold"] as const).map((phase) => (
          <div key={phase}>
            <p className="text-xs text-text-muted">{phaseLabel[phase]}</p>
            <strong className={`text-xl tabular-nums ${phase === "ringing" && (data?.summary.ringing || 0) > 0 ? "text-warn" : "text-text"}`}>{data ? data.summary[phase] : "—"}</strong>
          </div>
        ))}
      </div>

      <div className="border-b border-border p-3">
        <div className="mb-2 flex items-center justify-between gap-2">
          <p className="text-xs font-semibold text-text">Who can answer now</p>
          {data && <p className="text-xs text-text-muted">{ready} of {humans.length} ready</p>}
        </div>
        {data && data.answerers.length === 0 && <p className="text-xs text-text-muted">No browser or AI destinations are configured.</p>}
        <ul className="flex flex-wrap gap-1.5" aria-label="Answer destinations">
          {data?.answerers.map((item) => (
            <li key={item.id} title={`${item.name}: ${stateLabel[item.state]}${item.kind === "browser" ? ` · ${item.people} ${item.people === 1 ? "person" : "people"}` : ""}`} className="flex max-w-full items-center gap-1.5 rounded border border-border bg-bg px-2 py-1 text-xs">
              <Dot color={stateDot[item.state]} />
              <span className="truncate text-text">{item.name}</span>
              <span className="shrink-0 text-text-muted">{stateLabel[item.state]}{item.calls > 1 ? ` · ${item.calls}` : ""}</span>
            </li>
          ))}
        </ul>
      </div>

      <div className="grid grid-cols-2 gap-2 border-b border-border p-3">
        <label className="text-xs text-text-muted">Direction
          <select aria-label="Live calls direction" className={control} value={direction} onChange={(e) => setDirection(e.target.value)}>
            <option value="">All directions</option><option value="inbound">Inbound</option><option value="outbound">Outbound</option>
          </select>
        </label>
        <label className="text-xs text-text-muted">Number
          <input aria-label="Live calls number" className={control} placeholder="+33…" value={typed} onChange={(e) => setTyped(e.target.value.trim())} />
        </label>
      </div>

      {error && <div role="alert" className="border-b border-error/30 bg-error/10 p-3 text-xs text-error">{error}<button onClick={() => void reload()} className="ml-2 underline">Retry</button></div>}

      <ul className="min-h-0 flex-1 divide-y divide-border overflow-auto" aria-label="Live calls">
        {data?.calls.map((call) => {
          const since = Date.parse(call.answered_at || call.started_at);
          return (
            <li key={call.call_id} className="flex items-center gap-3 px-3 py-2">
              <DirectionIcon direction={call.direction} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm text-text">{call.number || "Unknown number"}</p>
                <p className="truncate text-xs text-text-muted">
                  {call.direction === "inbound" ? "From" : "To"} {call.remote || "unknown"}
                  {call.handler ? ` · ${call.answered_by === "" ? "ringing " : ""}${call.handler}` : ""}
                </p>
              </div>
              <div className="flex shrink-0 flex-col items-end gap-1">
                <span className={`rounded border px-2 py-0.5 text-xs font-semibold ${phaseTone[call.phase]}`}>{phaseLabel[call.phase]}</span>
                <span className="text-xs tabular-nums text-text-muted">{Number.isFinite(since) ? formatDuration((now - since) / 1000) : "—"}</span>
              </div>
            </li>
          );
        })}
        {!data && !error && <li className="p-3 text-xs text-text-muted">Loading live calls…</li>}
        {data?.calls.length === 0 && <li className="p-3 text-xs text-text-muted">No calls right now.</li>}
      </ul>

      <footer className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-3 py-2 text-xs text-text-muted">
        <span>{live ? "Live updates" : "Refreshing every few seconds"}</span>
        {data && <span>Updated {new Date(data.generated_at).toLocaleTimeString()}{data.truncated ? " · first 200 calls" : ""}</span>}
      </footer>
    </section>
  );
}

