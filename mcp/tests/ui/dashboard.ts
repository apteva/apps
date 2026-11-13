export type WidgetHostProps = {
  appName?: string; projectId?: string; installId?: number; widgetId?: string;
  eventRevision?: number; widgetSize?: "half" | "full"; widgetSettings?: Record<string, unknown>;
};
export type WidgetPreferences = { hours: 24 | 168; suite: number; environment: string; limit: number; group: boolean };
export type WidgetOptions = { suites: { id: number; name: string; archived: boolean }[]; environments: string[] };
export type WidgetRun = { id: number; suite_id: number; suite_name: string; environment: string; status: string; passed: number; failed: number; created_at: string; duration_ms: number | null };
export type WidgetFailure = { run_id: number; suite_id: number; suite_name: string; environment: string; check_id: number; check_name: string; step_id?: string; message: string; created_at: string; occurrences: number; has_screenshot: boolean };
export type WidgetDay = { date: string; runs: number; passed: number; failed: number; cancelled: number };
export type WidgetData = {
  as_of: string; from: string; hours: number; options: WidgetOptions;
  stats?: { runs: number; passed: number; failed: number; cancelled: number; running: number; queued: number; pass_rate: number | null };
  recent?: WidgetRun[]; trend?: WidgetDay[]; failures: WidgetFailure[]; total: number;
};
export function widgetPreferences(settings?: Record<string, unknown>): WidgetPreferences {
  const suite = Number(settings?.suite_id || 0), limit = Number(settings?.max_results ?? 8);
  return { hours: settings?.window === "7d" ? 168 : 24, suite: Number.isSafeInteger(suite) && suite > 0 ? suite : 0,
    environment: typeof settings?.environment === "string" ? settings.environment.slice(0, 200) : "",
    limit: Number.isFinite(limit) ? Math.max(3, Math.min(10, Math.round(limit))) : 8, group: settings?.group_repeated !== false };
}
export function dashboardURL(props: WidgetHostProps, kind: "overview" | "failures", preferences: WidgetPreferences): string {
  const query = new URLSearchParams({ project_id: props.projectId || "", hours: String(preferences.hours), limit: String(props.widgetSize === "full" ? preferences.limit : Math.min(3, preferences.limit)), group: String(preferences.group) });
  if (props.installId) query.set("install_id", String(props.installId));
  if (preferences.suite) query.set("suite_id", String(preferences.suite));
  if (preferences.environment) query.set("environment", preferences.environment);
  return `/api/apps/${encodeURIComponent(props.appName || "tests")}/dashboard/${kind}?${query}`;
}
export function testsPageLink(props: WidgetHostProps, run?: { id: number; suite_id: number }): string {
  const query = new URLSearchParams();
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  if (run) { query.set("suite_id", String(run.suite_id)); query.set("run_id", String(run.id)); }
  return `/apps/${encodeURIComponent(props.appName || "tests")}/page?${query}`;
}
export function testsPageRoute(search: string): { suite: number; run: number } {
  const q = new URLSearchParams(search), positive = (v: string | null) => { const n = Number(v); return Number.isSafeInteger(n) && n > 0 ? n : 0; };
  return { suite: positive(q.get("suite_id")), run: positive(q.get("run_id")) };
}
export function relativeTime(value: string, now = Date.now()): string {
  const time = Date.parse(value); if (!Number.isFinite(time)) return "Unknown time";
  const seconds = Math.max(0, Math.floor((now - time) / 1000));
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}
export function duration(ms: number | null): string {
  if (ms === null) return "";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)}s`;
  return `${Math.floor(ms / 60000)}m ${Math.floor(ms / 1000) % 60}s`;
}
