import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { batchResultsByID, defaultDashboardFilters, formatMetric, resolveMetricConfig, scopedAppURL, selectDashboardID } from "./dashboard-ui";

interface HostProps {
  appName?: string;
  projectId?: string;
  eventRevision?: number;
  preview?: boolean;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

interface DashboardWidget {
  id: number;
  type: "stat" | "timeseries" | "top" | "breakdown" | "feed";
  title: string;
  position: number;
  config: Record<string, unknown>;
}

interface FilterOption {
  label: string;
  value: string;
}

interface DashboardFilter {
  key: string;
  label?: string;
  type?: "select" | "multi_select" | "date_window";
  default?: string;
  options?: Array<FilterOption | string>;
  source?: Record<string, unknown>;
}

interface Dashboard {
  id: number;
  project_id: string;
  name: string;
  description?: string;
  config?: { filters?: DashboardFilter[] };
  widgets?: DashboardWidget[];
}

interface ActivitySummary {
  total: number;
  apps: number;
  topics: number;
  topics_list: Array<{ app: string; topic: string; count: number; last_ts: number }>;
}

const PREVIEW_SUMMARY: ActivitySummary = {
  total: 12840,
  apps: 6,
  topics: 18,
  topics_list: [
    { app: "website", topic: "page_view", count: 9210, last_ts: Date.now() },
    { app: "subscriptions", topic: "mrr.snapshot", count: 31, last_ts: Date.now() },
  ],
};

function settingNumber(settings: Record<string, unknown> | undefined, key: string, fallback: number): number {
  const value = Number(settings?.[key]);
  return Number.isFinite(value) ? value : fallback;
}

function settingBoolean(settings: Record<string, unknown> | undefined, key: string, fallback: boolean): boolean {
  const value = settings?.[key];
  return typeof value === "boolean" ? value : fallback;
}

function asOptions(raw?: Array<FilterOption | string>): FilterOption[] {
  return (raw ?? []).map((option) =>
    typeof option === "string" ? { label: option, value: option } : { label: option.label || option.value, value: option.value },
  );
}

function relativeTime(ms: number): string {
  const seconds = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h ago` : `${Math.floor(hours / 24)}d ago`;
}

function Empty({ children }: { children: string }) {
  return <div className="flex min-h-16 items-center text-xs text-text-dim">{children}</div>;
}

function Sparkline({ rows, config }: { rows: Array<Record<string, unknown>>; config: Record<string, unknown> }) {
  if (!rows.length) return <Empty>No values in this window.</Empty>;
  const values = rows.map((row) => Number(row.value ?? row.count ?? 0));
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;
  const points = values.map((value, index) =>
    `${(index / Math.max(1, values.length - 1)) * 300},${64 - ((value - min) / range) * 56}`,
  ).join(" ");
  return (
    <div>
      <svg viewBox="0 0 300 68" className="h-20 w-full text-accent" preserveAspectRatio="none" aria-label="Metric trend">
        <polyline points={points} fill="none" stroke="currentColor" strokeWidth="2.5" vectorEffect="non-scaling-stroke" />
      </svg>
      <div className="flex justify-between text-[10px] text-text-dim">
        <span>{String(rows[0]?.bucket ?? "")}</span>
        <span>{formatMetric(values.at(-1) ?? 0, config)}</span>
      </div>
    </div>
  );
}

function MetricCard({ widget, data, filters }: { widget: DashboardWidget; data?: Record<string, any>; filters: Record<string, string> }) {
  const config = resolveMetricConfig(widget.config, filters);
  const aggregation = String(data?.aggregation ?? widget.config.aggregation ?? "");
  return (
    <article className="min-w-0 rounded border border-border bg-bg-subtle/30 px-3 py-3">
      <div className="truncate text-[10px] font-bold uppercase tracking-wide text-text-dim" title={widget.title}>{widget.title}</div>
      {data?.error ? (
        <div className="mt-2 truncate text-xs text-error" title={String(data.error)}>{String(data.error)}</div>
      ) : (
        <>
          <div className="mt-1 truncate text-2xl font-semibold tabular-nums text-text">
            {data ? formatMetric(Number(data.value ?? 0), config) : "—"}
          </div>
          <div className="mt-1 flex items-center gap-2 text-[10px] text-text-dim">
            {aggregation && <span className="capitalize">{aggregation}</span>}
            {typeof data?.change_percent === "number" && (
              <span className={data.change_percent >= 0 ? "text-success" : "text-error"}>
                {data.change_percent >= 0 ? "+" : ""}{data.change_percent.toFixed(1)}%
              </span>
            )}
          </div>
        </>
      )}
    </article>
  );
}

function DetailCard({ widget, data, filters }: { widget: DashboardWidget; data?: Record<string, any>; filters: Record<string, string> }) {
  const config = resolveMetricConfig(widget.config, filters);
  return (
    <article className="min-w-0 rounded border border-border px-3 py-3">
      <div className="flex items-center gap-2">
        <div className="truncate text-[10px] font-bold uppercase tracking-wide text-text-dim">{widget.title}</div>
        <span className="ml-auto text-[10px] text-text-dim">{String(config.window ?? "24h")}</span>
      </div>
      {data?.error ? (
        <div className="mt-3 truncate text-xs text-error">{String(data.error)}</div>
      ) : widget.type === "timeseries" ? (
        <Sparkline rows={data?.series ?? []} config={config} />
      ) : widget.type === "feed" ? (
        <div className="mt-2 space-y-1.5">
          {(data?.events ?? []).slice(0, 5).map((event: any) => (
            <div key={event.id} className="flex items-center gap-2 border-t border-border pt-1.5 text-[10px]">
              <span className="w-12 shrink-0 text-text-dim">{relativeTime(event.ts)}</span>
              <span className="truncate text-text-muted">{event.app}.{event.topic}</span>
            </div>
          ))}
          {data && !(data.events ?? []).length && <Empty>No recent events.</Empty>}
        </div>
      ) : (
        <div className="mt-2 space-y-1.5">
          {(data?.top ?? []).slice(0, 5).map((row: { value: unknown; count: number }) => (
            <div key={String(row.value)} className="flex items-center gap-2 text-[10px]">
              <span className="min-w-0 flex-1 truncate text-text-muted">{String(row.value ?? "—")}</span>
              <span className="tabular-nums text-text">{row.count.toLocaleString()}</span>
            </div>
          ))}
          {data && !(data.top ?? []).length && <Empty>No values in this window.</Empty>}
        </div>
      )}
    </article>
  );
}

function ActivityFallback({ summary }: { summary: ActivitySummary | null }) {
  if (!summary) return <div className="p-4"><Empty>Loading project activity…</Empty></div>;
  return (
    <div className="min-h-0 flex-1 overflow-auto p-3">
      <div className="grid grid-cols-3 gap-2">
        {[["Events", summary.total], ["Apps", summary.apps], ["Topics", summary.topics]].map(([label, value]) => (
          <div key={String(label)} className="rounded border border-border px-3 py-2">
            <div className="text-lg font-semibold tabular-nums text-text">{Number(value).toLocaleString()}</div>
            <div className="text-[9px] uppercase text-text-dim">{label}</div>
          </div>
        ))}
      </div>
      <div className="mt-3 space-y-1">
        {summary.topics_list.slice(0, 4).map((topic) => (
          <div key={`${topic.app}:${topic.topic}`} className="flex items-center gap-2 text-[10px]">
            <span className="min-w-0 flex-1 truncate text-text-muted">{topic.app}.{topic.topic}</span>
            <span className="tabular-nums text-text">{topic.count.toLocaleString()}</span>
          </div>
        ))}
      </div>
      {!summary.total && <Empty>No analytics yet. Open Analytics to enable capture or tracking.</Empty>}
    </div>
  );
}

export default function AnalyticsDashboardWidget(props: HostProps) {
  const appName = props.appName || "analytics";
  const projectID = props.projectId || "";
  const full = props.widgetSize === "full";
  const showTrends = settingBoolean(props.widgetSettings, "show_trends", true);
  const maxMetrics = Math.floor(Math.max(1, Math.min(6, settingNumber(props.widgetSettings, "max_metrics", 3))));
  const preferredDashboardID = Math.floor(settingNumber(props.widgetSettings, "dashboard_id", 0));
  const storageKey = `apteva:analytics:dashboard-widget:${props.widgetId || projectID || "global"}`;
  const api = `/api/apps/${encodeURIComponent(appName)}`;
  const dashboardSequence = useRef(0);
  const querySequence = useRef(0);

  const [dashboards, setDashboards] = useState<Dashboard[]>([]);
  const [dashboardID, setDashboardID] = useState(0);
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [widgetData, setWidgetData] = useState<Record<number, Record<string, any>>>({});
  const [filterValues, setFilterValues] = useState<Record<string, string>>({});
  const [filterOptions, setFilterOptions] = useState<Record<string, FilterOption[]>>({});
  const [fallback, setFallback] = useState<ActivitySummary | null>(props.preview ? PREVIEW_SUMMARY : null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const getJSON = useCallback(async <T,>(path: string): Promise<T> => {
    const response = await fetch(scopedAppURL(`${api}${path}`, projectID), { credentials: "same-origin" });
    if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
    return response.json();
  }, [api, projectID]);

  const loadDashboards = useCallback(async () => {
    if (!projectID || props.preview) return;
    try {
      const result = await getJSON<{ dashboards: Dashboard[] }>("/dashboards");
      const rows = result.dashboards ?? [];
      setDashboards(rows);
      if (!rows.length) {
        setDashboardID(0);
        setDashboard(null);
        const since = Date.now() - 24 * 3600_000;
        setFallback(await getJSON<ActivitySummary>(`/summary?since=${since}&limit=6`));
        return;
      }
      let storedID = 0;
      try { storedID = Number(window.localStorage.getItem(storageKey) || 0); } catch { /* storage is optional */ }
      setDashboardID((current) => selectDashboardID(rows, preferredDashboardID, current || storedID));
      setError("");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    }
  }, [getJSON, preferredDashboardID, projectID, props.preview, storageKey]);

  useEffect(() => { void loadDashboards(); }, [loadDashboards, props.eventRevision]);

  useEffect(() => {
    if (!dashboardID || !projectID || props.preview) return;
    const sequence = ++dashboardSequence.current;
    try { window.localStorage.setItem(storageKey, String(dashboardID)); } catch { /* storage is optional */ }
    setLoading(true);
    getJSON<Dashboard>(`/dashboards/${dashboardID}`).then((next) => {
      if (sequence !== dashboardSequence.current) return;
      setDashboard(next);
      setFilterValues(defaultDashboardFilters(next.config?.filters ?? []));
      setWidgetData({});
      setError("");
    }).catch((reason) => {
      if (sequence === dashboardSequence.current) setError(reason instanceof Error ? reason.message : String(reason));
    }).finally(() => {
      if (sequence === dashboardSequence.current) setLoading(false);
    });
  }, [dashboardID, getJSON, projectID, props.preview, storageKey]);

  const filters = dashboard?.config?.filters ?? [];
  useEffect(() => {
    if (!filters.length || props.preview) {
      setFilterOptions({});
      return;
    }
    let cancelled = false;
    void Promise.all(filters.map(async (filter) => {
      const configured = asOptions(filter.options);
      if (configured.length || !filter.source) return [filter.key, configured] as const;
      const rows = await getJSON<{ options: FilterOption[] }>(`/dashboard-filter-options?filter=${encodeURIComponent(JSON.stringify(filter))}`);
      return [filter.key, rows.options ?? []] as const;
    })).then((pairs) => {
      if (!cancelled) setFilterOptions(Object.fromEntries(pairs));
    }).catch(() => { /* keep static/default choices when discovery is unavailable */ });
    return () => { cancelled = true; };
  }, [dashboard?.id, getJSON, props.preview]);

  const visibleWidgets = useMemo(() => {
    const widgets = [...(dashboard?.widgets ?? [])].sort((a, b) => a.position - b.position || a.id - b.id);
    const stats = widgets.filter((widget) => widget.type === "stat").slice(0, maxMetrics);
    const details = showTrends ? widgets.filter((widget) => widget.type !== "stat").slice(0, full ? 4 : 1) : [];
    return [...stats, ...details];
  }, [dashboard, full, maxMetrics, showTrends]);

  const refresh = useCallback(async () => {
    if (!dashboard || !projectID || props.preview) return;
    const sequence = ++querySequence.current;
    try {
      const payload = await getJSON<{ widgets?: Array<{ widget_id: number; data?: Record<string, unknown>; error?: string }> }>(
        `/query-dashboard?dashboard_id=${dashboard.id}&filters=${encodeURIComponent(JSON.stringify(filterValues))}`,
      );
      if (sequence !== querySequence.current) return;
      setWidgetData(batchResultsByID(payload.widgets ?? []));
      setError("");
    } catch (reason) {
      if (sequence === querySequence.current) setError(reason instanceof Error ? reason.message : String(reason));
    }
  }, [dashboard, filterValues, getJSON, projectID, props.preview]);

  useEffect(() => { void refresh(); }, [refresh, props.eventRevision]);
  useEffect(() => {
    if (props.preview) return;
    const timer = window.setInterval(() => void refresh(), 30000);
    return () => window.clearInterval(timer);
  }, [props.preview, refresh]);

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card">
      <header className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="truncate text-sm font-bold text-text">{dashboard?.name || "Analytics"}</h2>
            <span className="rounded border border-success/25 bg-success/10 px-1.5 py-0.5 text-[8px] font-bold uppercase text-success">Live</span>
          </div>
          <p className="mt-0.5 truncate text-[10px] text-text-dim">{dashboard?.description || "Project activity and saved metrics"}</p>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {dashboards.length > 1 && (
            <select aria-label="Analytics dashboard" value={dashboardID} onChange={(event) => setDashboardID(Number(event.target.value))} className="max-w-40 rounded border border-border bg-bg-input px-2 py-1 text-[10px] text-text">
              {dashboards.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          )}
          <a href={`/apps/${encodeURIComponent(appName)}/page`} className="text-[10px] text-text-muted hover:text-text">Open →</a>
        </div>
      </header>

      {!!filters.length && (
        <nav className="flex shrink-0 gap-2 overflow-x-auto border-b border-border px-4 py-2" aria-label="Dashboard filters">
          {filters.map((filter) => {
            const discovered = filterOptions[filter.key] ?? [];
            const options = discovered.length ? discovered : filter.type === "date_window"
              ? ["7d", "30d", "90d", "all"].map((value) => ({ value, label: value === "all" ? "All" : value }))
              : asOptions(filter.options);
            return (
              <label key={filter.key} className="flex shrink-0 items-center gap-1.5 text-[9px] text-text-dim">
                <span>{filter.label || filter.key}</span>
                <select value={filterValues[filter.key] || filter.default || "all"} onChange={(event) => setFilterValues((current) => ({ ...current, [filter.key]: event.target.value }))} className="rounded border border-border bg-bg-input px-2 py-1 text-[10px] text-text">
                  {filter.type !== "date_window" && <option value="all">All</option>}
                  {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                </select>
              </label>
            );
          })}
        </nav>
      )}

      {error && <div className="mx-3 mt-3 rounded border border-error/30 bg-error/10 px-3 py-2 text-[10px] text-error">{error}</div>}
      {!dashboard ? (
        <ActivityFallback summary={fallback} />
      ) : loading ? (
        <div className="p-4"><Empty>Loading dashboard…</Empty></div>
      ) : !visibleWidgets.length ? (
        <div className="p-4"><Empty>This dashboard has no widgets yet.</Empty></div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto p-3">
          <div className="grid gap-2" style={{ gridTemplateColumns: `repeat(${Math.max(1, Math.min(3, visibleWidgets.filter((widget) => widget.type === "stat").length))}, minmax(0, 1fr))` }}>
            {visibleWidgets.filter((widget) => widget.type === "stat").map((widget) => <MetricCard key={widget.id} widget={widget} data={widgetData[widget.id]} filters={filterValues} />)}
          </div>
          {showTrends && (
            <div className={`mt-2 grid gap-2 ${full ? "grid-cols-2" : "grid-cols-1"}`}>
              {visibleWidgets.filter((widget) => widget.type !== "stat").map((widget) => <DetailCard key={widget.id} widget={widget} data={widgetData[widget.id]} filters={filterValues} />)}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
