import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { areaChartEndMarkerPath, areaChartGeometry, formatMetric, scopedAppURL } from "./dashboard-ui";
import { allowedValues, rankingRows, settingString, widgetConfig, WidgetConfig } from "./standalone-config";
import { useLiveRefresh } from "./use-live-refresh";

export interface WidgetHostProps {
  appName?: string; projectId?: string; eventRevision?: number; preview?: boolean;
  widgetId?: string; widgetSize?: "half" | "full"; widgetSettings?: Record<string, unknown>;
}
type Option = { value: string; label: string };
const fieldClass = "max-w-44 rounded border border-border bg-bg-input px-2 py-1 text-[10px] text-text";
function Selector({label, value, options, onChange, all = true}: {label: string; value: string; options: Option[]; onChange: (value: string) => void; all?: boolean}) {
  const choices = options.some(x => x.value === value) || !value ? options : [{ value, label: value }, ...options];
  return <label className="flex items-center gap-1.5 text-[10px] text-text-dim"><span>{label}</span><select className={fieldClass} aria-label={label} value={value} onChange={event => onChange(event.target.value)}>
    {all && <option value="">All</option>}{choices.map(x => <option key={x.value} value={x.value}>{x.label}</option>)}
  </select></label>;
}
function TrendChart({rows, config, gradientId}: { rows: Array<Record<string, any>>; config: WidgetConfig; gradientId: string }) {
  const values = rows.map(row => Object.prototype.hasOwnProperty.call(row, "value") ? row.value == null ? null : Number(row.value) : Number(row.count ?? 0));
  const timestamps = rows.map((row, i) => Number(row.ts) || Date.parse(String(row.bucket)) || i);
  const {points, linePath, areaPath, baseline} = areaChartGeometry(values, 300, 92, timestamps);
  const date = (row: Record<string, any>) => Number.isFinite(Date.parse(row.bucket)) ? new Date(row.bucket).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : String(row.bucket);
  return <div className="mt-3"><svg viewBox="0 0 300 92" className="h-36 w-full text-accent" preserveAspectRatio="none" role="img" aria-label="Analytics trend">
    <defs><linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="currentColor" stopOpacity="0.38"/><stop offset="100%" stopColor="currentColor" stopOpacity="0.02"/></linearGradient></defs>
    {[10,35,60,85].map(y => <line key={y} x1="0" x2="300" y1={y} y2={y} stroke="currentColor" strokeOpacity="0.12" />)}
    <path d={areaPath} fill={`url(#${gradientId})`}/><path d={linePath} fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke"/>
    <path d={areaChartEndMarkerPath(points, baseline)} fill="none" stroke="currentColor" strokeWidth="5.5" strokeLinecap="round" vectorEffect="non-scaling-stroke"/>
    {points.map(point => <circle key={point.index} cx={point.x} cy={point.y} r="3" fill="transparent"><title>{date(rows[point.index!])}: {formatMetric(values[point.index!] ?? null, config)}</title></circle>)}
  </svg><div className="flex justify-between text-[10px] text-text-dim"><span>{date(rows[0])}</span><span>{date(rows.at(-1)!)}</span></div>
  <div className="mt-2 text-xs text-text-muted">Latest: <span className="tabular-nums text-text">{formatMetric(values.at(-1) ?? null, config)}</span></div></div>;
}
export default function StandaloneWidget(props: WidgetHostProps & { kind: "trend" | "ranking" }) {
  return <WidgetContent key={`${props.projectId}:${JSON.stringify(props.widgetSettings || {})}`} {...props}/>;
}
function WidgetContent(props: WidgetHostProps & {kind: "trend" | "ranking"}) {
  const settings = props.widgetSettings || {};
  const appName = props.appName || "analytics", project = props.projectId || "";
  const api = `/api/apps/${encodeURIComponent(appName)}`;
  const [topic, setTopic] = useState(settingString(settings, "topic"));
  const [filter, setFilter] = useState(settingString(settings, "filter_value"));
  const [window, setWindow] = useState(settingString(settings, "window", "30d"));
  const [eventOptions, setEventOptions] = useState<Option[]>([]), [filterOptions, setFilterOptions] = useState<Option[]>([]);
  const [data, setData] = useState<Record<string, any> | null>(null), [error, setError] = useState("");
  const [loading, setLoading] = useState(!props.preview);
  const sequence = useRef(0), controller = useRef<AbortController | null>(null);
  const field = settingString(settings, "filter_field"), app = settingString(settings, "app");
  const title = settingString(settings, "title", props.kind === "trend" ? "Trend" : "Ranking");
  const query = useMemo(() => {
    try { return {config: widgetConfig(settings, props.kind, {topic, filter, window}), error: ""}; }
    catch (reason) { return {config: null, error: String((reason as Error).message)}; }
  }, [props.widgetSettings, props.kind, topic, filter, window]);
  useEffect(() => {
    const abort = new AbortController();
    const discover = async (valueField: string, configured: unknown): Promise<Option[]> => {
      const allowed = allowedValues(configured);
      if (allowed.length) return allowed.map(value => ({ value, label: value.replaceAll("_", " ") }));
      if (props.preview || !project) return [];
      const source = { value_field: valueField, app, ...(valueField !== "topic" ? {topic} : {}) };
      const res = await fetch(scopedAppURL(`${api}/dashboard-filter-options?filter=${encodeURIComponent(JSON.stringify({ source }))}`, project), { credentials: "same-origin", signal: abort.signal });
      if (!res.ok) throw new Error(await res.text());
      return (await res.json()).options?.filter((x: any) => typeof x.value === "string") || [];
    };
    discover("topic", settings.event_options).then(setEventOptions).catch(() => {});
    if (field) discover(field, settings.filter_options).then(setFilterOptions).catch(() => {});
    return () => abort.abort();
  }, [api, project, app, field, topic, props.preview, props.widgetSettings]);
  const refresh = useCallback(async () => {
    const id = ++sequence.current;
    controller.current?.abort();
    if (props.preview || !project || !query.config) { setLoading(false); return; }
    const abort = new AbortController(); controller.current = abort;
    setLoading(true);
    try {
      const queryType = props.kind === "trend" ? "timeseries" : query.config.aggregation === "count" ? "top" : "table";
      const res = await fetch(scopedAppURL(`${api}/query-widget`, project), { method: "POST", credentials: "same-origin", signal: abort.signal, headers: {"Content-Type": "application/json"}, body: JSON.stringify({ widget: {type: queryType, config: query.config} }) });
      if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
      const next = await res.json();
      if (id === sequence.current) { setData(next); setError(""); }
    } catch (reason) {
      if (id === sequence.current && !abort.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason));
    } finally { if (id === sequence.current) setLoading(false); }
  }, [api, project, props.kind, props.preview, query]);
  useEffect(() => { setData(null); void refresh(); return () => { sequence.current++; controller.current?.abort(); }; }, [refresh]);
  const queued = useLiveRefresh(refresh);
  useEffect(() => { queued(); }, [props.eventRevision, queued]);
  useEffect(() => { if (props.preview) return; const timer = globalThis.setInterval(() => { if (document.visibilityState !== "hidden") void refresh(); }, 30000); return () => globalThis.clearInterval(timer); }, [refresh, props.preview]);
  const config = query.config;
  const rows = props.preview ? Array.from({length: 7}, (_, i) => ({bucket: `${i + 1} Oct`, count: [42,58,51,74,68,91,84][i]})) : data?.series || [];
  const ranked = props.preview ? [{label: "Example A", value: 120, count: 120}, {label: "Example B", value: 84, count: 84}] : rankingRows(data);
  const max = Math.max(1, ...ranked.map(row => Math.abs(row.value)));
  const problem = query.error || error;
  return <section className="flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card">
    <header className="flex items-center gap-2 border-b border-border px-4 py-3"><div className="min-w-0 flex-1"><h2 className="truncate text-sm font-bold text-text">{title}</h2><p className="truncate text-[10px] text-text-dim">{config?.aggregation || "count"}{props.kind === "ranking" ? ` by ${config?.by || "group"}` : ` per ${config?.interval || "day"}`}</p></div><button className="text-[10px] text-text-muted hover:text-text" onClick={() => void refresh()} aria-label="Refresh analytics widget">Refresh</button></header>
    <nav className="flex flex-wrap gap-2 border-b border-border px-4 py-2" aria-label="Widget filters">
      {settings.show_event_selector !== false && <Selector label="Event" value={topic} options={eventOptions} onChange={setTopic}/>}
      {field && <Selector label={settingString(settings, "filter_label", "Filter")} value={filter} options={filterOptions} onChange={setFilter}/>}
      <Selector label="Window" value={window} options={["7d","30d","90d","all"].map(value => ({value, label: value === "all" ? "All time" : value}))} all={false} onChange={setWindow}/>
    </nav>
    <div className="min-h-0 flex-1 overflow-auto p-4">
      {problem ? <p className="text-xs text-error" role="alert">{problem}</p> : loading ? <p className="text-xs text-text-dim">Loading…</p> : config && (props.kind === "trend" ? rows.length ? <TrendChart rows={rows} config={config} gradientId={`analytics-trend-${(props.widgetId || "preview").replace(/[^\w-]/g,"-")}`}/> : <p className="text-xs text-text-dim">No values in this window.</p> : ranked.length ? <div className="space-y-3">{ranked.map((row, index) => <div key={`${row.label}:${index}`}><div className="flex items-center gap-2 text-xs"><span className="text-text-dim">{index + 1}.</span><span className="min-w-0 flex-1 truncate text-text-muted" title={row.label}>{row.label}</span><span className="tabular-nums text-text">{formatMetric(row.value, config)}</span></div><div className="mt-1 h-1 rounded bg-bg-input"><div className="h-full rounded bg-success" style={{width: `${Math.abs(row.value) / max * 100}%`}}/></div></div>)}</div> : <p className="text-xs text-text-dim">No values in this window.</p>)}
    </div>
  </section>;
}
