// AnalyticsPanel — live overview of the generic event store.
//
// Layout:
//   ┌─ header: title · window (24h/7d/30d/All) · live · refresh ─────┐
//   │ filter bar: app · event type · props.key = value · clear       │
//   │ stat tiles: total events · topics · apps                       │
//   │ events/day  — inline SVG bar chart over /series                │
//   │ top values  — horizontal bars over /top?by=props.X             │
//   │ event feed  — recent rows over /events (real-time)             │
//   │ topics      — table over /summary.topics_list                  │
//   └──────────────────────────────────────────────────────────────┘
//
// Real-time: each analytics_track emits "event.recorded" on the app bus;
// this panel subscribes via the inlined useAppEvents helper and reloads
// (debounced) when events land — same pattern as the crm / calendar
// panels. Charts are hand-rolled inline SVG coloured via currentColor +
// a theme text token. Backend: GET /api/apps/analytics/{summary,series,
// top,events,dimensions}.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Archive, Plus, RefreshCw } from "lucide-react";
import { batchResultsByID, formatMetric, formatObjectivePeriod, formatObjectiveValue, isCurrentRequest, objectiveMonthBounds, objectiveProgressWidth, partitionDashboardWidgets, resolveMetricConfig, resolvedWindow, scopedAppURL } from "./dashboard-ui";

// Inlined SDK app-event subscription. Each app ships its own copy
// because panels are bundled standalone and apps are independently
// installable — cross-app imports would break a one-off install.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents so every
    // panel reuses one EventSource per (app, project).
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

const API = "/api/apps/analytics";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface TopicRow {
  app: string;
  topic: string;
  last_ts: number;
  count: number;
}

interface Summary {
  total: number;
  apps: number;
  topics: number;
  topics_list: TopicRow[];
}

interface SeriesPoint {
  day: string;
  count: number;
  value?: number;
}

interface TopRow {
  value: string | null;
  count: number;
}

interface EventRow {
  id: number;
  ts: number;
  app: string;
  topic: string;
  source: string;
  user_id?: string;
  session_id?: string;
  props: Record<string, unknown>;
}

interface Dimensions {
  apps: string[];
  topics: string[];
}

interface DashboardWidget {
  id: number;
  dashboard_id?: number;
  type: "stat" | "timeseries" | "top" | "breakdown" | "feed";
  title: string;
  position: number;
  config: Record<string, unknown>;
}

interface DashboardFilter {
  key: string;
  label: string;
  type: "select" | "multi_select" | "date_window";
  default?: string;
  options?: Array<{ value: string; label?: string } | string>;
  source?: Record<string, unknown>;
}

interface Dashboard {
  id: number;
  project_id: string;
  name: string;
  description: string;
  config?: {
    filters?: DashboardFilter[];
  };
  widgets?: DashboardWidget[];
}

interface EventPropertySpec {
  key: string;
  type: string;
  required?: boolean;
  description?: string;
  enum_values?: string[];
  pii_classification?: string;
  example_value?: string;
}

interface EventIngestPolicy {
  target_topic?: string;
  bucket?: "none" | "hour" | "day" | "week" | "month";
  timezone?: string;
  timestamp_property?: string;
  operation?: "replace" | "increment" | "sum" | "min" | "max";
  value?: unknown;
  value_key?: string;
  output_property?: string;
  dimensions?: string[];
}

interface EventSpec {
  id?: number;
  project_id: string;
  app: string;
  topic: string;
  kind: "occurrence" | "aggregate_observation";
  display_name: string;
  description: string;
  category: string;
  status: "draft" | "active" | "deprecated" | "blocked";
  validation_mode: "observe" | "warn" | "reject";
  ingest_mode?: "raw" | "upsert" | "raw_plus_rollup";
  upsert_policy?: EventIngestPolicy;
  rollup_policy?: EventIngestPolicy;
  properties: EventPropertySpec[];
}

interface EventViolation {
  id: number;
  app: string;
  topic: string;
  violation_type: string;
  message: string;
  property_key?: string;
  seen_at: number;
}

interface ObjectiveMetricQuery {
  aggregation: "count" | "sum" | "distinct";
  app?: string;
  topic?: string;
  source?: "track" | "auto";
  value?: string;
  by?: string;
  where?: Record<string, unknown>;
}

interface TargetProgress {
  target_id: number;
  actual_value?: number;
  target_value: number;
  progress_percent?: number;
  achieved: boolean;
  period_state: "upcoming" | "active" | "ended";
  status: "ok" | "error";
  error?: string;
  measured_at?: number;
}

interface ObjectiveTarget {
  id: number;
  name: string;
  metric_key: string;
  target_value: number;
  unit: "money" | "count" | "percent" | "number";
  currency?: string;
  direction: "at_least" | "at_most";
  period_start: number;
  period_end: number;
  timezone: string;
  query: ObjectiveMetricQuery;
  last_progress?: TargetProgress;
}

interface Objective {
  id: number;
  name: string;
  description: string;
  owner_type?: string;
  owner_id?: string;
  status: "draft" | "active" | "paused" | "archived";
  targets: ObjectiveTarget[];
  updated_at: number;
}

const WINDOWS = [
  { key: "24h", label: "24h", ms: 24 * 3600e3 },
  { key: "7d", label: "7d", ms: 7 * 24 * 3600e3 },
  { key: "30d", label: "30d", ms: 30 * 24 * 3600e3 },
  { key: "all", label: "All", ms: 0 },
];

function fmt(n: number): string {
  return (n ?? 0).toLocaleString();
}

function relTime(ms: number): string {
  if (!ms) return "";
  const dt = Date.now() - ms;
  const s = Math.max(0, Math.floor(dt / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function propsPreview(props: Record<string, unknown>): string {
  if (!props || typeof props !== "object") return "";
  return Object.entries(props)
    .slice(0, 5)
    .map(([k, v]) => `${k}=${typeof v === "object" ? JSON.stringify(v) : String(v)}`)
    .join("  ");
}

function ChartIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3 3v18h18" />
      <path d="M7 14l4-4 3 3 5-6" />
    </svg>
  );
}

function Dot() {
  return (
    <svg width="8" height="8" viewBox="0 0 8 8" className="text-success" aria-hidden="true">
      <circle cx="4" cy="4" r="4" fill="currentColor" />
    </svg>
  );
}

function Empty({ label }: { label: string }) {
  return <div className="py-8 text-center text-text-muted text-sm">{label}</div>;
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="border border-border rounded px-4 py-3 flex-1">
      <div
        className="text-text font-semibold tabular-nums"
        style={{ fontSize: "24px", lineHeight: 1.1 }}
      >
        {fmt(value)}
      </div>
      <div className="text-text-dim text-xs uppercase mt-1">{label}</div>
    </div>
  );
}

function BarSeries({ data }: { data: SeriesPoint[] }) {
  if (!data.length) return <Empty label="No events in this window." />;
  const W = 600;
  const H = 120;
  const max = Math.max(...data.map((d) => d.value ?? d.count), 1);
  const bw = W / data.length;
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      className="w-full text-accent"
      style={{ height: "120px" }}
      preserveAspectRatio="none"
    >
      {data.map((d, i) => {
        const v = d.value ?? d.count;
        const h = Math.max(1, (v / max) * (H - 2));
        return (
          <rect
            key={d.day}
            x={i * bw + bw * 0.12}
            y={H - h}
            width={bw * 0.76}
            height={h}
            fill="currentColor"
          >
            <title>{`${d.day}: ${fmt(v)}`}</title>
          </rect>
        );
      })}
    </svg>
  );
}

function TrendSeries({ data, config }: { data: SeriesPoint[]; config: Record<string, unknown> }) {
  if (!data.length) return <Empty label="No observations in this window." />;
  const W = 600;
  const H = 132;
  const padX = 14;
  const padY = 12;
  const values = data.map((d) => d.value ?? d.count);
  const max = Math.max(...values, 1);
  const min = Math.min(...values, 0);
  const span = Math.max(max - min, 1);
  const coords = data.map((d, i) => {
    const x = data.length === 1 ? W / 2 : padX + (i / (data.length - 1)) * (W - padX * 2);
    const y = H - padY - (((d.value ?? d.count) - min) / span) * (H - padY * 2);
    return { x, y };
  });
  const points = coords.map(({ x, y }) => `${x},${y}`).join(" ");
  return (
    <div className="flex flex-col gap-1" role="img" aria-label={`Trend from ${data[0].day} to ${data[data.length - 1].day}`}>
      <div className="flex items-center justify-between text-text-dim text-xs tabular-nums">
        <span>{formatMetric(max, config)}</span>
        <span>{formatMetric(values[values.length - 1], config)}</span>
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full text-accent" style={{ height: "132px" }} preserveAspectRatio="none">
        <line x1={padX} y1={H - padY} x2={W - padX} y2={H - padY} stroke="currentColor" opacity="0.2" />
        <polyline points={points} fill="none" stroke="currentColor" strokeWidth="3" vectorEffect="non-scaling-stroke" />
        {data.map((d, i) => {
          const { x, y } = coords[i];
          const value = d.value ?? d.count;
          return <circle key={d.day} cx={x} cy={y} r="4" fill="currentColor"><title>{`${d.day}: ${formatMetric(value, config)}`}</title></circle>;
        })}
      </svg>
      <div className="flex items-center justify-between text-text-dim text-xs">
        <span>{data[0].day}</span>
        <span>{data[data.length - 1].day}</span>
      </div>
    </div>
  );
}

function TopBars({ rows }: { rows: TopRow[] }) {
  if (!rows.length) return <Empty label="No values for this key in this window." />;
  const max = Math.max(...rows.map((r) => r.count), 1);
  return (
    <div className="flex flex-col gap-1.5">
      {rows.map((r) => (
        <div key={String(r.value)} className="flex items-center gap-2 text-xs">
          <div className="text-text-muted truncate" style={{ width: "140px" }} title={r.value ?? ""}>
            {r.value ?? "—"}
          </div>
          <div className="flex-1 bg-bg-input rounded overflow-hidden" style={{ height: "14px" }}>
            <div className="h-full bg-accent" style={{ width: `${(r.count / max) * 100}%` }} />
          </div>
          <div className="text-text-dim tabular-nums" style={{ width: "56px", textAlign: "right" }}>
            {fmt(r.count)}
          </div>
        </div>
      ))}
    </div>
  );
}

function EventFeed({ rows }: { rows: EventRow[] }) {
  if (!rows.length) return <Empty label="No events match these filters." />;
  return (
    <div style={{ maxHeight: "320px", overflow: "auto" }}>
      {rows.map((e) => (
        <div key={e.id} className="flex items-center gap-3 text-xs border-t border-border py-1.5">
          <span className="text-text-dim tabular-nums" style={{ width: "64px" }}>
            {relTime(e.ts)}
          </span>
          <span className="text-text-muted truncate" style={{ width: "84px" }}>
            {e.app}
          </span>
          <span className="text-text font-medium truncate" style={{ width: "120px" }}>
            {e.topic}
          </span>
          <span className="text-text-dim truncate flex-1" title={JSON.stringify(e.props)}>
            {propsPreview(e.props)}
          </span>
        </div>
      ))}
    </div>
  );
}

interface EventDecl {
  name: string;
  description?: string;
  dynamic?: boolean;
}

interface WriteKey {
  id: number;
  key: string;
  site: string;
  project_id: string;
  allowed_origins?: string[];
  created_at: number;
  revoked_at?: number;
  last_used_ts?: number;
  event_count: number;
}

function CopyButton({ text, label }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      setTimeout(() => setDone(false), 1500);
    } catch {
      /* clipboard blocked (insecure context) — no-op */
    }
  };
  return (
    <button
      onClick={copy}
      className={
        "px-2 py-1 text-xs rounded border whitespace-nowrap " +
        (done ? "border-success text-success" : "border-border text-text-muted hover:text-text")
      }
    >
      {done ? "Copied" : label || "Copy"}
    </button>
  );
}

function currentMonth(): string {
  const now = new Date();
  return `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, "0")}`;
}

function ObjectivesTab({ projectId }: { projectId: string }) {
  const [objectives, setObjectives] = useState<Objective[]>([]);
  const [dims, setDims] = useState<Dimensions>({ apps: [], topics: [] });
  const [showForm, setShowForm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [refreshingId, setRefreshingId] = useState(0);
  const [err, setErr] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [targetName, setTargetName] = useState("");
  const [targetValue, setTargetValue] = useState("");
  const [unit, setUnit] = useState<ObjectiveTarget["unit"]>("count");
  const [currency, setCurrency] = useState("USD");
  const [direction, setDirection] = useState<ObjectiveTarget["direction"]>("at_least");
  const [month, setMonth] = useState(currentMonth());
  const [aggregation, setAggregation] = useState<ObjectiveMetricQuery["aggregation"]>("count");
  const [app, setApp] = useState("");
  const [topic, setTopic] = useState("");
  const [value, setValue] = useState("props.amount_usd");
  const [by, setBy] = useState("session_id");
  const [whereKey, setWhereKey] = useState("");
  const [whereValue, setWhereValue] = useState("");

  const scopedURL = useCallback((path: string) => scopedAppURL(path, projectId), [projectId]);

  const load = useCallback(async () => {
    setErr("");
    try {
      const r = await fetch(scopedURL(`${API}/objectives`), { credentials: "same-origin" });
      if (!r.ok) throw new Error((await r.text()) || `Objectives ${r.status}`);
      setObjectives((await r.json()).objectives ?? []);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [scopedURL]);

  useEffect(() => {
    load();
    fetch(scopedURL(`${API}/dimensions`), { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : { apps: [], topics: [] }))
      .then((d) => setDims({ apps: d.apps ?? [], topics: d.topics ?? [] }))
      .catch(() => {});
  }, [load, scopedURL]);

  const resetForm = () => {
    setName("");
    setDescription("");
    setTargetName("");
    setTargetValue("");
    setUnit("count");
    setCurrency("USD");
    setDirection("at_least");
    setMonth(currentMonth());
    setAggregation("count");
    setApp("");
    setTopic("");
    setValue("props.amount_usd");
    setBy("session_id");
    setWhereKey("");
    setWhereValue("");
  };

  const create = async () => {
    setErr("");
    const numericTarget = Number(targetValue);
    if (!name.trim() || !targetName.trim() || !Number.isFinite(numericTarget)) {
      setErr("Objective name, target name and target value are required.");
      return;
    }
    let bounds: { start: number; end: number };
    try {
      bounds = objectiveMonthBounds(month);
    } catch (e) {
      setErr((e as Error).message);
      return;
    }
    const query: ObjectiveMetricQuery = { aggregation };
    if (app.trim()) query.app = app.trim();
    if (topic.trim()) query.topic = topic.trim();
    if (aggregation === "sum") query.value = value.trim();
    if (aggregation === "distinct") query.by = by.trim();
    if (whereKey.trim() && whereValue.trim()) query.where = { [whereKey.trim()]: whereValue.trim() };
    setBusy(true);
    try {
      const r = await fetch(scopedURL(`${API}/objectives`), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: name.trim(),
          description: description.trim(),
          status: "active",
          targets: [{
            name: targetName.trim(),
            metric_key: aggregation === "sum" ? "custom_sum" : aggregation === "distinct" ? "custom_distinct" : "custom_count",
            target_value: numericTarget,
            unit,
            currency: unit === "money" ? currency.trim().toUpperCase() : "",
            direction,
            period_start: bounds.start,
            period_end: bounds.end,
            timezone: "UTC",
            query,
          }],
        }),
      });
      if (!r.ok) throw new Error((await r.text()) || `Create ${r.status}`);
      resetForm();
      setShowForm(false);
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const refreshObjective = async (id: number) => {
    setRefreshingId(id);
    setErr("");
    try {
      const r = await fetch(scopedURL(`${API}/objectives/${id}/progress`), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error((await r.text()) || `Refresh ${r.status}`);
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setRefreshingId(0);
    }
  };

  const archive = async (id: number) => {
    if (!window.confirm("Archive this objective?")) return;
    setErr("");
    try {
      const r = await fetch(scopedURL(`${API}/objectives/${id}`), { method: "DELETE", credentials: "same-origin" });
      if (!r.ok) throw new Error((await r.text()) || `Archive ${r.status}`);
      await load();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const inputCls = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text";
  const labelCls = "flex flex-col gap-1 text-xs text-text-dim";

  return (
    <div className="flex-1 overflow-auto">
      <div className="flex items-center gap-3 border-b border-border px-4 py-3">
        <div>
          <div className="text-text font-medium">Objectives</div>
          <div className="text-text-dim text-xs">{objectives.length} {objectives.length === 1 ? "objective" : "objectives"}</div>
        </div>
        {err && <span className="text-error text-xs truncate">{err}</span>}
        <button
          onClick={() => setShowForm((v) => !v)}
          className="ml-auto px-3 py-1.5 text-sm border border-accent rounded text-accent hover:text-text flex items-center gap-1.5"
        >
          {!showForm && <Plus size={14} aria-hidden="true" />}
          {showForm ? "Cancel" : "New objective"}
        </button>
      </div>

      {showForm && (
        <section className="border-b border-border px-4 py-4">
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))" }}>
            <label className={labelCls}>Objective name<input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></label>
            <label className={labelCls}>Target name<input className={inputCls} value={targetName} onChange={(e) => setTargetName(e.target.value)} /></label>
            <label className={labelCls}>Month<input type="month" className={inputCls} value={month} onChange={(e) => setMonth(e.target.value)} /></label>
            <label className={labelCls}>Aggregation<select className={inputCls} value={aggregation} onChange={(e) => setAggregation(e.target.value as ObjectiveMetricQuery["aggregation"])}><option value="count">Count</option><option value="sum">Sum</option><option value="distinct">Distinct</option></select></label>
            <label className={labelCls}>App<input list="objective-apps" className={inputCls} value={app} onChange={(e) => setApp(e.target.value)} /><datalist id="objective-apps">{dims.apps.map((v) => <option key={v} value={v} />)}</datalist></label>
            <label className={labelCls}>Event topic<input list="objective-topics" className={inputCls} value={topic} onChange={(e) => setTopic(e.target.value)} /><datalist id="objective-topics">{dims.topics.map((v) => <option key={v} value={v} />)}</datalist></label>
            {aggregation === "sum" && <label className={labelCls}>Value field<input className={inputCls} value={value} onChange={(e) => setValue(e.target.value)} /></label>}
            {aggregation === "distinct" && <label className={labelCls}>Distinct field<input className={inputCls} value={by} onChange={(e) => setBy(e.target.value)} /></label>}
            <label className={labelCls}>Target value<input type="number" step="any" className={inputCls} value={targetValue} onChange={(e) => setTargetValue(e.target.value)} /></label>
            <label className={labelCls}>Direction<select className={inputCls} value={direction} onChange={(e) => setDirection(e.target.value as ObjectiveTarget["direction"])}><option value="at_least">At least</option><option value="at_most">At most</option></select></label>
            <label className={labelCls}>Unit<select className={inputCls} value={unit} onChange={(e) => setUnit(e.target.value as ObjectiveTarget["unit"])}><option value="count">Count</option><option value="money">Money</option><option value="percent">Percent</option><option value="number">Number</option></select></label>
            {unit === "money" && <label className={labelCls}>Currency<input className={inputCls} maxLength={3} value={currency} onChange={(e) => setCurrency(e.target.value)} /></label>}
            <label className={labelCls}>Property filter<input className={inputCls} placeholder="props.site" value={whereKey} onChange={(e) => setWhereKey(e.target.value)} /></label>
            <label className={labelCls}>Filter value<input className={inputCls} value={whereValue} onChange={(e) => setWhereValue(e.target.value)} /></label>
          </div>
          <label className={`${labelCls} mt-3`}>Description<textarea className={inputCls} rows={2} value={description} onChange={(e) => setDescription(e.target.value)} /></label>
          <div className="flex justify-end mt-3">
            <button disabled={busy} onClick={create} className="px-4 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50">{busy ? "Creating…" : "Create objective"}</button>
          </div>
        </section>
      )}

      <main className="px-4 py-4 flex flex-col gap-3">
        {!objectives.length ? <Empty label="No objectives yet." /> : objectives.map((objective) => (
          <section key={objective.id} className="border border-border rounded overflow-hidden">
            <div className="flex items-center gap-3 px-3 py-2 border-b border-border">
              <div className="min-w-0">
                <div className="text-text font-medium truncate">{objective.name}</div>
                {objective.description && <div className="text-text-dim text-xs truncate">{objective.description}</div>}
              </div>
              <span className="text-text-dim text-xs uppercase">{objective.status}</span>
              <button aria-label="Refresh progress" title="Refresh progress" disabled={refreshingId === objective.id} onClick={() => refreshObjective(objective.id)} className="ml-auto w-8 h-8 inline-flex items-center justify-center border border-border rounded text-text-muted hover:text-text disabled:opacity-50"><RefreshCw size={14} className={refreshingId === objective.id ? "animate-spin" : ""} /></button>
              <button aria-label="Archive objective" title="Archive objective" onClick={() => archive(objective.id)} className="w-8 h-8 inline-flex items-center justify-center border border-border rounded text-text-muted hover:text-error"><Archive size={14} /></button>
            </div>
            {objective.targets.map((target) => {
              const p = target.last_progress;
              const actual = p?.actual_value;
              const width = objectiveProgressWidth(p?.progress_percent);
              return (
                <div key={target.id} className="px-3 py-3 border-t first:border-t-0 border-border">
                  <div className="flex items-start gap-4 flex-wrap">
                    <div className="flex-1" style={{ minWidth: "220px" }}>
                      <div className="flex items-center gap-2">
                        <span className="text-text text-sm font-medium">{target.name}</span>
                        {p?.achieved && <span className="text-success text-xs">Achieved</span>}
                        {p?.status === "error" && <span className="text-error text-xs">Stale</span>}
                      </div>
                      <div className="text-text-dim text-xs mt-0.5">
                        {[target.query.app, target.query.topic, target.query.value || target.query.by || target.query.aggregation].filter(Boolean).join(" · ")}
                      </div>
                    </div>
                    <div className="text-right tabular-nums">
                      <div className="text-text font-semibold">{actual == null ? "—" : formatObjectiveValue(actual, target.unit, target.currency)}</div>
                      <div className="text-text-dim text-xs">{target.direction === "at_least" ? "of" : "limit"} {formatObjectiveValue(target.target_value, target.unit, target.currency)}</div>
                    </div>
                  </div>
                  <div className="bg-bg-input rounded overflow-hidden mt-2" style={{ height: "6px" }}><div className={p?.status === "error" ? "h-full bg-error" : p?.achieved ? "h-full bg-success" : "h-full bg-accent"} style={{ width: `${width}%` }} /></div>
                  <div className="flex items-center justify-between text-text-dim text-xs mt-1">
                    <span>{formatObjectivePeriod(target.period_start, target.period_end, target.timezone)}</span>
                    <span>{p?.error || (p?.measured_at ? `Updated ${relTime(p.measured_at)}` : "Not measured")}</span>
                  </div>
                </div>
              );
            })}
          </section>
        ))}
      </main>
    </div>
  );
}

// TrackingTab — mint/copy public write keys and the static-site snippet.
// All routes are authenticated (dashboard session cookie travels with
// credentials: same-origin). The snippet points at this server's origin.
function TrackingTab({ projectId }: { projectId: string }) {
  const [keys, setKeys] = useState<WriteKey[]>([]);
  const [site, setSite] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/keys?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (r.ok) setKeys((await r.json()).keys ?? []);
      else setErr(`keys ${r.status}`);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  const create = async () => {
    const s = site.trim();
    if (!s) return;
    setBusy(true);
    setErr("");
    try {
	  const r = await fetch(scopedAppURL(`${API}/keys`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ site: s, project_id: projectId }),
      });
      if (r.ok) {
        setSite("");
        load();
      } else {
        setErr("create: " + (await r.text()));
      }
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (id: number) => {
    if (!confirm("Revoke this key? Sites using it stop tracking immediately.")) return;
    try {
	  await fetch(scopedAppURL(`${API}/keys/revoke`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id, project_id: projectId }),
      });
      load();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const active = keys.filter((k) => !k.revoked_at);
  const origin = typeof window !== "undefined" ? window.location.origin : "";
  const tagURL = `${origin}/api/apps/analytics/ui/tag.js`;
  const featured = active[0];
  const snippet = featured
    ? `<script async src="${tagURL}" data-key="${featured.key}"></script>`
    : "";

  return (
    <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
      {err && <div className="text-error text-xs">{err}</div>}

      <section className="border border-border rounded p-4 flex flex-col gap-3">
        <div className="text-text-dim text-xs uppercase">Install on your site</div>
        {featured ? (
          <>
            <div className="text-text-muted text-sm">
              Paste this once in your site's <code>&lt;head&gt;</code>. It tracks page views
              automatically; call <code>apa("event_name", {`{ ...props }`})</code> for custom events.
            </div>
            <div className="flex items-start gap-2">
              <pre
                className="flex-1 bg-bg-input border border-border rounded p-2 text-xs overflow-auto"
                style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}
              >
                {snippet}
              </pre>
              <CopyButton text={snippet} label="Copy snippet" />
            </div>
            <div className="flex items-center gap-2 text-xs">
              <span className="text-text-dim" style={{ width: "72px" }}>Write key</span>
              <code className="text-text truncate flex-1">{featured.key}</code>
              <CopyButton text={featured.key} label="Copy key" />
            </div>
            <div className="flex items-center gap-2 text-xs">
              <span className="text-text-dim" style={{ width: "72px" }}>Script URL</span>
              <code className="text-text-muted truncate flex-1">{tagURL}</code>
              <CopyButton text={tagURL} label="Copy URL" />
            </div>
          </>
        ) : (
          <Empty label="Create a write key below to get your tracking snippet." />
        )}
      </section>

      <section className="border border-border rounded p-4 flex flex-col gap-3">
        <div className="text-text-dim text-xs uppercase">Write keys</div>
			<div className="flex items-center gap-2 flex-wrap">
          <input
            type="text"
            value={site}
            onChange={(e) => setSite(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") create();
            }}
            placeholder="site name (e.g. marketing-site)"
            spellCheck={false}
            className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          />
          <button
            onClick={create}
            disabled={busy || !site.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            Create key
          </button>
        </div>
        {keys.length ? (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-dim text-xs uppercase">
                <th className="text-left font-normal py-1">Site</th>
                <th className="text-left font-normal py-1">Key</th>
                <th className="text-right font-normal py-1">Events</th>
                <th className="text-right font-normal py-1">Last used</th>
                <th className="py-1"></th>
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr
                  key={k.id}
                  className="border-t border-border"
                  style={{ opacity: k.revoked_at ? 0.5 : 1 }}
                >
                  <td className="py-1.5 text-text">{k.site}</td>
                  <td className="py-1.5">
                    <div className="flex items-center gap-1.5">
                      <code className="text-text-muted truncate" style={{ maxWidth: "190px" }}>
                        {k.key}
                      </code>
                      {!k.revoked_at && <CopyButton text={k.key} />}
                    </div>
                  </td>
                  <td className="py-1.5 text-right tabular-nums text-text">{fmt(k.event_count)}</td>
                  <td className="py-1.5 text-right text-text-dim">
                    {k.last_used_ts ? relTime(k.last_used_ts) : "—"}
                  </td>
                  <td className="py-1.5 text-right">
                    {k.revoked_at ? (
                      <span className="text-text-dim text-xs">revoked</span>
                    ) : (
                      <button
                        onClick={() => revoke(k.id)}
                        className="text-xs text-text-muted hover:text-error"
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <Empty label="No write keys yet." />
        )}
      </section>
    </div>
  );
}

// CaptureTab — toggle + tune the bus auto-capture subscriber. Records
// what every app already emits on the platform bus, no per-app dependency.
function CaptureTab({ projectId }: { projectId: string }) {
  const [enabled, setEnabled] = useState(false);
  const [mode, setMode] = useState("denylist");
  const [patterns, setPatterns] = useState<string[]>([]);
  const [catalog, setCatalog] = useState<{ app: string; events: EventDecl[] }[]>([]);
  const [sampleRate, setSampleRate] = useState(1);
  const [captured, setCaptured] = useState(0);
  const [status, setStatus] = useState("");

  const togglePattern = (name: string) =>
    setPatterns((ps) => (ps.includes(name) ? ps.filter((p) => p !== name) : [...ps, name]));

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/capture?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (!r.ok) {
        setStatus(`load ${r.status}`);
        return;
      }
      const d = await r.json();
      setEnabled(!!d.enabled);
      setMode(d.mode || "denylist");
      setPatterns(Array.isArray(d.topic_patterns) ? d.topic_patterns : []);
      setSampleRate(typeof d.sample_rate === "number" ? d.sample_rate : 1);
      setCaptured(d.captured ?? 0);
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  // Event catalog for the checklist: every installed app's declared
  // `publishes` topics (GET /api/apps — the platform list, not this app).
  useEffect(() => {
    fetch(`/api/apps?project_id=${encodeURIComponent(projectId)}`, { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : []))
      .then((j) => {
        const rows = Array.isArray(j) ? j : j.apps ?? [];
        setCatalog(
          rows
            .filter((a: any) => a.name !== "analytics" && Array.isArray(a.publishes) && a.publishes.length)
            .map((a: any) => ({ app: a.name, events: a.publishes as EventDecl[] })),
        );
      })
      .catch(() => {});
  }, [projectId]);

  const save = async () => {
    setStatus("saving…");
    const topic_patterns = patterns;
    try {
      const r = await fetch(`${API}/capture`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled, mode, topic_patterns, sample_rate: sampleRate }),
      });
      setStatus(r.ok ? "saved" : "save: " + (await r.text()));
      if (r.ok) load();
    } catch (e) {
      setStatus((e as Error).message);
    }
  };

  return (
    <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
      <section className="border border-border rounded p-4 flex flex-col gap-3">
        <div className="flex items-center gap-3">
          <div className="text-text-dim text-xs uppercase">Bus auto-capture</div>
          <button
            onClick={() => setEnabled((v) => !v)}
            className={
              "px-2 py-1 text-xs rounded border " +
              (enabled ? "border-success text-success" : "border-border text-text-muted hover:text-text")
            }
          >
            {enabled ? "Enabled" : "Disabled"}
          </button>
          <span className="ml-auto text-text-dim text-xs">{captured.toLocaleString()} captured</span>
        </div>
        <div className="text-text-muted text-sm">
          Records what other apps already emit on the platform bus (e.g.{" "}
          <code>campaign.sent</code>, <code>file.deleted</code>) — no per-app dependency. Off by
          default. Recorded events show up with <code>source=bus</code>.
        </div>

        <div className="flex items-center gap-2">
          <span className="text-text-dim text-xs" style={{ width: "72px" }}>Mode</span>
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value)}
            className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
          >
            <option value="all">All topics</option>
            <option value="denylist">Everything except…</option>
            <option value="allowlist">Only…</option>
          </select>
        </div>

        {mode !== "all" && (
          <>
            {catalog.length > 0 && (
              <div className="flex flex-col gap-1">
                <span className="text-text-dim text-xs">
                  {mode === "denylist" ? "Check events to exclude" : "Check events to capture"}
                </span>
                <div
                  className="border border-border rounded p-2 flex flex-col gap-0.5"
                  style={{ maxHeight: "260px", overflow: "auto" }}
                >
                  {catalog.map((a) => (
                    <div key={a.app} className="flex flex-col">
                      <div className="text-text-dim text-xs uppercase mt-1.5">{a.app}</div>
                      {a.events.map((ev) => (
                        <label
                          key={ev.name}
                          className="flex items-center gap-2 text-xs py-0.5"
                          style={{ cursor: "pointer" }}
                          title={ev.description || ""}
                        >
                          <input
                            type="checkbox"
                            checked={patterns.includes(ev.name)}
                            onChange={() => togglePattern(ev.name)}
                          />
                          <code className="text-text">{ev.name}</code>
                          {ev.description && (
                            <span className="text-text-dim truncate">{ev.description}</span>
                          )}
                        </label>
                      ))}
                    </div>
                  ))}
                </div>
              </div>
            )}
            <div className="flex flex-col gap-1">
              <span className="text-text-dim text-xs">
                Patterns (advanced) — one per line; exact (<code>file.deleted</code>), prefix
                (<code>campaign.*</code>), or <code>*</code>. For topics apps don't declare.
              </span>
              <textarea
                value={patterns.join("\n")}
                onChange={(e) =>
                  setPatterns(
                    e.target.value
                      .split(/[\n,]/)
                      .map((s) => s.trim())
                      .filter(Boolean),
                  )
                }
                spellCheck={false}
                className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
                style={{ minHeight: "70px", fontFamily: "monospace" }}
              />
            </div>
          </>
        )}

        <div className="flex items-center gap-2">
          <span className="text-text-dim text-xs" style={{ width: "72px" }}>Sample</span>
          <input
            type="number"
            min={0}
            max={1}
            step={0.05}
            value={sampleRate}
            onChange={(e) => setSampleRate(Math.max(0, Math.min(1, parseFloat(e.target.value) || 0)))}
            className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
            style={{ width: "90px" }}
          />
          <span className="text-text-dim text-xs">fraction kept (1 = all)</span>
        </div>

        <div className="flex items-center gap-2 justify-end">
          <span className="text-text-dim text-xs mr-auto">{status}</span>
          <button
            onClick={save}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold"
          >
            Save
          </button>
        </div>
      </section>
    </div>
  );
}

function widgetPreset(type: DashboardWidget["type"], position: number): Partial<DashboardWidget> {
  switch (type) {
    case "stat":
      return {
        type,
        title: "Page Views",
        position,
        config: { topic: "page_view", window: "24h", aggregation: "count", format: "number" },
      };
    case "timeseries":
      return {
        type,
        title: "Live Page Views",
        position,
        config: { topic: "page_view", window: "30m", interval: "minute", aggregation: "count", format: "number" },
      };
    case "top":
      return {
        type,
        title: "Top Pages",
        position,
        config: { topic: "page_view", window: "24h", by: "props.path", limit: 8 },
      };
    case "breakdown":
      return {
        type,
        title: "Devices",
        position,
        config: { topic: "page_view", window: "24h", by: "props.device", limit: 5 },
      };
    default:
      return {
        type: "feed",
        title: "Live Activity",
        position,
        config: { topic: "page_view", window: "30m", limit: 20 },
      };
  }
}

function WidgetView({ widget, data, filters }: { widget: DashboardWidget; data: any; filters: Record<string, string> }) {
	const metricConfig = resolveMetricConfig(widget.config, filters);
  const body = !data ? (
    <Empty label="Loading widget…" />
  ) : data.error ? (
    <div className="border border-error text-error rounded p-3 text-sm" role="alert">{String(data.error)}</div>
  ) : widget.type === "stat" ? (
    <div className="text-text font-semibold tabular-nums" style={{ fontSize: "34px", lineHeight: 1 }}>
      {formatMetric(data.value ?? 0, metricConfig)}
    </div>
  ) : widget.type === "timeseries" ? (
    <TrendSeries config={metricConfig} data={(data.series ?? []).map((p: any) => ({ day: p.bucket, count: p.count, value: p.value }))} />
  ) : widget.type === "feed" ? (
    <EventFeed rows={data.events ?? []} />
  ) : (
    <TopBars rows={data.top ?? []} />
  );
  return (
    <section className="border border-border rounded p-4 flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <div className="text-text-dim text-xs uppercase truncate">{widget.title}</div>
        <span className="ml-auto text-text-dim text-xs">{resolvedWindow(widget.config, filters)}</span>
      </div>
      {body}
    </section>
  );
}

function filterList(dashboard: Dashboard | null): DashboardFilter[] {
  return Array.isArray(dashboard?.config?.filters) ? dashboard.config!.filters! : [];
}

function initialFilterValues(filters: DashboardFilter[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const filter of filters) {
    out[filter.key] = filter.default || (filter.type === "date_window" ? "90d" : "all");
  }
  return out;
}

function staticFilterOptions(filter: DashboardFilter): Array<{ value: string; label: string }> {
  if (filter.type === "date_window") {
    return [
      { value: "7d", label: "7d" },
      { value: "30d", label: "30d" },
      { value: "90d", label: "90d" },
      { value: "all", label: "All" },
    ];
  }
  if (!Array.isArray(filter.options)) return [];
  return filter.options.map((option) =>
    typeof option === "string" ? { value: option, label: option } : { value: option.value, label: option.label || option.value },
  );
}

function DashboardsTab({ projectId }: { projectId: string }) {
  const [dashboards, setDashboards] = useState<Dashboard[]>([]);
  const [selectedId, setSelectedId] = useState(0);
  const [selected, setSelected] = useState<Dashboard | null>(null);
  const [widgetData, setWidgetData] = useState<Record<number, any>>({});
  const [filterValues, setFilterValues] = useState<Record<string, string>>({});
  const [filterOptions, setFilterOptions] = useState<Record<string, Array<{ value: string; label: string; count?: number }>>>({});
  const [status, setStatus] = useState("");
  const reloadTimer = useRef<number | null>(null);
  const dashboardRequestRef = useRef(0);
  const widgetRequestRef = useRef(0);
  const filterRequestRef = useRef(0);
  const scopedURL = useCallback(
	(path: string) => scopedAppURL(path, projectId),
    [projectId],
  );

  const loadList = useCallback(async () => {
    try {
      const r = await fetch(scopedURL(`${API}/dashboards`), {
        credentials: "same-origin",
      });
      if (!r.ok) {
        setStatus(`dashboards ${r.status}`);
        return;
      }
      const rows = (await r.json()).dashboards ?? [];
      setDashboards(rows);
      if (!selectedId && rows[0]) setSelectedId(rows[0].id);
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [scopedURL, selectedId]);

  const loadDashboard = useCallback(async (id: number) => {
	const sequence = ++dashboardRequestRef.current;
    if (!id) {
      setSelected(null);
	  setWidgetData({});
      return;
    }
    try {
      const r = await fetch(scopedURL(`${API}/dashboards/${id}`), { credentials: "same-origin" });
      if (!r.ok) {
		if (isCurrentRequest(sequence, dashboardRequestRef.current)) setStatus(`dashboard ${r.status}`);
        return;
      }
      const d = await r.json();
	  if (!isCurrentRequest(sequence, dashboardRequestRef.current)) return;
      setSelected(d);
	  setWidgetData({});
      setFilterValues(initialFilterValues(filterList(d)));
    } catch (e) {
	  if (isCurrentRequest(sequence, dashboardRequestRef.current)) setStatus((e as Error).message);
    }
	}, [scopedURL]);

  const loadFilterOptions = useCallback(async (d: Dashboard | null) => {
	const sequence = ++filterRequestRef.current;
    const filters = filterList(d);
    if (!filters.length) {
      setFilterOptions({});
      return;
    }
    const pairs = await Promise.all(filters.map(async (filter) => {
      const staticOptions = staticFilterOptions(filter);
      if (staticOptions.length) return [filter.key, staticOptions] as const;
      if (!filter.source) return [filter.key, []] as const;
	  const filterURL = `${API}/dashboard-filter-options?filter=${encodeURIComponent(JSON.stringify(filter))}`;
	  const r = await fetch(scopedURL(filterURL), { credentials: "same-origin" });
      if (!r.ok) return [filter.key, []] as const;
      const rows = (await r.json()).options ?? [];
      return [filter.key, rows] as const;
    }));
	if (isCurrentRequest(sequence, filterRequestRef.current)) setFilterOptions(Object.fromEntries(pairs));
	}, [projectId, scopedURL]);

  const refreshWidgets = useCallback(async (d: Dashboard | null, filters: Record<string, string>) => {
    if (!d?.widgets?.length) return;
	const sequence = ++widgetRequestRef.current;
    setStatus("refreshing…");
    try {
	  const queryURL = `${API}/query-dashboard?dashboard_id=${d.id}&filters=${encodeURIComponent(JSON.stringify(filters))}`;
	  const r = await fetch(scopedURL(queryURL), { credentials: "same-origin" });
	  if (!r.ok) throw new Error(await r.text());
	  const payload = await r.json();
	  if (!isCurrentRequest(sequence, widgetRequestRef.current)) return;
	  setWidgetData(batchResultsByID(Array.isArray(payload.widgets) ? payload.widgets : []));
      setStatus("");
    } catch (e) {
	  if (isCurrentRequest(sequence, widgetRequestRef.current)) setStatus((e as Error).message);
    }
	}, [projectId, scopedURL]);

  useEffect(() => {
    loadList();
  }, [loadList]);

  useEffect(() => {
    loadDashboard(selectedId);
  }, [selectedId, loadDashboard]);

  useEffect(() => {
    loadFilterOptions(selected);
  }, [selected, loadFilterOptions]);

  useEffect(() => {
    refreshWidgets(selected, filterValues);
  }, [filterValues, selected, refreshWidgets]);

  useAppEvents("analytics", projectId, () => {
    if (reloadTimer.current) window.clearTimeout(reloadTimer.current);
    reloadTimer.current = window.setTimeout(() => refreshWidgets(selected, filterValues), 1000);
  });

  const createTemplate = async (template: "website_traffic" | "patreon_overview") => {
    setStatus("creating…");
	const r = await fetch(scopedURL(`${API}/dashboards`), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ project_id: projectId, template }),
    });
    if (r.ok) {
      const d = await r.json();
      setSelectedId(d.id);
      setSelected(d);
      setFilterValues(initialFilterValues(filterList(d)));
      await loadList();
      setStatus("");
    } else {
      setStatus(await r.text());
    }
  };

  const addWidget = async (type: DashboardWidget["type"]) => {
    if (!selected) return;
    const preset = widgetPreset(type, selected.widgets?.length ?? 0);
	const r = await fetch(scopedURL(`${API}/dashboards/${selected.id}/widgets`), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(preset),
    });
    if (r.ok) loadDashboard(selected.id);
    else setStatus(await r.text());
  };

	const { stats: statWidgets, charts: chartWidgets } = partitionDashboardWidgets(selected?.widgets ?? []);

	return (
	  <div className="analytics-dashboard-shell flex-1 min-h-0 flex">
		<style>{`
		  .analytics-dashboard-sidebar { width: 230px; }
		  .analytics-stat-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
		  .analytics-chart-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
		  @media (max-width: 760px) {
			.analytics-dashboard-shell { flex-direction: column; }
			.analytics-dashboard-sidebar { width: 100%; max-height: 180px; border-right: 0; border-bottom-width: 1px; border-bottom-style: solid; }
			.analytics-stat-grid, .analytics-chart-grid { grid-template-columns: minmax(0, 1fr); }
		  }
		`}</style>
		<aside className="analytics-dashboard-sidebar border-r border-border p-3 flex flex-col gap-2">
        <button
          onClick={() => createTemplate("website_traffic")}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold"
        >
          Website Traffic
        </button>
        <button
          onClick={() => createTemplate("patreon_overview")}
          className="px-3 py-1.5 text-sm border border-accent text-accent rounded font-bold"
        >
          Patreon Overview
        </button>
        <div className="text-text-dim text-xs uppercase mt-2">Dashboards</div>
        <div className="flex flex-col gap-1 overflow-auto">
          {dashboards.map((d) => (
            <button
              key={d.id}
              onClick={() => setSelectedId(d.id)}
              className={
                "text-left px-2 py-1.5 text-sm rounded border truncate " +
                (selectedId === d.id
                  ? "border-accent text-accent"
                  : "border-border text-text-muted hover:text-text")
              }
            >
              {d.name}
            </button>
          ))}
        </div>
      </aside>
	  <main className="flex-1 min-w-0 overflow-auto p-4 flex flex-col gap-4">
        {!selected ? (
          <section className="border border-border rounded p-6">
            <div className="text-text font-medium">Create a live analytics dashboard</div>
            <div className="text-text-muted text-sm mt-1">
              Start with Website Traffic to visualize page views, active sessions, top pages,
              devices, and live activity from the tracking tag.
            </div>
          </section>
        ) : (
          <>
			<div className="flex items-center gap-2 flex-wrap">
			  <div>
				<div className="text-text font-medium">{selected.name}</div>
                <div className="text-text-dim text-xs">
                  {selected.widgets?.length ?? 0} widgets · live refresh
                </div>
              </div>
              <button
                onClick={() => refreshWidgets(selected, filterValues)}
                className="ml-auto px-3 py-1 text-sm border border-border rounded text-text-muted hover:text-text"
              >
                Refresh
              </button>
              <select
                onChange={(e) => {
                  if (e.target.value) addWidget(e.target.value as DashboardWidget["type"]);
                  e.currentTarget.value = "";
                }}
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
                defaultValue=""
              >
                <option value="" disabled>Add widget</option>
                <option value="stat">Stat</option>
                <option value="timeseries">Timeseries</option>
                <option value="top">Top values</option>
                <option value="breakdown">Breakdown</option>
                <option value="feed">Feed</option>
              </select>
            </div>
            {filterList(selected).length > 0 && (
              <div className="flex items-center gap-2 flex-wrap border border-border rounded p-2">
                {filterList(selected).map((filter) => {
                  const options = filterOptions[filter.key] ?? [];
                  const value = filterValues[filter.key] ?? filter.default ?? "all";
                  return (
                    <label key={filter.key} className="flex items-center gap-1.5 text-xs">
                      <span className="text-text-dim">{filter.label}</span>
                      <select
                        value={value}
                        onChange={(e) => setFilterValues((current) => ({ ...current, [filter.key]: e.target.value }))}
                        className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
                      >
                        {filter.type !== "date_window" && <option value="all">All</option>}
                        {options.map((option) => (
                          <option key={option.value} value={option.value}>
                            {option.label || option.value}
                          </option>
                        ))}
                      </select>
                    </label>
                  );
                })}
              </div>
            )}
            {status && <div className="text-text-dim text-xs">{status}</div>}
			{statWidgets.length > 0 && <div className="analytics-stat-grid grid gap-4">
			  {statWidgets.map((widget) => (
				<WidgetView key={widget.id} widget={widget} data={widgetData[widget.id]} filters={filterValues} />
			  ))}
			</div>}
			<div className="analytics-chart-grid grid gap-4">
			  {chartWidgets.map((widget) => (
				<WidgetView key={widget.id} widget={widget} data={widgetData[widget.id]} filters={filterValues} />
			  ))}
			</div>
          </>
        )}
      </main>
    </div>
  );
}

const DEFAULT_SPEC: EventSpec = {
  project_id: "",
  app: "patreon",
  topic: "post_views_daily_observed",
  kind: "aggregate_observation",
  display_name: "Post Views Daily Observed",
  description: "Imported daily post view count.",
  category: "content",
  status: "active",
  validation_mode: "warn",
  ingest_mode: "upsert",
	upsert_policy: {
	  bucket: "day",
	  timezone: "UTC",
	  timestamp_property: "props.date",
    operation: "replace",
    value: "props.views",
    output_property: "views",
    dimensions: ["props.post_id"],
  },
  properties: [
    { key: "props.creator_id", type: "string", required: true, pii_classification: "identifier" },
    { key: "props.post_id", type: "string", required: true, pii_classification: "identifier" },
    { key: "props.date", type: "string", required: true, example_value: "2026-06-16" },
    { key: "props.views", type: "number", required: true, example_value: "150" },
  ],
};

const PATREON_TEMPLATE: EventSpec[] = [
  {
    project_id: "",
    app: "patreon",
    topic: "post_view",
    kind: "occurrence",
    display_name: "Post View",
    description: "One post view occurred.",
    category: "content",
    status: "active",
    validation_mode: "warn",
    ingest_mode: "raw_plus_rollup",
    rollup_policy: {
      target_topic: "post_view_daily_rollup",
      bucket: "day",
      timezone: "UTC",
      operation: "increment",
      value: 1,
      output_property: "views",
      dimensions: ["props.post_id"],
    },
    properties: [
      { key: "props.creator_id", type: "string", required: true, pii_classification: "identifier" },
      { key: "props.post_id", type: "string", required: true, pii_classification: "identifier" },
      { key: "props.post_type", type: "string", enum_values: ["text", "image", "video", "audio"] },
      { key: "session_id", type: "string", required: true },
    ],
  },
  DEFAULT_SPEC,
  {
    project_id: "",
    app: "patreon",
    topic: "active_members_daily_observed",
    kind: "aggregate_observation",
    display_name: "Active Members Daily Observed",
    description: "Imported active member count.",
    category: "membership",
    status: "active",
    validation_mode: "warn",
    ingest_mode: "upsert",
	upsert_policy: {
	  bucket: "day",
	  timezone: "UTC",
	  timestamp_property: "props.date",
      operation: "replace",
      value: "props.members",
      output_property: "members",
      dimensions: ["props.creator_id"],
    },
    properties: [
      { key: "props.creator_id", type: "string", required: true, pii_classification: "identifier" },
      { key: "props.date", type: "string", required: true, example_value: "2026-06-16" },
      { key: "props.members", type: "number", required: true, example_value: "842" },
    ],
  },
];

function CatalogTab({ projectId }: { projectId: string }) {
  const [specs, setSpecs] = useState<EventSpec[]>([]);
  const [selected, setSelected] = useState<EventSpec | null>(null);
  const [propsText, setPropsText] = useState("[]");
  const [upsertPolicyText, setUpsertPolicyText] = useState("{}");
  const [rollupPolicyText, setRollupPolicyText] = useState("{}");
  const [sampleText, setSampleText] = useState(`{
  "props": {
    "creator_id": "creator_42",
    "post_id": "post_999",
    "date": "2026-06-16",
    "views": 150
  }
}`);
  const [validation, setValidation] = useState("");
  const [violations, setViolations] = useState<EventViolation[]>([]);
  const [status, setStatus] = useState("");

  const load = useCallback(async () => {
    const r = await fetch(`${API}/event-specs?project_id=${encodeURIComponent(projectId)}`, {
      credentials: "same-origin",
    });
    if (!r.ok) {
      setStatus(`specs ${r.status}`);
      return;
    }
    const rows = (await r.json()).specs ?? [];
    setSpecs(rows);
    if (!selected && rows[0]) {
      setSelected(rows[0]);
      setPropsText(JSON.stringify(rows[0].properties ?? [], null, 2));
      setUpsertPolicyText(JSON.stringify(rows[0].upsert_policy ?? {}, null, 2));
      setRollupPolicyText(JSON.stringify(rows[0].rollup_policy ?? {}, null, 2));
    }
  }, [projectId, selected]);

  const loadViolations = useCallback(async () => {
    const r = await fetch(`${API}/event-spec-violations?project_id=${encodeURIComponent(projectId)}&limit=20`, {
      credentials: "same-origin",
    });
    if (r.ok) setViolations((await r.json()).violations ?? []);
  }, [projectId]);

  useEffect(() => {
    load();
    loadViolations();
  }, [load, loadViolations]);

  const selectSpec = (spec: EventSpec) => {
    setSelected(spec);
    setPropsText(JSON.stringify(spec.properties ?? [], null, 2));
    setUpsertPolicyText(JSON.stringify(spec.upsert_policy ?? {}, null, 2));
    setRollupPolicyText(JSON.stringify(spec.rollup_policy ?? {}, null, 2));
    setValidation("");
  };

  const newSpec = () => {
    const spec = { ...DEFAULT_SPEC, project_id: projectId, id: undefined };
    setSelected(spec);
    setPropsText(JSON.stringify(spec.properties, null, 2));
    setUpsertPolicyText(JSON.stringify(spec.upsert_policy ?? {}, null, 2));
    setRollupPolicyText(JSON.stringify(spec.rollup_policy ?? {}, null, 2));
  };

  const save = async () => {
    if (!selected) return;
    let properties: EventPropertySpec[];
    try {
      properties = JSON.parse(propsText);
    } catch (e) {
      setStatus("properties json: " + (e as Error).message);
      return;
    }
    let upsertPolicy: EventIngestPolicy | undefined;
    let rollupPolicy: EventIngestPolicy | undefined;
    try {
      const parsed = JSON.parse(upsertPolicyText || "{}");
      upsertPolicy = Object.keys(parsed).length ? parsed : undefined;
    } catch (e) {
      setStatus("upsert policy json: " + (e as Error).message);
      return;
    }
    try {
      const parsed = JSON.parse(rollupPolicyText || "{}");
      rollupPolicy = Object.keys(parsed).length ? parsed : undefined;
    } catch (e) {
      setStatus("rollup policy json: " + (e as Error).message);
      return;
    }
    const body = { ...selected, project_id: projectId, properties, upsert_policy: upsertPolicy, rollup_policy: rollupPolicy };
	const baseURL = selected.id ? `${API}/event-specs/${selected.id}` : `${API}/event-specs`;
	const url = scopedAppURL(baseURL, projectId);
    const r = await fetch(url, {
      method: selected.id ? "PUT" : "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      setStatus(await r.text());
      return;
    }
    const saved = await r.json();
    setSelected(saved);
    setPropsText(JSON.stringify(saved.properties ?? [], null, 2));
    setUpsertPolicyText(JSON.stringify(saved.upsert_policy ?? {}, null, 2));
    setRollupPolicyText(JSON.stringify(saved.rollup_policy ?? {}, null, 2));
    setStatus("saved");
    await load();
  };

  const installPatreon = async () => {
    setStatus("creating…");
    for (const spec of PATREON_TEMPLATE) {
	  const r = await fetch(scopedAppURL(`${API}/event-specs`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...spec, project_id: projectId }),
      });
      if (!r.ok) {
        setStatus(await r.text());
        return;
      }
    }
    setStatus("template created");
    setSelected(null);
    await load();
  };

  const validate = async () => {
    if (!selected) return;
    let sample: any;
    try {
      sample = JSON.parse(sampleText);
    } catch (e) {
      setValidation("sample json: " + (e as Error).message);
      return;
    }
	const r = await fetch(scopedAppURL(`${API}/event-specs/validate`, projectId), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        app: selected.app,
        topic: selected.topic,
        project_id: projectId,
        ...sample,
      }),
    });
    setValidation(r.ok ? JSON.stringify(await r.json(), null, 2) : await r.text());
  };

  return (
    <div className="flex-1 min-h-0 flex">
      <aside className="border-r border-border p-3 flex flex-col gap-2" style={{ width: "250px" }}>
        <div className="flex items-center gap-2">
          <button onClick={newSpec} className="px-2 py-1 text-xs rounded border border-border text-text-muted hover:text-text">
            New
          </button>
          <button onClick={installPatreon} className="px-2 py-1 text-xs rounded border border-accent text-accent">
            Patreon
          </button>
        </div>
        <div className="text-text-dim text-xs uppercase mt-2">Sources</div>
        <div className="flex flex-col gap-1 overflow-auto">
          {specs.map((spec) => (
            <button
              key={spec.id}
              onClick={() => selectSpec(spec)}
              className={
                "text-left px-2 py-1.5 text-xs rounded border " +
                (selected?.id === spec.id
                  ? "border-accent text-accent"
                  : "border-border text-text-muted hover:text-text")
              }
              >
                <div className="truncate text-text">{spec.app}.{spec.topic}</div>
                <div className="text-text-dim truncate">{spec.kind} · {spec.ingest_mode ?? "raw"} · {spec.validation_mode}</div>
              </button>
          ))}
        </div>
      </aside>
      <main className="flex-1 overflow-auto p-4 flex flex-col gap-4">
        {!selected ? (
          <section className="border border-border rounded p-6">
            <div className="text-text font-medium">Event Catalog</div>
          </section>
        ) : (
          <>
            <section className="border border-border rounded p-4 grid gap-3" style={{ gridTemplateColumns: "repeat(2, minmax(0, 1fr))" }}>
              <input value={selected.app} onChange={(e) => setSelected({ ...selected, app: e.target.value })} placeholder="source" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
              <input value={selected.topic} onChange={(e) => setSelected({ ...selected, topic: e.target.value })} placeholder="event" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
              <input value={selected.display_name} onChange={(e) => setSelected({ ...selected, display_name: e.target.value })} placeholder="display name" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
              <input value={selected.category} onChange={(e) => setSelected({ ...selected, category: e.target.value })} placeholder="category" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
              <select value={selected.kind} onChange={(e) => setSelected({ ...selected, kind: e.target.value as EventSpec["kind"] })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                <option value="occurrence">Occurrence</option>
                <option value="aggregate_observation">Aggregate observation</option>
              </select>
              <select value={selected.validation_mode} onChange={(e) => setSelected({ ...selected, validation_mode: e.target.value as EventSpec["validation_mode"] })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                <option value="observe">Observe</option>
                <option value="warn">Warn</option>
                <option value="reject">Reject</option>
              </select>
              <select value={selected.ingest_mode ?? "raw"} onChange={(e) => setSelected({ ...selected, ingest_mode: e.target.value as EventSpec["ingest_mode"] })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                <option value="raw">Raw</option>
                <option value="upsert">Auto upsert</option>
                <option value="raw_plus_rollup">Raw + rollup</option>
              </select>
              <select value={selected.status} onChange={(e) => setSelected({ ...selected, status: e.target.value as EventSpec["status"] })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                <option value="draft">Draft</option>
                <option value="active">Active</option>
                <option value="deprecated">Deprecated</option>
                <option value="blocked">Blocked</option>
              </select>
              <button onClick={save} className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold">
                Save
              </button>
              <textarea value={selected.description} onChange={(e) => setSelected({ ...selected, description: e.target.value })} placeholder="description" className="col-span-2 bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
            </section>

            <section className="border border-border rounded p-4 grid gap-3" style={{ gridTemplateColumns: "1fr 1fr" }}>
              <div className="flex flex-col gap-2">
                <div className="text-text-dim text-xs uppercase">Upsert Policy</div>
                <textarea
                  value={upsertPolicyText}
                  onChange={(e) => setUpsertPolicyText(e.target.value)}
                  spellCheck={false}
                  className="bg-bg-input border border-border rounded p-2 text-xs"
                  style={{ minHeight: "160px", fontFamily: "monospace" }}
                />
              </div>
              <div className="flex flex-col gap-2">
                <div className="text-text-dim text-xs uppercase">Rollup Policy</div>
                <textarea
                  value={rollupPolicyText}
                  onChange={(e) => setRollupPolicyText(e.target.value)}
                  spellCheck={false}
                  className="bg-bg-input border border-border rounded p-2 text-xs"
                  style={{ minHeight: "160px", fontFamily: "monospace" }}
                />
              </div>
            </section>

            <section className="border border-border rounded p-4 flex flex-col gap-2">
              <div className="text-text-dim text-xs uppercase">Properties</div>
              <textarea
                value={propsText}
                onChange={(e) => setPropsText(e.target.value)}
                spellCheck={false}
                className="bg-bg-input border border-border rounded p-2 text-xs"
                style={{ minHeight: "190px", fontFamily: "monospace" }}
              />
            </section>

            <section className="border border-border rounded p-4 grid gap-3" style={{ gridTemplateColumns: "1fr 1fr" }}>
              <div className="flex flex-col gap-2">
                <div className="text-text-dim text-xs uppercase">Validate Sample</div>
                <textarea
                  value={sampleText}
                  onChange={(e) => setSampleText(e.target.value)}
                  spellCheck={false}
                  className="bg-bg-input border border-border rounded p-2 text-xs"
                  style={{ minHeight: "160px", fontFamily: "monospace" }}
                />
                <button onClick={validate} className="px-3 py-1.5 text-sm border border-border rounded text-text-muted hover:text-text">
                  Validate
                </button>
              </div>
              <pre className="bg-bg-input border border-border rounded p-2 text-xs overflow-auto" style={{ minHeight: "205px" }}>
                {validation || status}
              </pre>
            </section>

            <section className="border border-border rounded p-4 flex flex-col gap-2">
              <div className="text-text-dim text-xs uppercase">Recent Violations</div>
              {violations.length ? (
                violations.map((v) => (
                  <div key={v.id} className="border-t border-border py-1.5 text-xs flex items-center gap-2">
                    <span className="text-text-dim" style={{ width: "64px" }}>{relTime(v.seen_at)}</span>
                    <span className="text-text-muted">{v.app}.{v.topic}</span>
                    <span className="text-error">{v.violation_type}</span>
                    <span className="text-text-dim truncate">{v.property_key || ""} {v.message}</span>
                  </div>
                ))
              ) : (
                <Empty label="No validation violations." />
              )}
            </section>
          </>
        )}
      </main>
    </div>
  );
}

export default function AnalyticsPanel({ projectId }: NativePanelProps) {
  const [view, setView] = useState<"overview" | "dashboards" | "objectives" | "catalog" | "tracking" | "capture">("overview");
  const [windowKey, setWindowKey] = useState("7d");
  const [byKey, setByKey] = useState("props.platform");
  const [appF, setAppF] = useState("");
  const [topicF, setTopicF] = useState("");
  const [whereKey, setWhereKey] = useState("");
  const [whereVal, setWhereVal] = useState("");

  const [summary, setSummary] = useState<Summary | null>(null);
  const [series, setSeries] = useState<SeriesPoint[]>([]);
  const [top, setTop] = useState<TopRow[]>([]);
  const [events, setEvents] = useState<EventRow[]>([]);
  const [dims, setDims] = useState<Dimensions>({ apps: [], topics: [] });
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");
  const [lastEventAt, setLastEventAt] = useState(0);

  const since = useMemo(() => {
    const w = WINDOWS.find((x) => x.key === windowKey);
    return w && w.ms ? Date.now() - w.ms : 0;
  }, [windowKey]);

  // Shared query string for every data endpoint: window + dimension
  // filters (app / event type / one props.X = value equality).
  const qs = useMemo(() => {
    const p = new URLSearchParams();
    if (projectId) p.set("project_id", projectId); // scope the panel to this project
    if (since) p.set("since", String(since));
    if (appF) p.set("app", appF);
    if (topicF) p.set("topic", topicF);
    if (whereKey.trim() && whereVal.trim()) {
      p.set("where", JSON.stringify({ [whereKey.trim()]: whereVal.trim() }));
    }
    return p.toString();
  }, [projectId, since, appF, topicF, whereKey, whereVal]);

  const withQs = useCallback(
    (base: string) => base + (qs ? (base.includes("?") ? "&" : "?") + qs : ""),
    [qs],
  );

  const loadMain = useCallback(async () => {
    setLoading(true);
    setErr("");
    try {
      const [sumR, serR, evR] = await Promise.all([
        fetch(withQs(`${API}/summary`), { credentials: "same-origin" }),
        fetch(withQs(`${API}/series`), { credentials: "same-origin" }),
        fetch(withQs(`${API}/feed?limit=50`), { credentials: "same-origin" }),
      ]);
      if (sumR.ok) {
        const d = await sumR.json();
        setSummary({
          total: d.total ?? 0,
          apps: d.apps ?? 0,
          topics: d.topics ?? 0,
          topics_list: Array.isArray(d.topics_list) ? d.topics_list : [],
        });
      } else {
        setErr(`summary ${sumR.status}`);
      }
      if (serR.ok) setSeries((await serR.json()).series ?? []);
      if (evR.ok) setEvents((await evR.json()).events ?? []);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [withQs]);

  const loadTop = useCallback(async () => {
    try {
      const res = await fetch(withQs(`${API}/top?by=${encodeURIComponent(byKey)}`), {
        credentials: "same-origin",
      });
      if (res.ok) setTop((await res.json()).top ?? []);
    } catch {
      /* top is best-effort; loadMain surfaces errors */
    }
  }, [withQs, byKey]);

  // Dimension options for the filter dropdowns — scoped to this project.
  useEffect(() => {
    const p = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    fetch(`${API}/dimensions${p}`, { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : { apps: [], topics: [] }))
      .then((d) => setDims({ apps: d.apps ?? [], topics: d.topics ?? [] }))
      .catch(() => {});
  }, [projectId]);

  useEffect(() => {
    loadMain();
  }, [loadMain]);

  useEffect(() => {
    const t = setTimeout(loadTop, 250);
    return () => clearTimeout(t);
  }, [loadTop]);

  // Live: debounce bus events so a burst of tracks = one reload.
  const reloadTimer = useRef<number | null>(null);
  const onBusEvent = useCallback(() => {
    setLastEventAt(Date.now());
    if (reloadTimer.current) window.clearTimeout(reloadTimer.current);
    reloadTimer.current = window.setTimeout(() => {
      loadMain();
      loadTop();
    }, 1200);
  }, [loadMain, loadTop]);
  useAppEvents("analytics", projectId, onBusEvent);

  const refresh = () => {
    loadMain();
    loadTop();
  };

  const hasFilters = !!(appF || topicF || (whereKey && whereVal));
  const clearFilters = () => {
    setAppF("");
    setTopicF("");
    setWhereKey("");
    setWhereVal("");
  };

  const selCls = "bg-bg-input border border-border rounded px-2 py-1 text-xs";

  return (
    <div className="h-full flex flex-col">
	  <header className="flex items-center gap-3 border-b border-border px-4 py-2 overflow-x-auto whitespace-nowrap">
        <div className="flex items-center gap-2 text-text font-medium">
          <span className="text-accent">
            <ChartIcon />
          </span>
          Analytics
        </div>
		<div className="flex items-center gap-1 ml-2 flex-shrink-0">
		  {(["overview", "dashboards", "objectives", "catalog", "tracking", "capture"] as const).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={
                "px-2 py-1 text-xs rounded border " +
                (view === v
                  ? "border-accent text-accent"
                  : "border-border text-text-muted hover:text-text")
              }
            >
              {v === "overview"
                ? "Overview"
                : v === "dashboards"
                  ? "Dashboards"
                  : v === "objectives"
                    ? "Objectives"
                  : v === "catalog"
                    ? "Catalog"
                    : v === "tracking"
                      ? "Tracking"
                      : "Capture"}
            </button>
          ))}
        </div>
        {view === "overview" && (
          <>
            <div className="flex items-center gap-1 ml-2">
              {WINDOWS.map((w) => (
                <button
                  key={w.key}
                  onClick={() => setWindowKey(w.key)}
                  className={
                    "px-2 py-1 text-xs rounded border " +
                    (windowKey === w.key
                      ? "border-accent text-accent"
                      : "border-border text-text-muted hover:text-text")
                  }
                >
                  {w.label}
                </button>
              ))}
            </div>
            <span className="flex items-center gap-1.5 text-text-dim text-xs ml-1" title="Live — updates as events are recorded">
              <Dot />
              {lastEventAt ? `live · ${relTime(lastEventAt)}` : "live"}
            </span>
            <button
              onClick={refresh}
              disabled={loading}
              className="ml-auto px-3 py-1 text-sm border border-border rounded text-text-muted hover:text-text disabled:opacity-50"
            >
              Refresh
            </button>
            <span className="text-text-dim text-xs" style={{ minWidth: "48px" }}>
              {err ? <span className="text-error">{err}</span> : loading ? "loading…" : ""}
            </span>
          </>
        )}
      </header>

      {view === "tracking" ? (
        <TrackingTab projectId={projectId} />
      ) : view === "capture" ? (
        <CaptureTab projectId={projectId} />
      ) : view === "dashboards" ? (
        <DashboardsTab projectId={projectId} />
      ) : view === "objectives" ? (
        <ObjectivesTab projectId={projectId} />
      ) : view === "catalog" ? (
        <CatalogTab projectId={projectId} />
      ) : (
      <>
      <div className="flex items-center gap-2 flex-wrap border-b border-border px-4 py-2">
        <select value={appF} onChange={(e) => setAppF(e.target.value)} className={selCls}>
          <option value="">All apps</option>
          {dims.apps.map((a) => (
            <option key={a} value={a}>
              {a}
            </option>
          ))}
        </select>
        <select value={topicF} onChange={(e) => setTopicF(e.target.value)} className={selCls}>
          <option value="">All event types</option>
          {dims.topics.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <input
          type="text"
          value={whereKey}
          onChange={(e) => setWhereKey(e.target.value)}
          placeholder="props.country"
          spellCheck={false}
          className={selCls}
          style={{ width: "140px" }}
        />
        <span className="text-text-dim text-xs">=</span>
        <input
          type="text"
          value={whereVal}
          onChange={(e) => setWhereVal(e.target.value)}
          placeholder="value"
          spellCheck={false}
          className={selCls}
          style={{ width: "120px" }}
        />
        {hasFilters && (
          <button onClick={clearFilters} className="px-2 py-1 text-xs text-text-muted hover:text-text">
            Clear
          </button>
        )}
      </div>

      <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
		<div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))" }}>
          <Stat label="Events" value={summary?.total ?? 0} />
          <Stat label="Topics" value={summary?.topics ?? 0} />
          <Stat label="Apps" value={summary?.apps ?? 0} />
        </div>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Events per day</div>
          <BarSeries data={series} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-3">
          <div className="flex items-center gap-2">
            <div className="text-text-dim text-xs uppercase">Top values</div>
            <input
              type="text"
              value={byKey}
              onChange={(e) => setByKey(e.target.value)}
              placeholder="props.platform"
              spellCheck={false}
              className="ml-auto bg-bg-input border border-border rounded px-2 py-1 text-xs"
              style={{ width: "200px" }}
            />
          </div>
          <TopBars rows={top} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Event feed</div>
          <EventFeed rows={events} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Topics</div>
          {summary && summary.topics_list.length ? (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-text-dim text-xs uppercase">
                  <th className="text-left font-normal py-1">App</th>
                  <th className="text-left font-normal py-1">Topic</th>
                  <th className="text-right font-normal py-1">Count</th>
                  <th className="text-right font-normal py-1">Last seen</th>
                </tr>
              </thead>
              <tbody>
                {summary.topics_list.map((t) => (
                  <tr key={`${t.app}/${t.topic}`} className="border-t border-border">
                    <td className="py-1.5 text-text-muted">{t.app}</td>
                    <td className="py-1.5 text-text">{t.topic}</td>
                    <td className="py-1.5 text-right tabular-nums text-text">{fmt(t.count)}</td>
                    <td className="py-1.5 text-right text-text-dim">{relTime(t.last_ts)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <Empty label="No events recorded yet. Apps write via the analytics_track tool." />
          )}
        </section>
      </div>
      </>
      )}
    </div>
  );
}
