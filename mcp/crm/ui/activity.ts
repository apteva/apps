import { crmInboxIsGlobal, type CRMInboxHostProps, type InboxProject } from "./inbox";

export type ActivityPeriod = 7 | 30 | 90;
export type ActivityMetric = "messages_received" | "messages_sent" | "new_contacts";

export interface ActivityTotals {
  new_contacts: number;
  messages_received: number;
  messages_sent: number;
  send_failures: number;
  conversations_opened: number;
  conversations_closed: number;
}

export interface ActivityDay {
  date: string;
  new_contacts: number;
  messages_received: number;
  messages_sent: number;
}

export interface ActivityContact {
  id: number;
  name?: string;
  email?: string;
  phone?: string;
  source?: string;
  created_at: string;
  project_id?: string;
  project_name?: string;
}

export interface ActivityStats {
  period_days: number;
  since: string;
  until: string;
  hide_automated: boolean;
  totals: ActivityTotals;
  previous: ActivityTotals;
  open_now: number;
  daily: ActivityDay[];
  latest_contacts: ActivityContact[];
}

export interface ActivityResponse {
  stats: ActivityStats;
  projects?: InboxProject[];
  selected_project_id?: string;
}

export interface ActivityPreferences {
  period: ActivityPeriod;
  hideAutomated: boolean;
  metric: ActivityMetric;
}

export const activityMetrics: { key: ActivityMetric; label: string; unit: string }[] = [
  { key: "messages_received", label: "Received", unit: "received" },
  { key: "messages_sent", label: "Sent", unit: "sent" },
  { key: "new_contacts", label: "New contacts", unit: "new contacts" },
];

export function crmActivityPreferences(settings?: Record<string, unknown>): ActivityPreferences {
  const period = Number(settings?.period ?? 7);
  const metric = settings?.default_metric;
  return {
    period: period === 30 || period === 90 ? period : 7,
    hideAutomated: settings?.hide_automated === undefined ? true : settings.hide_automated === true || settings.hide_automated === "true",
    metric: metric === "messages_sent" || metric === "new_contacts" ? metric : "messages_received",
  };
}

export function crmActivityAPIURL(props: CRMInboxHostProps, preferences: ActivityPreferences, selectedProject = ""): string {
  const query = new URLSearchParams({ period: String(preferences.period) });
  if (preferences.hideAutomated) query.set("hide_automated", "1");
  if (props.installId) query.set("install_id", String(props.installId));
  if (crmInboxIsGlobal(props)) {
    query.set("scope", "global");
    if (selectedProject) query.set("project_id", selectedProject);
  } else if (props.projectId) {
    query.set("project_id", props.projectId);
  }
  return `/api/apps/${encodeURIComponent(props.appName || "crm")}/stats?${query.toString()}`;
}

// Change against the previous period of the same length.
export function crmActivityDelta(current: number, previous: number): { label: string; direction: "up" | "down" | "flat" } {
  if (current === previous) return { label: previous === 0 ? "No change" : "Same as before", direction: "flat" };
  if (previous === 0) return { label: `+${current} vs prev.`, direction: "up" };
  const pct = Math.round(((current - previous) / previous) * 100);
  return { label: `${pct > 0 ? "+" : ""}${pct}% vs prev.`, direction: pct >= 0 ? "up" : "down" };
}

export function crmActivityPeriodLabel(days: number): string {
  return days === 7 ? "Last 7 days" : days === 30 ? "Last 30 days" : `Last ${days} days`;
}

export function crmActivityDayLabel(date: string): string {
  const parsed = Date.parse(`${date}T00:00:00Z`);
  if (!Number.isFinite(parsed)) return date;
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", timeZone: "UTC" }).format(new Date(parsed));
}

export function crmActivityContactLabel(contact: ActivityContact): string {
  return contact.name?.trim() || contact.email?.trim() || contact.phone?.trim() || `Contact #${contact.id}`;
}

// The CRM contacts page, in the contact's own project when it has one.
export function crmActivityContactsLink(props: CRMInboxHostProps, projectId = ""): string | null {
  const project = projectId || (crmInboxIsGlobal(props) ? "" : props.projectId || "");
  if (!project) return null;
  const query = new URLSearchParams({ tab: "contacts", project_id: project });
  if (props.installId) query.set("install_id", String(props.installId));
  return `/apps/${encodeURIComponent(props.appName || "crm")}/page?${query.toString()}`;
}
