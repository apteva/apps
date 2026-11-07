export const ACCOUNT_METRICS_STALE_MS = 4 * 60 * 60 * 1000;

export interface AccountMetricsFreshness {
  status?: string;
  updated_at?: string;
}

export interface MetricPoint {
  time?: string;
  value: number | null;
  data_status?: "ready" | "pending" | "estimated" | "unavailable" | "unknown";
  included?: number; expected?: number; partial?: boolean;
}

export function accountMetricsNeedRefresh(
  metrics: AccountMetricsFreshness | null | undefined,
  now = Date.now(),
): boolean {
  if (!metrics || metrics.status === "failed") return false;
  if (!metrics.updated_at) return true;
  const updated = Date.parse(metrics.updated_at);
  return !Number.isFinite(updated) || now - updated >= ACCOUNT_METRICS_STALE_MS;
}

export function metricTrendEntries(
  insights: Record<string, MetricPoint[]> | null | undefined,
): { name: string; points: MetricPoint[] }[] {
  return Object.entries(insights || {})
    .filter(([name]) => !name.startsWith("_") && !name.startsWith("total_") && !name.endsWith("_total"))
    .map(([name, points]) => ({ name, points }))
    .filter(({ points }) => points.length >= 2);
}

export function metricChartValue(point: MetricPoint): number | null {
  return point.data_status === "pending" || point.data_status === "unavailable" || point.value == null || !Number.isFinite(point.value) ? null : point.value;
}
export function metricDateLabel(date: string): string {
  return new Intl.DateTimeFormat(undefined, {day:"numeric",month:"long",year:"numeric",timeZone:"UTC"}).format(new Date(date));
}
export function metricDateRanges(dates: string[]): string {
  const sorted = [...new Set(dates)].sort();
  const ranges: [string,string][] = [];
  for (const day of sorted) {
    const last = ranges.at(-1);
    if (last && Date.parse(day) - Date.parse(last[1]) === 86400000) last[1] = day;
    else ranges.push([day,day]);
  }
  return ranges.map(([start,end])=>start===end ? metricDateLabel(start) : `${metricDateLabel(start)} – ${metricDateLabel(end)}`).join(", ");
}
