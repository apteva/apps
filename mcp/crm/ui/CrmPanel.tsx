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
  kind: string;
  value: string;
  label?: string;
  is_primary?: boolean;
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
  started_at: string;
  last_activity_at: string;
}
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

type Tab = "contacts" | "lists" | "segments" | "settings";

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

export default function CrmPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("contacts");
  const [contacts, setContacts] = useState<Contact[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<Contact | null>(null);
  const [activities, setActivities] = useState<Activity[]>([]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [edits, setEdits] = useState<Partial<Contact>>({});
  const [composer, setComposer] = useState<ComposerState | null>(null);
  const [errorToast, setErrorToast] = useState<string | null>(null);
  const [confirmDialog, setConfirmDialog] = useState<ConfirmState | null>(null);
  const [logActivityOpen, setLogActivityOpen] = useState(false);
  const [newContactOpen, setNewContactOpen] = useState(false);
  const [attrDefs, setAttrDefs] = useState<AttributeDef[]>([]);
  const [defineFieldOpen, setDefineFieldOpen] = useState(false);
  const [lists, setLists] = useState<List[]>([]);
  const [contactLists, setContactLists] = useState<List[]>([]);
  const [newListOpen, setNewListOpen] = useState(false);
  const [editListId, setEditListId] = useState<number | null>(null);
  const [segments, setSegments] = useState<Segment[]>([]);
  const [editSegmentId, setEditSegmentId] = useState<number | "new" | null>(null);
  const [segmentPreview, setSegmentPreview] = useState<{ id: number; contacts: Contact[]; total: number } | null>(null);

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
    const res = await fetch(`${MESSAGING_API}/tools/call?${withParams()}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool, args: { ...args, _project_id: projectId } }),
    });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [withParams, projectId]);

  const loadList = useCallback(async (q = "") => {
    setStatus("Loading…");
    try {
      const r = await api<{ contacts?: Contact[] }>("GET", "/contacts", undefined, q ? { q } : {});
      setContacts(r.contacts || []);
      setStatus(`${(r.contacts || []).length} contact${(r.contacts || []).length !== 1 ? "s" : ""}`);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [api]);

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

  // Initial load.
  useEffect(() => { loadList(""); }, [loadList]);
  useEffect(() => { loadAttrDefs(); }, [loadAttrDefs]);
  useEffect(() => { loadLists(); }, [loadLists]);
  useEffect(() => { loadSegments(); }, [loadSegments]);

  // Debounced search.
  useEffect(() => {
    const id = setTimeout(() => loadList(query.trim()), 250);
    return () => clearTimeout(id);
  }, [query, loadList]);

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
    setEdits({});
    try {
      const [c, a, conv, ls] = await Promise.all([
        api<{ contact: Contact }>("GET", `/contacts/${id}`),
        api<{ activities?: Activity[] }>("GET", `/contacts/${id}/activities`),
        api<{ conversations?: Conversation[] }>("GET", `/contacts/${id}/conversations`),
        api<{ lists?: List[] }>("GET", `/contacts/${id}/lists`).catch(() => ({ lists: [] })),
      ]);
      setDetail(c.contact);
      setActivities(a.activities || []);
      setConversations(conv.conversations || []);
      setContactLists(ls.lists || []);
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
    // First channel listed becomes is_primary; we prefer email when both
    // are given, matching the channel-precedence used by the send tool.
    const channels: Channel[] = [];
    if (email) channels.push({ kind: "email", value: email, is_primary: true });
    if (phone) channels.push({ kind: "phone", value: phone, is_primary: channels.length === 0 });
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
  const openCompose = (preset: Partial<ComposerState> = {}) => {
    if (!detail) return;
    setComposer({
      mode: preset.mode || "new",
      channel: preset.channel || preferredChannel(detail) || "email",
      subject: preset.subject || "",
      body: "",
      conversationId: preset.conversationId,
      replyToActivityId: preset.replyToActivityId,
      busy: false,
      error: null,
    });
  };

  const handleSendFromComposer = async () => {
    if (!composer || !detail) return;
    setComposer({ ...composer, busy: true, error: null });
    try {
      const path = composer.mode === "reply" ? `/contacts/${detail.id}/reply` : `/contacts/${detail.id}/messages`;
      await api(
        "POST",
        path,
        {
          channel: composer.channel,
          subject: composer.subject || undefined,
          body: composer.body,
          conversation_id: composer.conversationId,
        },
      );
      setComposer(null);
      reloadActivities(detail.id);
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

  // Group activities by conversation. Within a conversation, order
  // chronologically (oldest first) so the agent reads the thread top-
  // down. Loose activities (notes, calls, system, anything without a
  // conversation_id) keep their reverse-chrono order.
  const grouped = useMemo(() => groupActivitiesByConversation(activities, conversations), [activities, conversations]);
  const possibleMatches = useMemo(() => extractPossibleMatchIds(activities), [activities]);

  return (
    <div className="h-full flex flex-col">
      {/* Tabs */}
      <nav className="flex gap-1 border-b border-border px-3 pt-2 text-xs">
        <TabButton active={tab === "contacts"} onClick={() => setTab("contacts")}>Contacts</TabButton>
        <TabButton active={tab === "lists"} onClick={() => setTab("lists")}>Lists</TabButton>
        <TabButton active={tab === "segments"} onClick={() => setTab("segments")}>Segments</TabButton>
        <TabButton active={tab === "settings"} onClick={() => setTab("settings")}>Settings</TabButton>
      </nav>

      <div className="flex-1 min-h-0">
        {tab === "contacts" ? (
          <div className="h-full flex">
            {/* List */}
            <aside className="w-80 border-r border-border flex flex-col">
              <div className="p-3 border-b border-border flex items-center gap-2">
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
                <div className="max-w-2xl space-y-6">
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

                  {detail.channels && detail.channels.length > 0 && (
                    <section>
                      <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Channels</h2>
                      <ul className="space-y-1">
                        {detail.channels.map((ch, i) => (
                          <li key={i} className="text-sm flex items-center gap-2">
                            <span className="text-[10px] uppercase text-text-dim w-12">{ch.kind}</span>
                            <span className="text-text">{ch.value}</span>
                            {ch.label && <span className="text-[10px] px-1 rounded bg-border text-text-muted">{ch.label}</span>}
                            {ch.is_primary && <span className="text-[10px] px-1 rounded bg-accent/15 text-accent">primary</span>}
                          </li>
                        ))}
                      </ul>
                    </section>
                  )}

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


                  <section>
                    <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
                      Activity ({activities.length})
                    </h2>
                    {grouped.length === 0 ? (
                      <p className="text-text-muted text-sm">No activity logged.</p>
                    ) : (
                      <ul className="space-y-3">
                        {grouped.map((group, gi) => (
                          <ActivityGroup
                            key={`g${gi}`}
                            group={group}
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
        ) : tab === "lists" ? (
          <ListsTab
            lists={lists}
            onCreate={() => setNewListOpen(true)}
            onEdit={(id) => setEditListId(id)}
            onArchive={handleArchiveList}
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
            attrDefs={attrDefs}
            onAddField={() => setDefineFieldOpen(true)}
          />
        )}
      </div>

      {composer && detail && (
        <ComposerModal
          composer={composer}
          contact={detail}
          onCancel={() => setComposer(null)}
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
  | { kind: "conversation"; conversationId: string; channel: string; subject: string; activities: Activity[] }
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
      activities: inSameConvo,
    });
  }
  return out;
}

function ActivityGroup({ group, onReply }: { group: Group; onReply: (a: Activity) => void }) {
  if (group.kind === "loose") {
    const a = group.activities[0]!;
    if (TEST_SENT_KINDS.has(a.kind)) return null; // hide tests by default
    return <ActivityRow activity={a} onReply={onReply} />;
  }
  return (
    <li className="border border-border rounded">
      <div className="px-2 py-1 border-b border-border bg-bg-input/30 flex items-center gap-2 text-xs">
        <span className="text-[10px] uppercase text-text-dim">{group.channel}</span>
        <span className="text-text font-medium truncate flex-1">{group.subject || "(no subject)"}</span>
        <span className="text-text-dim">{group.activities.length} message{group.activities.length === 1 ? "" : "s"}</span>
      </div>
      <ul className="divide-y divide-border">
        {group.activities.map((a) => (
          <ActivityRow key={a.id} activity={a} onReply={onReply} compact />
        ))}
      </ul>
    </li>
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
        <span>{formatTime(activity.occurred_at)}{activity.source ? ` · ${activity.source}` : ""}</span>
        {isReceived && (
          <button
            type="button"
            onClick={() => onReply(activity)}
            className="ml-auto text-[10px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input"
          >Reply</button>
        )}
      </div>
      <div className="text-sm text-text whitespace-pre-wrap">{activity.body}</div>
    </li>
  );
}

// ─── Composer ─────────────────────────────────────────────────────

interface ComposerState {
  mode: "new" | "reply";
  channel: string;
  subject: string;
  body: string;
  conversationId?: number | string;
  replyToActivityId?: string;
  busy: boolean;
  error: string | null;
}

function preferredChannel(c: Contact): string {
  if (c.primary_email) return "email";
  if (c.primary_phone) return "sms";
  return "email";
}

function ComposerModal({
  composer,
  contact,
  onCancel,
  onChange,
  onSend,
}: {
  composer: ComposerState;
  contact: Contact;
  onCancel: () => void;
  onChange: (patch: Partial<ComposerState>) => void;
  onSend: () => void;
}) {
  const channels = availableChannels(contact);
  const isEmail = composer.channel === "email";
  return (
    <div className="absolute inset-0 bg-black/40 flex items-end justify-center pointer-events-auto">
      <div className="bg-bg border-t border-border w-full max-w-2xl rounded-t-lg shadow-lg">
        <header className="flex items-center justify-between px-4 py-2 border-b border-border">
          <div className="text-sm text-text">
            {composer.mode === "reply" ? "Reply to" : "Send to"} <span className="font-medium">{displayName(contact)}</span>
          </div>
          <button
            type="button"
            onClick={onCancel}
            className="text-text-dim hover:text-text px-2"
          >×</button>
        </header>
        <div className="p-4 space-y-3 text-sm">
          <div className="flex items-center gap-2">
            <label className="text-text-muted w-20">Channel</label>
            <select
              value={composer.channel}
              onChange={(e) => onChange({ channel: e.target.value })}
              disabled={composer.mode === "reply"}
              className="bg-bg-input border border-border rounded px-2 py-1 disabled:opacity-50"
            >
              {channels.map((ch) => <option key={ch} value={ch}>{ch}</option>)}
            </select>
            <span className="text-text-dim text-xs">→ {addressForChannel(contact, composer.channel)}</span>
          </div>
          {isEmail && (
            <div className="flex items-center gap-2">
              <label className="text-text-muted w-20">Subject</label>
              <input
                type="text"
                value={composer.subject}
                onChange={(e) => onChange({ subject: e.target.value })}
                className="flex-1 bg-bg-input border border-border rounded px-2 py-1"
              />
            </div>
          )}
          <div>
            <label className="text-text-muted text-xs uppercase tracking-wide block mb-1">Message</label>
            <textarea
              value={composer.body}
              onChange={(e) => onChange({ body: e.target.value })}
              rows={8}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 font-mono text-sm"
              placeholder="Write…"
            />
          </div>
          {composer.error && (
            <div className="text-red text-xs">{composer.error}</div>
          )}
        </div>
        <footer className="flex items-center gap-2 px-4 py-3 border-t border-border">
          <button
            type="button"
            onClick={onSend}
            disabled={composer.busy || !composer.body.trim()}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{composer.busy ? "Sending…" : "Send"}</button>
          <button
            type="button"
            onClick={onCancel}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          {composer.conversationId && (
            <span className="ml-auto text-text-dim text-xs">in conversation #{composer.conversationId}</span>
          )}
        </footer>
      </div>
    </div>
  );
}

function availableChannels(c: Contact): string[] {
  const out: string[] = [];
  if (c.primary_email) out.push("email");
  if (c.primary_phone) out.push("sms", "whatsapp");
  // Fall back to any contact_channels entries.
  if (c.channels) {
    for (const ch of c.channels) {
      if (ch.kind === "email" && !out.includes("email")) out.push("email");
      if (ch.kind === "phone") {
        if (!out.includes("sms")) out.push("sms");
        if (!out.includes("whatsapp")) out.push("whatsapp");
      }
    }
  }
  return out.length > 0 ? out : ["email"];
}

function addressForChannel(c: Contact, channel: string): string {
  if (channel === "email") return c.primary_email || c.channels?.find((ch) => ch.kind === "email")?.value || "(no email)";
  if (channel === "sms" || channel === "whatsapp") return c.primary_phone || c.channels?.find((ch) => ch.kind === "phone")?.value || "(no phone)";
  return "—";
}

// ─── Settings tab ─────────────────────────────────────────────────

function SettingsTab({ messagingTool, attrDefs, onAddField }: {
  messagingTool: <T,>(tool: string, args?: Record<string, unknown>) => Promise<T>;
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

  useEffect(() => { loadRoutes(); }, [loadRoutes]);

  // CRM-pointing routes — what we wire up.
  const crmRoutes = useMemo(
    () => (routes || []).filter((r) => r.target_app === "crm" && r.target_route === "/inbound"),
    [routes],
  );
  const hasEmail = crmRoutes.some((r) => r.channel === "email");
  const hasSMS = crmRoutes.some((r) => r.channel === "sms");
  const hasWhatsApp = crmRoutes.some((r) => r.channel === "whatsapp");

  const wire = async (channel: "email" | "sms" | "whatsapp") => {
    setBusy(true);
    setError(null);
    try {
      await messagingTool("inbound_route_set", {
        pattern: "*",
        channel,
        target_app: "crm",
        target_route: "/inbound",
        priority: 0,
      });
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
        <p className="text-text-muted text-sm">Wire CRM into the messaging app so inbound mail/SMS/WhatsApp lands on the right contact's timeline automatically.</p>
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
              Wire-up creates a low-priority <span className="font-mono">*</span> catch-all route in messaging. To
              constrain by recipient pattern, edit the route from the messaging panel.
            </p>
          </div>
        )}

        {error && !messagingMissing && (
          <div className="mt-3 text-red text-xs">Error: {error}</div>
        )}
      </section>

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
      >{wired ? "Wired" : "Wire up"}</button>
    </div>
  );
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

// ListsTab renders the main lists view: left column = list of lists,
// right column = nothing for v0.4 (member view comes via the Contacts
// tab once the agent picks a contact). Keeping it lean for v0.4 — a
// dedicated list-detail pane with member browsing is a v0.5 polish.
function ListsTab({ lists, onCreate, onEdit, onArchive }: {
  lists: List[];
  onCreate: () => void;
  onEdit: (id: number) => void;
  onArchive: (l: List) => void;
}) {
  const active = lists.filter((l) => !l.archived_at);
  return (
    <div className="p-6 max-w-3xl space-y-4">
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
        <ul className="divide-y divide-border border border-border rounded">
          {active.map((l) => (
            <li key={l.id} className="px-3 py-2 flex items-center gap-3">
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-text font-medium truncate">{l.name}</span>
                  <span className="text-[10px] uppercase text-text-dim font-mono">{l.slug}</span>
                  {typeof l.member_count === "number" && (
                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">
                      {l.member_count} member{l.member_count === 1 ? "" : "s"}
                    </span>
                  )}
                </div>
                <div className="text-xs text-text-muted truncate">
                  {[
                    l.default_sender_email && `from: ${l.default_sender_email}`,
                    l.default_sender_phone && `phone: ${l.default_sender_phone}`,
                    l.inbound_route_pattern && `inbound: ${l.inbound_route_pattern}`,
                  ].filter(Boolean).join(" · ") || (l.description || "—")}
                </div>
              </div>
              <button
                type="button"
                onClick={() => onEdit(l.id)}
                className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
              >Edit</button>
              <button
                type="button"
                onClick={() => onArchive(l)}
                className="text-xs px-2 py-1 text-red border border-red/50 rounded hover:bg-red/10"
              >Archive</button>
            </li>
          ))}
        </ul>
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
