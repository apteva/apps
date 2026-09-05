export interface DashboardWidgetResult {
  widget_id: number;
  data?: Record<string, unknown>;
  error?: string;
}

export interface DashboardChoice {
  id: number;
}

export interface DashboardFilterDefault {
  key: string;
  type?: string;
  default?: string;
}

export function defaultDashboardFilters(
  filters: DashboardFilterDefault[],
): Record<string, string> {
  return Object.fromEntries(
    filters.map((filter) => [
      filter.key,
      filter.default || (filter.type === "date_window" ? "30d" : "all"),
    ]),
  );
}

export function selectDashboardID(
  rows: DashboardChoice[],
  preferredID: number,
  storedID: number,
): number {
  return (
    rows.find((row) => row.id === preferredID)?.id ||
    rows.find((row) => row.id === storedID)?.id ||
    rows[0]?.id ||
    0
  );
}

export interface ObjectiveQueryLike {
  aggregation?: string;
  app?: string;
  topic?: string;
  source?: string;
  value?: string;
  by?: string;
  where?: Record<string, unknown>;
  currency_field?: string;
  reporting_currency?: string;
  amount_unit?: string;
  rate_date_field?: string;
}

export function metricAggregation(config: Record<string, unknown>): string {
  const explicit =
    typeof config.aggregation === "string"
      ? config.aggregation.toLowerCase().trim()
      : "";
  if (explicit === "avg") return "average";
  if (explicit) return explicit;
  if (
    typeof config.by === "string" &&
    config.by &&
    !(typeof config.value === "string" && config.value)
  )
    return "distinct";
  if (typeof config.value === "string" && config.value) return "sum";
  return "count";
}

export function objectiveTargetIDs(config: Record<string, unknown>): number[] {
  if (!Array.isArray(config.objective_target_ids)) return [];
  return [
    ...new Set(
      config.objective_target_ids
        .map(Number)
        .filter((id) => Number.isSafeInteger(id) && id > 0),
    ),
  ];
}

function stableRecord(value: unknown): string {
  if (!value || typeof value !== "object" || Array.isArray(value)) return "{}";
  return JSON.stringify(
    Object.fromEntries(
      Object.entries(value as Record<string, unknown>).sort(([a], [b]) =>
        a.localeCompare(b),
      ),
    ),
  );
}

export function objectiveMatchesMetric(
  config: Record<string, unknown>,
  query: ObjectiveQueryLike,
): boolean {
  const aggregation = metricAggregation(config);
  const targetAggregation =
    query.aggregation === "avg" ? "average" : query.aggregation || "count";
  if (aggregation !== targetAggregation) return false;
  for (const key of ["app", "topic", "source"] as const) {
    if (String(config[key] ?? "") !== String(query[key] ?? "")) return false;
  }
  if (
    ["sum", "sum_money", "average", "min", "max", "latest", "change"].includes(
      aggregation,
    )
  ) {
    if (String(config.value ?? "") !== String(query.value ?? "")) return false;
  }
  if (aggregation === "sum_money") {
    for (const key of [
      "currency_field",
      "reporting_currency",
      "amount_unit",
      "rate_date_field",
    ] as const) {
      if (String(config[key] ?? "") !== String(query[key] ?? "")) return false;
    }
  }
  if (aggregation === "distinct") {
    if (String(config.by ?? config.value ?? "") !== String(query.by ?? ""))
      return false;
  }
  return stableRecord(config.where) === stableRecord(query.where);
}

export function formatMetric(
  value: number | null,
  config: Record<string, unknown>,
): string {
  if (value == null || !Number.isFinite(value)) return "—";
  const raw = value;
  const configuredScale = Number(config.scale ?? 1);
  const numeric =
    raw * (Number.isFinite(configuredScale) ? configuredScale : 1);
  const configuredDecimals = Number(config.decimals);
  const hasConfiguredDecimals = Number.isFinite(configuredDecimals);
  const decimals = Math.max(
    0,
    Math.min(6, hasConfiguredDecimals ? Math.floor(configuredDecimals) : 2),
  );
  const moneyAggregation = metricAggregation(config) === "sum_money";
  if (config.format === "currency" || moneyAggregation) {
    const currency =
      typeof config.currency === "string" && config.currency
        ? config.currency
        : typeof config.reporting_currency === "string" &&
            config.reporting_currency
          ? config.reporting_currency
          : "USD";
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: /^[A-Za-z]{3}$/.test(currency) ? currency.toUpperCase() : "USD",
      minimumFractionDigits: hasConfiguredDecimals ? decimals : 0,
      maximumFractionDigits: decimals,
    }).format(numeric);
  }
  if (config.format === "percent") {
    return new Intl.NumberFormat("en-US", {
      style: "percent",
      minimumFractionDigits: hasConfiguredDecimals ? decimals : 0,
      maximumFractionDigits: hasConfiguredDecimals ? decimals : 1,
    }).format(config.percent_input === "points" ? numeric / 100 : numeric);
  }
  const formatted = new Intl.NumberFormat("en-US", {
    ...(config.format === "compact" || config.compact === true
      ? { notation: "compact" as const }
      : {}),
    ...(hasConfiguredDecimals
      ? { minimumFractionDigits: decimals, maximumFractionDigits: decimals }
      : {}),
  }).format(numeric);
  return typeof config.unit === "string" && config.unit
    ? `${formatted} ${config.unit}`
    : formatted;
}

export interface AreaChartPoint {
  index?: number;
  x: number;
  y: number;
}

export function areaChartGeometry(
  values: Array<number | null>,
  width = 300,
  height = 72,
  timestamps?: number[],
): {
  points: AreaChartPoint[];
  linePath: string;
  areaPath: string;
  baseline: number;
} {
  const pad = 8,
    top = 8,
    bottom = height - 6;
  const valid = values.filter(
    (v): v is number => v != null && Number.isFinite(v),
  );
  const min = Math.min(0, ...valid),
    max = Math.max(1, ...valid),
    span = max - min;
  const y = (value: number) => bottom - ((value - min) / span) * (bottom - top);
  const baseline = y(0);
  const first = timestamps?.[0] ?? 0,
    last = timestamps?.at(-1) ?? values.length - 1;
  const points: AreaChartPoint[] = [];
  const segments: AreaChartPoint[][] = [];
  let segment: AreaChartPoint[] = [];
  values.forEach((value, index) => {
    if (value == null || !Number.isFinite(value)) {
      if (segment.length) segments.push(segment);
      segment = [];
      return;
    }
    const time = timestamps?.[index] ?? index;
    const point = {
      index,
      x:
        last > first
          ? pad + ((time - first) / (last - first)) * (width - pad * 2)
          : width / 2,
      y: y(value),
    };
    points.push(point);
    segment.push(point);
  });
  if (segment.length) segments.push(segment);
  const path = (points: AreaChartPoint[]) =>
    points.map((p, i) => `${i === 0 ? "M" : "L"} ${p.x} ${p.y}`).join(" ");
  return {
    points,
    baseline,
    linePath: segments.map(path).join(" "),
    areaPath: segments
      .map(
        (ps) =>
          `${path(ps)} L ${ps.at(-1)!.x} ${baseline} L ${ps[0].x} ${baseline} Z`,
      )
      .join(" "),
  };
}

export function areaChartEndMarkerPath(
  points: AreaChartPoint[],
  baseline: number,
): string {
  const point = points.at(-1) ?? { x: 292, y: baseline };
  // A nearly zero-length path with a round, non-scaling stroke renders as a
  // true screen-space dot. An SVG circle is distorted when the chart uses
  // preserveAspectRatio="none" to fill a wide dashboard card.
  return `M ${point.x} ${point.y} h 0.001`;
}

export function resolveMetricConfig(
  config: Record<string, unknown>,
  filters: Record<string, string>,
): Record<string, unknown> {
  const out = { ...config };
  for (const [key, value] of Object.entries(out)) {
    if (typeof value === "string" && value.startsWith("$filters.")) {
      out[key] = filters[value.slice("$filters.".length)] || value;
    }
  }
  return out;
}

export function resolvedWindow(
  config: Record<string, unknown>,
  filters: Record<string, string>,
): string {
  const raw = typeof config.window === "string" ? config.window : "24h";
  if (!raw.startsWith("$filters.")) return raw;
  return filters[raw.slice("$filters.".length)] || "all";
}

export function isCurrentRequest(
  responseSequence: number,
  currentSequence: number,
): boolean {
  return responseSequence === currentSequence;
}

export function scopedAppURL(path: string, projectID: string): string {
  const separator = path.includes("?") ? "&" : "?";
  return `${path}${separator}project_id=${encodeURIComponent(projectID)}`;
}

export function batchResultsByID(
  results: DashboardWidgetResult[],
): Record<number, Record<string, unknown>> {
  return Object.fromEntries(
    results.map((result) => [
      result.widget_id,
      result.error ? { error: result.error } : (result.data ?? {}),
    ]),
  );
}

export function dedupeWidgetGoals(
  widgetIDs: number[],
  dataByWidget: Record<number, Record<string, any>>,
): Record<number, Record<string, any>> {
  const seen = new Set<number>();
  const out = { ...dataByWidget };
  for (const widgetID of widgetIDs) {
    const data = dataByWidget[widgetID];
    if (!data || !Array.isArray(data.goals)) continue;
    out[widgetID] = {
      ...data,
      goals: data.goals.filter((goal: Record<string, unknown>) => {
        const targetID = Number(goal.target_id);
        if (!Number.isSafeInteger(targetID) || targetID <= 0) return true;
        if (seen.has(targetID)) return false;
        seen.add(targetID);
        return true;
      }),
    };
  }
  return out;
}

export function partitionDashboardWidgets<T extends { type: string }>(
  widgets: T[],
): { stats: T[]; charts: T[] } {
  return {
    stats: widgets.filter((widget) => widget.type === "stat"),
    charts: widgets.filter((widget) => widget.type !== "stat"),
  };
}

export function objectiveMonthBounds(month: string): {
  start: number;
  end: number;
} {
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

export function formatObjectiveValue(
  value: number,
  unit: string,
  currency = "",
): string {
  if (unit === "money") {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: /^[A-Za-z]{3}$/.test(currency) ? currency.toUpperCase() : "USD",
      maximumFractionDigits: 2,
    }).format(value);
  }
  if (unit === "percent")
    return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
  return value.toLocaleString(undefined, { maximumFractionDigits: 2 });
}

export function formatObjectivePeriod(
  start: number,
  end: number,
  timezone = "UTC",
): string {
  const formatter = new Intl.DateTimeFormat("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
    timeZone: timezone || "UTC",
  });
  return `${formatter.format(new Date(start))} - ${formatter.format(new Date(end - 1))}`;
}
