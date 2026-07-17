export const ACCOUNT_METRICS_STALE_MS = 4 * 60 * 60 * 1000;

export interface AccountMetricsFreshness {
  status?: string;
  updated_at?: string;
}

export interface MetricPoint {
  time?: string;
  value: number;
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
    .filter(({ points }) => points.length >= 2 && points.some((point) => Number(point.value) !== 0));
}
