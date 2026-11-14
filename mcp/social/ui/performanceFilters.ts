import {
  widgetPreferences,
  widgetURL,
  type HomeWidgetProps,
  type PerformanceSummary,
} from "./homeWidgetData";

export interface PerformanceOptions {
  profiles: { id: number; name: string }[];
  accounts: {
    id: number;
    name: string;
    platform: string;
    profile_id: number;
    profile_name: string;
  }[];
  platforms: string[];
}
export interface PerformanceFilters {
  profileID: number;
  platform: string;
  accountIDs: string;
  period: string;
  startDate: string;
  endDate: string;
}
export type PerformanceSort =
  | "name"
  | "profile_name"
  | "platform"
  | "followers"
  | "views"
  | "impressions"
  | "interactions";
export const performanceMetrics = [
  "followers",
  "views",
  "impressions",
  "interactions",
] as const;
export type PerformanceMetric = (typeof performanceMetrics)[number];

export function performanceDisplay(props: HomeWidgetProps) {
  const settings = props.widgetSettings || {};
  const columns = performanceMetrics.filter(
    (metric) => settings[`show_${metric}`] !== false,
  );
  const requested = performanceMetrics.includes(
    settings.ranking_metric as PerformanceMetric,
  )
    ? (settings.ranking_metric as PerformanceMetric)
    : "views";
  const ranking: PerformanceSort = columns.includes(requested)
    ? requested
    : columns.length > 0
      ? columns[0]
      : "name";
  const limit = Number(settings.max_accounts ?? 10);
  return {
    columns,
    ranking,
    maxAccounts: Number.isSafeInteger(limit)
      ? Math.max(1, Math.min(100, limit))
      : 10,
    showSummary: settings.show_summary !== false,
    showAccounts: settings.show_accounts !== false,
    showFilters: settings.show_filters !== false,
    showProfile: settings.show_profile !== false,
    showPlatform: settings.show_platform !== false,
  };
}
const stamp = (date: Date) => date.toISOString().slice(0, 10);
export function completePerformanceDates(now = new Date()) {
  const end = new Date(
    Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - 1),
  );
  const start = new Date(end);
  start.setUTCDate(start.getUTCDate() - 27);
  return { startDate: stamp(start), endDate: stamp(end) };
}
export function performanceDefaults(
  props: HomeWidgetProps,
): PerformanceFilters {
  const prefs = widgetPreferences(props),
    dates = completePerformanceDates();
  const settings = props.widgetSettings || {};
  return {
    profileID: prefs.profileID,
    platform: String(settings.platform || "all"),
    accountIDs: prefs.accountIDs,
    period:
      settings.start_date || settings.end_date ? "custom" : String(prefs.days),
    startDate: String(settings.start_date || dates.startDate),
    endDate: String(settings.end_date || dates.endDate),
  };
}
export function performanceDateError(
  filters: PerformanceFilters,
  now = new Date(),
): string {
  if (filters.period !== "custom") return "";
  const dates = [filters.startDate, filters.endDate];
  if (
    dates.some(
      (value) =>
        !/^\d{4}-\d{2}-\d{2}$/.test(value) ||
        !Number.isFinite(Date.parse(value)) ||
        stamp(new Date(value)) !== value,
    )
  )
    return "Choose a valid start and end date.";
  const days =
    (Date.parse(filters.endDate) - Date.parse(filters.startDate)) / 86400000 +
    1;
  if (days < 1 || days > 366) return "Choose a range of 1–366 days.";
  if (filters.endDate > completePerformanceDates(now).endDate)
    return "Choose completed UTC days; today is still in progress.";
  return "";
}
export function performanceURL(
  props: HomeWidgetProps,
  filters: PerformanceFilters,
) {
  return widgetURL(
    {
      ...props,
      widgetSettings: {
        ...props.widgetSettings,
        profile_id: filters.profileID,
        account_ids: filters.accountIDs,
      },
    },
    "/widgets/performance",
    {
      ...(filters.period === "custom"
        ? { start_date: filters.startDate, end_date: filters.endDate }
        : { days: Number(filters.period) }),
      ...(filters.platform !== "all" ? { platforms: filters.platform } : {}),
    },
  );
}
export function sortPerformanceAccounts(
  accounts: PerformanceSummary["accounts"],
  key: PerformanceSort,
  descending: boolean,
) {
  const value = (
    a: PerformanceSummary["accounts"][number],
  ): string | number | null | undefined =>
    key === "name" || key === "platform" || key === "profile_name"
      ? a[key] || ""
      : a.metrics[key]?.value;
  return [...accounts].sort((a, b) => {
    const x = value(a),
      y = value(b);
    if (x == null || y == null)
      return x == null && y == null ? a.id - b.id : x == null ? 1 : -1;
    const compare =
      typeof x === "string" && typeof y === "string"
        ? x.localeCompare(y)
        : Number(x) - Number(y);
    return compare === 0 ? a.id - b.id : descending ? -compare : compare;
  });
}
