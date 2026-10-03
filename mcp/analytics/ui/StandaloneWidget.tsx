import { useCallback, useEffect, useMemo, useState } from "react";
import { areaChartEndMarkerPath, areaChartGeometry, formatMetric, scopedAppURL } from "./dashboard-ui";

export interface WidgetHostProps {
  appName?: string;
  projectId?: string;
  eventRevision?: number;
  preview?: boolean;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

export interface WidgetConfig {
  app?: string;
  topic?: string;
  aggregation?: string;
  value?: string;
  by?: string;
  window?: string;
  interval?: string;
  limit?: number;
  where?: Record<string, unknown>;
  format?: string;
}

export function settingString(
  settings: Record<string, unknown> | undefined,
  key: string,
  fallback: string,
): string {
  const value = settings?.[key];
  return typeof value === "string" && value.trim() ? value.trim() : fallback;
}

export function settingNumber(
  settings: Record<string, unknown> | undefined,
  key: string,
  fallback: number,
): number {
  const value = Number(settings?.[key]);
  return Number.isFinite(value) ? value : fallback;
}

export function widgetConfig(
  settings: Record<string, unknown> | undefined,
  includeInterval = false,
): WidgetConfig {
  const filterField = settingString(settings, "filter_field", "");
  const filterValue = settingString(settings, "filter_value", "");
  const where = filterField && filterValue ? { [filterField]: filterValue } : {};
  const config: WidgetConfig = {
    app: settingString(settings, "app", ""),
    topic: settingString(settings, "topic", "page_view"),
    aggregation: settingString(settings, "aggregation", "count"),
    window: settingString(settings, "window", "30d"),
    limit: Math.max(1, Math.min(100, Math.floor(settingNumber(settings, "limit", 10)))),
    where,
  };
  const value = settingString(settings, "value", "");
  const by = settingString(settings, "group_by", "");
  if (value) config.value = value;
  if (by) config.by = by;
  if (includeInterval) config.interval = settingString(settings, "interval", "day");
  return config;
}

export function apiPath(appName: string, projectId: string): string {
  return scopedAppURL(`/api/apps/${encodeURIComponent(appName)}/query-widget`, projectId);
}

export function displayLabel(config: WidgetConfig): string {
  const topic = config.topic || "events";
  const app = config.app ? `${config.app}.` : "";
  return `${app}${topic}`;
}

export function LoadingOrError({
  loading,
  error,
}: {
  loading: boolean;
  error: string;
}) {
  if (loading) return <div className="mt-3 text-xs text-text-dim">Loading…</div>;
  if (error) return <div className="mt-3 truncate text-xs text-error" title={error}>{error}</div>;
  return null;
}

export function Empty({ children }: { children: string }) {
  return <div className="mt-3 text-xs text-text-dim">{children}</div>;
}

export function TrendChart({
  rows,
  config,
  tone = "accent",
  gradientId,
}: {
  rows: Array<Record<string, any>>;
  config: WidgetConfig;
  tone?: "accent" | "success";
  gradientId: string;
}) {
  if (!rows.length) return <Empty>No values in this window.</Empty>;
  const values = rows.map((row) => row.value == null ? Number(row.count ?? 0) : Number(row.value));
  const timestamps = rows.map((row, index) => Number(row.ts) || Date.parse(String(row.bucket)) || index);
  const { points, linePath, areaPath, baseline } = areaChartGeometry(values, 300, 76, timestamps);
  const marker = areaChartEndMarkerPath(points, baseline);
  return (
    <div className="mt-2">
      <svg viewBox="0 0 300 76" className={`h-24 w-full ${tone === "success" ? "text-success" : "text-accent"}`} preserveAspectRatio="none" role="img" aria-label="Analytics trend">
        <defs>
          <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="currentColor" stopOpacity="0.38" />
            <stop offset="100%" stopColor="currentColor" stopOpacity="0.02" />
          </linearGradient>
        </defs>
        {[10, 30, 50, 70].map((y) => <line key={y} x1="0" x2="300" y1={y} y2={y} stroke="currentColor" strokeOpacity="0.2" strokeWidth="0.7" />)}
        <path d={areaPath} fill={`url(#${gradientId})`} />
        <path d={linePath} fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
        <path d={marker} fill="none" stroke="currentColor" strokeWidth="5.5" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
      </svg>
      <div className="flex justify-between text-[10px] text-text-dim">
        <span>{String(rows[0]?.bucket ?? "")}</span>
        <span>{formatMetric(values.at(-1) ?? null, config)}</span>
      </div>
    </div>
  );
}

export function useWidgetQuery(
  props: WidgetHostProps,
  type: "timeseries" | "top" | "table",
  config: WidgetConfig,
) {
  const appName = props.appName || "analytics";
  const projectId = props.projectId || "";
  const [data, setData] = useState<Record<string, any> | null>(props.preview ? null : null);
  const [loading, setLoading] = useState(!props.preview);
  const [error, setError] = useState("");
  const serializedConfig = useMemo(() => JSON.stringify(config), [config]);
  const refresh = useCallback(async () => {
    if (props.preview || !projectId) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const response = await fetch(apiPath(appName, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          project_id: projectId,
          widget: { type, title: type, config },
          filters: {},
        }),
      });
      if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
      setData(await response.json());
      setError("");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setLoading(false);
    }
  }, [appName, projectId, props.preview, serializedConfig, type]);
  useEffect(() => { void refresh(); }, [refresh, props.eventRevision]);
  return { data, loading, error };
}
