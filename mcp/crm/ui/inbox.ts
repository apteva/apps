import type { MessageAddresses } from "./message_addresses";

export type CRMInboxStatus = "open" | "pending" | "closed" | "spam" | "all";
export type CRMInboxChannel = "all" | "email" | "sms" | "whatsapp";

export interface InboxItem {
  id: number;
  contact_id: number;
  contact_name?: string;
  contact_email?: string;
  contact_phone?: string;
  channel: string;
  channels?: string[];
  subject?: string;
  status: string;
  priority: string;
  last_activity_at: string;
  snippet?: string;
  automated?: boolean;
  last_message_addresses?: MessageAddresses;
  // All projects (scope=global) rows only.
  project_id?: string;
  project_name?: string;
}

export interface InboxProject {
  id: string;
  name: string;
}

export interface InboxResponse {
  inbox?: InboxItem[];
  count?: number;
  total?: number;
  offset?: number;
  // All projects (scope=global) responses only.
  projects?: InboxProject[];
  selected_project_id?: string;
}

// A filtered/paginated queue is not the current conversation. A reply moves
// open -> pending, but refreshing that queue must not navigate away from it.
export function refreshedInboxSelection(
  rows: InboxItem[], current: InboxItem | null, requestedId?: number, resetOnMissing = false,
): InboxItem | null {
  if (current) {
    const refreshed = rows.find(row => String(row.id) === String(current.id));
    if (refreshed) return refreshed;
    if (!resetOnMissing) return current;
  }
  const requested = requestedId == null ? undefined : rows.find(row => String(row.id) === String(requestedId));
  return requested || rows[0] || null;
}

// Display-only retention: never include this extra row in queue totals/offsets.
export function inboxRowsWithSelection(rows: InboxItem[], selected: InboxItem | null): InboxItem[] {
  return selected && !rows.some(row => String(row.id) === String(selected.id))
    ? [selected, ...rows]
    : rows;
}

export interface CRMInboxHostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  // "global" on the All projects Home: no projectId, rows from every project.
  dashboardScope?: "project" | "global";
  // Dashboard-supplied project details; color is "#rgb"/"#rrggbb" or "".
  // Older dashboards omit projects.
  widgetContext?: { projects?: { id: string; name?: string; color?: string }[] };
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

export interface CRMInboxPreferences {
  defaultStatus: "open" | "pending" | "all";
  defaultChannel: CRMInboxChannel;
  maxConversations: number;
}

export interface CRMPanelInitialRoute {
  tab: "contacts" | "inbox" | "opportunities" | "lists" | "segments" | "settings";
  conversationId?: number;
  status: CRMInboxStatus;
}

const panelTabs = new Set<CRMPanelInitialRoute["tab"]>([
  "contacts",
  "inbox",
  "opportunities",
  "lists",
  "segments",
  "settings",
]);

const inboxStatuses = new Set<CRMInboxStatus>(["open", "pending", "closed", "spam", "all"]);

export function crmInboxPreferences(settings?: Record<string, unknown>): CRMInboxPreferences {
  const requestedStatus = settings?.default_status;
  const defaultStatus = requestedStatus === "pending" || requestedStatus === "all" ? requestedStatus : "open";
  const requestedChannel = settings?.default_channel;
  const defaultChannel: CRMInboxChannel = requestedChannel === "email" || requestedChannel === "sms" || requestedChannel === "whatsapp"
    ? requestedChannel
    : "all";
  const requestedLimit = Number(settings?.max_conversations ?? 8);
  return {
    defaultStatus,
    defaultChannel,
    maxConversations: Number.isFinite(requestedLimit)
      ? Math.max(4, Math.min(20, Math.round(requestedLimit)))
      : 8,
  };
}

export function crmInboxIsGlobal(props: CRMInboxHostProps): boolean {
  return props.dashboardScope === "global";
}

// In global scope the request carries scope=global and only the project the
// viewer picked in the filter; the backend decides which projects are visible.
export function crmInboxAPIURL(props: CRMInboxHostProps, preferences: CRMInboxPreferences, selectedProject = ""): string {
  const query = new URLSearchParams({
    status: preferences.defaultStatus,
    limit: String(preferences.maxConversations),
  });
  if (props.installId) query.set("install_id", String(props.installId));
  if (crmInboxIsGlobal(props)) {
    query.set("scope", "global");
    if (selectedProject) query.set("project_id", selectedProject);
  } else if (props.projectId) {
    query.set("project_id", props.projectId);
  }
  if (preferences.defaultChannel !== "all") {
    query.set("filters", JSON.stringify([
      { field: "channel", op: "is", value: preferences.defaultChannel },
    ]));
  }
  return `/api/apps/${encodeURIComponent(props.appName || "crm")}/inbox?${query.toString()}`;
}

// Links open the row's own project (global rows carry project_id), else the
// widget's project.
export function crmInboxPageLink(props: CRMInboxHostProps, item?: InboxItem, projectId = ""): string {
  const query = new URLSearchParams({ tab: "inbox" });
  const project = item?.project_id || projectId || props.projectId;
  if (project) query.set("project_id", project);
  if (props.installId) query.set("install_id", String(props.installId));
  if (item) {
    query.set("conversation_id", String(item.id));
    if (inboxStatuses.has(item.status as CRMInboxStatus)) query.set("status", item.status);
  }
  return `/apps/${encodeURIComponent(props.appName || "crm")}/page?${query.toString()}`;
}

const hexColor = /^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i;

// The project's colour from the dashboard, or "" for the neutral look.
export function crmInboxProjectColor(props: CRMInboxHostProps, projectId?: string): string {
  if (!projectId) return "";
  const color = props.widgetContext?.projects?.find(project => project.id === projectId)?.color?.trim() || "";
  return hexColor.test(color) ? color : "";
}

// The "View inbox" link needs one project: in global scope only once the
// viewer picked one in the filter.
export function crmInboxHeaderLink(props: CRMInboxHostProps, selectedProject = ""): string | null {
  if (!crmInboxIsGlobal(props)) return crmInboxPageLink(props);
  return selectedProject ? crmInboxPageLink(props, undefined, selectedProject) : null;
}

export function crmPanelInitialRoute(search: string): CRMPanelInitialRoute {
  const params = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const requestedTab = params.get("tab") as CRMPanelInitialRoute["tab"] | null;
  const requestedStatus = params.get("status") as CRMInboxStatus | null;
  const rawConversationID = Number(params.get("conversation_id") || 0);
  return {
    tab: requestedTab && panelTabs.has(requestedTab) ? requestedTab : "contacts",
    conversationId: Number.isSafeInteger(rawConversationID) && rawConversationID > 0 ? rawConversationID : undefined,
    status: requestedStatus && inboxStatuses.has(requestedStatus) ? requestedStatus : "open",
  };
}

export function crmInboxContactLabel(item: InboxItem): string {
  return item.contact_name?.trim() || item.contact_email?.trim() || item.contact_phone?.trim() || `Contact #${item.contact_id}`;
}

function collapseSpace(value?: string): string {
  return (value || "").replace(/\s+/g, " ").trim();
}

// Email activity bodies start with the subject ("Subject\n\nbody"), so the
// snippet repeats it. The preview is the body after the subject; it is empty
// when the message has nothing beyond its subject.
export function crmInboxPreview(item: InboxItem): string {
  const subject = collapseSpace(item.subject);
  let snippet = collapseSpace(item.snippet);
  if (subject && snippet) {
    const lowerSnippet = snippet.toLowerCase();
    const lowerSubject = subject.toLowerCase();
    if (lowerSnippet.startsWith(lowerSubject)) {
      snippet = snippet.slice(subject.length).replace(/^[\s·:|\-–—]+/, "");
    } else if (snippet.endsWith("…") && lowerSubject.startsWith(lowerSnippet.slice(0, -1).trimEnd())) {
      snippet = ""; // a long subject truncated into the snippet
    }
  }
  return snippet;
}

// One-line row summary: "Subject · body", or whichever part exists.
export function crmInboxSummary(item: InboxItem): string {
  const subject = collapseSpace(item.subject);
  const preview = crmInboxPreview(item);
  if (subject && preview) return `${subject} · ${preview}`;
  return subject || preview || "Conversation";
}

export function crmInboxRelativeTime(value: string, now = Date.now()): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return "";
  const seconds = Math.max(0, Math.floor((now - timestamp) / 1000));
  if (seconds < 60) return "now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(new Date(timestamp));
}
