// CrmPanel — native React port of the crm app's ContactsPanel.
// Talks to /api/apps/crm/* through the platform proxy. Two-pane
// layout: contact list on the left, detail on the right. Tabbed
// shell exposes a Settings pane for the messaging coupling.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

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
  value: unknown;
}
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

function formatAttrValue(a: Attribute): string {
  if (a.value == null) return "—";
  if (Array.isArray(a.value)) return a.value.join(", ");
  if (typeof a.value === "boolean") return a.value ? "yes" : "no";
  return String(a.value);
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

type Tab = "contacts" | "settings";

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

  // Initial load.
  useEffect(() => { loadList(""); }, [loadList]);

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
  });

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
    setEdits({});
    try {
      const [c, a, conv] = await Promise.all([
        api<{ contact: Contact }>("GET", `/contacts/${id}`),
        api<{ activities?: Activity[] }>("GET", `/contacts/${id}/activities`),
        api<{ conversations?: Conversation[] }>("GET", `/contacts/${id}/conversations`),
      ]);
      setDetail(c.contact);
      setActivities(a.activities || []);
      setConversations(conv.conversations || []);
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
      alert("Save failed: " + (e as Error).message);
    }
  };

  const handleArchive = async () => {
    if (!detail) return;
    if (!confirm(`Archive ${displayName(detail)}?`)) return;
    try {
      await api("DELETE", `/contacts/${detail.id}`);
      setDetail(null);
      setSelectedId(null);
      await loadList(query.trim());
    } catch (e) {
      alert("Archive failed: " + (e as Error).message);
    }
  };

  const handleLogActivity = async () => {
    if (!detail) return;
    const kind = prompt("Kind (call / meeting / note):", "note");
    if (!kind) return;
    const body = prompt("Body:");
    if (!body) return;
    try {
      await api("POST", `/contacts/${detail.id}/activities`, { kind, body, source: "human" });
      reloadActivities(detail.id);
    } catch (e) {
      alert("Log failed: " + (e as Error).message);
    }
  };

  const handleNewContact = async () => {
    const first = prompt("First name:");
    if (!first) return;
    const email = prompt("Email (optional):", "") || "";
    try {
      const r = await api<{ contact: Contact }>("POST", "/contacts", {
        first_name: first,
        source: "human",
        channels: email ? [{ kind: "email", value: email, is_primary: true }] : [],
      });
      await loadList();
      selectContact(r.contact.id);
    } catch (e) {
      alert("Create failed: " + (e as Error).message);
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
                  onClick={handleNewContact}
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

                  {detail.attributes && detail.attributes.length > 0 && (
                    <section>
                      <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Attributes</h2>
                      <div className="grid grid-cols-[140px_1fr] gap-2 text-sm">
                        {detail.attributes.map((a, i) => (
                          <span key={i} className="contents">
                            <span className="text-text-muted">{a.label || a.key}</span>
                            <span className="text-text">{formatAttrValue(a)}</span>
                          </span>
                        ))}
                      </div>
                    </section>
                  )}

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
                      onClick={handleLogActivity}
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
        ) : (
          <SettingsTab
            messagingTool={messagingTool}
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

function SettingsTab({ messagingTool }: {
  messagingTool: <T,>(tool: string, args?: Record<string, unknown>) => Promise<T>;
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
