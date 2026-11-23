import { useEffect, useMemo, useState } from "react";
import type { AudioDashboardHost } from "./audio-dashboard";
import { callLifecycleTopics, formatDuration, freeSetting, stringSetting, useWidgetQuery } from "./widget-query";

interface Totals {
  calls: number;
  answered_person: number;
  answered_ai: number;
  missed: number;
  abandoned: number;
  failed: number;
  active: number;
  answer_rate: number | null;
  average_wait_seconds: number | null;
  average_length_seconds: number | null;
}
interface Bucket { from: string; until: string; answered_person: number; answered_ai: number; missed: number; abandoned: number }
interface OverviewResponse {
  generated_at: string;
  granularity: "hour" | "day";
  totals: Totals;
  buckets: Bucket[];
  numbers: string[];
}

type Range = "today" | "7d" | "30d";
const ranges: Record<Range, string> = { today: "Today", "7d": "Last 7 days", "30d": "Last 30 days" };
const series = [
  { key: "answered_person", label: "Answered by a person", color: "var(--success)" },
  { key: "answered_ai", label: "Answered by AI", color: "var(--accent)" },
  { key: "missed", label: "Missed", color: "var(--warn)" },
  { key: "abandoned", label: "Abandoned", color: "var(--error)" },
] as const;

export function overviewWindow(range: Range, now = new Date()): { from: string; until: string } {
  const until = new Date(Math.floor(now.getTime() / 1000) * 1000);
  let from: Date;
  if (range === "today") from = new Date(until.getFullYear(), until.getMonth(), until.getDate());
  else from = new Date(until.getTime() - (range === "7d" ? 7 : 30) * 86400000);
  if (from.getTime() >= until.getTime()) from = new Date(until.getTime() - 3600000);
  return { from: from.toISOString(), until: until.toISOString() };
}

function timezone(): string {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { return ""; }
}

function OutcomeChart({ data }: { data: OverviewResponse }) {
  const [hover, setHover] = useState<number | null>(null);
  const buckets = data.buckets;
  const max = Math.max(1, ...buckets.map((b) => b.answered_person + b.answered_ai + b.missed + b.abandoned));
  const width = 600, height = 120, gap = buckets.length > 40 ? 1 : 2;
  const barWidth = buckets.length ? Math.max(1, width / buckets.length - gap) : 0;
  const label = (b: Bucket) => {
    const d = new Date(b.from);
    return data.granularity === "hour" ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : d.toLocaleDateString([], { day: "numeric", month: "short" });
  };
  const shown = hover != null ? buckets[hover] : null;
  return (
    <div>
      <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="block h-28 w-full" role="img" aria-label="Calls over time by outcome" onMouseLeave={() => setHover(null)}>
        <line x1="0" x2={width} y1={height - 0.5} y2={height - 0.5} stroke="var(--border)" strokeWidth="1" />
        {buckets.map((b, i) => {
          let y = height;
          return (
            <g key={b.from} onMouseEnter={() => setHover(i)}>
              <rect x={i * (barWidth + gap)} y="0" width={barWidth + gap} height={height} fill="transparent" />
              {series.map((s) => {
                const value = b[s.key];
                if (!value) return null;
                const h = (value / max) * (height - 4);
                y -= h;
                return <rect key={s.key} x={i * (barWidth + gap)} y={y} width={barWidth} height={h} fill={s.color} opacity={hover == null || hover === i ? 1 : 0.45} />;
              })}
            </g>
          );
        })}
      </svg>
      <div className="mt-1 flex justify-between text-xs text-text-muted">
        <span>{buckets[0] ? label(buckets[0]) : ""}</span>
        <span className="tabular-nums">{shown ? `${label(shown)} · ${shown.answered_person} person · ${shown.answered_ai} AI · ${shown.missed} missed · ${shown.abandoned} abandoned` : `Peak ${max} calls per ${data.granularity}`}</span>
        <span>{buckets.length ? label(buckets[buckets.length - 1]) : ""}</span>
      </div>
      <ul className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-text-muted" aria-label="Chart legend">
        {series.map((s) => (
          <li key={s.key} className="flex items-center gap-1.5">
            <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><rect width="10" height="10" rx="2" fill={s.color} /></svg>{s.label}
          </li>
        ))}
      </ul>
    </div>
  );
}

const control = "w-full min-w-0 rounded border border-border bg-bg p-2 text-xs text-text";
const overviewInterval = 60000;

export default function CallsOverviewWidget(props: AudioDashboardHost) {
  const preferredRange = stringSetting(props.widgetSettings, "time_range", ["today", "7d", "30d"], "today") as Range;
  const preferredDirection = stringSetting(props.widgetSettings, "direction", ["", "inbound", "outbound"], "");
  const preferredNumber = freeSetting(props.widgetSettings, "number");
  const [range, setRange] = useState<Range>(preferredRange);
  const [direction, setDirection] = useState(preferredDirection);
  const [number, setNumber] = useState(preferredNumber);
  useEffect(() => setRange(preferredRange), [preferredRange]);
  useEffect(() => setDirection(preferredDirection), [preferredDirection]);
  useEffect(() => setNumber(preferredNumber), [preferredNumber]);
  // The window is recomputed on each refresh tick, so "today" keeps moving.
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const timer = window.setInterval(() => { if (!document.hidden) setTick((t) => t + 1); }, overviewInterval);
    return () => window.clearInterval(timer);
  }, []);
  const params = useMemo(() => ({ ...overviewWindow(range), timezone: timezone(), direction, number }), [range, direction, number, tick]);
  const { data, error, loading, reload } = useWidgetQuery<OverviewResponse>(props, "overview", params, callLifecycleTopics, 10 * overviewInterval);

  if (!props.projectId) {
    return <section className="rounded border border-border bg-bg-card p-4"><h2 className="text-sm font-semibold">Telephony stats</h2><p className="text-xs text-text-muted">Select a project to see call statistics.</p></section>;
  }
  const t = data?.totals;
  const figures: { label: string; value: string; tone?: string; hint?: string }[] = [
    { label: "Total calls", value: t ? String(t.calls) : "—", hint: t && t.active ? `${t.active} still in progress` : undefined },
    { label: "Answered by a person", value: t ? String(t.answered_person) : "—" },
    { label: "Answered by AI", value: t ? String(t.answered_ai) : "—" },
    { label: "Missed", value: t ? String(t.missed + t.failed) : "—", tone: t && t.missed + t.failed > 0 ? "text-warn" : undefined, hint: t && t.failed ? `includes ${t.failed} failed` : undefined },
    { label: "Abandoned", value: t ? String(t.abandoned) : "—", tone: t && t.abandoned > 0 ? "text-error" : undefined, hint: "Caller hung up before an answer" },
    { label: "Answer rate", value: t?.answer_rate != null ? `${Math.round(t.answer_rate)}%` : "—" },
    { label: "Average wait", value: formatDuration(t?.average_wait_seconds), hint: "Until answered" },
    { label: "Average call length", value: formatDuration(t?.average_length_seconds) },
  ];
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border p-3">
        <div>
          <h2 className="text-sm font-semibold">Telephony stats</h2>
          <p className="mt-1 text-xs text-text-muted">Call volume and outcomes · {ranges[range].toLowerCase()}</p>
        </div>
        <button type="button" aria-label="Refresh telephony stats" onClick={() => void reload()} disabled={loading} className="rounded border border-border px-3 py-1.5 text-xs text-text-muted disabled:opacity-50">{loading ? "Refreshing…" : "Refresh"}</button>
      </header>

      <div className="grid grid-cols-3 gap-2 border-b border-border p-3">
        <label className="text-xs text-text-muted">Period
          <select aria-label="Stats period" className={control} value={range} onChange={(e) => setRange(e.target.value as Range)}>
            {Object.entries(ranges).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
        <label className="text-xs text-text-muted">Direction
          <select aria-label="Stats direction" className={control} value={direction} onChange={(e) => setDirection(e.target.value)}>
            <option value="">All</option><option value="inbound">Inbound</option><option value="outbound">Outbound</option>
          </select>
        </label>
        <label className="text-xs text-text-muted">Number
          <select aria-label="Stats number" className={control} value={number} onChange={(e) => setNumber(e.target.value)}>
            <option value="">All numbers</option>
            {[...new Set([number, ...(data?.numbers || [])])].filter(Boolean).map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </label>
      </div>

      {error && <div role="alert" className="border-b border-error/30 bg-error/10 p-3 text-xs text-error">{error}<button onClick={() => void reload()} className="ml-2 underline">Retry</button></div>}

      <div className="min-h-0 flex-1 overflow-auto">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-3 border-b border-border p-3 @xl:grid-cols-4">
          {figures.map((f) => (
            <div key={f.label} title={f.hint}>
              <dt className="text-xs text-text-muted">{f.label}</dt>
              <dd className={`text-xl font-semibold tabular-nums ${f.tone || "text-text"}`}>{f.value}</dd>
              {f.hint && t && <p className="text-xs text-text-muted">{f.hint}</p>}
            </div>
          ))}
        </dl>
        <div className="p-3">
          {data && data.totals.calls > 0 && <OutcomeChart data={data} />}
          {data && data.totals.calls === 0 && <p className="text-xs text-text-muted">No calls in this period.</p>}
          {!data && !error && <p className="text-xs text-text-muted">Loading call statistics…</p>}
        </div>
      </div>

      <footer className="border-t border-border px-3 py-2 text-xs text-text-muted">
        {data ? `Updated ${new Date(data.generated_at).toLocaleTimeString()} · ${data.granularity === "hour" ? "hourly" : "daily"} buckets` : " "}
      </footer>
    </section>
  );
}
