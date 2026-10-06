import { useEffect, useRef, useState } from "react";
import { previewCalendarPosts, type SocialPost } from "./socialCardData";
import { calendarWindow } from "./postCalendar";

export interface HomeWidgetProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
  preview?: boolean;
}
export interface PublishingSummary {
  posts: SocialPost[];
  total: number;
  attention: { failed: number; approval: number; overdue: number };
}
export interface MetricPoint { date: string; value: number | null; accounts: number }
export interface WidgetMetric {
  value: number | null;
  accounts: number;
  days?: number;
  change_percent?: number;
  trend: MetricPoint[];
}
export interface PerformanceSummary {
  days: number;
  start_date: string;
  end_date: string;
  updated_at: string;
  accounts: { id: number; name: string; platform: string; updated_at: string; metrics: Record<string, WidgetMetric> }[];
  metrics: Record<string, WidgetMetric>;
}

function settingInteger(settings: HomeWidgetProps["widgetSettings"], key: string, fallback: number, min: number, max: number) {
  const value = Number(settings?.[key] ?? fallback);
  return Number.isSafeInteger(value) ? Math.max(min, Math.min(max, value)) : fallback;
}

export function widgetPreferences(props: HomeWidgetProps) {
  const settings = props.widgetSettings;
  const rawIDs = String(settings?.account_ids ?? "").trim();
  // Keep invalid settings visible to the API, which reports a scope error.
  // Silently dropping an invalid ID would widen the requested scope.
  const accountIDs = rawIDs;
  return {
    profileID: settingInteger(settings, "profile_id", 0, 0, Number.MAX_SAFE_INTEGER),
    accountIDs,
    maxPosts: settingInteger(settings, "max_posts", 5, 1, 20),
    horizonDays: settingInteger(settings, "horizon_days", 7, 1, 42),
    mode: settings?.view === "calendar" || (settings?.view !== "upcoming" && props.widgetSize === "full") ? "calendar" : "upcoming",
    days: [7, 28, 90].includes(Number(settings?.days)) ? Number(settings?.days) : 28,
    visibilityMetric: settings?.visibility_metric === "impressions" ? "impressions" : "views",
    showTrends: settings?.show_trends !== false,
    showAttention: settings?.show_attention !== false,
  };
}

export function widgetURL(props: HomeWidgetProps, path: string, values: Record<string, string | number> = {}) {
  const prefs = widgetPreferences(props);
  const params = new URLSearchParams(Object.entries(values).map(([key, value]) => [key, String(value)]));
  if (props.projectId) params.set("project_id", props.projectId);
  if (props.installId) params.set("install_id", String(props.installId));
  if (prefs.profileID) params.set("profile_id", String(prefs.profileID));
  if (prefs.accountIDs) params.set("account_ids", prefs.accountIDs);
  return `/api/apps/${encodeURIComponent(props.appName || "social")}${path}?${params}`;
}

export function socialPageLink(props: HomeWidgetProps, values: Record<string, string | number> = {}) {
  const prefs = widgetPreferences(props);
  const params = new URLSearchParams({ profile_id: String(prefs.profileID) });
  if (prefs.accountIDs) params.set("account_ids", prefs.accountIDs);
  if (props.installId) params.set("install_id", String(props.installId));
  for (const [key, value] of Object.entries(values)) params.set(key, String(value));
  return `/apps/${encodeURIComponent(props.appName || "social")}/page?${params}`;
}

export function publishingWindow(now: Date, mode: string, horizon: number, cursor: Date) {
  if (mode === "calendar") return calendarWindow(cursor, "week");
  const end = new Date(now);
  end.setDate(end.getDate() + horizon);
  return { start: now, end, days: [] as Date[] };
}

// Old project/settings data is hidden immediately, even if an aborted request
// finishes later. Host event revisions and a polling fallback refresh reads.
export function useWidgetData<T>(url: string, props: HomeWidgetProps, previewData: () => T) {
  const key = `${props.preview ? "preview" : props.projectId || "missing"}:${url}`;
  const [state, setState] = useState<{ key: string; data?: T; error?: string }>({ key });
  const [revision, setRevision] = useState(0);
  const previewRef = useRef(previewData); previewRef.current = previewData;
  useEffect(() => {
    if (props.preview) { setState({ key, data: previewRef.current() }); return; }
    if (!props.projectId) { setState({ key, error: "Select a project to view Social data." }); return; }
    let alive = true;
    let controller: AbortController | undefined;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    let requestID = 0;
    const load = async () => {
      controller?.abort(); clearTimeout(timeout);
      controller = new AbortController();
      const signal = controller.signal;
      const request = ++requestID;
      timeout = setTimeout(() => controller?.abort(), 15_000);
      try {
        const response = await fetch(url, { credentials: "same-origin", signal });
        if (!response.ok) throw new Error((await response.text()).trim() || `Request failed (${response.status})`);
        const data = await response.json() as T;
        if (alive && request === requestID) setState({ key, data });
      } catch (cause) {
        if (alive && request === requestID) {
          const message = signal.aborted ? "Request timed out. Try again." : cause instanceof Error ? cause.message : "Couldn't load Social data.";
          setState(previous => ({ key, data: previous.key === key ? previous.data : undefined, error: message }));
        }
      } finally { if (request === requestID) clearTimeout(timeout); }
    };
    void load();
    const poll = setInterval(() => void load(), 60_000);
    return () => { alive = false; requestID++; controller?.abort(); clearTimeout(timeout); clearInterval(poll); };
  }, [key, url, props.preview, props.projectId, props.eventRevision, revision]);
  return { data: state.key === key ? state.data : undefined, error: state.key === key ? state.error : undefined, retry: () => setRevision(value => value + 1) };
}

export function previewPublishingSummary(): PublishingSummary {
  return { posts: previewCalendarPosts, total: previewCalendarPosts.length, attention: { failed: 1, approval: 2, overdue: 0 } };
}

export function previewPerformanceSummary(days = 28): PerformanceSummary {
  const today = new Date();
  const metric = (total: number): WidgetMetric => ({ value: total, accounts: 2, days, change_percent: 12.5,
    trend: Array.from({ length: days }, (_, i) => {
      const date = new Date(today); date.setDate(date.getDate() - days + i);
      return { date: date.toISOString().slice(0, 10), value: Math.round(total / days * (0.6 + i / (days * 1.25))), accounts: 2 };
    }) });
  const metrics = { followers: { ...metric(8420), change_percent: undefined, trend: [] }, views: metric(24180), impressions: metric(35200), interactions: metric(1350) };
  const start = new Date(today); start.setDate(start.getDate() - days);
  const end = new Date(today); end.setDate(end.getDate() - 1);
  const accountMetrics = (fraction: number) => Object.fromEntries(Object.entries(metrics).map(([name, value]) => [name, { ...value, accounts: 1, value: Math.round(value.value! * fraction), trend: value.trend.map(point => ({ ...point, accounts: 1, value: Math.round(point.value! * fraction) })) }]));
  return { days, start_date: start.toISOString().slice(0, 10), end_date: end.toISOString().slice(0, 10), updated_at: today.toISOString(), metrics,
    accounts: [{ id: 1, name: "Studio", platform: "youtube", updated_at: today.toISOString(), metrics: accountMetrics(0.6) }, { id: 2, name: "Brand", platform: "facebook", updated_at: today.toISOString(), metrics: accountMetrics(0.4) }] };
}

export function trendPath(points: MetricPoint[], width = 280, height = 64) {
  const values = points.filter(point => point.value != null).map(point => point.value!);
  if (values.length < 2) return "";
  const max = Math.max(1, ...values);
  let previousPresent = false;
  return points.map((point, index) => {
    if (point.value == null) { previousPresent = false; return ""; }
    const command = previousPresent ? "L" : "M"; previousPresent = true;
    return `${command}${(index / Math.max(1, points.length - 1) * width).toFixed(2)},${(height - point.value / max * (height - 8) - 4).toFixed(2)}`;
  }).join(" ");
}
