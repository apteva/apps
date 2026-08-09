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

export function objectiveMonthBounds(month: string): { start: number; end: number } {
  if (!/^\d{4}-\d{2}$/.test(month)) throw new Error("month must be YYYY-MM");
  const [year, monthNumber] = month.split("-").map(Number);
  if (monthNumber < 1 || monthNumber > 12) throw new Error("invalid month");
  return {
    start: Date.UTC(year, monthNumber - 1, 1),
    end: Date.UTC(year, monthNumber, 1),
  };
}

export function objectiveProgressWidth(percent?: number): number {
  if (!Number.isFinite(percent)) return 0;
  return Math.max(0, Math.min(100, percent as number));
}

export function formatObjectiveValue(value: number, unit: string, currency = ""): string {
  if (unit === "money") {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: currency || "USD",
      maximumFractionDigits: 2,
    }).format(value);
  }
  if (unit === "percent") return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
  return value.toLocaleString(undefined, { maximumFractionDigits: 2 });
}

export function formatObjectivePeriod(start: number, end: number, timezone = "UTC"): string {
  const formatter = new Intl.DateTimeFormat("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
    timeZone: timezone || "UTC",
  });
  return `${formatter.format(new Date(start))} - ${formatter.format(new Date(end - 1))}`;
}
