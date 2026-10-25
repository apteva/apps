import type { AudioDashboardHost } from "./audio-dashboard";
export type StatisticsFilters = {
  range: "today" | "24h" | "7d" | "custom";
  from: string;
  until: string;
  provider: string;
  adviser: string;
  number: string;
  direction: string;
  threshold: string;
};
export const defaultStatisticsFilters: StatisticsFilters = {
  range: "24h",
  from: "",
  until: "",
  provider: "",
  adviser: "",
  number: "",
  direction: "",
  threshold: "200",
};
export type Totals = {
  metric_calls?: Record<string, number>;
  calls: number;
  answered: number;
  missed: number;
  failed: number;
  active: number;
  observed: number;
  problems: number;
  pending: number;
  talk_seconds: number;
  duration_calls?: number;
  average_seconds: number | null;
  problem_percent: number | null;
  coverage_percent: number | null;
  sip_discard_ms: number;
  sip_concealed_ms: number;
  browser_discard_ms: number;
  browser_concealed_ms: number;
  capture_discard_ms: number;
  ingress_discard_ms: number;
  playback_gap_ms: number;
  send_discard_ms: number;
  sip_missing_packets: number;
  browser_lost_packets: number;
  send_duration_ms: number;
  send_rate_calls: number;
  send_discard_percent: number | null;
};
export type StatisticsBucket = Totals & { from: string; until: string };
export type StatisticsGroup = Totals & { key: string; label: string };
export type StatisticsResponse = {
  totals: Totals;
  previous: Totals;
  buckets: StatisticsBucket[];
  breakdowns: Record<
    "provider" | "adviser" | "number",
    { items: StatisticsGroup[]; total_groups: number; omitted_groups: number }
  >;
  calls: {
    call_id: string;
    status: string;
    direction: string;
    provider: string;
    from: string;
    to: string;
    started_at: string;
    adviser: string;
    observed: boolean;
    problem: boolean;
    talk_seconds: number;
  }[];
  matching_calls: number;
  next_cursor: string;
  from: string;
  until: string;
  previous_from: string;
  generated_at: string;
  timezone: string;
  pending_reports: number;
  query_ms: number;
};
export type StatisticsSelection = {
  from: string;
  until: string;
  anchor: number;
} | null;
export function statisticsURL(
  host: AudioDashboardHost,
  f: StatisticsFilters,
  selection: StatisticsSelection,
  focus: string,
  cursor: string,
  now = Date.now(),
  timezone = Intl.DateTimeFormat().resolvedOptions().timeZone,
): string {
  const at = selection?.anchor ?? now;
  let from = at - 86400000,
    until = at;
  if (f.range === "today") {
    const date = new Date(at);
    date.setHours(0, 0, 0, 0);
    from = date.getTime();
  }
  if (f.range === "7d") from = at - 7 * 86400000;
  if (f.range === "custom") {
    from = Date.parse(f.from);
    until = Date.parse(f.until);
  }
  if (
    !Number.isFinite(from) ||
    !Number.isFinite(until) ||
    from >= until ||
    until - from > 31 * 86400000
  )
    throw new Error("Choose a valid date range of at most 31 days.");
  const q = new URLSearchParams({
    view: "statistics",
    project_id: host.projectId ?? "",
    install_id: String(host.installId ?? ""),
    from: new Date(from).toISOString(),
    until: new Date(until).toISOString(),
    timezone,
    threshold_ms: f.threshold,
  });
  for (const key of ["provider", "adviser", "number", "direction"] as const)
    if (f[key]) q.set(key, f[key]);
  if (selection) {
    q.set("segment_from", selection.from);
    q.set("segment_until", selection.until);
  }
  if (focus) q.set("focus", focus);
  if (cursor) q.set("cursor", cursor);
  return `/api/apps/${encodeURIComponent(host.appName || "telephony")}/audio-health?${q}`;
}
export const percent = (n: number | null | undefined) =>
  n == null ? "—" : `${n.toFixed(1)}%`;
export function duration(seconds: number | null | undefined) {
  if (seconds == null) return "—";
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  if (seconds < 3600) return `${(seconds / 60).toFixed(1)}m`;
  return `${(seconds / 3600).toFixed(1)}h`;
}
export function comparison(
  current: number | null,
  previous: number | null,
  points = false,
) {
  if (current === null || previous === null)
    return "No comparable observations";
  if (points)
    return `${current - previous >= 0 ? "+" : ""}${(current - previous).toFixed(1)} pp vs previous`;
  if (previous === 0)
    return current === 0 ? "No change vs previous" : "No previous baseline";
  const d = ((current - previous) / previous) * 100;
  return `${d >= 0 ? "+" : ""}${d.toFixed(1)}% vs previous`;
}
