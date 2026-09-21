export type CRMInboxStatus = "open" | "pending" | "closed" | "spam" | "all";
export type CRMInboxChannel = "all" | "email" | "sms" | "whatsapp";

export interface InboxItem {
  id: number;
  contact_id: number;
  contact_name?: string;
  contact_email?: string;
  contact_phone?: string;
  channel: string;
  subject?: string;
  status: string;
  priority: string;
  last_activity_at: string;
  snippet?: string;
  automated?: boolean;
}

export interface InboxResponse {
  inbox?: InboxItem[];
  count?: number;
  total?: number;
  offset?: number;
}

export interface CRMInboxHostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
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

export function crmInboxAPIURL(props: CRMInboxHostProps, preferences: CRMInboxPreferences): string {
  const query = new URLSearchParams({
    status: preferences.defaultStatus,
    limit: String(preferences.maxConversations),
  });
  if (props.installId) query.set("install_id", String(props.installId));
  if (props.projectId) query.set("project_id", props.projectId);
  if (preferences.defaultChannel !== "all") {
    query.set("filters", JSON.stringify([
      { field: "channel", op: "is", value: preferences.defaultChannel },
    ]));
  }
  return `/api/apps/${encodeURIComponent(props.appName || "crm")}/inbox?${query.toString()}`;
}

export function crmInboxPageLink(props: CRMInboxHostProps, item?: InboxItem): string {
  const query = new URLSearchParams({ tab: "inbox" });
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  if (item) {
    query.set("conversation_id", String(item.id));
    if (inboxStatuses.has(item.status as CRMInboxStatus)) query.set("status", item.status);
  }
  return `/apps/${encodeURIComponent(props.appName || "crm")}/page?${query.toString()}`;
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

export function crmInboxPreview(item: InboxItem): string {
  return item.snippet?.trim() || item.subject?.trim() || "Conversation";
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
