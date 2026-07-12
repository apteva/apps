export interface DashboardWidgetResult {
  widget_id: number;
  data?: Record<string, unknown>;
  error?: string;
}

export function formatMetric(value: number, config: Record<string, unknown>): string {
  const numeric = Number.isFinite(value) ? value : 0;
  if (config.format === "currency") {
    const currency = typeof config.currency === "string" && config.currency ? config.currency : "USD";
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency,
      minimumFractionDigits: 0,
      maximumFractionDigits: 2,
    }).format(numeric);
  }
  if (config.format === "percent") {
    return new Intl.NumberFormat("en-US", { style: "percent", maximumFractionDigits: 1 }).format(numeric);
  }
  return numeric.toLocaleString();
}

export function resolvedWindow(config: Record<string, unknown>, filters: Record<string, string>): string {
  const raw = typeof config.window === "string" ? config.window : "24h";
  if (!raw.startsWith("$filters.")) return raw;
  return filters[raw.slice("$filters.".length)] || "all";
}

export function isCurrentRequest(responseSequence: number, currentSequence: number): boolean {
  return responseSequence === currentSequence;
}

export function scopedAppURL(path: string, projectID: string): string {
  const separator = path.includes("?") ? "&" : "?";
  return `${path}${separator}project_id=${encodeURIComponent(projectID)}`;
}

export function batchResultsByID(results: DashboardWidgetResult[]): Record<number, Record<string, unknown>> {
  return Object.fromEntries(
    results.map((result) => [
      result.widget_id,
      result.error ? { error: result.error } : (result.data ?? {}),
    ]),
  );
}

export function partitionDashboardWidgets<T extends { type: string }>(widgets: T[]): { stats: T[]; charts: T[] } {
  return {
    stats: widgets.filter((widget) => widget.type === "stat"),
    charts: widgets.filter((widget) => widget.type !== "stat"),
  };
}
