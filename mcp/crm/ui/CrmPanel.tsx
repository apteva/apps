// CrmPanel — native React port of the crm app's ContactsPanel.
// Talks to /api/apps/crm/* through the platform proxy. Two-pane
// layout: contact list on the left, detail on the right.

import { useCallback, useEffect, useRef, useState } from "react";

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

const API = "/api/apps/crm";

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

export default function CrmPanel({ projectId, installId }: NativePanelProps) {
  const [contacts, setContacts] = useState<Contact[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<Contact | null>(null);
  const [activities, setActivities] = useState<Activity[]>([]);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [edits, setEdits] = useState<Partial<Contact>>({});

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
  // event lands on the (crm, project) lane. Topic filter keeps unrelated
  // chatter (e.g. activity-only events when nothing in the list cares)
  // from triggering an extra fetch.
  useAppEvents("crm", projectId, (ev) => {
    if (
      ev.topic === "contact.added" ||
      ev.topic === "contact.updated" ||
      ev.topic === "contact.deleted" ||
      ev.topic === "contact.merged"
    ) {
      loadList(query.trim());
    }
  });

  const selectContact = useCallback(async (id: string) => {
    setSelectedId(id);
    setDetail(null);
    setActivities([]);
    setEdits({});
    try {
      const [c, a] = await Promise.all([
        api<{ contact: Contact }>("GET", `/contacts/${id}`),
        api<{ activities?: Activity[] }>("GET", `/contacts/${id}/activities`),
      ]);
      setDetail(c.contact);
      setActivities(a.activities || []);
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
    const kind = prompt("Kind (call / meeting / note / email_sent / email_received):", "note");
    if (!kind) return;
    const body = prompt("Body:");
    if (!body) return;
    try {
      await api("POST", `/contacts/${detail.id}/activities`, { kind, body, source: "human" });
      const r = await api<{ activities?: Activity[] }>("GET", `/contacts/${detail.id}/activities`);
      setActivities(r.activities || []);
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

  const fieldValue = <K extends keyof Contact>(key: K): string => {
    const e = edits[key];
    if (e !== undefined) return String(e ?? "");
    if (!detail) return "";
    return String(detail[key] ?? "");
  };

  const updateField = <K extends keyof Contact>(key: K, v: string) => {
    setEdits((prev) => ({ ...prev, [key]: v }));
  };

  return (
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
            <header>
              <h1 className="text-xl text-text font-semibold">{displayName(detail)}</h1>
              <p className="text-text-muted text-sm">{secondaryLine(detail) || "—"}</p>
            </header>

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
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Activity ({activities.length})</h2>
              {activities.length === 0 ? (
                <p className="text-text-muted text-sm">No activity logged.</p>
              ) : (
                <ul className="space-y-2">
                  {activities.map((a) => (
                    <li key={a.id} className="border border-border rounded p-2">
                      <div className="flex items-center gap-2 text-xs text-text-dim mb-1">
                        <span className="text-[10px] px-1.5 py-0.5 rounded bg-accent/10 text-accent">{a.kind}</span>
                        <span>{formatTime(a.occurred_at)}{a.source ? ` · ${a.source}` : ""}</span>
                      </div>
                      <div className="text-sm text-text">{a.body}</div>
                    </li>
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
