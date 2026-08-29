// CrmPanel — native React port of the crm app's ContactsPanel.
// Talks to /api/apps/crm/* through the platform proxy. Two-pane
// layout: contact list on the left, detail on the right. Tabbed
// shell exposes a Settings pane for the messaging coupling.

import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";

// Inlined SDK app-event subscription. Each app ships its own copy
// because panels are bundled standalone and apps are independently
// installable — cross-app imports would break a one-off install.
// Hook spec lives in apteva-server's appbus_handlers.go.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}
interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}
interface Channel {
  id?: number;
  kind: string;
  value: string;
  label?: string;
  is_primary?: boolean;
  verification_verdict?: string;
  verification_confidence?: string;
  verification_reason?: string;
  verification_recommendation?: string;
  verification_source?: string;
  verification_suggested_value?: string;
  verification_checked_at?: string;
  verification_details?: {
    disposable?: boolean;
    role?: boolean;
    domain_status?: string;
    retryable?: boolean;
    smtp?: {
      checked?: boolean;
      rcpt_status?: string;
      catch_all?: boolean;
      retryable?: boolean;
      note?: string;
    };
  };
  deliverability?: ChannelDeliverability[];
}
interface ChannelDeliverability {
  transport: "email" | "sms" | "whatsapp";
  status: "active" | "soft_bounced" | "hard_bounced" | "complained" | "unsubscribed";
  consecutive_soft_bounces: number;
  last_bounce_at?: string;
  last_delivered_at?: string;
  status_reason?: string;
  status_updated_at?: string;
  quarantined: boolean;
  quarantined_at?: string;
  suppressed: boolean;
  suppression_kind?: string;
  suppression_match?: string;
  suppression_reason?: string;
  suppression_source?: string;
  suppressed_at?: string;
  suppression_checked_at?: string;
  messageable: boolean;
  messageability_reason?: string;
}
interface Attribute {
  key: string;
  label?: string;
  type?: string;
  value: unknown;
}
interface AttributeDef {
  key: string;
  label: string;
  type: AttributeType;
  enum_values?: string[];
  required?: boolean;
  sort_order?: number;
  is_system?: number;
}
type AttributeType = "text" | "number" | "date" | "bool" | "select" | "multi_select" | "url";
interface Activity {
  id: string;
  kind: string;
  body: string;
  source?: string;
  occurred_at: string;
  conversation_id?: number | string;
  message_id_header?: string;
  messaging_id?: number | string;
  message_status?: MessageStatus;
  attachments?: ActivityAttachment[];
}
interface ActivityAttachment {
  id: number | string;
  messaging_attachment_id?: number | string;
  storage_id?: number | string;
  url?: string;
  download_url?: string;
  filename?: string;
  content_type?: string;
  size_bytes?: number;
  source?: string;
  processing_status?: string;
  processing_error?: string;
}
interface ComposeAttachment {
  key: string;
  storage_id?: number;
  content_base64?: string;
  filename: string;
  content_type: string;
  size_bytes: number;
}
interface StorageFile {
  id: number | string;
  name: string;
  folder?: string;
  content_type?: string;
  size_bytes?: number;
}
interface MessageStatusEvent {
  kind: string;
  recipient?: string;
  reason?: string;
  occurred_at?: string;
}
interface MessageStatus {
  id: number | string;
  direction?: string;
  status: string;
  status_reason?: string;
  provider_message_id?: string;
  sent_at?: string;
  received_at?: string;
  last_event_at?: string;
  event_counts?: Record<string, number>;
  events?: MessageStatusEvent[];
}
interface Contact {
  id: string;
  first_name?: string;
  last_name?: string;
  display_name?: string;
  pronouns?: string;
  company?: string;
  job_title?: string;
  status?: string;
  primary_email?: string;
  primary_phone?: string;
  channels?: Channel[];
  tags?: string[];
  attributes?: Attribute[];
}
interface Conversation {
  id: number | string;
  contact_id: number | string;
  channel: string;
  subject?: string;
  root_message_id?: string;
  status?: string;   // open | pending | closed | spam
  priority?: string; // low | normal | high | urgent
  started_at: string;
  last_activity_at: string;
}

type ConvoStatus = "open" | "pending" | "closed" | "spam";
const CONVO_STATUSES: ConvoStatus[] = ["open", "pending", "closed", "spam"];
type ConvoPriority = "low" | "normal" | "high" | "urgent";
const CONVO_PRIORITIES: ConvoPriority[] = ["low", "normal", "high", "urgent"];
const CHANNEL_KINDS = ["email", "phone", "linkedin", "twitter", "github", "website", "other_url"];

// Tailwind tokens only (no arbitrary values) — see panel-theme memory.
const STATUS_STYLES: Record<string, string> = {
  open: "bg-accent/10 text-accent",
  pending: "bg-yellow/15 text-yellow",
  closed: "bg-border text-text-muted",
  spam: "bg-red/15 text-red",
};
const PRIORITY_DOT: Record<string, string> = {
  low: "bg-text-dim",
  normal: "bg-text-muted",
  high: "bg-yellow",
  urgent: "bg-red",
};
const MESSAGE_STATUS_STYLES: Record<string, string> = {
  pending: "bg-border text-text-muted",
  sent: "bg-accent/10 text-accent",
  delivered: "bg-green/15 text-green",
  opened: "bg-green/20 text-green",
  received: "bg-accent/10 text-accent",
  delivery_delayed: "bg-yellow/15 text-yellow",
  bounced: "bg-red/15 text-red",
  complained: "bg-red/15 text-red",
  failed: "bg-red/15 text-red",
  rejected: "bg-red/15 text-red",
};
interface InboundRoute {
  id: number;
  pattern: string;
  channel: string;
  target_app: string;
  target_route: string;
  priority?: number;
}

const API = "/api/apps/crm";
const MESSAGING_API = "/api/apps/messaging";
type PanelApi = <T,>(method: string, path: string, body?: any, params?: Record<string, string>) => Promise<T>;

// Activity-kind families. Used to pick icons + decide whether the
// row gets a Reply button (received-only) and whether it groups into
// a conversation lane.
const RECEIVED_KINDS = new Set([
  "email_received",
  "sms_received",
  "whatsapp_received",
]);
const SENT_KINDS = new Set([
  "email_sent",
  "sms_sent",
  "whatsapp_sent",
]);
const FAILED_KINDS = new Set([
  "email_send_failed",
  "sms_send_failed",
  "whatsapp_send_failed",
]);
const TEST_SENT_KINDS = new Set([
  "email_test_sent",
  "sms_test_sent",
  "whatsapp_test_sent",
]);
const MESSAGE_KINDS = new Set<string>([
  ...RECEIVED_KINDS,
  ...SENT_KINDS,
  ...FAILED_KINDS,
]);

function channelOfKind(kind: string): "email" | "sms" | "whatsapp" | null {
  if (kind.startsWith("email_")) return "email";
  if (kind.startsWith("sms_")) return "sms";
  if (kind.startsWith("whatsapp_")) return "whatsapp";
  return null;
}

function iconForKind(kind: string): string {
  if (kind.startsWith("email_")) return "✉";
  if (kind.startsWith("sms_")) return "✆";
  if (kind.startsWith("whatsapp_")) return "◐";
  if (kind === "call") return "📞";
  if (kind === "meeting") return "🗓";
  if (kind === "note") return "✎";
  if (kind === "system") return "⚙";
  return "•";
}

function displayName(c: Contact | undefined | null): string {
  if (!c) return "(no name)";
  return c.display_name ||
    [c.first_name, c.last_name].filter(Boolean).join(" ") ||
    c.primary_email || c.primary_phone || "(no name)";
}

function secondaryLine(c: Contact): string {
  const bits: string[] = [];
  if (c.company) bits.push(c.company);
  if (c.job_title) bits.push(c.job_title);
  if (c.primary_email) bits.push(c.primary_email);
  return bits.join(" · ");
}

function formatTime(s: string | undefined): string {
  if (!s) return "";
  try { return new Date(s).toLocaleString(); } catch { return s; }
}

// Detect possible-match candidates surfaced by the inbound handler
// when an exact-address miss had domain neighbours. The CRM logs
// these as a `system` activity with source_detail.possible_match_ids.
// For simplicity we pattern-match on the activity body; if we wanted
// type safety we'd surface possible matches as a dedicated endpoint.
function extractPossibleMatchIds(activities: Activity[]): string[] {
  for (const a of activities) {
    if (a.kind !== "system") continue;
    if (!a.body.includes("possible duplicates")) continue;
    // Body is a fixed string — the ids live in source_detail JSON
    // which the activities endpoint doesn't currently return. The
    // banner is informational ("possible duplicates flagged") for
    // v0.1; merge UX comes when source_detail surfaces in the API.
    return ["pending"];
  }
  return [];
}

type Tab = "contacts" | "inbox" | "opportunities" | "lists" | "segments" | "settings";

interface List {
  id: number;
  slug: string;
  name: string;
  description?: string;
  default_sender_email?: string;
  default_sender_phone?: string;
  inbound_route_pattern?: string;
  archived_at?: string;
  member_count?: number;
}

// Segment definition is an array of predicate entries. Each is either
// a column-level filter ({field, op, value}) or a synthetic predicate
// ({predicate, ...args}). The panel editor produces the synthetic
// shape — the simpler case of "agent crafts a JSON spec by hand" goes
// straight through the MCP layer.
type SegmentPredicate =
  | { field: string; op?: string; value?: unknown }
  | { predicate: "tag_in" | "tag_not_in"; tags: string[] }
  | { predicate: "attribute"; key: string; op?: string; value?: unknown }
  | { predicate: "last_activity_within"; days: number; kind?: string }
  | { predicate: "channel_present"; kind: "email" | "phone" }
  | { predicate: "in_list" | "not_in_list"; list_id: number }
  | { predicate: "not_in_segment"; segment_id: number };

interface Segment {
  id: number;
  name: string;
  description?: string;
  kind: "dynamic" | "static";
  list_id?: number | null;
  definition?: SegmentPredicate[];
  cached_count?: number;
  cached_at?: string;
  archived_at?: string;
}

interface PipelineStage {
  id: number;
  pipeline_id: number;
  name: string;
  position: number;
  category: "open" | "won" | "lost";
  probability?: number;
  archived_at?: string;
}

interface Pipeline {
  id: number;
  name: string;
  description?: string;
  is_default?: boolean;
  archived_at?: string;
  stages?: PipelineStage[];
}

interface Opportunity {
  id: number;
  contact_id: number;
  pipeline_id: number;
  stage_id: number;
  title: string;
  status: "open" | "won" | "lost";
  value?: number;
  currency?: string;
  offer_key?: string;
  offer_name?: string;
  source?: string;
  source_site?: string;
  sender_identity?: string;
  owner?: string;
  expected_close_date?: string;
  closed_at?: string;
  lost_reason?: string;
  pipeline_name?: string;
  stage_name?: string;
  stage_category?: string;
  contact_name?: string;
  contact_email?: string;
  contact_phone?: string;
  updated_at?: string;
  created_at?: string;
}

// ─── Contact filters ──────────────────────────────────────────────
// A UI filter row. `kind` decides how it serializes: core columns go
// out as {field,…}, custom attributes as {attribute,…} — matching the
// contacts_search backend.
interface UIFilter {
  kind: "core" | "attr";
  key: string;
  label: string;
  type: string;        // text | number | date | bool | select | url
  op: string;          // eq | contains | starts_with | gte | lte
  value: string;
  enumValues?: string[];
}

// Contacts list page size (matches the backend default limit).
const CONTACTS_PAGE = 50;
const INBOX_PAGE = 100;

// Core columns the backend's buildFilterClause allow-lists.
const CORE_FILTER_FIELDS: { key: string; label: string; type: string }[] = [
  { key: "display_name", label: "Name", type: "text" },
  { key: "company", label: "Company", type: "text" },
  { key: "job_title", label: "Job title", type: "text" },
  { key: "primary_email", label: "Email", type: "text" },
  { key: "primary_phone", label: "Phone", type: "text" },
  { key: "status", label: "Status", type: "text" },
];

function opsForType(type: string): { value: string; label: string }[] {
  switch (type) {
    case "number":
    case "date":
      return [{ value: "eq", label: "is" }, { value: "gte", label: "≥" }, { value: "lte", label: "≤" }];
    case "bool":
    case "select":
      return [{ value: "eq", label: "is" }];
    default:
      return [{ value: "eq", label: "is" }, { value: "contains", label: "contains" }, { value: "starts_with", label: "starts with" }];
  }
}

// serializeFilters → the contacts_search `filters` array.
function serializeFilters(filters: UIFilter[]): any[] {
  return filters.map((f) => {
    let value: unknown = f.value;
    if (f.type === "number") value = Number(f.value);
    else if (f.type === "bool") value = f.value === "true";
    return f.kind === "attr"
      ? { attribute: f.key, op: f.op, value }
      : { field: f.key, op: f.op, value };
  });
}

// filtersToSegmentDefinition → the segments `definition` predicate array.
function filtersToSegmentDefinition(filters: UIFilter[]): any[] {
  return filters.map((f) => {
    let value: unknown = f.value;
    if (f.type === "number") value = Number(f.value);
    else if (f.type === "bool") value = f.value === "true";
    return f.kind === "attr"
      ? { predicate: "attribute", key: f.key, op: f.op, value }
      : { field: f.key, op: f.op, value };
  });
}

function describeFilter(f: UIFilter): string {
  const op = opsForType(f.type).find((o) => o.value === f.op)?.label || f.op;
  return `${f.label} ${op} ${f.value}`;
}

export default function CrmPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("contacts");
  const [contacts, setContacts] = useState<Contact[]>([]);
  const [total, setTotal] = useState(0);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<Contact | null>(null);
  const [activities, setActivities] = useState<Activity[]>([]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [edits, setEdits] = useState<Partial<Contact>>({});
  const [composer, setComposer] = useState<ComposerState | null>(null);
  const [composerContact, setComposerContact] = useState<Contact | null>(null);
  const composerAfterSendRef = useRef<(() => void | Promise<void>) | null>(null);
  const [verifiedSenders, setVerifiedSenders] = useState<SenderOption[]>([]);
  const [messagingTemplates, setMessagingTemplates] = useState<TemplateOption[]>([]);
  const [errorToast, setErrorToast] = useState<string | null>(null);
  const [confirmDialog, setConfirmDialog] = useState<ConfirmState | null>(null);
  const [logActivityOpen, setLogActivityOpen] = useState(false);
  const [newContactOpen, setNewContactOpen] = useState(false);
  const [addChannelOpen, setAddChannelOpen] = useState(false);
  const [attrDefs, setAttrDefs] = useState<AttributeDef[]>([]);
  const [defineFieldOpen, setDefineFieldOpen] = useState(false);
  // Structured filters on the contacts list. filtersRef mirrors the
  // state so loadList (stable useCallback) always reads the current set.
  const [filters, setFilters] = useState<UIFilter[]>([]);
  const filtersRef = useRef<UIFilter[]>([]);
  const applyFilters = useCallback((next: UIFilter[]) => {
    filtersRef.current = next;
    setFilters(next);
  }, []);
  const [lists, setLists] = useState<List[]>([]);
  const [listFilterId, setListFilterId] = useState("");
  const [contactLists, setContactLists] = useState<List[]>([]);
  const [newListOpen, setNewListOpen] = useState(false);
  const [editListId, setEditListId] = useState<number | null>(null);
  const [segments, setSegments] = useState<Segment[]>([]);
  const [editSegmentId, setEditSegmentId] = useState<number | "new" | null>(null);
  const [segmentPreview, setSegmentPreview] = useState<{ id: number; contacts: Contact[]; total: number } | null>(null);
  const [pipelines, setPipelines] = useState<Pipeline[]>([]);
  const [opportunities, setOpportunities] = useState<Opportunity[]>([]);
  const [contactOpportunities, setContactOpportunities] = useState<Opportunity[]>([]);
  const [opportunityStatusFilter, setOpportunityStatusFilter] = useState("open");
  const [newOpportunityOpen, setNewOpportunityOpen] = useState(false);

  // Auto-dismiss the error toast after 5s. Manual dismiss via the
  // X button is also wired up below; this prevents stale errors
  // from lingering when the user moves on.
  useEffect(() => {
    if (!errorToast) return;
    const id = window.setTimeout(() => setErrorToast(null), 5000);
    return () => window.clearTimeout(id);
  }, [errorToast]);

  const withParams = useCallback((extra: Record<string, string> = {}) => {
    const u = new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra });
    return u.toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, body?: any, params: Record<string, string> = {}): Promise<T> => {
    const res = await fetch(`${API}${path}?${withParams(params)}`, {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [withParams]);

  const messagingTool = useCallback(async <T,>(tool: string, args: Record<string, unknown> = {}): Promise<T> => {
    const params = new URLSearchParams({ project_id: projectId });
    const res = await fetch(`${MESSAGING_API}/tools/call?${params.toString()}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool, args: { ...args, _project_id: projectId } }),
    });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [projectId]);

  // loadList(q, offset): offset 0 replaces the list; offset > 0 appends
  // the next page (for "Load more"). total comes back from the backend
  // so the UI can show "X of Y" and know when to stop paging.
  const loadList = useCallback(async (q = "", offset = 0) => {
    setStatus("Loading…");
    try {
      const params: Record<string, string> = { limit: String(CONTACTS_PAGE) };
      if (q) params.q = q;
      if (offset) params.offset = String(offset);
      const ser = serializeFilters(filtersRef.current);
      if (listFilterId) ser.push({ predicate: "in_list", list_id: Number(listFilterId) });
      if (ser.length) params.filters = JSON.stringify(ser);
      const r = await api<{ contacts?: Contact[]; total?: number }>("GET", "/contacts", undefined, params);
      const rows = r.contacts || [];
      setContacts((prev) => (offset > 0 ? [...prev, ...rows] : rows));
      const t = typeof r.total === "number" ? r.total : rows.length;
      setTotal(t);
      const shown = offset > 0 ? offset + rows.length : rows.length;
      setStatus(`${shown} of ${t}${ser.length ? ` · ${ser.length} filter${ser.length !== 1 ? "s" : ""}` : ""}`);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [api, listFilterId]);

  const loadAttrDefs = useCallback(async () => {
    try {
      const r = await api<{ attribute_defs?: AttributeDef[] }>("GET", "/attribute-defs");
      setAttrDefs(r.attribute_defs || []);
    } catch (e) {
      // Best-effort — settings tab surfaces errors more loudly.
    }
  }, [api]);

  const loadLists = useCallback(async () => {
    try {
      const r = await api<{ lists?: List[] }>("GET", "/lists");
      setLists(r.lists || []);
    } catch (e) {
      // Best-effort.
    }
  }, [api]);

  const loadSegments = useCallback(async () => {
    try {
      const r = await api<{ segments?: Segment[] }>("GET", "/segments");
      setSegments(r.segments || []);
    } catch (e) {
      // Best-effort.
    }
  }, [api]);

  const loadPipelines = useCallback(async () => {
    try {
      const r = await api<{ pipelines?: Pipeline[] }>("GET", "/pipelines");
      setPipelines(r.pipelines || []);
    } catch {
      setPipelines([]);
    }
  }, [api]);

  const loadOpportunities = useCallback(async () => {
    try {
      const r = await api<{ opportunities?: Opportunity[] }>(
        "GET",
        "/opportunities",
        undefined,
        { status: opportunityStatusFilter, limit: "200" },
      );
      setOpportunities(r.opportunities || []);
    } catch {
      setOpportunities([]);
    }
  }, [api, opportunityStatusFilter]);

  const loadContactOpportunities = useCallback(async (id: string | number) => {
    try {
      const r = await api<{ opportunities?: Opportunity[] }>(
        "GET",
        `/contacts/${id}/opportunities`,
        undefined,
        { status: "all" },
      );
      setContactOpportunities(r.opportunities || []);
    } catch {
      setContactOpportunities([]);
    }
  }, [api]);

  // Turn the active filter set into a saved Segment (reuses the segment
  // engine + tab). Returns the created name on success.
  const saveFiltersAsSegment = useCallback(async (name: string) => {
    const definition = filtersToSegmentDefinition(filtersRef.current);
    if (!definition.length) throw new Error("add at least one filter first");
    await api("POST", "/segments", { name, definition });
    await loadSegments();
  }, [api, loadSegments]);

  // Initial load.
  useEffect(() => { loadList(""); }, [loadList]);
  useEffect(() => { loadAttrDefs(); }, [loadAttrDefs]);
  useEffect(() => { loadLists(); }, [loadLists]);
  useEffect(() => { loadSegments(); }, [loadSegments]);
  useEffect(() => { loadPipelines(); }, [loadPipelines]);
  useEffect(() => { loadOpportunities(); }, [loadOpportunities]);
  useEffect(() => {
    if (listFilterId && !lists.some((l) => !l.archived_at && String(l.id) === listFilterId)) {
      setListFilterId("");
    }
  }, [lists, listFilterId]);

  // Fetch the bound messaging app's verified senders/templates through
  // CRM's backend. The browser only knows the CRM install_id; the
  // backend knows which messaging install is bound to this CRM.
  useEffect(() => {
    api<{ senders?: { channel: string; address: string; display_name?: string; is_default?: boolean }[] }>("GET", "/messaging/senders")
      .then((r) => {
        const out = (r.senders || []).map((s) => ({
          channel: s.channel,
          address: s.address,
          label: s.display_name ? `${s.display_name} <${s.address}>` : s.address,
          isDefault: !!s.is_default,
        }));
        setVerifiedSenders(out);
      })
      .catch(() => setVerifiedSenders([]));
  }, [api]);

  useEffect(() => {
    api<{ templates?: TemplateOption[] }>("GET", "/messaging/templates", undefined, { channel: "whatsapp" })
      .then((r) => setMessagingTemplates((r.templates || []).filter((t) =>
        t.channel === "whatsapp" &&
        !!t.provider_template_id &&
        (t.provider_status || "").toLowerCase() === "approved"
      )))
      .catch(() => setMessagingTemplates([]));
  }, [api]);

  // Debounced search — re-runs on query OR filter changes.
  useEffect(() => {
    const id = setTimeout(() => loadList(query.trim()), 250);
    return () => clearTimeout(id);
  }, [query, filters, loadList]);

  // Live refresh: reload the contact list whenever a contact mutation
  // event lands on the (crm, project) lane. Activity-added refreshes
  // the detail pane's timeline so an inbound message lands without a
  // manual refresh.
  useAppEvents("crm", projectId, (ev) => {
    if (
      ev.topic === "contact.added" ||
      ev.topic === "contact.updated" ||
      ev.topic === "contact.deleted" ||
      ev.topic === "contact.merged"
    ) {
      loadList(query.trim());
    }
    if (ev.topic === "contact.channel.deliverability.changed") {
      const data = (ev.data || {}) as { contact_id?: number };
      loadList(query.trim());
      if (detail && String(data.contact_id) === String(detail.id)) {
        api<{ contact: Contact }>("GET", `/contacts/${detail.id}`)
          .then((result) => setDetail(result.contact))
          .catch(() => {});
      }
    }
    if (ev.topic === "contact.activity.added" && detail) {
      const data = (ev.data || {}) as { contact_id?: number };
      if (String(data.contact_id) === String(detail.id)) {
        reloadActivities(detail.id);
      }
    }
    if (ev.topic === "list.created" || ev.topic === "list.updated" || ev.topic === "list.archived") {
      loadLists();
    }
    if (ev.topic === "list.member.added" || ev.topic === "list.member.removed") {
      loadLists();
      if (detail) {
        const data = (ev.data || {}) as { contact_id?: number };
        if (String(data.contact_id) === String(detail.id)) {
          loadContactLists(detail.id);
        }
      }
    }
    if (
      ev.topic === "segment.created" || ev.topic === "segment.updated" ||
      ev.topic === "segment.archived" || ev.topic === "segment.materialised"
    ) {
      loadSegments();
    }
    if (ev.topic.startsWith("pipeline.")) {
      loadPipelines();
      loadOpportunities();
    }
    if (ev.topic.startsWith("opportunity.")) {
      loadOpportunities();
      if (detail) {
        const data = (ev.data || {}) as { contact_id?: number };
        if (String(data.contact_id) === String(detail.id)) {
          loadContactOpportunities(detail.id);
        }
      }
    }
  });

  const loadContactLists = useCallback(async (id: string | number) => {
    try {
      const r = await api<{ lists?: List[] }>("GET", `/contacts/${id}/lists`);
      setContactLists(r.lists || []);
    } catch (e) {
      // Endpoint may not exist on older CRMs; fallback to client-side
      // filter from the global list set won't show membership, so the
      // chip row just stays empty in that case.
      setContactLists([]);
    }
  }, [api]);

  const reloadActivities = useCallback(async (id: string) => {
    try {
      const [a, conv] = await Promise.all([
        api<{ activities?: Activity[] }>("GET", `/contacts/${id}/activities`),
        api<{ conversations?: Conversation[] }>("GET", `/contacts/${id}/conversations`),
      ]);
      setActivities(a.activities || []);
      setConversations(conv.conversations || []);
    } catch (e) {
      // Best-effort — don't disrupt the panel on transient refresh errors.
    }
  }, [api]);

  const selectContact = useCallback(async (id: string) => {
    setSelectedId(id);
    setDetail(null);
    setActivities([]);
    setConversations([]);
    setContactLists([]);
    setContactOpportunities([]);
    setEdits({});
    try {
      const [c, a, conv, ls, opps] = await Promise.all([
        api<{ contact: Contact }>("GET", `/contacts/${id}`),
        api<{ activities?: Activity[] }>("GET", `/contacts/${id}/activities`),
        api<{ conversations?: Conversation[] }>("GET", `/contacts/${id}/conversations`),
        api<{ lists?: List[] }>("GET", `/contacts/${id}/lists`).catch(() => ({ lists: [] })),
        api<{ opportunities?: Opportunity[] }>("GET", `/contacts/${id}/opportunities`, undefined, { status: "all" })
          .catch(() => ({ opportunities: [] })),
      ]);
      setDetail(c.contact);
      setActivities(a.activities || []);
      setConversations(conv.conversations || []);
      setContactLists(ls.lists || []);
      setContactOpportunities(opps.opportunities || []);
    } catch (e) {
      setStatus("Detail error: " + (e as Error).message);
    }
  }, [api]);

  const handleSave = async () => {
    if (!detail) return;
    try {
      const r = await api<{ contact: Contact }>("PATCH", `/contacts/${detail.id}`, edits);
      setDetail(r.contact);
      setEdits({});
      await loadList(query.trim());
    } catch (e) {
      setErrorToast("Save failed: " + (e as Error).message);
    }
  };

  const handleArchive = () => {
    if (!detail) return;
    setConfirmDialog({
      title: "Archive contact",
      message: `Archive ${displayName(detail)}? You can still view archived contacts via Status filter.`,
      confirmLabel: "Archive",
      destructive: true,
      onConfirm: async () => {
        try {
          await api("DELETE", `/contacts/${detail.id}`);
          setDetail(null);
          setSelectedId(null);
          await loadList(query.trim());
        } catch (e) {
          setErrorToast("Archive failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleLogActivity = async (kind: string, body: string) => {
    if (!detail) return;
    try {
      await api("POST", `/contacts/${detail.id}/activities`, { kind, body, source: "human" });
      reloadActivities(detail.id);
      setLogActivityOpen(false);
    } catch (e) {
      setErrorToast("Log failed: " + (e as Error).message);
    }
  };

  const handleSetAttribute = async (key: string, value: unknown) => {
    if (!detail) return;
    try {
      await api("POST", `/contacts/${detail.id}/attributes`, { key, value, source: "human" });
      // Refresh the contact to pick up the new attribute value.
      const r = await api<{ contact: Contact }>("GET", `/contacts/${detail.id}`);
      setDetail(r.contact);
    } catch (e) {
      setErrorToast("Save field failed: " + (e as Error).message);
    }
  };

  const handleDefineField = async (def: { key: string; label: string; type: AttributeType; enum_values?: string[]; required?: boolean }) => {
    try {
      await api("POST", "/attribute-defs", def);
      await loadAttrDefs();
      setDefineFieldOpen(false);
    } catch (e) {
      setErrorToast("Define field failed: " + (e as Error).message);
    }
  };

  const handleCreateList = async (l: Partial<List>) => {
    try {
      await api("POST", "/lists", l);
      await loadLists();
      setNewListOpen(false);
    } catch (e) {
      setErrorToast("Create list failed: " + (e as Error).message);
    }
  };

  const handleSaveList = async (id: number, patch: Partial<List>) => {
    try {
      await api("PATCH", `/lists/${id}`, patch);
      await loadLists();
      setEditListId(null);
    } catch (e) {
      setErrorToast("Save list failed: " + (e as Error).message);
    }
  };

  const handleArchiveList = (l: List) => {
    setConfirmDialog({
      title: "Archive list",
      message: `Archive "${l.name}"? Members rows are kept; the list just stops appearing in active views.`,
      confirmLabel: "Archive",
      destructive: true,
      onConfirm: async () => {
        try {
          await api("DELETE", `/lists/${l.id}`);
          await loadLists();
        } catch (e) {
          setErrorToast("Archive list failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleSaveSegment = async (id: number | "new", patch: Partial<Segment>) => {
    try {
      if (id === "new") {
        await api("POST", "/segments", patch);
      } else {
        await api("PATCH", `/segments/${id}`, patch);
      }
      await loadSegments();
      setEditSegmentId(null);
    } catch (e) {
      setErrorToast("Save segment failed: " + (e as Error).message);
    }
  };

  const handleArchiveSegment = (s: Segment) => {
    setConfirmDialog({
      title: "Archive segment",
      message: `Archive "${s.name}"? Snapshot rows are kept; the segment just stops appearing.`,
      confirmLabel: "Archive",
      destructive: true,
      onConfirm: async () => {
        try {
          await api("DELETE", `/segments/${s.id}`);
          await loadSegments();
        } catch (e) {
          setErrorToast("Archive segment failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleMaterialiseSegment = async (s: Segment) => {
    try {
      await api("POST", `/segments/${s.id}/materialise`);
      await loadSegments();
    } catch (e) {
      setErrorToast("Materialise failed: " + (e as Error).message);
    }
  };

  const handlePreviewSegment = async (s: Segment) => {
    try {
      const r = await api<{ contacts?: Contact[]; count?: number }>("GET", `/segments/${s.id}/members`, undefined, { limit: "20" });
      setSegmentPreview({ id: s.id, contacts: r.contacts || [], total: r.count || 0 });
    } catch (e) {
      setErrorToast("Preview failed: " + (e as Error).message);
    }
  };

  const handleCreateOpportunity = async (draft: Partial<Opportunity>) => {
    try {
      const r = await api<{ opportunity: Opportunity }>("POST", "/opportunities", draft);
      await Promise.all([
        loadOpportunities(),
        detail ? loadContactOpportunities(detail.id) : Promise.resolve(),
      ]);
      setNewOpportunityOpen(false);
      if (detail && String(r.opportunity.contact_id) === String(detail.id)) {
        setContactOpportunities((prev) => [
          r.opportunity,
          ...prev.filter((o) => o.id !== r.opportunity.id),
        ]);
      }
    } catch (e) {
      setErrorToast("Create opportunity failed: " + (e as Error).message);
    }
  };

  const handleMoveOpportunity = async (
    opportunity: Opportunity,
    patch: Partial<Opportunity> & { note?: string },
  ) => {
    try {
      await api("PATCH", `/opportunities/${opportunity.id}`, patch);
      await Promise.all([
        loadOpportunities(),
        detail ? loadContactOpportunities(detail.id) : Promise.resolve(),
      ]);
    } catch (e) {
      setErrorToast("Opportunity update failed: " + (e as Error).message);
    }
  };

  const toggleContactList = async (l: List, on: boolean) => {
    if (!detail) return;
    try {
      if (on) {
        await api("POST", `/contacts/${detail.id}/lists`, { list_id: l.id, source: "human" });
      } else {
        await api("DELETE", `/lists/${l.id}/members/${detail.id}`);
      }
      await loadContactLists(detail.id);
    } catch (e) {
      setErrorToast("List membership failed: " + (e as Error).message);
    }
  };

  const handleNewContact = async (firstName: string, email: string, phone: string) => {
    // Primary is per channel kind so one contact can have both a
    // primary email and a primary phone.
    const channels: Channel[] = [];
    if (email) channels.push({ kind: "email", value: email, is_primary: true });
    if (phone) channels.push({ kind: "phone", value: phone, is_primary: true });
    try {
      const r = await api<{ contact: Contact }>("POST", "/contacts", {
        first_name: firstName,
        source: "human",
        channels,
      });
      setNewContactOpen(false);
      await loadList();
      selectContact(r.contact.id);
    } catch (e) {
      setErrorToast("Create failed: " + (e as Error).message);
    }
  };

  // Composer wiring. New send opens with no pre-filled subject; reply
  // pre-fills "Re: <subject>" and pins the conversation_id so the
  // outbound stays in-thread.
  const openCompose = (
    preset: Partial<ComposerState> = {},
    targetContact?: Contact | null,
    afterSend?: () => void | Promise<void>,
  ) => {
    const target = targetContact || detail;
    if (!target) return;
    const channel = preset.channel || preferredChannel(target, verifiedSenders) || "email";
    const defaultForChannel = preferredSenderForChannel(verifiedSenders, channel);
    setComposerContact(target);
    composerAfterSendRef.current = afterSend || null;
    setComposer({
      mode: preset.mode || "new",
      channel,
      subject: preset.subject || "",
      body: "",
      from: defaultForChannel?.address || "",
      templateId: "",
      templateVars: {},
      attachments: [],
      whatsAppSession: { state: "idle" },
      conversationId: preset.conversationId,
      replyToActivityId: preset.replyToActivityId,
      busy: false,
      error: null,
    });
  };

  useEffect(() => {
    if (!composer || !composerContact || composer.channel !== "whatsapp") return;
    const to = addressForChannel(composerContact, "whatsapp");
    const from = composer.from;
    if (!from || !to || to.startsWith("(no ")) {
      setComposer((prev) => prev ? { ...prev, whatsAppSession: { state: "idle" } } : prev);
      return;
    }
    let cancelled = false;
    setComposer((prev) => prev ? { ...prev, whatsAppSession: { state: "checking" } } : prev);
    api<{ active: boolean; last_inbound?: string }>("GET", "/messaging/whatsapp-session", undefined, { from, to })
      .then((r) => {
        if (cancelled) return;
        setComposer((prev) => prev ? {
          ...prev,
          whatsAppSession: { state: r.active ? "active" : "closed", lastInbound: r.last_inbound },
        } : prev);
      })
      .catch((e) => {
        if (cancelled) return;
        setComposer((prev) => prev ? {
          ...prev,
          whatsAppSession: { state: "error", error: (e as Error).message },
        } : prev);
    });
    return () => { cancelled = true; };
  }, [api, composer?.channel, composer?.from, composerContact?.id, composerContact?.primary_phone]);

  useEffect(() => {
    if (!composer || composer.from) return;
    const sender = preferredSenderForChannel(verifiedSenders, composer.channel);
    if (!sender) return;
    setComposer((prev) => prev && !prev.from ? { ...prev, from: sender.address } : prev);
  }, [composer?.channel, composer?.from, verifiedSenders]);

  useEffect(() => {
    if (!composer || composer.channel !== "whatsapp" || composer.templateId || messagingTemplates.length === 0) return;
    const first = messagingTemplates[0];
    const vars: Record<string, string> = {};
    templateVarKeys(first).forEach((key) => { vars[key] = ""; });
    setComposer((prev) => prev && !prev.templateId ? { ...prev, templateId: String(first.id), templateVars: vars } : prev);
  }, [composer?.channel, composer?.templateId, messagingTemplates]);

  // Conversation status / priority. POSTs to the dedicated sub-route
  // and reloads so the lane reflects the new state (and any auto-reopen
  // that an inbound triggered server-side).
  const [convoBusy, setConvoBusy] = useState(false);
  const [showClosed, setShowClosed] = useState(false);
  const setConversationStatus = useCallback(
    async (conversationId: string, patch: { status?: string; priority?: string; spam_scope?: string; force?: boolean }) => {
      if (!selectedId) return;
      setConvoBusy(true);
      try {
        await api("POST", `/contacts/${selectedId}/conversations/${conversationId}/status`, patch);
        await reloadActivities(selectedId);
      } catch (e) {
        setErrorToast("Status update failed: " + (e as Error).message);
      } finally {
        setConvoBusy(false);
      }
    },
    [api, selectedId, reloadActivities],
  );

  const handleSendFromComposer = async () => {
    const target = composerContact || detail;
    if (!composer || !target) return;
    setComposer({ ...composer, busy: true, error: null });
    try {
      const path = composer.mode === "reply" ? `/contacts/${target.id}/reply` : `/contacts/${target.id}/messages`;
      const useTemplate = composer.channel === "whatsapp" && whatsappSessionRequiresTemplate(composer.whatsAppSession);
      await api(
        "POST",
        path,
        {
          channel: composer.channel,
          subject: composer.subject || undefined,
          body: useTemplate ? "" : composer.body,
          conversation_id: composer.conversationId,
          template_id: useTemplate && composer.templateId ? Number(composer.templateId) : undefined,
          template_vars: useTemplate && composer.templateId ? composer.templateVars : undefined,
          attachments: composer.attachments.length > 0
            ? composer.attachments.map(({ key: _key, ...attachment }) => attachment)
            : undefined,
          // Only include `from` when the operator picked something
          // specific. Empty string means "let the backend default
          // kick in" — pass nothing so we don't override.
          from: composer.from || undefined,
        },
      );
      setComposer(null);
      setComposerContact(null);
      const afterSend = composerAfterSendRef.current;
      composerAfterSendRef.current = null;
      if (afterSend) {
        await afterSend();
      } else if (detail && String(detail.id) === String(target.id)) {
        reloadActivities(detail.id);
      }
    } catch (e) {
      setComposer((prev) => prev ? { ...prev, busy: false, error: (e as Error).message } : prev);
    }
  };

  const fieldValue = <K extends keyof Contact>(key: K): string => {
    const e = edits[key];
    if (e !== undefined) return String(e ?? "");
    if (!detail) return "";
    return String(detail[key] ?? "");
  };

  const updateField = <K extends keyof Contact>(key: K, v: string) => {
    setEdits((prev) => ({ ...prev, [key]: v }));
  };
  const saveChannels = useCallback(async (channels: Channel[]) => {
    if (!detail) return false;
    try {
      const r = await api<{ contact: Contact }>("PATCH", `/contacts/${detail.id}`, { channels, source: "human" });
      setDetail(r.contact);
      setEdits((prev) => {
        const next = { ...prev };
        delete next.channels;
        return next;
      });
      await loadList(query.trim());
      return true;
    } catch (e) {
      setErrorToast("Channel update failed: " + (e as Error).message);
      return false;
    }
  }, [api, detail, loadList, query]);
  const handleAddChannel = useCallback(async (channel: Channel) => {
    if (!detail) return;
    const current = detail.channels || [];
    const hasPrimary = current.some((ch) => ch.kind === channel.kind && ch.is_primary);
    const nextChannel = { ...channel, is_primary: channel.is_primary || !hasPrimary };
    if (await saveChannels([...current, nextChannel])) {
      setAddChannelOpen(false);
    }
  }, [detail, saveChannels]);
  const handleRemoveChannel = useCallback(async (idx: number) => {
    if (!detail) return;
    await saveChannels((detail.channels || []).filter((_, i) => i !== idx));
  }, [detail, saveChannels]);
  const handleVerifyEmail = useCallback(async (channel: Channel, smtp: boolean) => {
    if (!detail || !channel.id) return;
    try {
      const r = await api<{ contact: Contact }>(
        "POST",
        `/contacts/${detail.id}/channels/${channel.id}/verify`,
        { smtp },
      );
      setDetail(r.contact);
    } catch (e) {
      setErrorToast("Email verification failed: " + (e as Error).message);
    }
  }, [api, detail]);

  // Group activities by conversation. Within a conversation, order
  // chronologically (oldest first) so the agent reads the thread top-
  // down. Loose activities (notes, calls, system, anything without a
  // conversation_id) keep their reverse-chrono order.
  const grouped = useMemo(() => groupActivitiesByConversation(activities, conversations), [activities, conversations]);
  // Closed conversations are hidden by default (inbox convention). The
  // toggle reveals them; loose activities are always shown.
  const closedConvoCount = useMemo(
    () => grouped.filter((g) => g.kind === "conversation" && g.status === "closed").length,
    [grouped],
  );
  const visibleGroups = useMemo(
    () => (showClosed ? grouped : grouped.filter((g) => !(g.kind === "conversation" && g.status === "closed"))),
    [grouped, showClosed],
  );
  const possibleMatches = useMemo(() => extractPossibleMatchIds(activities), [activities]);

  return (
    <div className="h-full flex flex-col">
      {/* Tabs */}
      <nav className="flex gap-1 border-b border-border px-3 pt-2 text-xs">
        <TabButton active={tab === "contacts"} onClick={() => setTab("contacts")}>Contacts</TabButton>
        <TabButton active={tab === "inbox"} onClick={() => setTab("inbox")}>Inbox</TabButton>
        <TabButton active={tab === "opportunities"} onClick={() => setTab("opportunities")}>Opportunities</TabButton>
        <TabButton active={tab === "lists"} onClick={() => setTab("lists")}>Lists</TabButton>
        <TabButton active={tab === "segments"} onClick={() => setTab("segments")}>Segments</TabButton>
        <TabButton active={tab === "settings"} onClick={() => setTab("settings")}>Settings</TabButton>
      </nav>

      <div className="flex-1 min-h-0">
        {tab === "contacts" ? (
          <div className="h-full flex">
            {/* List */}
            <aside style={{ width: 460, maxWidth: "45%" }} className="shrink-0 border-r border-border flex flex-col">
              <div className="p-3 border-b border-border space-y-2">
                <div className="flex items-center gap-2">
                  <input
                    type="text"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    placeholder="Search contacts…"
                    className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  />
                  <button
                    type="button"
                    onClick={() => setNewContactOpen(true)}
                    className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                  >+ New</button>
                </div>
                <select
                  value={listFilterId}
                  onChange={(e) => setListFilterId(e.target.value)}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs text-text"
                >
                  <option value="">All lists</option>
                  {lists.filter((l) => !l.archived_at).map((l) => (
                    <option key={l.id} value={String(l.id)}>
                      {l.name}{typeof l.member_count === "number" ? ` (${l.member_count})` : ""}
                    </option>
                  ))}
                </select>
                <ContactFilterBar
                  attrDefs={attrDefs}
                  filters={filters}
                  onChange={applyFilters}
                  onSaveSegment={saveFiltersAsSegment}
                />
              </div>
              <div className="flex-1 overflow-auto">
                {contacts.length === 0 ? (
                  <div className="p-4 text-text-muted text-xs">No contacts.</div>
                ) : (
                  <ul>
                    {contacts.map((c) => (
                      <li
                        key={c.id}
                        onClick={() => selectContact(c.id)}
                        className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                          c.id === selectedId ? "bg-bg-input" : ""
                        }`}
                      >
                        <div className="text-sm text-text font-medium truncate">{displayName(c)}</div>
                        <div className="text-xs text-text-muted truncate">{secondaryLine(c)}</div>
                      </li>
                    ))}
                  </ul>
                )}
                {contacts.length > 0 && contacts.length < total && (
                  <button
                    type="button"
                    onClick={() => loadList(query.trim(), contacts.length)}
                    className="w-full px-3 py-2 text-xs text-accent hover:bg-bg-input border-b border-border"
                  >
                    Load {Math.min(CONTACTS_PAGE, total - contacts.length)} more ({contacts.length} of {total})
                  </button>
                )}
              </div>
              <div className="p-2 text-xs text-text-dim border-t border-border">{status}</div>
            </aside>

            {/* Detail */}
            <main className="flex-1 overflow-auto p-6">
              {!detail ? (
                <div className="text-text-muted text-sm text-center mt-12">
                  {selectedId ? "Loading…" : "Select a contact to see details."}
                </div>
              ) : (
                <div className="max-w-4xl space-y-6">
                  <header className="flex items-start justify-between gap-3">
                    <div>
                      <h1 className="text-xl text-text font-semibold">{displayName(detail)}</h1>
                      <p className="text-text-muted text-sm">{secondaryLine(detail) || "—"}</p>
                    </div>
                    <button
                      type="button"
                      onClick={() => openCompose({ mode: "new" })}
                      className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg whitespace-nowrap"
                    >Send message</button>
                  </header>

                  {possibleMatches.length > 0 && (
                    <div className="border border-amber/40 bg-amber/10 rounded p-2 text-xs text-text">
                      <span className="font-medium">Possible duplicates flagged.</span>{" "}
                      Inbound mail matched this contact's domain but not the address — review and merge if needed.
                    </div>
                  )}

                  <ContactListChips
                    lists={lists}
                    contactLists={contactLists}
                    onToggle={toggleContactList}
                  />

                  <section>
                    <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Core fields</h2>
                    <div className="grid grid-cols-[140px_1fr] gap-2 text-sm">
                      {([
                        ["First name", "first_name"],
                        ["Last name", "last_name"],
                        ["Display name", "display_name"],
                        ["Pronouns", "pronouns"],
                        ["Company", "company"],
                        ["Job title", "job_title"],
                      ] as const).map(([label, key]) => (
                        <ContactField
                          key={key}
                          label={label}
                          value={fieldValue(key)}
                          onChange={(v) => updateField(key, v)}
                        />
                      ))}
                      <label className="text-text-muted self-center">Status</label>
                      <select
                        value={fieldValue("status")}
                        onChange={(e) => updateField("status", e.target.value)}
                        className="bg-bg-input border border-border rounded px-2 py-1"
                      >
                        {["active", "archived", "spam", "merged"].map((opt) => (
                          <option key={opt} value={opt}>{opt}</option>
                        ))}
                      </select>
                    </div>
                  </section>

                  <ChannelsList
                    channels={detail.channels || []}
                    onAdd={() => setAddChannelOpen(true)}
                    onRemove={handleRemoveChannel}
                    onVerify={handleVerifyEmail}
                  />

                  {detail.tags && detail.tags.length > 0 && (
                    <section>
                      <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Tags</h2>
                      <div className="flex flex-wrap gap-1">
                        {detail.tags.map((t) => (
                          <span key={t} className="text-[11px] px-1.5 py-0.5 rounded bg-border text-text">{t}</span>
                        ))}
                      </div>
                    </section>
                  )}

                  <FieldsSection
                    contact={detail}
                    defs={attrDefs}
                    onSet={handleSetAttribute}
                  />

                  <ContactOpportunitiesSection
                    opportunities={contactOpportunities}
                    pipelines={pipelines}
                    onCreate={() => setNewOpportunityOpen(true)}
                    onMove={handleMoveOpportunity}
                  />

                  <section>
                    <div className="flex items-center justify-between mb-2">
                      <h2 className="text-xs uppercase tracking-wide text-text-dim">
                        Activity ({activities.length})
                      </h2>
                      {closedConvoCount > 0 && (
                        <button
                          type="button"
                          onClick={() => setShowClosed((v) => !v)}
                          className="text-[10px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input text-text-muted"
                        >
                          {showClosed ? "Hide" : "Show"} closed ({closedConvoCount})
                        </button>
                      )}
                    </div>
                    {visibleGroups.length === 0 ? (
                      <p className="text-text-muted text-sm">
                        {grouped.length === 0 ? "No activity logged." : "No open conversations."}
                      </p>
                    ) : (
                      <ul className="space-y-3">
                        {visibleGroups.map((group, gi) => (
                          <ActivityGroup
                            key={`g${gi}`}
                            group={group}
                            busy={convoBusy}
                            onSetStatus={setConversationStatus}
                            onReply={(act) => openCompose({
                              mode: "reply",
                              channel: channelOfKind(act.kind) || undefined,
                              conversationId: act.conversation_id,
                              subject: group.kind === "conversation" ? group.subject : undefined,
                              replyToActivityId: act.id,
                            })}
                          />
                        ))}
                      </ul>
                    )}
                  </section>

                  <div className="flex items-center gap-2 pt-2 border-t border-border">
                    <button
                      type="button"
                      onClick={handleSave}
                      disabled={Object.keys(edits).length === 0}
                      className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
                    >Save</button>
                    <button
                      type="button"
                      onClick={() => setLogActivityOpen(true)}
                      className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
                    >Log activity</button>
                    <button
                      type="button"
                      onClick={handleArchive}
                      className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10 ml-auto"
                    >Archive</button>
                  </div>
                </div>
              )}
            </main>
          </div>
        ) : tab === "inbox" ? (
          <InboxTab
            api={api}
            projectId={projectId}
            lists={lists}
            onOpenContact={(id) => { setTab("contacts"); selectContact(String(id)); }}
            onReply={(contact, activity, conversation, afterSend) => openCompose({
              mode: "reply",
              channel: channelOfKind(activity.kind) || conversation.channel || undefined,
              conversationId: conversation.id,
              subject: conversation.subject,
              replyToActivityId: activity.id,
            }, contact, afterSend)}
          />
        ) : tab === "opportunities" ? (
          <OpportunitiesTab
            opportunities={opportunities}
            pipelines={pipelines}
            statusFilter={opportunityStatusFilter}
            onStatusFilter={setOpportunityStatusFilter}
            onRefresh={() => { loadPipelines(); loadOpportunities(); }}
            onMove={handleMoveOpportunity}
            onOpenContact={(id) => { setTab("contacts"); selectContact(String(id)); }}
          />
        ) : tab === "lists" ? (
          <ListsTab
            api={api}
            lists={lists}
            onCreate={() => setNewListOpen(true)}
            onEdit={(id) => setEditListId(id)}
            onArchive={handleArchiveList}
            onOpenContact={(id) => { setTab("contacts"); selectContact(String(id)); }}
          />
        ) : tab === "segments" ? (
          <SegmentsTab
            segments={segments}
            lists={lists}
            preview={segmentPreview}
            onCreate={() => setEditSegmentId("new")}
            onEdit={(id) => setEditSegmentId(id)}
            onArchive={handleArchiveSegment}
            onMaterialise={handleMaterialiseSegment}
            onPreview={handlePreviewSegment}
            onClosePreview={() => setSegmentPreview(null)}
          />
        ) : (
          <SettingsTab
            messagingTool={messagingTool}
            api={api}
            lists={lists}
            attrDefs={attrDefs}
            onAddField={() => setDefineFieldOpen(true)}
          />
        )}
      </div>

      {composer && composerContact && (
        <ComposerModal
          composer={composer}
          contact={composerContact}
          projectId={projectId}
          senders={verifiedSenders}
          templates={messagingTemplates}
          onCancel={() => {
            setComposer(null);
            setComposerContact(null);
            composerAfterSendRef.current = null;
          }}
          onChange={(patch) => setComposer((prev) => prev ? { ...prev, ...patch } : prev)}
          onSend={handleSendFromComposer}
        />
      )}

      {newContactOpen && (
        <NewContactModal
          onCancel={() => setNewContactOpen(false)}
          onSubmit={handleNewContact}
        />
      )}
      {addChannelOpen && (
        <AddChannelModal
          onCancel={() => setAddChannelOpen(false)}
          onSubmit={handleAddChannel}
        />
      )}

      {logActivityOpen && detail && (
        <LogActivityModal
          contactName={displayName(detail)}
          onCancel={() => setLogActivityOpen(false)}
          onSubmit={handleLogActivity}
        />
      )}

      {confirmDialog && (
        <ConfirmDialog
          state={confirmDialog}
          onClose={() => setConfirmDialog(null)}
        />
      )}

      {defineFieldOpen && (
        <DefineFieldModal
          existing={attrDefs}
          onCancel={() => setDefineFieldOpen(false)}
          onSubmit={handleDefineField}
        />
      )}

      {newListOpen && (
        <ListEditorModal
          existing={lists}
          onCancel={() => setNewListOpen(false)}
          onSubmit={handleCreateList}
        />
      )}

      {editListId !== null && (
        <ListEditorModal
          existing={lists}
          editing={lists.find((l) => l.id === editListId) || null}
          onCancel={() => setEditListId(null)}
          onSubmit={(patch) => handleSaveList(editListId, patch)}
        />
      )}

      {editSegmentId !== null && (
        <SegmentEditorModal
          editing={editSegmentId === "new" ? null : (segments.find((s) => s.id === editSegmentId) || null)}
          lists={lists}
          segments={segments}
          onCancel={() => setEditSegmentId(null)}
          onSubmit={(patch) => handleSaveSegment(editSegmentId, patch)}
        />
      )}

      {newOpportunityOpen && detail && (
        <OpportunityEditorModal
          contact={detail}
          pipelines={pipelines}
          onCancel={() => setNewOpportunityOpen(false)}
          onSubmit={(draft) => handleCreateOpportunity({ ...draft, contact_id: Number(detail.id) })}
        />
      )}

      {errorToast && (
        <ErrorToast
          message={errorToast}
          onDismiss={() => setErrorToast(null)}
        />
      )}
    </div>
  );
}

function ContactField({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <>
      <label className="text-text-muted self-center">{label}</label>
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="bg-bg-input border border-border rounded px-2 py-1"
      />
    </>
  );
}

function EmailVerificationBadge({ channel }: { channel: Channel }) {
  if (channel.kind !== "email") return null;
  const verdict = channel.verification_verdict || "unchecked";
  const styles: Record<string, string> = {
    deliverable: "bg-green-500/15 text-green-400",
    risky: "bg-amber-500/15 text-amber-400",
    undeliverable: "bg-red-500/15 text-red-400",
    unknown: "bg-border text-text-muted",
    unchecked: "bg-border/60 text-text-dim",
  };
  return (
    <span className={`text-[10px] px-1.5 py-0.5 rounded ${styles[verdict] || styles.unknown}`}>
      {verdict}
    </span>
  );
}

function DeliveryBadge({ state }: { state: ChannelDeliverability }) {
  const healthy = state.messageable && state.status === "active";
  const warning = state.messageable && state.status === "soft_bounced";
  const cls = healthy
    ? "bg-green-500/15 text-green-400"
    : warning
      ? "bg-amber-500/15 text-amber-400"
      : "bg-red-500/15 text-red-400";
  const label = state.suppressed
    ? "suppressed"
    : state.quarantined
      ? "quarantined"
      : state.status.replaceAll("_", " ");
  const title = [
    `${state.transport}: ${label}`,
    state.messageability_reason,
    state.status_reason,
    state.suppression_match ? `matched ${state.suppression_kind || "suppression"}: ${state.suppression_match}` : "",
    state.suppression_source ? `source: ${state.suppression_source}` : "",
  ].filter(Boolean).join("\n");
  return (
    <span className={`text-[10px] px-1.5 py-0.5 rounded ${cls}`} title={title}>
      {state.transport} · {label}
    </span>
  );
}

function ChannelsList({ channels, onAdd, onRemove, onVerify }: {
  channels: Channel[];
  onAdd: () => void;
  onRemove: (idx: number) => void | Promise<void>;
  onVerify: (channel: Channel, smtp: boolean) => void | Promise<void>;
}) {
  const [verifying, setVerifying] = useState("");
  const verify = async (channel: Channel, smtp: boolean) => {
    const key = `${channel.id}-${smtp ? "smtp" : "local"}`;
    setVerifying(key);
    try {
      await onVerify(channel, smtp);
    } finally {
      setVerifying("");
    }
  };
  return (
    <section>
      <div className="flex items-center gap-2 mb-2">
        <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Channels</h2>
        <button
          type="button"
          onClick={onAdd}
          className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg"
        >Add channel</button>
      </div>
      {channels.length > 0 ? (
        <ul className="space-y-2">
          {channels.map((ch, i) => (
            <li key={ch.id || `${ch.kind}-${ch.value}-${i}`} className="text-sm rounded border border-border/70 p-2">
              <div className="flex items-center gap-2">
                <span className="text-[10px] uppercase text-text-dim w-16">{ch.kind}</span>
                <span className="text-text flex-1 truncate">{ch.value}</span>
                {ch.label && <span className="text-[10px] px-1 rounded bg-border text-text-muted">{ch.label}</span>}
                {ch.is_primary && <span className="text-[10px] px-1 rounded bg-accent/15 text-accent">primary</span>}
                <EmailVerificationBadge channel={ch} />
                {(ch.deliverability || []).map((state) => <DeliveryBadge key={state.transport} state={state} />)}
                <button
                  type="button"
                  onClick={() => onRemove(i)}
                  className="text-[11px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input"
                  title="Remove channel"
                >Remove</button>
              </div>
              {ch.kind === "email" && (
                <div className="mt-2 ml-[4.5rem] flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-text-muted">
                  {ch.verification_confidence && <span>Confidence: {ch.verification_confidence}</span>}
                  {ch.verification_reason && <span>Reason: {ch.verification_reason.replaceAll("_", " ")}</span>}
                  {ch.verification_recommendation && <span>Recommendation: {ch.verification_recommendation}</span>}
                  {ch.verification_source && <span>Source: {ch.verification_source}</span>}
                  {ch.verification_details?.smtp?.catch_all && <span>Catch-all domain</span>}
                  {(ch.verification_details?.retryable || ch.verification_details?.smtp?.retryable) && <span>Retry recommended</span>}
                  {ch.verification_suggested_value && <span>Suggestion: {ch.verification_suggested_value}</span>}
                  {ch.verification_checked_at && <span>Checked {formatTime(ch.verification_checked_at)}</span>}
                  <span className="flex-1" />
                  <button
                    type="button"
                    disabled={!ch.id || Boolean(verifying)}
                    onClick={() => verify(ch, false)}
                    className="px-1.5 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-50"
                  >{verifying === `${ch.id}-local` ? "Checking…" : "Verify again"}</button>
                  <button
                    type="button"
                    disabled={!ch.id || Boolean(verifying)}
                    onClick={() => verify(ch, true)}
                    className="px-1.5 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-50"
                    title="Ask Email Checker to probe the recipient mail server"
                  >{verifying === `${ch.id}-smtp` ? "Probing…" : "Run SMTP probe"}</button>
                </div>
              )}
              {(ch.deliverability || []).some((state) => state.status !== "active" || state.suppressed || state.quarantined) && (
                <div className="mt-2 ml-[4.5rem] flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-text-muted">
                  {(ch.deliverability || []).map((state) => (
                    <Fragment key={state.transport}>
                      {state.consecutive_soft_bounces > 0 && <span>{state.transport}: {state.consecutive_soft_bounces} consecutive transient bounce{state.consecutive_soft_bounces === 1 ? "" : "s"}</span>}
                      {state.messageability_reason && <span>{state.transport}: {state.messageability_reason}</span>}
                      {state.last_bounce_at && <span>Last bounce {formatTime(state.last_bounce_at)}</span>}
                    </Fragment>
                  ))}
                </div>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-text-muted">No channels yet.</p>
      )}
    </section>
  );
}

function AddChannelModal({ onCancel, onSubmit }: {
  onCancel: () => void;
  onSubmit: (channel: Channel) => void | Promise<void>;
}) {
  const [kind, setKind] = useState("email");
  const [value, setValue] = useState("");
  const [label, setLabel] = useState("");
  const [isPrimary, setIsPrimary] = useState(true);
  const [busy, setBusy] = useState(false);
  const canSubmit = value.trim().length > 0;
  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    try {
      await onSubmit({
        kind,
        value: value.trim(),
        label: label.trim(),
        is_primary: isPrimary,
      });
    } finally {
      setBusy(false);
    }
  };
  return (
    <ModalShell
      title="Add channel"
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !canSubmit}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Adding…" : "Add"}</button>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Kind</label>
        <select value={kind} onChange={(e) => setKind(e.target.value)} className="flex-1 bg-bg-input border border-border rounded px-2 py-1">
          {CHANNEL_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Value</label>
        <input
          type={kind === "phone" ? "tel" : kind === "email" ? "email" : "text"}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={kind === "phone" ? "+15551230000" : kind === "email" ? "name@example.com" : "value"}
          autoFocus
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Label</label>
        <input
          type="text"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="work, mobile, personal"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <label className="flex items-center gap-2 text-sm text-text">
        <input type="checkbox" checked={isPrimary} onChange={(e) => setIsPrimary(e.target.checked)} />
        Primary for this channel kind
      </label>
    </ModalShell>
  );
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`px-3 py-1.5 rounded-t border-b-2 ${active ? "border-accent text-accent" : "border-transparent text-text-muted hover:text-text"}`}
    >{children}</button>
  );
}

// ─── Activity grouping ────────────────────────────────────────────

type Group =
  | { kind: "conversation"; conversationId: string; channel: string; subject: string; status: string; priority: string; activities: Activity[] }
  | { kind: "loose"; activities: Activity[] };

// groupActivitiesByConversation walks the reverse-chrono activities
// list and splits it into ordered groups: each conversation lane
// shows up once at the position of its newest activity, with its
// rows sorted chronologically. Non-message activities (notes, calls,
// system, anything without a conversation_id) sit between groups,
// preserving the agent's "what happened most recently" reading order.
function groupActivitiesByConversation(activities: Activity[], conversations: Conversation[]): Group[] {
  const convoById = new Map<string, Conversation>();
  for (const c of conversations) convoById.set(String(c.id), c);

  const seen = new Set<string>();
  const out: Group[] = [];
  for (const a of activities) {
    const cid = a.conversation_id != null && String(a.conversation_id) !== "0" ? String(a.conversation_id) : "";
    if (!cid || !MESSAGE_KINDS.has(a.kind)) {
      out.push({ kind: "loose", activities: [a] });
      continue;
    }
    if (seen.has(cid)) continue;
    seen.add(cid);
    const inSameConvo = activities.filter((x) => String(x.conversation_id || "") === cid);
    inSameConvo.sort((p, q) => {
      const t = p.occurred_at.localeCompare(q.occurred_at);
      if (t !== 0) return t;
      return Number(p.id) - Number(q.id);
    });
    const convo = convoById.get(cid);
    out.push({
      kind: "conversation",
      conversationId: cid,
      channel: convo?.channel || channelOfKind(a.kind) || "",
      subject: convo?.subject || (a.body.split("\n", 1)[0] || ""),
      status: convo?.status || "open",
      priority: convo?.priority || "normal",
      activities: inSameConvo,
    });
  }
  return out;
}

function ActivityGroup({
  group,
  onReply,
  onSetStatus,
  busy,
}: {
  group: Group;
  onReply: (a: Activity) => void;
  onSetStatus: (conversationId: string, patch: { status?: string; priority?: string; spam_scope?: string; force?: boolean }) => void;
  busy: boolean;
}) {
  if (group.kind === "loose") {
    const a = group.activities[0]!;
    if (TEST_SENT_KINDS.has(a.kind)) return null; // hide tests by default
    return <ActivityRow activity={a} onReply={onReply} />;
  }
  const isClosed = group.status === "closed";
  return (
    <li className={`border border-border rounded ${isClosed ? "opacity-70" : ""}`}>
      <div className="px-2 py-1 border-b border-border bg-bg-input/30 flex items-center gap-2 text-xs">
        <span
          className={`inline-block w-1.5 h-1.5 rounded-full shrink-0 ${PRIORITY_DOT[group.priority] || PRIORITY_DOT.normal}`}
          title={`priority: ${group.priority}`}
        />
        <span className="text-[10px] uppercase text-text-dim">{group.channel}</span>
        <span className="text-text font-medium truncate flex-1">{group.subject || "(no subject)"}</span>
        <span className="text-text-dim">{group.activities.length} msg{group.activities.length === 1 ? "" : "s"}</span>
        <ConversationStatusControl group={group} onSetStatus={onSetStatus} busy={busy} />
      </div>
      <ul className="divide-y divide-border">
        {group.activities.map((a) => (
          <ActivityRow key={a.id} activity={a} onReply={onReply} compact />
        ))}
      </ul>
    </li>
  );
}

// ConversationStatusControl renders the status pill and, on the same
// row, the quick state actions. Priority is a tiny inline select.
function ConversationStatusControl({
  group,
  onSetStatus,
  busy,
}: {
  group: Extract<Group, { kind: "conversation" }>;
  onSetStatus: (conversationId: string, patch: { status?: string; priority?: string; spam_scope?: string; force?: boolean }) => void;
  busy: boolean;
}) {
  const cid = group.conversationId;
  const pillCls = `text-[10px] px-1.5 py-0.5 rounded ${STATUS_STYLES[group.status] || STATUS_STYLES.open}`;
  const btn = "text-[10px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-50";
  return (
    <span className="flex items-center gap-1">
      <span className={pillCls}>{group.status}</span>
      <select
        value={group.priority}
        disabled={busy}
        onChange={(e) => onSetStatus(cid, { priority: e.target.value })}
        className="bg-bg-input border border-border rounded text-[10px] px-0.5 py-0.5"
        title="priority"
      >
        {CONVO_PRIORITIES.map((p) => (
          <option key={p} value={p}>{p}</option>
        ))}
      </select>
      {group.status !== "pending" && group.status !== "closed" && group.status !== "spam" && (
        <button type="button" disabled={busy} className={btn} onClick={() => onSetStatus(cid, { status: "pending" })}>Pending</button>
      )}
      {group.status !== "open" && (
        <button type="button" disabled={busy} className={btn} onClick={() => onSetStatus(cid, { status: "open" })}>Reopen</button>
      )}
      {group.status !== "closed" && group.status !== "spam" && (
        <button type="button" disabled={busy} className={btn} onClick={() => onSetStatus(cid, { status: "closed" })}>Close</button>
      )}
      {group.status !== "spam" && (
        <button type="button" disabled={busy} className={btn} onClick={() => onSetStatus(cid, { status: "spam", spam_scope: "sender" })}>Spam</button>
      )}
      {group.channel === "email" && (
        <button type="button" disabled={busy} className={btn} onClick={() => onSetStatus(cid, { status: "spam", spam_scope: "domain" })}>Block domain</button>
      )}
    </span>
  );
}

function messageStatusLabel(status: string): string {
  switch (status) {
    case "delivery_delayed": return "delayed";
    case "opened": return "opened";
    default: return status || "unknown";
  }
}

function messageStatusTitle(status: MessageStatus): string {
  const lines = [`Messaging status: ${messageStatusLabel(status.status)}`];
  if (status.status_reason) lines.push(status.status_reason);
  if (status.last_event_at) lines.push(`last event: ${formatTime(status.last_event_at)}`);
  if (status.provider_message_id) lines.push(`provider: ${status.provider_message_id}`);
  if (status.events && status.events.length > 0) {
    const latest = status.events[status.events.length - 1];
    const detail = [latest.kind, latest.recipient, latest.reason].filter(Boolean).join(" · ");
    lines.push(`latest: ${detail}${latest.occurred_at ? ` · ${formatTime(latest.occurred_at)}` : ""}`);
  }
  return lines.join("\n");
}

function MessageStatusPill({ status }: { status: MessageStatus }) {
  const cls = MESSAGE_STATUS_STYLES[status.status] || "bg-border text-text-muted";
  return (
    <span
      className={`text-[10px] px-1.5 py-0.5 rounded ${cls}`}
      title={messageStatusTitle(status)}
    >
      {messageStatusLabel(status.status)}
    </span>
  );
}

function ActivityRow({ activity, onReply, compact }: { activity: Activity; onReply: (a: Activity) => void; compact?: boolean }) {
  const isFailed = FAILED_KINDS.has(activity.kind);
  const isReceived = RECEIVED_KINDS.has(activity.kind);
  return (
    <li className={`${compact ? "p-2" : "border border-border rounded p-2"}`}>
      <div className="flex items-center gap-2 text-xs text-text-dim mb-1">
        <span className="text-base leading-none">{iconForKind(activity.kind)}</span>
        <span className={`text-[10px] px-1.5 py-0.5 rounded ${isFailed ? "bg-red/15 text-red" : "bg-accent/10 text-accent"}`}>
          {activity.kind}
        </span>
        {activity.message_status && <MessageStatusPill status={activity.message_status} />}
        <span>{formatTime(activity.occurred_at)}{activity.source ? ` · ${activity.source}` : ""}</span>
        {isReceived && (
          <button
            type="button"
            onClick={() => onReply(activity)}
            className="ml-auto text-[10px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input"
          >Reply</button>
        )}
      </div>
      {activity.body && <div className="text-sm text-text whitespace-pre-wrap">{activity.body}</div>}
      {activity.attachments && activity.attachments.length > 0 && (
        <ActivityAttachments attachments={activity.attachments} />
      )}
    </li>
  );
}

function ActivityAttachments({ attachments }: { attachments: ActivityAttachment[] }) {
  return (
    <ul className="mt-2 grid gap-2 sm:grid-cols-2">
      {attachments.map((attachment, index) => {
        const href = attachment.download_url || attachment.url || "";
        const ready = !attachment.processing_status || attachment.processing_status === "ready";
        const image = ready && !!href && (attachment.content_type || "").startsWith("image/");
        const name = attachment.filename || `attachment-${index + 1}`;
        const content = (
          <>
            {image ? (
              <img src={href} alt="" className="w-10 h-10 rounded object-cover bg-bg-input shrink-0" />
            ) : (
              <span className="w-10 h-10 rounded bg-bg-input flex items-center justify-center text-lg shrink-0">📎</span>
            )}
            <span className="min-w-0">
              <span className="block truncate text-xs text-text">{name}</span>
              <span className="block truncate text-[10px] text-text-dim">
                {ready ? (attachment.content_type || "file") : (attachment.processing_status || "unavailable")}
                {attachment.size_bytes ? ` · ${formatBytes(attachment.size_bytes)}` : ""}
              </span>
              {!ready && attachment.processing_error && (
                <span className="block truncate text-[10px] text-red" title={attachment.processing_error}>{attachment.processing_error}</span>
              )}
            </span>
          </>
        );
        return (
          <li key={String(attachment.id || attachment.messaging_attachment_id || index)}>
            {href && ready ? (
              <a href={href} target="_blank" rel="noopener noreferrer" className="flex items-center gap-2 rounded border border-border p-2 hover:bg-bg-input" title={`Open ${name}`}>
                {content}
              </a>
            ) : (
              <div className="flex items-center gap-2 rounded border border-border p-2 opacity-75">{content}</div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

// ─── Composer ─────────────────────────────────────────────────────

interface ComposerState {
  mode: "new" | "reply";
  channel: string;
  subject: string;
  body: string;
  // from: explicit sender override. "" = let backend pick the install
  // default. Operator picks via the composer dropdown; defaults to
  // the messaging-side default sender for the current channel when
  // the composer opens.
  from: string;
  templateId: string;
  templateVars: Record<string, string>;
  attachments: ComposeAttachment[];
  whatsAppSession: WhatsAppSessionState;
  conversationId?: number | string;
  replyToActivityId?: string;
  busy: boolean;
  error: string | null;
}

// SenderOption mirrors the verified-sender shape we pull from
// messaging.senders_list for the From picker.
interface SenderOption {
  channel: string;
  address: string;
  label: string;
  isDefault: boolean;
}

interface TemplateOption {
  id: number;
  channel: string;
  name: string;
  body_text?: string;
  body_html?: string;
  vars_schema?: Record<string, unknown>;
  provider_template_id?: string;
  provider_status?: string;
}

type WhatsAppSessionState =
  | { state: "idle" }
  | { state: "checking" }
  | { state: "active"; lastInbound?: string }
  | { state: "closed"; lastInbound?: string }
  | { state: "error"; error?: string };

function preferredChannel(c: Contact, senders: SenderOption[] = []): string {
  const channels = availableChannels(c);
  if (channels.includes("email")) return "email";
  if (channels.includes("whatsapp") && senders.some((s) => s.channel === "whatsapp")) return "whatsapp";
  return channels[0] || "email";
}

function preferredSenderForChannel(senders: SenderOption[], channel: string): SenderOption | undefined {
  return senders.find((s) => s.channel === channel && s.isDefault) ||
    senders.find((s) => s.channel === channel);
}

function whatsappSessionRequiresTemplate(session: WhatsAppSessionState): boolean {
  return session.state === "closed" || session.state === "error" || session.state === "idle";
}

function ComposerModal({
  composer,
  contact,
  projectId,
  senders,
  templates,
  onCancel,
  onChange,
  onSend,
}: {
  composer: ComposerState;
  contact: Contact;
  projectId: string;
  senders: SenderOption[];
  templates: TemplateOption[];
  onCancel: () => void;
  onChange: (patch: Partial<ComposerState>) => void;
  onSend: () => void;
}) {
  const [storageOpen, setStorageOpen] = useState(false);
  const [storageFiles, setStorageFiles] = useState<StorageFile[]>([]);
  const [storageSearch, setStorageSearch] = useState("");
  const [storageStatus, setStorageStatus] = useState("");
  const channels = availableChannels(contact);
  const isEmail = composer.channel === "email";
  const isWhatsApp = composer.channel === "whatsapp";
  const toAddr = addressForChannel(contact, composer.channel);
  // Filter senders to those that match the current channel — picking
  // an SMS sender for an email send is never what the operator wants.
  const sendersForChannel = senders.filter((s) => s.channel === composer.channel);
  const selectedTemplate = templates.find((t) => String(t.id) === composer.templateId) || null;
  const selectedTemplateVars = selectedTemplate ? templateVarKeys(selectedTemplate) : [];
  const whatsappClosed = isWhatsApp && whatsappSessionRequiresTemplate(composer.whatsAppSession);
  const whatsappChecking = isWhatsApp && composer.whatsAppSession.state === "checking";
  const canSendTemplate = !whatsappClosed || (!!composer.templateId && selectedTemplateVars.every((key) => composer.templateVars[key]?.trim()));
  const bodyRequired = !whatsappClosed;
  const hasFreeformContent = !!composer.body.trim() || composer.attachments.length > 0;
  const canSend = !composer.busy && !!toAddr && !toAddr.startsWith("(no ") &&
    !whatsappChecking &&
    sendersForChannel.length > 0 &&
    (bodyRequired ? hasFreeformContent : canSendTemplate);
  const labelW = "w-20 shrink-0 text-text-muted text-xs uppercase tracking-wide";
  const fieldCls = "flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm";

  useEffect(() => {
    if (!storageOpen) return;
    let cancelled = false;
    setStorageStatus("Loading Storage files…");
    const params = new URLSearchParams({ project_id: projectId, limit: "100" });
    fetch(`/api/apps/storage/files?${params.toString()}`, { credentials: "same-origin" })
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
        return res.json() as Promise<{ files?: StorageFile[] }>;
      })
      .then((result) => {
        if (cancelled) return;
        setStorageFiles(result.files || []);
        setStorageStatus((result.files || []).length === 0 ? "No Storage files found." : "");
      })
      .catch((error) => {
        if (!cancelled) setStorageStatus(`Storage unavailable: ${(error as Error).message}`);
      });
    return () => { cancelled = true; };
  }, [projectId, storageOpen]);

  const appendAttachments = (next: ComposeAttachment[]) => {
    const unique = next.filter((candidate) => !composer.attachments.some((existing) =>
      (candidate.storage_id && existing.storage_id === candidate.storage_id) || existing.key === candidate.key));
    if (composer.attachments.length + unique.length > 20) {
      onChange({ error: "You can attach at most 20 files." });
      return;
    }
    const total = [...composer.attachments, ...unique].reduce((sum, item) => sum + (item.size_bytes || 0), 0);
    if (total > 25 * 1024 * 1024) {
      onChange({ error: "Attachments cannot exceed 25 MB in total." });
      return;
    }
    onChange({ attachments: [...composer.attachments, ...unique], error: null });
  };

  const addLocalFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    try {
      appendAttachments(await Promise.all(Array.from(files).map(fileToComposeAttachment)));
    } catch (error) {
      onChange({ error: (error as Error).message });
    }
  };

  const addStorageFile = (file: StorageFile) => {
    appendAttachments([{
      key: `storage:${file.id}`,
      storage_id: Number(file.id),
      filename: file.name || `file-${file.id}`,
      content_type: file.content_type || "application/octet-stream",
      size_bytes: file.size_bytes || 0,
    }]);
  };

  const visibleStorageFiles = storageFiles.filter((file) => {
    const q = storageSearch.trim().toLowerCase();
    return !q || `${file.folder || ""}${file.name}`.toLowerCase().includes(q);
  });

  return (
    <div className="absolute inset-0 bg-black/50 flex items-center justify-center pointer-events-auto p-6">
      <div className="bg-bg border border-border w-full max-w-3xl rounded-lg shadow-xl flex flex-col" style={{ maxHeight: "85vh" }}>
        <header className="flex items-center justify-between px-5 py-3 border-b border-border">
          <div className="text-text">
            {composer.mode === "reply" ? "Reply to " : "New message to "}
            <span className="font-medium">{displayName(contact)}</span>
            {composer.conversationId && (
              <span className="ml-2 text-text-dim text-xs">· thread #{composer.conversationId}</span>
            )}
          </div>
          <button
            type="button"
            onClick={onCancel}
            className="text-text-dim hover:text-text text-lg leading-none px-2"
            aria-label="Close"
          >×</button>
        </header>

        <div className="px-5 py-4 space-y-3 overflow-auto">
          <div className="flex items-center gap-3">
            <label className={labelW}>From</label>
            {sendersForChannel.length > 0 ? (
              <select
                value={composer.from}
                onChange={(e) => onChange({ from: e.target.value })}
                className={fieldCls}
              >
                {sendersForChannel.map((s) => (
                  <option key={s.address} value={s.address}>
                    {s.label}{s.isDefault ? "  (default)" : ""}
                  </option>
                ))}
              </select>
            ) : (
              <span className="text-text-dim text-xs italic">
                no verified {composer.channel} senders in Messaging
              </span>
            )}
          </div>

          <div className="flex items-center gap-3">
            <label className={labelW}>To</label>
            <div className="flex-1 flex items-center gap-2 text-sm">
              <span className="text-text">{toAddr || <span className="text-red italic">no {composer.channel} address on contact</span>}</span>
              {channels.length > 1 && (
                <select
                  value={composer.channel}
                  onChange={(e) => {
                    const newCh = e.target.value;
                    const def = preferredSenderForChannel(senders, newCh);
                    onChange({
                      channel: newCh,
                      from: def?.address || "",
                      templateId: "",
                      templateVars: {},
                      whatsAppSession: { state: "idle" },
                    });
                  }}
                  disabled={composer.mode === "reply"}
                  className="bg-bg-input border border-border rounded px-2 py-0.5 text-xs disabled:opacity-50"
                  title="Switch channel"
                >
                  {channels.map((ch) => <option key={ch} value={ch}>via {ch}</option>)}
                </select>
              )}
            </div>
          </div>

          {isEmail && (
            <div className="flex items-center gap-3">
              <label className={labelW}>Subject</label>
              <input
                type="text"
                value={composer.subject}
                onChange={(e) => onChange({ subject: e.target.value })}
                placeholder={composer.mode === "reply" ? "Re: …" : "Subject"}
                className={fieldCls}
              />
            </div>
          )}

          {isWhatsApp && (
            <div className="rounded border border-border bg-bg-input/40 px-3 py-2 text-xs text-text-dim">
              {composer.whatsAppSession.state === "checking" ? (
                "Checking the WhatsApp 24-hour window..."
              ) : composer.whatsAppSession.state === "active" ? (
                "Recent inbound WhatsApp message found. Free-form reply is allowed."
              ) : (
                <>
                  No recent inbound WhatsApp message was found. Use an approved template outside the 24-hour window.
                  {composer.whatsAppSession.state === "error" && composer.whatsAppSession.error && (
                    <div className="mt-1 text-red">{composer.whatsAppSession.error}</div>
                  )}
                </>
              )}
            </div>
          )}

          {whatsappClosed && (
            <div className="space-y-3 rounded border border-border bg-bg-input/30 p-3">
              <div className="flex items-center gap-3">
                <label className={labelW}>Template</label>
                {templates.length > 0 ? (
                  <select
                    value={composer.templateId}
                    onChange={(e) => {
                      const next = templates.find((t) => String(t.id) === e.target.value) || null;
                      const vars: Record<string, string> = {};
                      if (next) templateVarKeys(next).forEach((key) => { vars[key] = composer.templateVars[key] || ""; });
                      onChange({ templateId: e.target.value, templateVars: vars, body: "" });
                    }}
                    className={fieldCls}
                  >
                    <option value="">Pick a template</option>
                    {templates.map((t) => (
                      <option key={t.id} value={String(t.id)}>{t.name}</option>
                    ))}
                  </select>
                ) : (
                  <span className="text-text-dim text-xs italic">no approved WhatsApp templates in Messaging</span>
                )}
              </div>
              {selectedTemplateVars.length > 0 && (
                <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                  {selectedTemplateVars.map((key) => (
                    <label key={key} className="text-xs text-text-dim">
                      {`{{${key}}}`}
                      <input
                        className={`${fieldCls} mt-1 w-full`}
                        value={composer.templateVars[key] || ""}
                        onChange={(e) => onChange({
                          templateVars: { ...composer.templateVars, [key]: e.target.value },
                        })}
                      />
                    </label>
                  ))}
                </div>
              )}
              {selectedTemplate && (
                <pre className="whitespace-pre-wrap rounded border border-border bg-bg px-3 py-2 text-xs text-text">
                  {renderTemplatePreview(selectedTemplate.body_text || selectedTemplate.body_html || "", composer.templateVars)}
                </pre>
              )}
            </div>
          )}

          {!whatsappClosed && (
          <div className="pt-2">
            <textarea
              value={composer.body}
              onChange={(e) => onChange({ body: e.target.value })}
              rows={14}
              className="w-full bg-bg-input border border-border rounded px-3 py-2 text-sm leading-relaxed resize-y"
              placeholder="Write your message…"
              autoFocus
            />
          </div>
          )}

          <div className="rounded border border-border p-3 space-y-2">
            <div className="flex items-center gap-2">
              <span className="text-xs uppercase tracking-wide text-text-muted">Attachments</span>
              <label className="ml-auto text-xs px-2 py-1 border border-border rounded hover:bg-bg-input cursor-pointer">
                Add from device
                <input
                  type="file"
                  multiple
                  className="hidden"
                  disabled={composer.busy}
                  onChange={(event) => {
                    void addLocalFiles(event.currentTarget.files);
                    event.currentTarget.value = "";
                  }}
                />
              </label>
              <button
                type="button"
                onClick={() => setStorageOpen((value) => !value)}
                disabled={composer.busy}
                className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input disabled:opacity-50"
              >{storageOpen ? "Hide Storage" : "Add from Storage"}</button>
            </div>

            {composer.attachments.length > 0 && (
              <ul className="divide-y divide-border rounded border border-border">
                {composer.attachments.map((attachment) => (
                  <li key={attachment.key} className="flex items-center gap-2 px-2 py-1.5 text-xs">
                    <span aria-hidden>📎</span>
                    <span className="min-w-0 flex-1 truncate">
                      {attachment.filename}
                      <span className="text-text-dim"> · {formatBytes(attachment.size_bytes)}{attachment.storage_id ? " · Storage" : ""}</span>
                    </span>
                    <button
                      type="button"
                      onClick={() => onChange({ attachments: composer.attachments.filter((item) => item.key !== attachment.key), error: null })}
                      className="text-text-dim hover:text-red"
                      aria-label={`Remove ${attachment.filename}`}
                    >Remove</button>
                  </li>
                ))}
              </ul>
            )}

            {storageOpen && (
              <div className="rounded border border-border bg-bg-input/40 p-2 space-y-2">
                <input
                  type="search"
                  value={storageSearch}
                  onChange={(event) => setStorageSearch(event.target.value)}
                  placeholder="Filter Storage files…"
                  className="w-full bg-bg border border-border rounded px-2 py-1 text-xs"
                />
                {storageStatus && <div className="text-xs text-text-dim">{storageStatus}</div>}
                {visibleStorageFiles.length > 0 && (
                  <ul className="max-h-40 overflow-auto divide-y divide-border">
                    {visibleStorageFiles.map((file) => {
                      const selected = composer.attachments.some((item) => item.storage_id === Number(file.id));
                      return (
                        <li key={String(file.id)}>
                          <button
                            type="button"
                            disabled={selected}
                            onClick={() => addStorageFile(file)}
                            className="w-full flex items-center gap-2 px-2 py-1.5 text-left text-xs hover:bg-bg disabled:opacity-50"
                          >
                            <span className="truncate flex-1">{file.folder || "/"}{file.name}</span>
                            <span className="text-text-dim">{formatBytes(file.size_bytes)}</span>
                            <span className="text-accent">{selected ? "Added" : "Add"}</span>
                          </button>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            )}
            <p className="text-[10px] text-text-dim">Up to 20 files and 25 MB total. Email sends files; SMS and WhatsApp send media.</p>
          </div>

          {composer.error && (
            <div className="rounded border border-red/40 bg-red/10 text-red text-xs px-3 py-2 whitespace-pre-wrap">
              {composer.error}
            </div>
          )}
        </div>

        <footer className="flex items-center gap-2 px-5 py-3 border-t border-border bg-bg-input/30">
          <button
            type="button"
            onClick={onSend}
            disabled={!canSend}
            className="px-4 py-1.5 text-sm bg-accent text-bg rounded hover:opacity-90 disabled:opacity-40 disabled:cursor-not-allowed"
          >{composer.busy ? "Sending…" : "Send"}</button>
          <button
            type="button"
            onClick={onCancel}
            className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <span className="ml-auto text-text-dim text-xs">
            {composer.from
              ? <>from <span className="font-mono">{composer.from}</span></>
              : <>pick a verified sender</>}
          </span>
        </footer>
      </div>
    </div>
  );
}

function availableChannels(c: Contact): string[] {
  const out: string[] = [];
  if (c.channels) {
    for (const ch of c.channels) {
      if (ch.kind === "email" && channelIsMessageable(ch, "email") && !out.includes("email")) out.push("email");
      if (ch.kind === "phone") {
        if (channelIsMessageable(ch, "whatsapp") && !out.includes("whatsapp")) out.push("whatsapp");
        if (channelIsMessageable(ch, "sms") && !out.includes("sms")) out.push("sms");
      }
    }
  }
  // Older CRM responses have no per-route state. Preserve their previous
  // behavior until the contact is refreshed against the upgraded backend.
  if (!c.channels || c.channels.length === 0) {
    if (c.primary_email) out.push("email");
    if (c.primary_phone) out.push("whatsapp", "sms");
  }
  return out;
}

function channelIsMessageable(channel: Channel, transport: string): boolean {
  const state = channel.deliverability?.find((item) => item.transport === transport);
  return state ? state.messageable : true;
}

async function fileToComposeAttachment(file: File): Promise<ComposeAttachment> {
  if (file.size > 25 * 1024 * 1024) {
    throw new Error(`${file.name} is larger than 25 MB.`);
  }
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error(`Could not read ${file.name}.`));
    reader.readAsDataURL(file);
  });
  const comma = dataURL.indexOf(",");
  return {
    key: `${file.name}:${file.size}:${file.lastModified}:${Math.random().toString(36).slice(2)}`,
    filename: file.name || "attachment",
    content_type: file.type || "application/octet-stream",
    size_bytes: file.size,
    content_base64: comma >= 0 ? dataURL.slice(comma + 1) : dataURL,
  };
}

function formatBytes(value?: number): string {
  if (!value || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return `${size >= 10 || index === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[index]}`;
}

function addressForChannel(c: Contact, channel: string): string {
  if (channel === "email") {
    return c.channels?.find((ch) => ch.kind === "email" && channelIsMessageable(ch, "email"))?.value || "(no messageable email)";
  }
  if (channel === "sms" || channel === "whatsapp") {
    return c.channels?.find((ch) => ch.kind === "phone" && channelIsMessageable(ch, channel))?.value || `(no messageable ${channel} phone)`;
  }
  return "—";
}

function templateVarKeys(template: TemplateOption): string[] {
  const keys = new Set<string>();
  if (template.vars_schema && typeof template.vars_schema === "object") {
    Object.keys(template.vars_schema).forEach((key) => keys.add(key));
  }
  for (const source of [template.body_text || "", template.body_html || ""]) {
    const re = /\{\{\s*([^{}\s]+)\s*\}\}/g;
    let match: RegExpExecArray | null;
    while ((match = re.exec(source))) keys.add(match[1]);
  }
  return Array.from(keys).sort((a, b) => {
    const na = Number(a);
    const nb = Number(b);
    if (Number.isFinite(na) && Number.isFinite(nb)) return na - nb;
    return a.localeCompare(b);
  });
}

function renderTemplatePreview(body: string, vars: Record<string, string>): string {
  return body.replace(/\{\{\s*([^{}\s]+)\s*\}\}/g, (_, key: string) => vars[key]?.trim() || `{{${key}}}`);
}

// ─── Opportunities ────────────────────────────────────────────────

function defaultPipeline(pipelines: Pipeline[]): Pipeline | null {
  return pipelines.find((p) => p.is_default && !p.archived_at)
    || pipelines.find((p) => !p.archived_at)
    || null;
}

function activeStages(pipeline: Pipeline | null | undefined): PipelineStage[] {
  return (pipeline?.stages || [])
    .filter((stage) => !stage.archived_at)
    .sort((a, b) => a.position - b.position || a.id - b.id);
}

function formatMoney(value?: number, currency?: string): string {
  if (typeof value !== "number") return "";
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "USD",
      maximumFractionDigits: 0,
    }).format(value);
  } catch {
    return `${value} ${currency || "USD"}`;
  }
}

function opportunityStatusStyle(status: string): string {
  if (status === "won") return "bg-accent/10 text-accent";
  if (status === "lost") return "bg-red/10 text-red";
  return "bg-border text-text-muted";
}

function OpportunityRow({ opportunity, stages, onMove, onOpenContact }: {
  opportunity: Opportunity;
  stages: PipelineStage[];
  onMove: (opportunity: Opportunity, patch: Partial<Opportunity> & { note?: string }) => void;
  onOpenContact?: (contactId: number) => void;
}) {
  return (
    <li className="border-b border-border px-3 py-2 last:border-b-0">
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="text-sm text-text font-medium truncate">{opportunity.title}</div>
          <div className="text-xs text-text-muted truncate">
            {opportunity.contact_name || opportunity.contact_email || `Contact #${opportunity.contact_id}`}
          </div>
        </div>
        <span className={`text-[10px] px-1.5 py-0.5 rounded ${opportunityStatusStyle(opportunity.status)}`}>
          {opportunity.status}
        </span>
      </div>
      <div className="mt-2 flex items-center gap-2">
        <select
          value={String(opportunity.stage_id)}
          onChange={(e) => onMove(opportunity, {
            stage_id: Number(e.target.value),
            note: "Stage changed from CRM panel",
          })}
          className="min-w-0 flex-1 bg-bg-input border border-border rounded px-1.5 py-0.5 text-xs"
        >
          {stages.map((stage) => (
            <option key={stage.id} value={String(stage.id)}>{stage.name}</option>
          ))}
        </select>
        {formatMoney(opportunity.value, opportunity.currency) && (
          <span className="text-xs text-text-dim">{formatMoney(opportunity.value, opportunity.currency)}</span>
        )}
        {onOpenContact && (
          <button
            type="button"
            onClick={() => onOpenContact(opportunity.contact_id)}
            className="text-xs px-2 py-0.5 border border-border rounded hover:bg-bg-input"
          >Contact</button>
        )}
      </div>
    </li>
  );
}

function ContactOpportunitiesSection({ opportunities, pipelines, onCreate, onMove }: {
  opportunities: Opportunity[];
  pipelines: Pipeline[];
  onCreate: () => void;
  onMove: (opportunity: Opportunity, patch: Partial<Opportunity> & { note?: string }) => void;
}) {
  const stagesByPipeline = useMemo(() => {
    const out = new Map<number, PipelineStage[]>();
    pipelines.forEach((pipeline) => out.set(pipeline.id, activeStages(pipeline)));
    return out;
  }, [pipelines]);
  return (
    <section>
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-xs uppercase tracking-wide text-text-dim">Opportunities</h2>
        <button
          type="button"
          onClick={onCreate}
          className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg"
        >New opportunity</button>
      </div>
      {opportunities.length === 0 ? (
        <p className="text-text-muted text-sm">No opportunities yet.</p>
      ) : (
        <ul className="border border-border rounded">
          {opportunities.map((opportunity) => (
            <OpportunityRow
              key={opportunity.id}
              opportunity={opportunity}
              stages={stagesByPipeline.get(opportunity.pipeline_id) || []}
              onMove={onMove}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

function OpportunitiesTab({ opportunities, pipelines, statusFilter, onStatusFilter, onRefresh, onMove, onOpenContact }: {
  opportunities: Opportunity[];
  pipelines: Pipeline[];
  statusFilter: string;
  onStatusFilter: (value: string) => void;
  onRefresh: () => void;
  onMove: (opportunity: Opportunity, patch: Partial<Opportunity> & { note?: string }) => void;
  onOpenContact: (contactId: number) => void;
}) {
  const pipeline = defaultPipeline(pipelines);
  const stages = activeStages(pipeline);
  const byStage = useMemo(() => {
    const out = new Map<number, Opportunity[]>();
    stages.forEach((stage) => out.set(stage.id, []));
    opportunities.forEach((opportunity) => {
      const rows = out.get(opportunity.stage_id) || [];
      rows.push(opportunity);
      out.set(opportunity.stage_id, rows);
    });
    return out;
  }, [opportunities, stages]);
  return (
    <div className="h-full flex flex-col">
      <header className="p-3 border-b border-border flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <h1 className="text-sm text-text font-medium">Opportunities</h1>
          <p className="text-xs text-text-muted truncate">
            {pipeline?.name || "No pipeline"} · {opportunities.length} shown
          </p>
        </div>
        <select
          value={statusFilter}
          onChange={(e) => onStatusFilter(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
        >
          <option value="open">Open</option>
          <option value="won">Won</option>
          <option value="lost">Lost</option>
          <option value="all">All</option>
        </select>
        <button type="button" onClick={onRefresh} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">
          Refresh
        </button>
      </header>
      <div className="flex-1 overflow-auto p-4">
        {stages.length === 0 ? (
          <p className="text-text-muted text-sm">No pipeline stages.</p>
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: `repeat(${Math.min(stages.length, 7)}, minmax(220px, 1fr))` }}>
            {stages.map((stage) => {
              const rows = byStage.get(stage.id) || [];
              return (
                <section key={stage.id} className="min-w-0">
                  <div className="flex items-center gap-2 mb-2">
                    <h2 className="text-xs uppercase tracking-wide text-text-dim truncate flex-1">{stage.name}</h2>
                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">{rows.length}</span>
                  </div>
                  <ul className="border border-border rounded">
                    {rows.length === 0 ? (
                      <li className="p-3 text-xs text-text-dim">No opportunities.</li>
                    ) : rows.map((opportunity) => (
                      <OpportunityRow
                        key={opportunity.id}
                        opportunity={opportunity}
                        stages={stages}
                        onMove={onMove}
                        onOpenContact={onOpenContact}
                      />
                    ))}
                  </ul>
                </section>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

function OpportunityEditorModal({ contact, pipelines, onCancel, onSubmit }: {
  contact: Contact;
  pipelines: Pipeline[];
  onCancel: () => void;
  onSubmit: (draft: Partial<Opportunity>) => void | Promise<void>;
}) {
  const pipeline = defaultPipeline(pipelines);
  const stages = activeStages(pipeline);
  const [title, setTitle] = useState("");
  const [stageId, setStageId] = useState(stages[0]?.id ? String(stages[0].id) : "");
  const [offerName, setOfferName] = useState("");
  const [offerKey, setOfferKey] = useState("");
  const [senderIdentity, setSenderIdentity] = useState("");
  const [sourceSite, setSourceSite] = useState("");
  const [value, setValue] = useState("");
  const [currency, setCurrency] = useState("USD");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!stageId && stages[0]?.id) setStageId(String(stages[0].id));
  }, [stageId, stages]);

  const submit = async () => {
    if (!pipeline || !stageId) return;
    setBusy(true);
    try {
      await onSubmit({
        pipeline_id: pipeline.id,
        stage_id: Number(stageId),
        title: title.trim() || `${offerName.trim() || "Opportunity"} for ${displayName(contact)}`,
        offer_name: offerName.trim() || undefined,
        offer_key: offerKey.trim() || undefined,
        sender_identity: senderIdentity.trim() || undefined,
        source_site: sourceSite.trim() || undefined,
        value: value.trim() ? Number(value) : undefined,
        currency: currency.trim().toUpperCase() || undefined,
      });
    } finally {
      setBusy(false);
    }
  };
  const fieldClass = "bg-bg-input border border-border rounded px-2 py-1 text-sm";
  return (
    <ModalShell
      title={`New opportunity · ${displayName(contact)}`}
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !pipeline || !stageId}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Creating…" : "Create"}</button>
          <button type="button" onClick={onCancel} disabled={busy} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">
            Cancel
          </button>
        </>
      }
    >
      <div className="grid grid-cols-[120px_1fr] gap-2 text-sm">
        <label className="text-text-muted self-center">Pipeline</label>
        <div className="text-text self-center">{pipeline?.name || "No pipeline"}</div>
        <label className="text-text-muted self-center">Stage</label>
        <select value={stageId} onChange={(e) => setStageId(e.target.value)} className={fieldClass}>
          {stages.map((stage) => <option key={stage.id} value={String(stage.id)}>{stage.name}</option>)}
        </select>
        <label className="text-text-muted self-center">Title</label>
        <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Opportunity title" className={fieldClass} />
        <label className="text-text-muted self-center">Offer</label>
        <input value={offerName} onChange={(e) => setOfferName(e.target.value)} placeholder="Offer name" className={fieldClass} />
        <label className="text-text-muted self-center">Offer key</label>
        <input value={offerKey} onChange={(e) => setOfferKey(e.target.value)} placeholder="offer_key" className={fieldClass} />
        <label className="text-text-muted self-center">Sender</label>
        <input value={senderIdentity} onChange={(e) => setSenderIdentity(e.target.value)} placeholder="sender@example.com" className={fieldClass} />
        <label className="text-text-muted self-center">Source site</label>
        <input value={sourceSite} onChange={(e) => setSourceSite(e.target.value)} placeholder="example.com" className={fieldClass} />
        <label className="text-text-muted self-center">Value</label>
        <div className="flex gap-2">
          <input value={value} onChange={(e) => setValue(e.target.value)} type="number" min="0" step="1" className={`${fieldClass} flex-1`} />
          <input value={currency} onChange={(e) => setCurrency(e.target.value)} className={`${fieldClass} w-24`} />
        </div>
      </div>
    </ModalShell>
  );
}

// ─── Settings tab ─────────────────────────────────────────────────

const CRM_INBOUND_TARGET_ROUTE = "messaging_inbound_receive";
const CRM_INBOUND_LEGACY_TARGET_ROUTE = "/inbound";

function SettingsTab({ messagingTool, api, lists, attrDefs, onAddField }: {
  messagingTool: <T,>(tool: string, args?: Record<string, unknown>) => Promise<T>;
  api: <T,>(method: string, path: string, body?: any, params?: Record<string, string>) => Promise<T>;
  lists: List[];
  attrDefs: AttributeDef[];
  onAddField: () => void;
}) {
  const [routes, setRoutes] = useState<InboundRoute[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const loadRoutes = useCallback(async () => {
    setError(null);
    try {
      const r = await messagingTool<{ routes?: InboundRoute[] }>("inbound_route_list");
      setRoutes(r.routes || []);
    } catch (e) {
      setRoutes(null);
      setError((e as Error).message);
    }
  }, [messagingTool]);

  const ensureRoute = useCallback(async (channel: "email" | "sms" | "whatsapp") => {
    await messagingTool("inbound_route_set", {
      pattern: "*",
      channel,
      target_app: "crm",
      target_route: CRM_INBOUND_TARGET_ROUTE,
      priority: 0,
    });
  }, [messagingTool]);

  useEffect(() => {
    let cancelled = false;
    setBusy(true);
    setError(null);
    Promise.all([
      ensureRoute("email"),
      ensureRoute("sms"),
      ensureRoute("whatsapp"),
    ])
      .catch((e) => { if (!cancelled) setError((e as Error).message); })
      .finally(async () => {
        if (!cancelled) {
          setBusy(false);
          await loadRoutes();
        }
      });
    return () => { cancelled = true; };
  }, [ensureRoute, loadRoutes]);

  // CRM-pointing routes — what we wire up.
  const crmRoutes = useMemo(
    () => (routes || []).filter((r) => r.target_app === "crm" && (r.target_route === CRM_INBOUND_TARGET_ROUTE || r.target_route === CRM_INBOUND_LEGACY_TARGET_ROUTE)),
    [routes],
  );
  const hasEmail = crmRoutes.some((r) => r.channel === "email");
  const hasSMS = crmRoutes.some((r) => r.channel === "sms");
  const hasWhatsApp = crmRoutes.some((r) => r.channel === "whatsapp");

  const wire = async (channel: "email" | "sms" | "whatsapp") => {
    setBusy(true);
    setError(null);
    try {
      await ensureRoute(channel);
      await loadRoutes();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const messagingMissing = error?.startsWith("404") ||
    error?.toLowerCase().includes("not installed") ||
    error?.toLowerCase().includes("no such app");

  return (
    <div className="p-6 max-w-2xl space-y-6">
      <header>
        <h1 className="text-xl text-text font-semibold">Settings</h1>
        <p className="text-text-muted text-sm">CRM keeps Messaging routes in place so inbound mail/SMS/WhatsApp lands on the right contact's timeline automatically.</p>
      </header>

      <section>
        <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Inbound routing</h2>

        {routes === null && !error ? (
          <p className="text-text-muted text-sm">Loading…</p>
        ) : messagingMissing ? (
          <div className="border border-border rounded p-3 text-sm">
            <p className="text-text">The <span className="font-mono">messaging</span> app isn't installed in this project.</p>
            <p className="text-text-muted text-xs mt-1">Install it from the Apps directory to enable Send / Reply on contacts and inbound auto-attachment.</p>
          </div>
        ) : (
          <div className="space-y-2">
            <RouteRow label="Email"    wired={hasEmail}     onWire={() => wire("email")} busy={busy} />
            <RouteRow label="SMS"      wired={hasSMS}       onWire={() => wire("sms")} busy={busy} />
            <RouteRow label="WhatsApp" wired={hasWhatsApp}  onWire={() => wire("whatsapp")} busy={busy} />
            <p className="text-text-dim text-xs pt-2">
              CRM creates low-priority <span className="font-mono">*</span> catch-all routes in messaging. To
              constrain by recipient pattern, edit the route from the messaging panel.
            </p>
          </div>
        )}

        {error && !messagingMissing && (
          <div className="mt-3 text-red text-xs">Error: {error}</div>
        )}
      </section>

      <RoutingRulesSection api={api} lists={lists} />

      <section>
        <div className="flex items-center justify-between mb-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">Custom fields</h2>
          <button
            type="button"
            onClick={onAddField}
            className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >+ Add field</button>
        </div>
        {attrDefs.length === 0 ? (
          <p className="text-text-muted text-sm">No custom fields yet. Add one to enrich contacts with project-specific data.</p>
        ) : (
          <ul className="divide-y divide-border border border-border rounded">
            {attrDefs.map((d) => (
              <li key={d.key} className="px-3 py-2 flex items-center gap-3 text-sm">
                <span className="text-text font-medium flex-1 truncate">{d.label}</span>
                <span className="text-[10px] uppercase text-text-dim">{d.key}</span>
                <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">{d.type}</span>
                {d.required ? <span className="text-[10px] text-red">required</span> : null}
                {d.is_system ? <span className="text-[10px] text-accent">system</span> : null}
              </li>
            ))}
          </ul>
        )}
        <p className="text-text-dim text-xs pt-2">
          Fields are project-scoped. Type drives the editor (text → input, bool → checkbox, select → dropdown, date → date picker, etc.).
        </p>
      </section>
    </div>
  );
}

function RouteRow({ label, wired, onWire, busy }: { label: string; wired: boolean; onWire: () => void; busy: boolean }) {
  return (
    <div className="flex items-center gap-3 border border-border rounded p-2">
      <span className="text-sm text-text w-24">{label}</span>
      {wired ? (
        <span className="text-xs px-1.5 py-0.5 rounded bg-accent/15 text-accent">active</span>
      ) : (
        <span className="text-xs px-1.5 py-0.5 rounded bg-border text-text-muted">not wired</span>
      )}
      <button
        type="button"
        onClick={onWire}
        disabled={busy || wired}
        className="ml-auto text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
      >{wired ? "Wired" : "Repair"}</button>
    </div>
  );
}

// ─── Routing rules ────────────────────────────────────────────────

interface RoutingRule {
  id: number;
  name?: string;
  match_recipient?: string;
  match_sender?: string;
  add_list_id?: number;
  add_tag?: string;
  priority: number;
  enabled: boolean;
}

function RoutingRulesSection({ api, lists }: {
  api: <T,>(method: string, path: string, body?: any, params?: Record<string, string>) => Promise<T>;
  lists: List[];
}) {
  const [rules, setRules] = useState<RoutingRule[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [recipient, setRecipient] = useState("");
  const [sender, setSender] = useState("");
  const [tag, setTag] = useState("");
  const [listId, setListId] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const r = await api<{ rules?: RoutingRule[] }>("GET", "/routing-rules");
      setRules(r.rules || []);
    } catch (e) { setErr((e as Error).message); setRules([]); }
  }, [api]);
  useEffect(() => { load(); }, [load]);

  const listName = (id?: number) => lists.find((l) => Number(l.id) === id)?.name || `list #${id}`;

  const add = async () => {
    if (!tag.trim() && !listId) { setErr("Set an action: a tag and/or a list"); return; }
    setBusy(true); setErr(null);
    try {
      await api("POST", "/routing-rules", {
        match_recipient: recipient.trim() || undefined,
        match_sender: sender.trim() || undefined,
        add_tag: tag.trim() || undefined,
        add_list_id: listId ? Number(listId) : undefined,
      });
      setRecipient(""); setSender(""); setTag(""); setListId("");
      await load();
    } catch (e) { setErr((e as Error).message); }
    finally { setBusy(false); }
  };

  const del = async (id: number) => {
    setBusy(true);
    try { await api("DELETE", `/routing-rules/${id}`); await load(); }
    catch (e) { setErr((e as Error).message); }
    finally { setBusy(false); }
  };

  const inp = "bg-bg-input border border-border rounded px-2 py-1 text-sm";
  return (
    <section>
      <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Inbound routing rules</h2>
      <p className="text-text-dim text-xs mb-2">
        Match inbound on the address it hit (recipient) and/or who sent it, then auto-add the
        contact to a list and/or tag it. Patterns: <span className="font-mono">support@acme.com</span>,{" "}
        <span className="font-mono">@acme.com</span>, <span className="font-mono">support@*</span>,{" "}
        <span className="font-mono">*</span>. Leave a field blank for "any".
      </p>

      {rules && rules.length > 0 && (
        <ul className="divide-y divide-border border border-border rounded mb-3">
          {rules.map((r) => (
            <li key={r.id} className="px-3 py-2 flex items-center gap-2 text-xs">
              <span className="text-text-muted">to</span>
              <span className="font-mono text-text">{r.match_recipient || "*"}</span>
              <span className="text-text-muted">from</span>
              <span className="font-mono text-text">{r.match_sender || "*"}</span>
              <span className="text-text-dim">→</span>
              {r.add_list_id ? <span className="px-1.5 py-0.5 rounded bg-accent/10 text-accent">{listName(r.add_list_id)}</span> : null}
              {r.add_tag ? <span className="px-1.5 py-0.5 rounded bg-border text-text">#{r.add_tag}</span> : null}
              <button type="button" onClick={() => del(r.id)} disabled={busy}
                className="ml-auto text-text-dim hover:text-red disabled:opacity-50" aria-label="Delete rule">×</button>
            </li>
          ))}
        </ul>
      )}
      {rules && rules.length === 0 && (
        <p className="text-text-muted text-sm mb-3">No rules yet — new inbound lands in the Inbox unrouted.</p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <input className={inp} style={{ width: 160 }} placeholder="recipient (to)" value={recipient} onChange={(e) => setRecipient(e.target.value)} />
        <input className={inp} style={{ width: 160 }} placeholder="sender (from)" value={sender} onChange={(e) => setSender(e.target.value)} />
        <span className="text-text-dim text-xs">→</span>
        <select className={inp} value={listId} onChange={(e) => setListId(e.target.value)}>
          <option value="">— list —</option>
          {lists.map((l) => <option key={String(l.id)} value={String(l.id)}>{l.name}</option>)}
        </select>
        <input className={inp} style={{ width: 120 }} placeholder="tag" value={tag} onChange={(e) => setTag(e.target.value)} />
        <button type="button" onClick={add} disabled={busy}
          className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">+ Add rule</button>
      </div>
      {err && <div className="mt-2 text-red text-xs">Error: {err}</div>}
    </section>
  );
}

// ─── Inbox (cross-contact triage queue) ───────────────────────────

interface InboxItem {
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

interface InboxResponse {
  inbox?: InboxItem[];
  count?: number;
  total?: number;
  offset?: number;
}

function inboxAddressOp(value: string): string {
  const v = value.trim();
  if (v.startsWith("@")) return "domain";
  if (!v.includes("@") && v.includes(".")) return "domain";
  return "is";
}

function stripDomainPrefix(value: string): string {
  return value.trim().replace(/^@+/, "");
}

function InboxTab({ api, projectId, lists, onOpenContact, onReply }: {
  api: <T,>(method: string, path: string, body?: any, params?: Record<string, string>) => Promise<T>;
  projectId: string;
  lists: List[];
  onOpenContact: (contactId: number) => void;
  onReply: (contact: Contact, activity: Activity, conversation: Conversation, afterSend: () => void | Promise<void>) => void;
}) {
  const [items, setItems] = useState<InboxItem[] | null>(null);
  const [total, setTotal] = useState(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const [selected, setSelected] = useState<InboxItem | null>(null);
  const [threadContact, setThreadContact] = useState<Contact | null>(null);
  const [threadConversation, setThreadConversation] = useState<Conversation | null>(null);
  const [threadActivities, setThreadActivities] = useState<Activity[]>([]);
  const [threadActivityTotal, setThreadActivityTotal] = useState(0);
  const [threadLoading, setThreadLoading] = useState(false);
  const [threadLoadingMore, setThreadLoadingMore] = useState(false);
  const [threadErr, setThreadErr] = useState<string | null>(null);
  const [statusBusy, setStatusBusy] = useState(false);
  const [statusFilter, setStatusFilter] = useState("open");
  const [channelFilter, setChannelFilter] = useState("");
  const [fromFilter, setFromFilter] = useState("");
  const [toFilter, setToFilter] = useState("");
  const [listFilter, setListFilter] = useState("");
  const [tagFilter, setTagFilter] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const itemsRef = useRef<InboxItem[]>([]);
  const listLoadSeq = useRef(0);
  const threadLoadSeq = useRef(0);

  const load = useCallback(async (offset = 0) => {
    const seq = ++listLoadSeq.current;
    setErr(null);
    if (offset > 0) setLoadingMore(true);
    try {
      const filters: { field: string; op: string; value: string | number }[] = [];
      if (channelFilter) filters.push({ field: "channel", op: "is", value: channelFilter });
      const from = fromFilter.trim();
      if (from) filters.push({ field: "from", op: inboxAddressOp(from), value: stripDomainPrefix(from) });
      const to = toFilter.trim();
      if (to) filters.push({ field: "to", op: inboxAddressOp(to), value: stripDomainPrefix(to) });
      if (listFilter) filters.push({ field: "list", op: "is", value: Number(listFilter) });
      const tag = tagFilter.trim();
      if (tag) filters.push({ field: "tag", op: "is", value: tag });
      const params: Record<string, string> = { status: statusFilter, limit: String(INBOX_PAGE) };
      if (offset > 0) params.offset = String(offset);
      if (filters.length) params.filters = JSON.stringify(filters);
      const r = await api<InboxResponse>("GET", "/inbox", undefined, params);
      if (seq !== listLoadSeq.current) return;
      const rows = r.inbox || [];
      const nextRows = offset > 0 ? [...itemsRef.current, ...rows] : rows;
      itemsRef.current = nextRows;
      setItems(nextRows);
      setTotal(typeof r.total === "number" ? r.total : nextRows.length);
      setSelected((cur) => {
        if (cur) {
          const stillHere = nextRows.find((row) => String(row.id) === String(cur.id));
          if (stillHere) return stillHere;
        }
        return nextRows[0] || null;
      });
    } catch (e) {
      if (seq !== listLoadSeq.current) return;
      setErr((e as Error).message);
      if (offset === 0) {
        itemsRef.current = [];
        setItems([]);
        setTotal(0);
      }
    } finally {
      if (seq === listLoadSeq.current) setLoadingMore(false);
    }
  }, [api, statusFilter, channelFilter, fromFilter, toFilter, listFilter, tagFilter]);
  useEffect(() => { load(); }, [load]);

  const loadThreadFor = useCallback(async (item: InboxItem, activityOffset = 0) => {
    const seq = ++threadLoadSeq.current;
    if (activityOffset > 0) setThreadLoadingMore(true);
    else setThreadLoading(true);
    setThreadErr(null);
    try {
      const [contact, convo] = await Promise.all([
        api<{ contact: Contact }>("GET", `/contacts/${item.contact_id}`),
        api<{
          conversation?: Conversation;
          activities?: Activity[];
          activity_total?: number;
        }>(
          "GET",
          `/contacts/${item.contact_id}/conversations/${item.id}`,
          undefined,
          {
            activity_limit: "200",
            activity_offset: String(activityOffset),
          },
        ),
      ]);
      if (seq !== threadLoadSeq.current) return;
      setThreadContact(contact.contact);
      setThreadConversation(convo.conversation || null);
      setThreadActivities((previous) => activityOffset > 0
        ? [...(convo.activities || []), ...previous]
        : (convo.activities || []));
      setThreadActivityTotal(typeof convo.activity_total === "number"
        ? convo.activity_total
        : (convo.activities || []).length);
    } catch (e) {
      if (seq !== threadLoadSeq.current) return;
      setThreadErr((e as Error).message);
      setThreadContact(null);
      setThreadConversation(null);
      setThreadActivities([]);
      setThreadActivityTotal(0);
    } finally {
      if (seq === threadLoadSeq.current) {
        setThreadLoading(false);
        setThreadLoadingMore(false);
      }
    }
  }, [api]);

  useEffect(() => {
    if (!selected) {
      threadLoadSeq.current++;
      setThreadContact(null);
      setThreadConversation(null);
      setThreadActivities([]);
      setThreadActivityTotal(0);
      setThreadErr(null);
      return;
    }
    loadThreadFor(selected);
  }, [selected, loadThreadFor]);

  const reloadSelectedThread = useCallback(async () => {
    if (selected) await loadThreadFor(selected);
    await load();
  }, [load, loadThreadFor, selected]);

  useAppEvents("crm", projectId, (ev) => {
    if (
      ev.topic === "conversation.message.received" ||
      ev.topic === "conversation.status.changed" ||
      ev.topic === "contact.activity.added" ||
      ev.topic === "contact.channel.deliverability.changed" ||
      ev.topic === "contact.merged"
    ) {
      load();
      if (selected) loadThreadFor(selected);
    }
  });

  useAppEvents("messaging", projectId, (ev) => {
    if (!ev.topic.startsWith("message.")) return;
    const data = (ev.data || {}) as { id?: number | string; message_id?: number | string };
    const rawMessageId = data.message_id ?? data.id;
    const messageId = rawMessageId == null ? "" : String(rawMessageId);
    const isVisibleThreadMessage = messageId
      ? threadActivities.some((a) =>
          String(a.messaging_id || "") === messageId ||
          String(a.message_status?.id || "") === messageId
        )
      : true;
    if (selected && isVisibleThreadMessage) {
      loadThreadFor(selected);
    }
  });

  const setThreadStatus = useCallback(async (
    conversationId: string,
    patch: { status?: string; priority?: string; spam_scope?: string; force?: boolean },
  ) => {
    if (!selected) return;
    setStatusBusy(true);
    try {
      await api("POST", `/contacts/${selected.contact_id}/conversations/${conversationId}/status`, patch);
      await reloadSelectedThread();
    } catch (e) {
      setThreadErr("Status update failed: " + (e as Error).message);
    } finally {
      setStatusBusy(false);
    }
  }, [api, reloadSelectedThread, selected]);

  const clearFilters = () => {
    setChannelFilter("");
    setFromFilter("");
    setToFilter("");
    setListFilter("");
    setTagFilter("");
  };
  const hasFilters = channelFilter || fromFilter.trim() || toFilter.trim() || listFilter || tagFilter.trim();
  const inp = "bg-bg-input border border-border rounded px-2 py-1 text-xs";
  const selectedGroup: Extract<Group, { kind: "conversation" }> | null = threadConversation ? {
    kind: "conversation",
    conversationId: String(threadConversation.id),
    channel: threadConversation.channel,
    subject: threadConversation.subject || "",
    status: threadConversation.status || "open",
    priority: threadConversation.priority || "normal",
    activities: threadActivities,
  } : null;
  const lastReceived = [...threadActivities].reverse().find((a) => RECEIVED_KINDS.has(a.kind)) || null;

  return (
    <div className="h-full flex flex-col">
      <div className="p-3 border-b border-border space-y-2">
        <div className="flex items-center gap-2">
          <h2 className="text-sm text-text font-medium flex-1">
            Inbox
            {items && total > 0 && <span className="text-text-dim font-normal"> {items.length} of {total}</span>}
          </h2>
          <button type="button" onClick={() => load()} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Refresh</button>
        </div>
        <div className="flex items-center gap-2 flex-wrap">
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          className={inp}
        >
          <option value="open">Open</option>
          <option value="pending">Pending</option>
          <option value="closed">Closed</option>
          <option value="spam">Spam</option>
          <option value="all">All</option>
        </select>
          <select value={channelFilter} onChange={(e) => setChannelFilter(e.target.value)} className={inp}>
            <option value="">Any channel</option>
            <option value="email">Email</option>
            <option value="sms">SMS</option>
            <option value="whatsapp">WhatsApp</option>
          </select>
          <input
            value={fromFilter}
            onChange={(e) => setFromFilter(e.target.value)}
            placeholder="from email or @domain"
            className={`${inp} w-40`}
          />
          <input
            value={toFilter}
            onChange={(e) => setToFilter(e.target.value)}
            placeholder="to / received at"
            className={`${inp} w-40`}
          />
          <select value={listFilter} onChange={(e) => setListFilter(e.target.value)} className={inp}>
            <option value="">Any list</option>
            {lists.filter((l) => !l.archived_at).map((l) => (
              <option key={String(l.id)} value={String(l.id)}>{l.name}</option>
            ))}
          </select>
          <input
            value={tagFilter}
            onChange={(e) => setTagFilter(e.target.value)}
            placeholder="tag"
            className={`${inp} w-28`}
          />
          {hasFilters && (
            <button type="button" onClick={clearFilters} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Clear</button>
          )}
        </div>
      </div>
      <div
        className="flex-1 min-h-0 grid"
        style={{ gridTemplateColumns: "minmax(320px, 420px) minmax(0, 1fr) 280px" }}
      >
        <aside className="min-h-0 border-r border-border overflow-auto">
          {err && <div className="p-4 text-red text-xs">Error: {err}</div>}
          {items === null ? (
            <div className="p-4 text-text-muted text-sm">Loading…</div>
          ) : items.length === 0 ? (
            <div className="p-4 text-text-muted text-sm">Nothing here. New inbound conversations show up as they arrive.</div>
          ) : (
            <ul className="divide-y divide-border">
              {items.map((it) => (
                <li
                  key={it.id}
                  onClick={() => setSelected(it)}
                  className={`px-4 py-3 hover:bg-bg-input/40 cursor-pointer ${String(selected?.id) === String(it.id) ? "bg-bg-input" : ""}`}
                >
                  <div className="flex items-center gap-2 mb-0.5">
                    <span className={`inline-block w-1.5 h-1.5 rounded-full shrink-0 ${PRIORITY_DOT[it.priority] || PRIORITY_DOT.normal}`} title={`priority: ${it.priority}`} />
                    <span className="text-sm text-text font-medium truncate flex-1">
                      {it.contact_name || it.contact_email || it.contact_phone || `contact #${it.contact_id}`}
                    </span>
                    <span className="text-[10px] uppercase text-text-dim">{it.channel}</span>
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${STATUS_STYLES[it.status] || STATUS_STYLES.open}`}>{it.status}</span>
                  </div>
                  {it.automated && <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">automated</span>}
                  {it.subject && <div className="text-xs text-text-muted truncate mt-1">{it.subject}</div>}
                  {it.snippet && <div className="text-xs text-text-dim truncate">{it.snippet}</div>}
                  <div className="text-[10px] text-text-dim mt-0.5">{formatTime(it.last_activity_at)}</div>
                </li>
              ))}
              {items.length < total && (
                <li>
                  <button
                    type="button"
                    onClick={() => load(items.length)}
                    disabled={loadingMore}
                    className="w-full px-4 py-2 text-xs text-accent hover:bg-bg-input disabled:opacity-50"
                  >
                    {loadingMore ? "Loading..." : `Load ${Math.min(INBOX_PAGE, total - items.length)} more (${items.length} of ${total})`}
                  </button>
                </li>
              )}
            </ul>
          )}
        </aside>

        <main className="min-h-0 overflow-auto p-4">
          {!selected ? (
            <div className="text-text-muted text-sm text-center mt-12">Select a conversation.</div>
          ) : threadLoading ? (
            <div className="text-text-muted text-sm text-center mt-12">Loading thread…</div>
          ) : threadErr ? (
            <div className="text-red text-xs">Error: {threadErr}</div>
          ) : !selectedGroup || !threadConversation ? (
            <div className="text-text-muted text-sm text-center mt-12">Thread not found.</div>
          ) : (
            <div className="space-y-3">
              <header className="flex items-start justify-between gap-3 border-b border-border pb-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h2 className="text-lg text-text font-semibold truncate">
                      {threadContact ? displayName(threadContact) : selected.contact_name || `contact #${selected.contact_id}`}
                    </h2>
                    <span className="text-[10px] uppercase text-text-dim">{threadConversation.channel}</span>
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${STATUS_STYLES[threadConversation.status || "open"] || STATUS_STYLES.open}`}>
                      {threadConversation.status || "open"}
                    </span>
                  </div>
                  <p className="text-xs text-text-muted truncate">
                    {threadConversation.subject || selected.snippet || "Conversation"}
                  </p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {threadContact && lastReceived && (
                    <button
                      type="button"
                      onClick={() => onReply(threadContact, lastReceived, threadConversation, reloadSelectedThread)}
                      className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                    >Reply</button>
                  )}
                  <button
                    type="button"
                    onClick={() => loadThreadFor(selected)}
                    className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
                  >Refresh</button>
                </div>
              </header>
              {threadActivities.length < threadActivityTotal && (
                <button
                  type="button"
                  onClick={() => loadThreadFor(selected, threadActivities.length)}
                  disabled={threadLoadingMore}
                  className="w-full px-3 py-1.5 text-xs text-accent border border-border rounded hover:bg-bg-input disabled:opacity-50"
                >
                  {threadLoadingMore
                    ? "Loading older messages..."
                    : `Load older messages (${threadActivities.length} of ${threadActivityTotal})`}
                </button>
              )}
              <ul>
                <ActivityGroup
                  group={selectedGroup}
                  busy={statusBusy}
                  onSetStatus={setThreadStatus}
                  onReply={(act) => {
                    if (threadContact && threadConversation) onReply(threadContact, act, threadConversation, reloadSelectedThread);
                  }}
                />
              </ul>
            </div>
          )}
        </main>

        <aside className="min-h-0 border-l border-border overflow-auto p-4">
          {!threadContact ? (
            <div className="text-text-muted text-xs">No contact selected.</div>
          ) : (
            <div className="space-y-4">
              <section>
                <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Contact</h3>
                <div className="text-sm text-text font-medium truncate">{displayName(threadContact)}</div>
                <div className="text-xs text-text-muted truncate">{secondaryLine(threadContact) || "—"}</div>
                <button
                  type="button"
                  onClick={() => onOpenContact(Number(threadContact.id))}
                  className="mt-3 text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                >Open full contact</button>
              </section>
              {threadContact.channels && threadContact.channels.length > 0 && (
                <section>
                  <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Channels</h3>
                  <ul className="space-y-1">
                    {threadContact.channels.map((ch, i) => (
                      <li key={`${ch.kind}-${ch.value}-${i}`} className="text-xs">
                        <div className="text-[10px] uppercase text-text-dim flex items-center gap-1">
                          <span>{ch.kind}{ch.is_primary ? " · primary" : ""}</span>
                          <EmailVerificationBadge channel={ch} />
                          {(ch.deliverability || []).map((state) => <DeliveryBadge key={state.transport} state={state} />)}
                        </div>
                        <div className="text-text truncate">{ch.value}</div>
                      </li>
                    ))}
                  </ul>
                </section>
              )}
              {threadContact.tags && threadContact.tags.length > 0 && (
                <section>
                  <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Tags</h3>
                  <div className="flex flex-wrap gap-1">
                    {threadContact.tags.map((t) => (
                      <span key={t} className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text">{t}</span>
                    ))}
                  </div>
                </section>
              )}
              <section>
                <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Thread</h3>
                <dl className="space-y-1 text-xs">
                  <div className="flex justify-between gap-2"><dt className="text-text-dim">Started</dt><dd className="text-text text-right">{threadConversation ? formatTime(threadConversation.started_at) : "—"}</dd></div>
                  <div className="flex justify-between gap-2"><dt className="text-text-dim">Last activity</dt><dd className="text-text text-right">{threadConversation ? formatTime(threadConversation.last_activity_at) : "—"}</dd></div>
                  <div className="flex justify-between gap-2"><dt className="text-text-dim">Messages</dt><dd className="text-text">{threadActivities.length}</dd></div>
                </dl>
              </section>
            </div>
          )}
        </aside>
      </div>
    </div>
  );
}

// ─── Contact filter bar ───────────────────────────────────────────

function ContactFilterBar({ attrDefs, filters, onChange, onSaveSegment }: {
  attrDefs: AttributeDef[];
  filters: UIFilter[];
  onChange: (f: UIFilter[]) => void;
  onSaveSegment: (name: string) => Promise<void>;
}) {
  const [adding, setAdding] = useState(false);
  const [sel, setSel] = useState("");      // "kind:key"
  const [op, setOp] = useState("eq");
  const [value, setValue] = useState("");
  const [saving, setSaving] = useState(false);
  const [segName, setSegName] = useState<string | null>(null); // non-null = naming a segment
  const [err, setErr] = useState<string | null>(null);

  const fieldOptions = useMemo(() => [
    ...CORE_FILTER_FIELDS.map((f) => ({ kind: "core" as const, key: f.key, label: f.label, type: f.type, enumValues: undefined as string[] | undefined })),
    ...attrDefs.map((d) => ({ kind: "attr" as const, key: d.key, label: d.label, type: d.type as string, enumValues: d.enum_values })),
  ], [attrDefs]);
  const current = fieldOptions.find((o) => `${o.kind}:${o.key}` === sel);

  const startAdd = () => {
    const first = fieldOptions[0];
    if (!first) return;
    setSel(`${first.kind}:${first.key}`);
    setOp(opsForType(first.type)[0].value);
    setValue(""); setErr(null); setAdding(true);
  };
  const pickField = (v: string) => {
    setSel(v);
    const o = fieldOptions.find((x) => `${x.kind}:${x.key}` === v);
    if (o) { setOp(opsForType(o.type)[0].value); setValue(""); }
  };
  const commit = () => {
    if (!current) return;
    if (current.type !== "bool" && value.trim() === "") { setErr("enter a value"); return; }
    onChange([...filters, {
      kind: current.kind, key: current.key, label: current.label, type: current.type,
      op, value: current.type === "bool" ? (value || "true") : value.trim(),
      enumValues: current.enumValues,
    }]);
    setAdding(false); setValue(""); setErr(null);
  };

  const inp = "bg-bg-input border border-border rounded px-2 py-1 text-xs";

  return (
    <div className="space-y-2">
      {filters.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {filters.map((f, i) => (
            <span key={i} className="inline-flex items-center gap-1 text-[11px] px-1.5 py-0.5 rounded bg-accent/10 text-accent">
              {describeFilter(f)}
              <button type="button" onClick={() => onChange(filters.filter((_, idx) => idx !== i))} className="hover:text-text" aria-label="Remove filter">×</button>
            </span>
          ))}
        </div>
      )}

      {adding ? (
        <div className="flex flex-wrap items-center gap-1">
          <select value={sel} onChange={(e) => pickField(e.target.value)} className={inp}>
            {fieldOptions.map((o) => <option key={`${o.kind}:${o.key}`} value={`${o.kind}:${o.key}`}>{o.label}</option>)}
          </select>
          <select value={op} onChange={(e) => setOp(e.target.value)} className={inp}>
            {opsForType(current?.type || "text").map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
          <FilterValueInput type={current?.type || "text"} enumValues={current?.enumValues} value={value} onChange={setValue} cls={inp} />
          <button type="button" onClick={commit} className="text-[11px] px-1.5 py-0.5 border border-accent text-accent rounded hover:bg-accent hover:text-bg">Add</button>
          <button type="button" onClick={() => { setAdding(false); setErr(null); }} className="text-[11px] px-1.5 py-0.5 text-text-dim hover:text-text">Cancel</button>
        </div>
      ) : (
        <div className="flex items-center gap-2">
          <button type="button" onClick={startAdd} className="text-[11px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input text-text-muted">+ Filter</button>
          {filters.length > 0 && segName === null && (
            <>
              <button type="button" onClick={() => setSegName("")} className="text-[11px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input text-text-muted">Save as segment</button>
              <button type="button" onClick={() => onChange([])} className="text-[11px] text-text-dim hover:text-red">Clear</button>
            </>
          )}
        </div>
      )}

      {segName !== null && (
        <div className="flex items-center gap-1">
          <input autoFocus value={segName} onChange={(e) => setSegName(e.target.value)} placeholder="Segment name" className={`${inp} flex-1`} />
          <button type="button" disabled={saving || !segName.trim()} className="text-[11px] px-1.5 py-0.5 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
            onClick={async () => {
              setSaving(true); setErr(null);
              try { await onSaveSegment(segName.trim()); setSegName(null); }
              catch (e) { setErr((e as Error).message); }
              finally { setSaving(false); }
            }}>Save</button>
          <button type="button" onClick={() => setSegName(null)} className="text-[11px] px-1.5 py-0.5 text-text-dim hover:text-text">Cancel</button>
        </div>
      )}
      {err && <div className="text-red text-[11px]">{err}</div>}
    </div>
  );
}

function FilterValueInput({ type, enumValues, value, onChange, cls }: {
  type: string; enumValues?: string[]; value: string; onChange: (v: string) => void; cls: string;
}) {
  if (type === "bool") {
    return (
      <select value={value || "true"} onChange={(e) => onChange(e.target.value)} className={cls}>
        <option value="true">true</option>
        <option value="false">false</option>
      </select>
    );
  }
  if (type === "select" && enumValues && enumValues.length > 0) {
    return (
      <select value={value} onChange={(e) => onChange(e.target.value)} className={cls}>
        <option value="">—</option>
        {enumValues.map((o) => <option key={o} value={o}>{o}</option>)}
      </select>
    );
  }
  const inputType = type === "number" ? "number" : type === "date" ? "date" : "text";
  return <input type={inputType} value={value} onChange={(e) => onChange(e.target.value)} placeholder="value" className={cls} style={{ width: 130 }} />;
}

// ─── Generic modal shell ──────────────────────────────────────────

function ModalShell({ title, onCancel, children, footer }: {
  title: string;
  onCancel: () => void;
  children: React.ReactNode;
  footer: React.ReactNode;
}) {
  // Esc-to-close. Centralised here so every modal honours the same
  // dismissal contract instead of each one reinventing it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onCancel(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);
  return (
    <div className="absolute inset-0 bg-black/40 flex items-center justify-center pointer-events-auto z-10">
      <div className="bg-bg border border-border rounded-lg shadow-lg w-full max-w-md mx-4">
        <header className="flex items-center justify-between px-4 py-2 border-b border-border">
          <div className="text-sm text-text font-medium">{title}</div>
          <button type="button" onClick={onCancel} className="text-text-dim hover:text-text px-2">×</button>
        </header>
        <div className="p-4 space-y-3 text-sm">{children}</div>
        <footer className="flex items-center gap-2 px-4 py-3 border-t border-border">{footer}</footer>
      </div>
    </div>
  );
}

// ─── ConfirmDialog ────────────────────────────────────────────────

interface ConfirmState {
  title: string;
  message: string;
  confirmLabel?: string;
  destructive?: boolean;
  onConfirm: () => void | Promise<void>;
}

function ConfirmDialog({ state, onClose }: { state: ConfirmState; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const handleConfirm = async () => {
    setBusy(true);
    try { await state.onConfirm(); }
    finally { setBusy(false); onClose(); }
  };
  return (
    <ModalShell
      title={state.title}
      onCancel={onClose}
      footer={
        <>
          <button
            type="button"
            onClick={handleConfirm}
            disabled={busy}
            className={`px-3 py-1 text-sm rounded disabled:opacity-50 ${
              state.destructive
                ? "border border-red/50 text-red hover:bg-red/10"
                : "border border-accent text-accent hover:bg-accent hover:text-bg"
            }`}
          >{busy ? "Working…" : (state.confirmLabel || "Confirm")}</button>
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
        </>
      }
    >
      <p className="text-text">{state.message}</p>
    </ModalShell>
  );
}

// ─── NewContactModal ──────────────────────────────────────────────

function NewContactModal({ onCancel, onSubmit }: {
  onCancel: () => void;
  onSubmit: (firstName: string, email: string, phone: string) => void | Promise<void>;
}) {
  const [firstName, setFirstName] = useState("");
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [busy, setBusy] = useState(false);
  // At least one of name / email / phone must be provided. Most CRMs
  // accept any of the three; we mirror that so a "phone-only" lead
  // (e.g. inbound SMS) can be filed without a placeholder name.
  const canSubmit = !!(firstName.trim() || email.trim() || phone.trim());
  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    try { await onSubmit(firstName.trim(), email.trim(), phone.trim()); }
    finally { setBusy(false); }
  };
  return (
    <ModalShell
      title="New contact"
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !canSubmit}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Creating…" : "Create"}</button>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">First name</label>
        <input
          type="text"
          value={firstName}
          onChange={(e) => setFirstName(e.target.value)}
          autoFocus
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Email</label>
        <input
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="optional"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Phone</label>
        <input
          type="tel"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          placeholder="+15551234567"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <p className="text-text-dim text-xs">At least one of name, email, or phone is required.</p>
    </ModalShell>
  );
}

// ─── LogActivityModal ─────────────────────────────────────────────

function LogActivityModal({ contactName, onCancel, onSubmit }: {
  contactName: string;
  onCancel: () => void;
  onSubmit: (kind: string, body: string) => void | Promise<void>;
}) {
  const [kind, setKind] = useState("note");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!body.trim()) return;
    setBusy(true);
    try { await onSubmit(kind, body.trim()); }
    finally { setBusy(false); }
  };
  return (
    <ModalShell
      title={`Log activity — ${contactName}`}
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !body.trim()}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Logging…" : "Log"}</button>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Kind</label>
        <select
          value={kind}
          onChange={(e) => setKind(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1"
        >
          {["note", "call", "meeting", "system"].map((k) => (
            <option key={k} value={k}>{k}</option>
          ))}
        </select>
      </div>
      <div>
        <label className="text-text-muted text-xs uppercase tracking-wide block mb-1">Body</label>
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          rows={5}
          autoFocus
          className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          placeholder="What happened?"
        />
      </div>
    </ModalShell>
  );
}

// ─── ErrorToast ───────────────────────────────────────────────────
//
// Bottom-right transient notification. Auto-dismisses on a timer
// (panel-level effect) and on manual click of the × button.

function ErrorToast({ message, onDismiss }: { message: string; onDismiss: () => void }) {
  return (
    <div className="absolute bottom-4 right-4 z-20 max-w-sm border border-red/50 bg-red/10 text-text rounded shadow-lg flex items-start gap-2 p-3 text-sm">
      <span className="text-red shrink-0">⚠</span>
      <span className="flex-1 break-words">{message}</span>
      <button
        type="button"
        onClick={onDismiss}
        className="text-text-dim hover:text-text shrink-0"
      >×</button>
    </div>
  );
}

// ─── Fields section (custom attributes) ───────────────────────────
//
// Renders one row per defined attribute, with a type-aware editor.
// Unset rows show a faint placeholder; set rows show the current
// value; both edit in place and save on blur (or onChange for the
// instant-feedback inputs like checkbox/select). When defs include
// attributes the contact doesn't yet have, an "Add a field value"
// dropdown lets the user start filling them in.

function FieldsSection({ contact, defs, onSet }: {
  contact: Contact;
  defs: AttributeDef[];
  onSet: (key: string, value: unknown) => void | Promise<void>;
}) {
  const valueByKey = useMemo(() => {
    const out: Record<string, unknown> = {};
    for (const a of contact.attributes || []) out[a.key] = a.value;
    return out;
  }, [contact]);

  if (defs.length === 0) {
    return null;
  }

  return (
    <section>
      <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Fields</h2>
      <div className="grid grid-cols-[140px_1fr] gap-y-2 gap-x-3 text-sm items-center">
        {defs.map((d) => (
          <Fragment key={d.key}>
            <label className="text-text-muted self-center" title={d.key}>
              {d.label}
              {d.required ? <span className="text-red ml-1">*</span> : null}
            </label>
            <FieldEditor
              def={d}
              value={valueByKey[d.key]}
              onCommit={(v) => onSet(d.key, v)}
            />
          </Fragment>
        ))}
      </div>
    </section>
  );
}

function FieldEditor({ def, value, onCommit }: {
  def: AttributeDef;
  value: unknown;
  onCommit: (v: unknown) => void;
}) {
  // Local draft so typing doesn't fire a save per keystroke. We
  // commit on blur (text/url/number/date) or on change (bool/select),
  // matching how each input naturally behaves.
  const initial = value == null ? "" : (def.type === "multi_select" ? (Array.isArray(value) ? value.join(", ") : "") : String(value));
  const [draft, setDraft] = useState<string>(initial);
  useEffect(() => { setDraft(initial); }, [initial]);

  const cls = "bg-bg-input border border-border rounded px-2 py-1 w-full";

  if (def.type === "bool") {
    const b = value === true || value === "true" || value === 1;
    return (
      <input
        type="checkbox"
        checked={b}
        onChange={(e) => onCommit(e.target.checked)}
        className="w-4 h-4 accent-accent"
      />
    );
  }
  if (def.type === "select") {
    return (
      <select
        value={(value == null ? "" : String(value))}
        onChange={(e) => onCommit(e.target.value || null)}
        className={cls}
      >
        <option value="">—</option>
        {(def.enum_values || []).map((opt) => (
          <option key={opt} value={opt}>{opt}</option>
        ))}
      </select>
    );
  }
  if (def.type === "multi_select") {
    const selected = Array.isArray(value) ? (value as string[]) : [];
    const toggle = (opt: string) => {
      const next = selected.includes(opt) ? selected.filter((s) => s !== opt) : [...selected, opt];
      onCommit(next);
    };
    return (
      <div className="flex flex-wrap gap-1">
        {(def.enum_values || []).map((opt) => {
          const on = selected.includes(opt);
          return (
            <button
              key={opt}
              type="button"
              onClick={() => toggle(opt)}
              className={`text-[11px] px-1.5 py-0.5 rounded border ${on ? "border-accent text-accent bg-accent/10" : "border-border text-text-muted hover:bg-bg-input"}`}
            >{opt}</button>
          );
        })}
      </div>
    );
  }
  if (def.type === "date") {
    return (
      <input
        type="date"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => { if (draft !== initial) onCommit(draft || null); }}
        className={cls}
      />
    );
  }
  if (def.type === "number") {
    return (
      <input
        type="number"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => {
          if (draft === initial) return;
          if (draft === "") onCommit(null);
          else {
            const n = Number(draft);
            if (!Number.isNaN(n)) onCommit(n);
          }
        }}
        className={cls}
      />
    );
  }
  // text, url, fallback
  return (
    <input
      type={def.type === "url" ? "url" : "text"}
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => { if (draft !== initial) onCommit(draft || null); }}
      placeholder={def.type === "url" ? "https://…" : ""}
      className={cls}
    />
  );
}

// ─── DefineFieldModal ─────────────────────────────────────────────
//
// Operator-facing form for creating a new custom field. Backed by the
// contacts_define_attribute MCP tool / POST /attribute-defs HTTP route.

function DefineFieldModal({ existing, onCancel, onSubmit }: {
  existing: AttributeDef[];
  onCancel: () => void;
  onSubmit: (def: { key: string; label: string; type: AttributeType; enum_values?: string[]; required?: boolean }) => void | Promise<void>;
}) {
  const [label, setLabel] = useState("");
  const [key, setKey] = useState("");
  const [type, setType] = useState<AttributeType>("text");
  const [required, setRequired] = useState(false);
  const [enumStr, setEnumStr] = useState("");
  const [busy, setBusy] = useState(false);
  const [touchedKey, setTouchedKey] = useState(false);

  // Auto-derive a snake_case key from the label until the user edits
  // the key directly.
  const suggestedKey = useMemo(() => slugify(label), [label]);
  const effectiveKey = touchedKey ? key : suggestedKey;
  const keyTaken = existing.some((d) => d.key === effectiveKey);
  const needsEnum = type === "select" || type === "multi_select";
  const enumVals = enumStr.split(",").map((s) => s.trim()).filter(Boolean);

  const submit = async () => {
    if (!label.trim() || !effectiveKey) return;
    if (needsEnum && enumVals.length === 0) return;
    setBusy(true);
    try {
      await onSubmit({
        key: effectiveKey,
        label: label.trim(),
        type,
        enum_values: needsEnum ? enumVals : undefined,
        required,
      });
    } finally {
      setBusy(false);
    }
  };

  return (
    <ModalShell
      title="Add custom field"
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !label.trim() || !effectiveKey || keyTaken || (needsEnum && enumVals.length === 0)}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Adding…" : "Add field"}</button>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Label</label>
        <input
          type="text"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          autoFocus
          placeholder="Renewal date"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Key</label>
        <input
          type="text"
          value={effectiveKey}
          onChange={(e) => { setTouchedKey(true); setKey(e.target.value); }}
          placeholder="renewal_date"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1 font-mono text-xs"
        />
      </div>
      {keyTaken && (
        <p className="text-red text-xs ml-22">Key already exists in this project.</p>
      )}
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Type</label>
        <select
          value={type}
          onChange={(e) => setType(e.target.value as AttributeType)}
          className="bg-bg-input border border-border rounded px-2 py-1"
        >
          {(["text", "number", "date", "bool", "select", "multi_select", "url"] as AttributeType[]).map((t) => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
      </div>
      {needsEnum && (
        <div className="flex items-start gap-2">
          <label className="text-text-muted w-20 mt-1">Options</label>
          <div className="flex-1">
            <input
              type="text"
              value={enumStr}
              onChange={(e) => setEnumStr(e.target.value)}
              placeholder="alpha, beta, gamma"
              className="w-full bg-bg-input border border-border rounded px-2 py-1"
            />
            <p className="text-text-dim text-xs mt-1">Comma-separated.</p>
          </div>
        </div>
      )}
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Required</label>
        <input
          type="checkbox"
          checked={required}
          onChange={(e) => setRequired(e.target.checked)}
          className="w-4 h-4 accent-accent"
        />
      </div>
    </ModalShell>
  );
}

function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "");
}

// ─── Lists ────────────────────────────────────────────────────────

// ContactListChips renders a one-line row of "is this contact on
// list X?" chips. Active memberships are filled; non-members are
// outlined and click-to-add. Hides itself when no lists exist
// (empty-state lives in the Lists tab, not on contact detail).
function ContactListChips({ lists, contactLists, onToggle }: {
  lists: List[];
  contactLists: List[];
  onToggle: (l: List, on: boolean) => void;
}) {
  const visible = lists.filter((l) => !l.archived_at);
  if (visible.length === 0) return null;
  const activeIds = new Set(contactLists.map((l) => l.id));
  return (
    <section>
      <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Lists</h2>
      <div className="flex flex-wrap gap-1.5">
        {visible.map((l) => {
          const on = activeIds.has(l.id);
          return (
            <button
              key={l.id}
              type="button"
              onClick={() => onToggle(l, !on)}
              title={on ? "Remove from list" : "Add to list"}
              className={`text-[11px] px-2 py-0.5 rounded border transition ${
                on
                  ? "border-accent text-accent bg-accent/10"
                  : "border-border text-text-muted hover:bg-bg-input"
              }`}
            >
              {on ? "✓ " : "+ "}{l.name}
            </button>
          );
        })}
      </div>
    </section>
  );
}

function ListsTab({ api, lists, onCreate, onEdit, onArchive, onOpenContact }: {
  api: PanelApi;
  lists: List[];
  onCreate: () => void;
  onEdit: (id: number) => void;
  onArchive: (l: List) => void;
  onOpenContact: (id: string | number) => void;
}) {
  const active = useMemo(() => lists.filter((l) => !l.archived_at), [lists]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [members, setMembers] = useState<Contact[]>([]);
  const [loadingMembers, setLoadingMembers] = useState(false);
  const [memberError, setMemberError] = useState("");
  const selected = active.find((l) => l.id === selectedId) || null;

  useEffect(() => {
    if (active.length === 0) {
      setSelectedId(null);
      setMembers([]);
      return;
    }
    if (!selectedId || !active.some((l) => l.id === selectedId)) {
      setSelectedId(active[0].id);
    }
  }, [active, selectedId]);

  useEffect(() => {
    if (!selectedId) return;
    let cancelled = false;
    setLoadingMembers(true);
    setMemberError("");
    api<{ contacts?: Contact[] }>("GET", `/lists/${selectedId}/members`, undefined, { limit: "100" })
      .then((r) => {
        if (!cancelled) setMembers(r.contacts || []);
      })
      .catch((e) => {
        if (!cancelled) {
          setMembers([]);
          setMemberError((e as Error).message);
        }
      })
      .finally(() => {
        if (!cancelled) setLoadingMembers(false);
      });
    return () => { cancelled = true; };
  }, [api, selectedId, lists]);

  return (
    <div className="p-6 max-w-6xl space-y-4">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-xl text-text font-semibold">Lists</h1>
          <p className="text-text-muted text-sm">
            Buckets of contacts with their own sender defaults and inbound routing — used to keep multiple brands or products in one CRM install.
          </p>
        </div>
        <button
          type="button"
          onClick={onCreate}
          className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg whitespace-nowrap"
        >+ New list</button>
      </header>

      {active.length === 0 ? (
        <div className="border border-border rounded p-4 text-sm text-text-muted">
          No lists yet. Create one for each brand / product / audience that needs its own sender identity.
        </div>
      ) : (
        <div className="grid grid-cols-[minmax(280px,360px)_1fr] gap-4 min-h-0">
          <ul className="divide-y divide-border border border-border rounded self-start">
            {active.map((l) => (
              <li key={l.id}>
                <button
                  type="button"
                  onClick={() => setSelectedId(l.id)}
                  className={`w-full px-3 py-2 text-left flex items-center gap-3 hover:bg-bg-input/60 ${selectedId === l.id ? "bg-bg-input" : ""}`}
                >
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-text font-medium truncate">{l.name}</span>
                      <span className="text-[10px] uppercase text-text-dim font-mono">{l.slug}</span>
                    </div>
                    <div className="text-xs text-text-muted truncate">
                      {[
                        l.default_sender_email && `from: ${l.default_sender_email}`,
                        l.default_sender_phone && `phone: ${l.default_sender_phone}`,
                        l.inbound_route_pattern && `inbound: ${l.inbound_route_pattern}`,
                      ].filter(Boolean).join(" · ") || (l.description || "—")}
                    </div>
                  </div>
                  {typeof l.member_count === "number" && (
                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted shrink-0">
                      {l.member_count}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>

          <section className="min-w-0 border border-border rounded">
            {selected ? (
              <>
                <header className="flex items-center justify-between gap-3 border-b border-border px-3 py-2">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <h2 className="text-sm text-text font-medium truncate">{selected.name}</h2>
                      {typeof selected.member_count === "number" && (
                        <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">
                          {selected.member_count} member{selected.member_count === 1 ? "" : "s"}
                        </span>
                      )}
                    </div>
                    <div className="text-xs text-text-muted truncate">{selected.description || selected.slug}</div>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <button
                      type="button"
                      onClick={() => onEdit(selected.id)}
                      className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                    >Edit</button>
                    <button
                      type="button"
                      onClick={() => onArchive(selected)}
                      className="text-xs px-2 py-1 text-red border border-red/50 rounded hover:bg-red/10"
                    >Archive</button>
                  </div>
                </header>

                {loadingMembers ? (
                  <div className="p-4 text-text-muted text-xs">Loading members…</div>
                ) : memberError ? (
                  <div className="p-4 text-red text-xs">{memberError}</div>
                ) : members.length === 0 ? (
                  <div className="p-4 text-text-muted text-xs">No members in this list.</div>
                ) : (
                  <ul className="divide-y divide-border">
                    {members.map((c) => (
                      <li key={c.id} className="px-3 py-2 flex items-center gap-3">
                        <div className="flex-1 min-w-0">
                          <div className="text-sm text-text font-medium truncate">{displayName(c)}</div>
                          <div className="text-xs text-text-muted truncate">{secondaryLine(c) || c.primary_email || c.primary_phone || "—"}</div>
                        </div>
                        <button
                          type="button"
                          onClick={() => onOpenContact(c.id)}
                          className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input shrink-0"
                        >Open</button>
                      </li>
                    ))}
                  </ul>
                )}
                {members.length >= 100 && (
                  <div className="border-t border-border px-3 py-2 text-xs text-text-dim">
                    Showing first 100 members.
                  </div>
                )}
              </>
            ) : (
              <div className="p-4 text-text-muted text-xs">Select a list.</div>
            )}
          </section>
        </div>
      )}
    </div>
  );
}

// ListEditorModal — used for both Create (no `editing` prop) and Edit
// (editing populated). Slug is auto-derived from name on create, and
// locked on edit (we don't expose slug renames in the panel since
// they're a stable identifier).
function ListEditorModal({ existing, editing, onCancel, onSubmit }: {
  existing: List[];
  editing?: List | null;
  onCancel: () => void;
  onSubmit: (patch: Partial<List>) => void | Promise<void>;
}) {
  const isEdit = !!editing;
  const [name, setName] = useState(editing?.name || "");
  const [slug, setSlug] = useState(editing?.slug || "");
  const [touchedSlug, setTouchedSlug] = useState(false);
  const [description, setDescription] = useState(editing?.description || "");
  const [senderEmail, setSenderEmail] = useState(editing?.default_sender_email || "");
  const [senderPhone, setSenderPhone] = useState(editing?.default_sender_phone || "");
  const [pattern, setPattern] = useState(editing?.inbound_route_pattern || "");
  const [busy, setBusy] = useState(false);

  const suggestedSlug = useMemo(() => slugify(name), [name]);
  const effectiveSlug = isEdit ? (editing!.slug) : (touchedSlug ? slug : suggestedSlug);
  const slugTaken = !isEdit && existing.some((l) => l.slug === effectiveSlug);

  const submit = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      if (isEdit) {
        await onSubmit({
          name: name.trim(),
          description: description.trim(),
          default_sender_email: senderEmail.trim(),
          default_sender_phone: senderPhone.trim(),
          inbound_route_pattern: pattern.trim(),
        });
      } else {
        await onSubmit({
          name: name.trim(),
          slug: effectiveSlug,
          description: description.trim(),
          default_sender_email: senderEmail.trim(),
          default_sender_phone: senderPhone.trim(),
          inbound_route_pattern: pattern.trim(),
        });
      }
    } finally { setBusy(false); }
  };

  return (
    <ModalShell
      title={isEdit ? `Edit list — ${editing!.name}` : "New list"}
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !name.trim() || slugTaken}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Saving…" : (isEdit ? "Save" : "Create")}</button>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Name</label>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          placeholder="SaaS 1 customers"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Slug</label>
        <input
          type="text"
          value={effectiveSlug}
          onChange={(e) => { if (!isEdit) { setTouchedSlug(true); setSlug(e.target.value); } }}
          disabled={isEdit}
          placeholder="saas_1_customers"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1 font-mono text-xs disabled:opacity-50"
        />
      </div>
      {slugTaken && (
        <p className="text-red text-xs ml-26">Slug already used in this project.</p>
      )}
      <div className="flex items-start gap-2">
        <label className="text-text-muted w-24 mt-1">Description</label>
        <textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={2}
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <hr className="border-border" />
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Default sender (email)</label>
        <input
          type="email"
          value={senderEmail}
          onChange={(e) => setSenderEmail(e.target.value)}
          placeholder="hello@saas1.example.com"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Default sender (phone)</label>
        <input
          type="tel"
          value={senderPhone}
          onChange={(e) => setSenderPhone(e.target.value)}
          placeholder="+15551234567"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Inbound pattern</label>
        <input
          type="text"
          value={pattern}
          onChange={(e) => setPattern(e.target.value)}
          placeholder="*@saas1.example.com"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1 font-mono text-xs"
        />
      </div>
      <p className="text-text-dim text-xs">
        When inbound mail/SMS matches this pattern (registered separately in messaging's inbound routes), the contact gets auto-added to this list.
      </p>
    </ModalShell>
  );
}

// ─── Segments ─────────────────────────────────────────────────────

function SegmentsTab({ segments, lists, preview, onCreate, onEdit, onArchive, onMaterialise, onPreview, onClosePreview }: {
  segments: Segment[];
  lists: List[];
  preview: { id: number; contacts: Contact[]; total: number } | null;
  onCreate: () => void;
  onEdit: (id: number) => void;
  onArchive: (s: Segment) => void;
  onMaterialise: (s: Segment) => void;
  onPreview: (s: Segment) => void;
  onClosePreview: () => void;
}) {
  const active = segments.filter((s) => !s.archived_at);
  const listById = useMemo(() => new Map(lists.map((l) => [l.id, l])), [lists]);
  return (
    <div className="p-6 max-w-4xl space-y-4">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-xl text-text font-semibold">Segments</h1>
          <p className="text-text-muted text-sm">
            Saved filters over contacts. Dynamic segments re-evaluate on each call; static segments freeze the membership for campaign sends.
          </p>
        </div>
        <button
          type="button"
          onClick={onCreate}
          className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg whitespace-nowrap"
        >+ New segment</button>
      </header>

      {active.length === 0 ? (
        <div className="border border-border rounded p-4 text-sm text-text-muted">
          No segments yet. Create one to slice your contacts by tag, attribute, list membership, or recent activity.
        </div>
      ) : (
        <ul className="divide-y divide-border border border-border rounded">
          {active.map((s) => {
            const list = s.list_id ? listById.get(s.list_id) : null;
            return (
              <li key={s.id} className="px-3 py-2">
                <div className="flex items-center gap-3">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-text font-medium truncate">{s.name}</span>
                      <span className={`text-[10px] px-1.5 py-0.5 rounded ${s.kind === "static" ? "bg-amber/15 text-amber" : "bg-accent/10 text-accent"}`}>
                        {s.kind}
                      </span>
                      {list && <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">in {list.name}</span>}
                      {typeof s.cached_count === "number" && (
                        <span className="text-[10px] text-text-dim">~ {s.cached_count} matches</span>
                      )}
                    </div>
                    {s.description && <div className="text-xs text-text-muted truncate mt-0.5">{s.description}</div>}
                  </div>
                  <button
                    type="button"
                    onClick={() => onPreview(s)}
                    className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                  >Preview</button>
                  <button
                    type="button"
                    onClick={() => onMaterialise(s)}
                    title="Freeze membership into a static snapshot"
                    className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                  >Snapshot</button>
                  <button
                    type="button"
                    onClick={() => onEdit(s.id)}
                    className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                  >Edit</button>
                  <button
                    type="button"
                    onClick={() => onArchive(s)}
                    className="text-xs px-2 py-1 text-red border border-red/50 rounded hover:bg-red/10"
                  >Archive</button>
                </div>
                {preview && preview.id === s.id && (
                  <div className="mt-2 border border-border rounded bg-bg-input/30 p-2">
                    <div className="flex items-center justify-between text-xs text-text-dim mb-1">
                      <span>Preview — {preview.total} match{preview.total === 1 ? "" : "es"}, showing {preview.contacts.length}</span>
                      <button type="button" onClick={onClosePreview} className="hover:text-text">×</button>
                    </div>
                    {preview.contacts.length === 0 ? (
                      <p className="text-text-muted text-xs">No contacts match.</p>
                    ) : (
                      <ul className="text-xs space-y-0.5">
                        {preview.contacts.map((c) => (
                          <li key={c.id} className="text-text truncate">{displayName(c)} — <span className="text-text-dim">{secondaryLine(c) || c.primary_email || c.primary_phone || "—"}</span></li>
                        ))}
                      </ul>
                    )}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
      <p className="text-text-dim text-xs">
        Dynamic segments re-evaluate on every call (TTL-cached count for 5min). Static segments hold a frozen snapshot — the kind a campaign uses so the audience doesn't shift mid-send.
      </p>
    </div>
  );
}

// SegmentEditorModal — predicate builder. Each row is one entry in
// the JSON definition: pick a kind (column / synthetic predicate),
// fill in args. v0.5 keeps the UI to a flat AND list; OR/NOT trees
// and richer composition come later if real demand emerges.

type PredicateKind =
  | "field"
  | "tag_in" | "tag_not_in"
  | "attribute"
  | "last_activity_within"
  | "channel_present"
  | "in_list" | "not_in_list"
  | "not_in_segment";

interface DraftPredicate {
  k: PredicateKind;
  // Free-form payload mirroring the chosen kind. Only fields used by
  // each kind are read at submit time.
  field?: string;
  op?: string;
  value?: string;
  tags?: string;       // comma-separated
  key?: string;
  days?: string;
  kind?: string;
  list_id?: number;
  segment_id?: number;
}

const FIELD_OPTIONS = ["first_name", "last_name", "display_name", "company", "job_title", "primary_email", "primary_phone", "status", "source"];
const FIELD_OPS = ["eq", "neq", "contains", "starts_with", "is_null", "in"];

function predicateToDraft(p: SegmentPredicate): DraftPredicate {
  if ("field" in p) {
    return { k: "field", field: p.field, op: p.op || "eq", value: p.value == null ? "" : String(p.value) };
  }
  switch (p.predicate) {
    case "tag_in": return { k: "tag_in", tags: (p.tags || []).join(", ") };
    case "tag_not_in": return { k: "tag_not_in", tags: (p.tags || []).join(", ") };
    case "attribute": return { k: "attribute", key: p.key, op: p.op || "eq", value: p.value == null ? "" : String(p.value) };
    case "last_activity_within": return { k: "last_activity_within", days: String(p.days), kind: p.kind || "" };
    case "channel_present": return { k: "channel_present", kind: p.kind };
    case "in_list": return { k: "in_list", list_id: p.list_id };
    case "not_in_list": return { k: "not_in_list", list_id: p.list_id };
    case "not_in_segment": return { k: "not_in_segment", segment_id: p.segment_id };
  }
  return { k: "field" };
}

function draftToPredicate(d: DraftPredicate): SegmentPredicate | null {
  switch (d.k) {
    case "field": {
      if (!d.field) return null;
      const value = d.op === "in" ? (d.value || "").split(",").map((s) => s.trim()).filter(Boolean) : (d.value ?? "");
      return { field: d.field, op: d.op || "eq", value };
    }
    case "tag_in":
    case "tag_not_in": {
      const tags = (d.tags || "").split(",").map((s) => s.trim()).filter(Boolean);
      if (tags.length === 0) return null;
      return { predicate: d.k, tags };
    }
    case "attribute": {
      if (!d.key) return null;
      return { predicate: "attribute", key: d.key, op: d.op || "eq", value: d.value ?? "" };
    }
    case "last_activity_within": {
      const days = Number(d.days || 0);
      if (!days) return null;
      const out: SegmentPredicate = { predicate: "last_activity_within", days };
      if (d.kind) (out as { kind?: string }).kind = d.kind;
      return out;
    }
    case "channel_present": {
      if (d.kind !== "email" && d.kind !== "phone") return null;
      return { predicate: "channel_present", kind: d.kind };
    }
    case "in_list":
    case "not_in_list": {
      if (!d.list_id) return null;
      return { predicate: d.k, list_id: d.list_id };
    }
    case "not_in_segment": {
      if (!d.segment_id) return null;
      return { predicate: "not_in_segment", segment_id: d.segment_id };
    }
  }
  return null;
}

function SegmentEditorModal({ editing, lists, segments, onCancel, onSubmit }: {
  editing?: Segment | null;
  lists: List[];
  segments: Segment[];
  onCancel: () => void;
  onSubmit: (patch: Partial<Segment>) => void | Promise<void>;
}) {
  const isEdit = !!editing;
  const [name, setName] = useState(editing?.name || "");
  const [description, setDescription] = useState(editing?.description || "");
  const [kind, setKind] = useState<"dynamic" | "static">((editing?.kind as "dynamic" | "static") || "dynamic");
  const [listID, setListID] = useState<number | "">(editing?.list_id ?? "");
  const [predicates, setPredicates] = useState<DraftPredicate[]>(
    (editing?.definition || []).map(predicateToDraft)
  );
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!name.trim()) return;
    const def = predicates.map(draftToPredicate).filter((p): p is SegmentPredicate => p !== null);
    setBusy(true);
    try {
      const patch: Partial<Segment> = {
        name: name.trim(),
        description: description.trim(),
        kind,
        list_id: listID === "" ? null : Number(listID),
        definition: def,
      };
      await onSubmit(patch);
    } finally { setBusy(false); }
  };

  return (
    <ModalShell
      title={isEdit ? `Edit segment — ${editing!.name}` : "New segment"}
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !name.trim()}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Saving…" : (isEdit ? "Save" : "Create")}</button>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Name</label>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          placeholder="EU trial users"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-start gap-2">
        <label className="text-text-muted w-24 mt-1">Description</label>
        <textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={2}
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Kind</label>
        <select
          value={kind}
          onChange={(e) => setKind(e.target.value as "dynamic" | "static")}
          className="bg-bg-input border border-border rounded px-2 py-1"
        >
          <option value="dynamic">dynamic — re-evaluates each call</option>
          <option value="static">static — frozen snapshot</option>
        </select>
      </div>
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">In list</label>
        <select
          value={listID === "" ? "" : String(listID)}
          onChange={(e) => setListID(e.target.value === "" ? "" : Number(e.target.value))}
          className="bg-bg-input border border-border rounded px-2 py-1"
        >
          <option value="">— project-wide —</option>
          {lists.filter((l) => !l.archived_at).map((l) => (
            <option key={l.id} value={l.id}>{l.name}</option>
          ))}
        </select>
      </div>
      <hr className="border-border" />
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-xs uppercase tracking-wide text-text-dim">Filters (all must match)</h3>
          <button
            type="button"
            onClick={() => setPredicates((p) => [...p, { k: "field", field: "company", op: "eq", value: "" }])}
            className="text-xs px-2 py-0.5 border border-border rounded hover:bg-bg-input"
          >+ Add filter</button>
        </div>
        {predicates.length === 0 ? (
          <p className="text-text-dim text-xs">No filters yet. The segment will match every contact in scope.</p>
        ) : (
          <ul className="space-y-2">
            {predicates.map((p, i) => (
              <li key={i} className="border border-border rounded p-2 space-y-1.5">
                <div className="flex items-center gap-2 text-xs">
                  <select
                    value={p.k}
                    onChange={(e) => {
                      const k = e.target.value as PredicateKind;
                      setPredicates((arr) => arr.map((x, j) => j === i ? { k } : x));
                    }}
                    className="bg-bg-input border border-border rounded px-1.5 py-0.5"
                  >
                    <option value="field">column…</option>
                    <option value="tag_in">tag in…</option>
                    <option value="tag_not_in">tag not in…</option>
                    <option value="attribute">attribute…</option>
                    <option value="last_activity_within">activity within last…</option>
                    <option value="channel_present">has channel…</option>
                    <option value="in_list">in list…</option>
                    <option value="not_in_list">not in list…</option>
                    <option value="not_in_segment">not in segment…</option>
                  </select>
                  <button
                    type="button"
                    onClick={() => setPredicates((arr) => arr.filter((_, j) => j !== i))}
                    className="ml-auto text-text-dim hover:text-red"
                    title="Remove filter"
                  >×</button>
                </div>
                <PredicateRow
                  draft={p}
                  lists={lists}
                  segments={segments}
                  excludeSegmentId={editing?.id}
                  onChange={(patch) => setPredicates((arr) => arr.map((x, j) => j === i ? { ...x, ...patch } : x))}
                />
              </li>
            ))}
          </ul>
        )}
      </div>
    </ModalShell>
  );
}

function PredicateRow({ draft, lists, segments, excludeSegmentId, onChange }: {
  draft: DraftPredicate;
  lists: List[];
  segments: Segment[];
  excludeSegmentId?: number;
  onChange: (patch: Partial<DraftPredicate>) => void;
}) {
  const cls = "bg-bg-input border border-border rounded px-1.5 py-0.5 text-xs";
  switch (draft.k) {
    case "field":
      return (
        <div className="flex items-center gap-1.5">
          <select value={draft.field || "company"} onChange={(e) => onChange({ field: e.target.value })} className={cls}>
            {FIELD_OPTIONS.map((f) => <option key={f} value={f}>{f}</option>)}
          </select>
          <select value={draft.op || "eq"} onChange={(e) => onChange({ op: e.target.value })} className={cls}>
            {FIELD_OPS.map((o) => <option key={o} value={o}>{o}</option>)}
          </select>
          {draft.op !== "is_null" && (
            <input
              type="text"
              value={draft.value || ""}
              onChange={(e) => onChange({ value: e.target.value })}
              placeholder={draft.op === "in" ? "comma,separated" : "value"}
              className={`${cls} flex-1`}
            />
          )}
        </div>
      );
    case "tag_in":
    case "tag_not_in":
      return (
        <input
          type="text"
          value={draft.tags || ""}
          onChange={(e) => onChange({ tags: e.target.value })}
          placeholder="enterprise, trial"
          className={`${cls} w-full`}
        />
      );
    case "attribute":
      return (
        <div className="flex items-center gap-1.5">
          <input type="text" value={draft.key || ""} onChange={(e) => onChange({ key: e.target.value })} placeholder="lead_score" className={`${cls} flex-1`} />
          <select value={draft.op || "eq"} onChange={(e) => onChange({ op: e.target.value })} className={cls}>
            {["eq", "neq", "gt", "gte", "lt", "lte", "contains", "is_null"].map((o) => <option key={o} value={o}>{o}</option>)}
          </select>
          {draft.op !== "is_null" && (
            <input type="text" value={draft.value || ""} onChange={(e) => onChange({ value: e.target.value })} placeholder="value" className={`${cls} flex-1`} />
          )}
        </div>
      );
    case "last_activity_within":
      return (
        <div className="flex items-center gap-1.5">
          <input type="number" value={draft.days || ""} onChange={(e) => onChange({ days: e.target.value })} placeholder="30" className={`${cls} w-24`} />
          <span className="text-text-dim text-xs">days</span>
          <select value={draft.kind || ""} onChange={(e) => onChange({ kind: e.target.value })} className={cls}>
            <option value="">any kind</option>
            {["email_received", "email_sent", "sms_received", "sms_sent", "whatsapp_received", "whatsapp_sent", "call", "meeting", "note"].map((k) => (
              <option key={k} value={k}>{k}</option>
            ))}
          </select>
        </div>
      );
    case "channel_present":
      return (
        <select value={draft.kind || ""} onChange={(e) => onChange({ kind: e.target.value })} className={cls}>
          <option value="">— pick channel —</option>
          <option value="email">email</option>
          <option value="phone">phone</option>
        </select>
      );
    case "in_list":
    case "not_in_list":
      return (
        <select
          value={draft.list_id || ""}
          onChange={(e) => onChange({ list_id: Number(e.target.value) })}
          className={`${cls} w-full`}
        >
          <option value="">— pick list —</option>
          {lists.filter((l) => !l.archived_at).map((l) => (
            <option key={l.id} value={l.id}>{l.name}</option>
          ))}
        </select>
      );
    case "not_in_segment":
      return (
        <select
          value={draft.segment_id || ""}
          onChange={(e) => onChange({ segment_id: Number(e.target.value) })}
          className={`${cls} w-full`}
        >
          <option value="">— pick segment —</option>
          {segments.filter((s) => !s.archived_at && s.id !== excludeSegmentId).map((s) => (
            <option key={s.id} value={s.id}>{s.name}{s.kind === "static" ? " (snapshot)" : ""}</option>
          ))}
        </select>
      );
  }
  return null;
}
