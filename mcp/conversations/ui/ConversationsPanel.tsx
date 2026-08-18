// ConversationsPanel — chat + inbox for the conversations app.
//
// The chat column is a faithful port of the dashboard Chat page's UX:
// conversation sidebar with unread badges, transcript with typed cards
// (approval / report / alert), an idempotent composer, SSE live rows
// PLUS ephemeral streaming bubbles on the named `stream` SSE channel
// (token text when the platform's telemetry bridge is on, phase frames
// otherwise — the panel renders either without knowing which), and a
// 5s REST reconcile poll as the transport backstop.
//
// The inbox tab is the priority view over the same rows: approvals
// first, alerts by severity, then reports, plus the per-agent status
// strip. Acting on a card anywhere updates every surface — same row.
//
// House rules: dashboard theme tokens only (bg-bg / text-text-muted /
// border-border / …), inline SVG glyphs in currentColor, no emojis,
// no arbitrary Tailwind values. Built by
// `bun run scripts/build-panels.ts --app conversations`.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/conversations";

// ─── types (mirror the app's wire shapes) ────────────────────────────

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Conversation {
  id: string;
  project_id: string;
  lead_agent_id: number;
  lead_agent_name?: string;
  title: string;
  kind: "direct" | "room";
  origin: string;
  updated_at: string;
}

interface CardComponent {
  app: string;
  name: string;
  props: Record<string, unknown>;
}

interface Message {
  id: number;
  conversation_id: string;
  role: "user" | "agent" | "system";
  content: string;
  agent_id?: number;
  component_kind?: string;
  severity?: string;
  components: CardComponent[];
  client_message_id?: string;
  created_at: string;
}

interface StreamFrame {
  chat_id: string;
  call_id: string;
  text: string;
  phase?: string;
  done: boolean;
}

interface InboxItem {
  message: Message;
  priority: number;
}

interface UnreadEntry {
  conversation_id: string;
  latest_id: number;
  unread: number;
}

interface CurrentStatus {
  agent_id: number;
  message: Message;
}

interface AgentInfo {
  id: number;
  name: string;
  status: string;
  attached: boolean;
}

// Pickers only offer agents that hold this app's MCP — an unattached
// agent has no conversations_send and can never reply. When no agent
// reports attached (an older server without the binding annotation, or
// genuinely nothing bound) we cannot tell the two apart, so fall back
// to the full list and let the dialog show a caveat instead of a
// dead-end empty picker.
function selectableAgents(agents: AgentInfo[] | null): {
  list: AgentInfo[];
  bindingKnown: boolean;
} {
  const all = agents ?? [];
  const attached = all.filter((a) => a.attached);
  return attached.length > 0
    ? { list: attached, bindingKnown: true }
    : { list: all, bindingKnown: false };
}

interface ParticipantsInfo {
  agent_ids: number[];
  lead_agent_id: number;
}

// ─── fetch helpers ───────────────────────────────────────────────────

async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(`${API}${path}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

async function apiSend<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

const apiPost = <T,>(path: string, body: unknown) => apiSend<T>("POST", path, body);
const apiPatch = <T,>(path: string, body: unknown) => apiSend<T>("PATCH", path, body);
const apiDelete = <T,>(path: string) => apiSend<T>("DELETE", path);

function newClientMessageId(): string {
  return `panel-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

function relTime(iso: string): string {
  if (!iso) return "";
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return "";
  const m = Math.floor(ms / 60000);
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

// ─── glyphs (inline SVG, currentColor — no emojis, no Tailwind color
//     utilities inside SVG) ──────────────────────────────────────────

function Glyph({ d, size = 16 }: { d: string; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={d} />
    </svg>
  );
}

const GLYPH_CHAT =
  "M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z";
const GLYPH_INBOX =
  "M22 12h-6l-2 3h-4l-2-3H2 M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z";
const GLYPH_ALERT =
  "M12 9v4 M12 17h.01 M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z";
const GLYPH_CHECK = "M20 6 9 17l-5-5";
const GLYPH_X = "M18 6 6 18 M6 6l12 12";
const GLYPH_REPORT =
  "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8";
const GLYPH_PLUS = "M12 5v14 M5 12h14";
const GLYPH_MORE = "M12 12h.01 M19 12h.01 M5 12h.01";
const GLYPH_ARCHIVE = "M21 8v13H3V8 M1 3h22v5H1z M10 12h4";
const GLYPH_TRASH =
  "M3 6h18 M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6 M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2";
const GLYPH_RESTORE = "M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8 M3 3v5h5";

// ─── typed cards ─────────────────────────────────────────────────────

function ApprovalCard({
  message,
  onAction,
}: {
  message: Message;
  onAction: (messageId: number, actionId: string, note: string) => Promise<void>;
}) {
  const card = message.components.find((c) => c.name === "approval-card");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!card) return null;
  const status = String(card.props.status ?? "pending");
  const actions = Array.isArray(card.props.actions)
    ? (card.props.actions as Array<{ id: string; label: string; style?: string }>)
    : [];

  const act = async (actionId: string) => {
    setBusy(true);
    setError("");
    try {
      await onAction(message.id, actionId, note);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="rounded-md border border-border bg-bg-card p-3">
      <div className="flex items-center gap-2 text-xs text-text-muted">
        <span className="text-warn">
          <Glyph d={GLYPH_ALERT} size={14} />
        </span>
        <span className="font-semibold uppercase tracking-wide">Approval</span>
        {status !== "pending" && (
          <span
            className={`ml-auto px-1.5 py-0.5 rounded text-bg ${status === "approve" ? "bg-success" : "bg-error"}`}
          >
            {status === "approve" ? "approved" : status}
          </span>
        )}
      </div>
      <p className="mt-1.5 text-sm font-medium text-text">{String(card.props.title ?? "")}</p>
      {card.props.body ? (
        <p className="mt-1 text-sm text-text-muted whitespace-pre-wrap">{String(card.props.body)}</p>
      ) : null}
      {status === "pending" && (
        <div className="mt-2.5 flex flex-col gap-2">
          <input
            type="text"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="Optional note for the agent"
            className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs text-text"
          />
          <div className="flex items-center gap-2">
            {actions.map((a) => (
              <button
                key={a.id}
                type="button"
                disabled={busy}
                onClick={() => act(a.id)}
                className={`px-3 py-1.5 rounded text-xs font-semibold disabled:opacity-50 ${
                  a.style === "danger"
                    ? "bg-error text-bg"
                    : a.style === "primary"
                      ? "bg-success text-bg"
                      : "border border-border text-text hover:bg-bg-input"
                }`}
              >
                {a.label}
              </button>
            ))}
          </div>
          {error && <p className="text-xs text-error">{error}</p>}
        </div>
      )}
      {status !== "pending" && card.props.note ? (
        <p className="mt-1.5 text-xs text-text-dim">Note: {String(card.props.note)}</p>
      ) : null}
    </div>
  );
}

function ReportCard({ message }: { message: Message }) {
  const card = message.components.find((c) => c.name === "report-card");
  if (!card) return null;
  return (
    <div className="rounded-md border border-border bg-bg-card p-3">
      <div className="flex items-center gap-2 text-xs text-text-muted">
        <span className="text-info">
          <Glyph d={GLYPH_REPORT} size={14} />
        </span>
        <span className="font-semibold uppercase tracking-wide">Report</span>
        {card.props.period ? <span className="ml-auto">{String(card.props.period)}</span> : null}
      </div>
      <p className="mt-1.5 text-sm font-medium text-text">{String(card.props.title ?? "")}</p>
      <p className="mt-1 text-sm text-text-muted whitespace-pre-wrap">
        {String(card.props.summary ?? "")}
      </p>
    </div>
  );
}

function AlertCard({ message }: { message: Message }) {
  const card = message.components.find((c) => c.name === "alert-card");
  if (!card) return null;
  const severity = String(card.props.severity ?? "info");
  const tone =
    severity === "error" ? "text-error" : severity === "warn" ? "text-warn" : "text-info";
  return (
    <div className="rounded-md border border-border bg-bg-card p-3">
      <div className={`flex items-center gap-2 text-xs ${tone}`}>
        <Glyph d={GLYPH_ALERT} size={14} />
        <span className="font-semibold uppercase tracking-wide">{severity}</span>
      </div>
      <p className="mt-1.5 text-sm text-text whitespace-pre-wrap">{String(card.props.text ?? "")}</p>
    </div>
  );
}

function MessageCards({
  message,
  onAction,
}: {
  message: Message;
  onAction: (messageId: number, actionId: string, note: string) => Promise<void>;
}) {
  switch (message.component_kind) {
    case "approval":
      return <ApprovalCard message={message} onAction={onAction} />;
    case "report":
      return <ReportCard message={message} />;
    case "alert":
      return <AlertCard message={message} />;
    default:
      return null;
  }
}

// ─── streaming bubble ────────────────────────────────────────────────

// StreamingBubble renders the ephemeral frame state. Phase
// "acknowledgement" shows the animated thinking indicator; any text
// replaces it. RAF coalescing happens upstream in the frame handler.
function StreamingBubble({ text, phase }: { text: string; phase?: string }) {
  return (
    <div className="max-w-[85%] self-start rounded-md border border-border bg-bg-card px-3 py-2">
      {text ? (
        <p className="text-sm text-text whitespace-pre-wrap">
          {text}
          <span className="inline-block w-2 text-text-dim animate-pulse">▍</span>
        </p>
      ) : (
        <div className="flex items-center gap-1.5 py-1" aria-label="Agent is thinking">
          <span className="w-1.5 h-1.5 rounded-full bg-text-dim animate-pulse" />
          <span
            className="w-1.5 h-1.5 rounded-full bg-text-dim animate-pulse"
            style={{ animationDelay: "150ms" }}
          />
          <span
            className="w-1.5 h-1.5 rounded-full bg-text-dim animate-pulse"
            style={{ animationDelay: "300ms" }}
          />
        </div>
      )}
      {phase === "acknowledgement" && !text ? (
        <p className="mt-1 text-xs text-text-dim">thinking</p>
      ) : null}
    </div>
  );
}

// ─── dialogs ─────────────────────────────────────────────────────────

// Overlay is a lightweight port of the dashboard's Modal shell: same
// blurred backdrop and card, Escape + click-outside close, no portal
// (the panel already lives in the page document).
function Overlay({
  open,
  onClose,
  ariaLabel,
  children,
}: {
  open: boolean;
  onClose: () => void;
  ariaLabel: string;
  children: React.ReactNode;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        className="relative bg-bg-card border border-border rounded-lg shadow-lg w-full max-w-md max-h-full overflow-hidden flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        {children}
      </div>
    </div>
  );
}

// NewConversationDialog is the panel's port of the dashboard's
// NewConversationModal: agent multi-select with filter, lead radio when
// more than one, optional title.
function NewConversationDialog({
  open,
  projectId,
  agents,
  agentsError,
  onClose,
  onCreated,
}: {
  open: boolean;
  projectId: string;
  agents: AgentInfo[] | null;
  agentsError: string;
  onClose: () => void;
  onCreated: (conversation: Conversation) => void;
}) {
  const [selected, setSelected] = useState<number[]>([]);
  const [leadId, setLeadId] = useState<number | null>(null);
  const [title, setTitle] = useState("");
  const [filter, setFilter] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const { list: selectable, bindingKnown } = useMemo(() => selectableAgents(agents), [agents]);
  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return q ? selectable.filter((a) => a.name.toLowerCase().includes(q)) : selectable;
  }, [selectable, filter]);

  const reset = () => {
    setSelected([]);
    setLeadId(null);
    setTitle("");
    setFilter("");
    setError("");
  };

  const close = () => {
    if (saving) return;
    reset();
    onClose();
  };

  const toggle = (agentId: number) => {
    setSelected((current) => {
      const next = current.includes(agentId)
        ? current.filter((id) => id !== agentId)
        : [...current, agentId];
      setLeadId((lead) => (next.length === 0 ? null : lead && next.includes(lead) ? lead : next[0]!));
      return next;
    });
  };

  const create = async () => {
    if (selected.length === 0 || saving) return;
    setSaving(true);
    setError("");
    try {
      const conversation = await apiPost<Conversation>("/chats", {
        agent_ids: selected,
        lead_agent_id: leadId ?? selected[0],
        title: title.trim() || undefined,
        project_id: projectId,
      });
      reset();
      onCreated(conversation);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Overlay open={open} onClose={close} ariaLabel="New conversation">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <h2 className="text-sm font-semibold text-text">New conversation</h2>
        <button
          type="button"
          onClick={close}
          className="inline-flex h-8 w-8 items-center justify-center rounded text-text-muted hover:bg-bg-input hover:text-text"
          aria-label="Close"
        >
          <Glyph d={GLYPH_X} size={14} />
        </button>
      </div>
      <div className="space-y-4 overflow-auto p-4">
        <label className="block">
          <span className="mb-1 block text-xs uppercase text-text-muted">Title</span>
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Optional"
            className="w-full rounded border border-border bg-bg-input px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
          />
        </label>
        <div>
          <div className="mb-1 text-xs uppercase text-text-muted">Agents</div>
          {agents === null && !agentsError ? (
            <p className="text-xs text-text-dim">Loading agents…</p>
          ) : agentsError ? (
            <p className="text-xs text-error">{agentsError}</p>
          ) : selectable.length === 0 ? (
            <p className="text-xs text-text-dim">No agents in this project yet.</p>
          ) : (
            <>
              {!bindingKnown && (
                <p className="mb-2 text-xs text-text-dim">
                  None of these agents report the conversations app attached — an agent without it
                  cannot reply here. Attach the app in the agent's settings first.
                </p>
              )}
              <input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Search agents"
                className="mb-2 w-full rounded border border-border bg-bg-input px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
              />
              <div className="max-h-56 divide-y divide-border overflow-y-auto rounded border border-border">
                {visible.map((agent) => {
                  const checked = selected.includes(agent.id);
                  return (
                    <div key={agent.id} className="flex items-center gap-3 px-3 py-2 hover:bg-bg-input">
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggle(agent.id)}
                        className="h-4 w-4"
                        aria-label={agent.name}
                      />
                      <button type="button" onClick={() => toggle(agent.id)} className="min-w-0 flex-1 text-left">
                        <span className="block truncate text-sm text-text">{agent.name}</span>
                        <span className="block text-xs text-text-muted">#{agent.id} · {agent.status}</span>
                      </button>
                      {selected.length > 1 && checked && (
                        <label className="flex items-center gap-1 text-xs text-text-muted">
                          <input
                            type="radio"
                            name="conversations-lead-agent"
                            checked={leadId === agent.id}
                            onChange={() => setLeadId(agent.id)}
                          />
                          lead
                        </label>
                      )}
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>
        {error && <p className="text-xs text-error">{error}</p>}
      </div>
      <div className="flex justify-end gap-2 border-t border-border px-4 py-3">
        <button
          type="button"
          onClick={close}
          className="rounded border border-border px-3 py-1.5 text-xs text-text-muted hover:bg-bg-input hover:text-text"
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={create}
          disabled={selected.length === 0 || saving}
          className="rounded bg-accent px-3 py-1.5 text-xs font-semibold text-bg disabled:opacity-40"
        >
          {saving ? "Creating…" : "Create"}
        </button>
      </div>
    </Overlay>
  );
}

// DetailsDialog ports the dashboard's ConversationDetails: rename,
// participant roster (lead fixed), archive, and delete behind an
// explicit confirm step.
function DetailsDialog({
  open,
  conversation,
  agents,
  onClose,
  onChanged,
  onRemoved,
}: {
  open: boolean;
  conversation: Conversation;
  agents: AgentInfo[] | null;
  onClose: () => void;
  onChanged: () => void;
  onRemoved: () => void;
}) {
  const [title, setTitle] = useState(conversation.title);
  const [participants, setParticipants] = useState<ParticipantsInfo | null>(null);
  const [addAgentId, setAddAgentId] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setTitle(conversation.title);
    setAddAgentId("");
    setConfirmDelete(false);
    setError("");
    apiGet<ParticipantsInfo>(`/participants?id=${encodeURIComponent(conversation.id)}`).then(
      setParticipants,
      (err) => setError(err instanceof Error ? err.message : String(err)),
    );
  }, [open, conversation.id, conversation.title]);

  const agentName = (id: number) =>
    agents?.find((a) => a.id === id)?.name || `agent ${id}`;
  // Same scoping as the new-conversation picker: only agents holding
  // this app's MCP can participate usefully.
  const available = useMemo(
    () =>
      selectableAgents(agents).list.filter(
        (a) => !(participants?.agent_ids ?? []).includes(a.id),
      ),
    [agents, participants],
  );

  const run = async (operation: () => Promise<void>) => {
    if (saving) return;
    setSaving(true);
    setError("");
    try {
      await operation();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const saveTitle = () => {
    const next = title.trim();
    if (!next || next === conversation.title) return;
    void run(async () => {
      await apiPatch(`/chats?id=${encodeURIComponent(conversation.id)}`, { title: next });
      onChanged();
    });
  };

  const addParticipant = () => {
    const id = Number(addAgentId);
    if (!id) return;
    void run(async () => {
      const updated = await apiPost<ParticipantsInfo>(
        `/participants?id=${encodeURIComponent(conversation.id)}`,
        { agent_id: id },
      );
      setParticipants(updated);
      setAddAgentId("");
      onChanged();
    });
  };

  const removeParticipant = (id: number) => {
    void run(async () => {
      const updated = await apiDelete<ParticipantsInfo>(
        `/participants?id=${encodeURIComponent(conversation.id)}&agent_id=${id}`,
      );
      setParticipants(updated);
      onChanged();
    });
  };

  const archive = () => {
    void run(async () => {
      await apiPatch(`/chats?id=${encodeURIComponent(conversation.id)}`, { archived: true });
      onClose();
      onRemoved();
    });
  };

  const remove = () => {
    void run(async () => {
      await apiDelete(`/chats?id=${encodeURIComponent(conversation.id)}`);
      onClose();
      onRemoved();
    });
  };

  return (
    <Overlay open={open} onClose={onClose} ariaLabel="Conversation details">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <h2 className="text-sm font-semibold text-text">Conversation details</h2>
        <button
          type="button"
          onClick={onClose}
          className="inline-flex h-8 w-8 items-center justify-center rounded text-text-muted hover:bg-bg-input hover:text-text"
          aria-label="Close"
        >
          <Glyph d={GLYPH_X} size={14} />
        </button>
      </div>
      <div className="space-y-4 overflow-auto p-4">
        <div>
          <div className="mb-1 text-xs uppercase text-text-muted">Title</div>
          <div className="flex gap-2">
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") saveTitle();
              }}
              maxLength={120}
              className="min-w-0 flex-1 rounded border border-border bg-bg-input px-2 py-1.5 text-sm text-text focus:border-accent focus:outline-none"
            />
            <button
              type="button"
              onClick={saveTitle}
              disabled={saving || !title.trim() || title.trim() === conversation.title}
              className="rounded border border-border px-2.5 text-xs text-text-muted hover:text-text disabled:opacity-40"
            >
              Save
            </button>
          </div>
        </div>

        <div>
          <div className="mb-2 text-xs uppercase text-text-muted">Participants</div>
          {participants === null ? (
            <p className="text-xs text-text-dim">Loading…</p>
          ) : (
            <div className="space-y-1.5">
              {participants.agent_ids.map((id) => (
                <div key={id} className="flex items-center gap-2 text-sm">
                  <span className="min-w-0 flex-1 truncate text-text-muted">{agentName(id)}</span>
                  {id === participants.lead_agent_id ? (
                    <span className="text-xs uppercase text-accent">lead</span>
                  ) : (
                    <button
                      type="button"
                      onClick={() => removeParticipant(id)}
                      disabled={saving}
                      className="text-xs text-text-dim hover:text-error"
                    >
                      Remove
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}
          {available.length > 0 && (
            <div className="mt-2 flex gap-2">
              <select
                value={addAgentId}
                onChange={(e) => setAddAgentId(e.target.value)}
                className="min-w-0 flex-1 rounded border border-border bg-bg-input px-2 py-1.5 text-xs text-text focus:border-accent focus:outline-none"
              >
                <option value="">Add an agent…</option>
                {available.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </select>
              <button
                type="button"
                onClick={addParticipant}
                disabled={!addAgentId || saving}
                className="rounded border border-border px-2.5 text-xs text-text-muted hover:text-text disabled:opacity-40"
              >
                Add
              </button>
            </div>
          )}
        </div>

        {error && <p className="text-xs text-error">{error}</p>}
      </div>
      <div className="flex items-center gap-2 border-t border-border px-4 py-3">
        <button
          type="button"
          onClick={archive}
          disabled={saving}
          className="inline-flex items-center gap-1.5 rounded border border-border px-2.5 py-1.5 text-xs text-text-muted hover:text-text disabled:opacity-40"
        >
          <Glyph d={GLYPH_ARCHIVE} size={13} />
          Archive
        </button>
        {confirmDelete ? (
          <span className="ml-auto flex items-center gap-2">
            <span className="text-xs text-text-muted">Delete permanently?</span>
            <button
              type="button"
              onClick={remove}
              disabled={saving}
              className="rounded bg-error px-2.5 py-1.5 text-xs font-semibold text-bg disabled:opacity-40"
            >
              {saving ? "Deleting…" : "Delete"}
            </button>
            <button
              type="button"
              onClick={() => setConfirmDelete(false)}
              disabled={saving}
              className="rounded border border-border px-2.5 py-1.5 text-xs text-text-muted hover:text-text"
            >
              Cancel
            </button>
          </span>
        ) : (
          <button
            type="button"
            onClick={() => setConfirmDelete(true)}
            disabled={saving}
            className="ml-auto inline-flex items-center gap-1.5 rounded border border-border px-2.5 py-1.5 text-xs text-error hover:bg-bg-input disabled:opacity-40"
          >
            <Glyph d={GLYPH_TRASH} size={13} />
            Delete
          </button>
        )}
      </div>
    </Overlay>
  );
}

// ─── transcript ──────────────────────────────────────────────────────

function MessageRow({
  message,
  onAction,
}: {
  message: Message;
  onAction: (messageId: number, actionId: string, note: string) => Promise<void>;
}) {
  if (message.component_kind) {
    return (
      <div className="max-w-[85%] self-start w-full">
        <MessageCards message={message} onAction={onAction} />
      </div>
    );
  }
  if (message.role === "system") {
    return (
      <p className="self-center text-xs text-text-dim italic px-4 text-center">{message.content}</p>
    );
  }
  const mine = message.role === "user";
  return (
    <div
      className={`max-w-[85%] rounded-md px-3 py-2 ${
        mine ? "self-end bg-accent text-bg" : "self-start border border-border bg-bg-card text-text"
      }`}
    >
      <p className="text-sm whitespace-pre-wrap">{message.content}</p>
      <p className={`mt-1 text-xs ${mine ? "text-bg" : "text-text-dim"}`}>
        {relTime(message.created_at)}
      </p>
    </div>
  );
}

interface StreamBubbleState {
  callId: string;
  text: string;
  phase?: string;
}

function useConversationTransport(conversationID: string) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [bubble, setBubble] = useState<StreamBubbleState | null>(null);
  const [connected, setConnected] = useState(false);
  const highestSeenRef = useRef(0);
  // Tombstones: calls whose durable row already landed. A late stream
  // frame for a settled call must never resurrect a bubble — the exact
  // race the dashboard's chatConnections guards against.
  const settledCallsRef = useRef<Set<string>>(new Set());
  const recentAgentTextsRef = useRef<string[]>([]);
  const pendingFrameRef = useRef<StreamFrame | null>(null);
  const rafRef = useRef(0);

  const mergeMessages = useCallback((incoming: Message[]) => {
    if (incoming.length === 0) return;
    setMessages((current) => {
      const byId = new Map<number, Message>(current.map((m) => [m.id, m]));
      for (const m of incoming) {
        byId.set(m.id, m);
        if (m.id > highestSeenRef.current) highestSeenRef.current = m.id;
        if (m.role === "agent" && !m.component_kind) {
          recentAgentTextsRef.current = [m.content, ...recentAgentTextsRef.current].slice(0, 5);
          // Any durable agent row settles the pending bubble.
          setBubble(null);
        }
      }
      return [...byId.values()].sort((a, b) => a.id - b.id);
    });
  }, []);

  const applyFrame = useCallback((frame: StreamFrame) => {
    if (frame.done) {
      settledCallsRef.current.add(frame.call_id);
      setBubble((current) => (current && current.callId === frame.call_id ? null : current));
      return;
    }
    if (settledCallsRef.current.has(frame.call_id)) return;
    // A frame whose full text already matches a recently landed agent
    // message is the race losing: the row beat the frame. Skip it.
    if (frame.text && recentAgentTextsRef.current.includes(frame.text)) return;
    setBubble({ callId: frame.call_id, text: frame.text, phase: frame.phase });
  }, []);

  // SSE: default events = durable rows, named `stream` events =
  // ephemeral frames (RAF-coalesced so token bursts don't thrash React).
  useEffect(() => {
    if (!conversationID) return;
    highestSeenRef.current = 0;
    settledCallsRef.current = new Set();
    setMessages([]);
    setBubble(null);

    let cancelled = false;
    let es: EventSource | null = null;
    let reconnectTimer = 0;

    const connect = () => {
      if (cancelled) return;
      es = new EventSource(
        `${API}/stream?chat_id=${encodeURIComponent(conversationID)}&since=${highestSeenRef.current}`,
        { withCredentials: true },
      );
      es.onopen = () => setConnected(true);
      es.onmessage = (e) => {
        try {
          const m = JSON.parse(e.data) as Message;
          if (m.id) mergeMessages([m]);
        } catch {
          /* ignore malformed */
        }
      };
      es.addEventListener("stream", (e) => {
        try {
          pendingFrameRef.current = JSON.parse((e as MessageEvent).data) as StreamFrame;
        } catch {
          return;
        }
        if (!rafRef.current) {
          rafRef.current = requestAnimationFrame(() => {
            rafRef.current = 0;
            const frame = pendingFrameRef.current;
            pendingFrameRef.current = null;
            if (frame && !cancelled) applyFrame(frame);
          });
        }
      });
      es.onerror = () => {
        setConnected(false);
        if (es && es.readyState === EventSource.CLOSED) {
          window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();

    // History + the 5s reconcile poll — SSE is the low-latency path,
    // never the only one.
    const load = async () => {
      try {
        const rows = await apiGet<Message[]>(
          `/messages?chat_id=${encodeURIComponent(conversationID)}&since=${highestSeenRef.current}&limit=200`,
        );
        if (!cancelled) mergeMessages(rows);
      } catch {
        /* transient */
      }
    };
    load();
    const poll = window.setInterval(load, 5000);

    return () => {
      cancelled = true;
      window.clearTimeout(reconnectTimer);
      window.clearInterval(poll);
      if (rafRef.current) cancelAnimationFrame(rafRef.current);
      if (es) es.close();
    };
  }, [conversationID, mergeMessages, applyFrame]);

  return { messages, bubble, connected, mergeMessages };
}

function ChatColumn({
  conversation,
  archived,
  agents,
  onEnsureAgents,
  onActed,
  onChanged,
  onRemoved,
}: {
  conversation: Conversation;
  archived: boolean;
  agents: AgentInfo[] | null;
  onEnsureAgents: () => void;
  onActed: () => void;
  onChanged: () => void;
  onRemoved: () => void;
}) {
  const { messages, bubble, connected, mergeMessages } = useConversationTransport(conversation.id);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState("");
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [archiveBusy, setArchiveBusy] = useState(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    setConfirmDelete(false);
  }, [conversation.id]);

  const setArchived = async (next: boolean) => {
    if (archiveBusy) return;
    setArchiveBusy(true);
    try {
      await apiPatch(`/chats?id=${encodeURIComponent(conversation.id)}`, { archived: next });
      onRemoved();
    } catch {
      /* surfaced by the next list refresh */
    } finally {
      setArchiveBusy(false);
    }
  };

  const deleteConversation = async () => {
    if (archiveBusy) return;
    setArchiveBusy(true);
    try {
      await apiDelete(`/chats?id=${encodeURIComponent(conversation.id)}`);
      onRemoved();
    } catch {
      /* surfaced by the next list refresh */
    } finally {
      setArchiveBusy(false);
    }
  };

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [messages.length, bubble?.text]);

  const send = async () => {
    const content = draft.trim();
    if (!content || sending) return;
    setSending(true);
    setSendError("");
    try {
      // Idempotent: retries and remounts resolve to the same row.
      const row = await apiPost<Message>(`/messages?chat_id=${encodeURIComponent(conversation.id)}`, {
        content,
        client_message_id: newClientMessageId(),
      });
      mergeMessages([row]);
      setDraft("");
      // Reset the auto-grown textarea and restore focus — the same
      // defensive refocus the dashboard composer does after a send.
      const el = inputRef.current;
      if (el) {
        el.style.height = "auto";
        el.focus();
      }
    } catch (err) {
      setSendError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  };

  const onAction = async (messageId: number, actionId: string, note: string) => {
    const result = await apiPost<{ message: Message }>(`/message-action`, {
      message_id: messageId,
      action_id: actionId,
      note,
    });
    mergeMessages([result.message]);
    onActed();
  };

  return (
    <section className="min-h-0 flex-1 flex flex-col">
      <div className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-text truncate">{conversation.title}</h2>
          <p className="text-xs text-text-muted truncate">
            {conversation.lead_agent_name || `agent ${conversation.lead_agent_id}`}
            {conversation.origin !== "web" ? ` · via ${conversation.origin}` : ""}
          </p>
        </div>
        <span
          className={`ml-auto shrink-0 w-2 h-2 rounded-full ${connected ? "bg-success" : "bg-border"}`}
          title={connected ? "Live" : "Reconnecting — the 5s poll keeps history current"}
        />
        {!archived && (
          <button
            type="button"
            onClick={() => {
              onEnsureAgents();
              setDetailsOpen(true);
            }}
            className="shrink-0 inline-flex h-8 w-8 items-center justify-center rounded text-text-muted hover:bg-bg-input hover:text-text"
            aria-label="Conversation details"
            title="Details"
          >
            <Glyph d={GLYPH_MORE} size={16} />
          </button>
        )}
      </div>

      <div className="flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-3">
        {messages.length === 0 && !bubble ? (
          <div className="flex-1 flex flex-col items-center justify-center gap-3 text-text-muted">
            <span className="text-text-dim">
              <Glyph d={GLYPH_CHAT} size={32} />
            </span>
            <p className="text-sm">No messages yet — say something.</p>
          </div>
        ) : (
          <>
            {messages.map((m) => (
              <MessageRow key={m.id} message={m} onAction={onAction} />
            ))}
            {bubble && <StreamingBubble text={bubble.text} phase={bubble.phase} />}
          </>
        )}
        <div ref={bottomRef} />
      </div>

      {archived ? (
        <footer className="shrink-0 border-t border-border p-3 flex items-center gap-2">
          <span className="text-xs text-text-muted">Archived conversation — read only.</span>
          <span className="ml-auto flex items-center gap-2">
            <button
              type="button"
              onClick={() => setArchived(false)}
              disabled={archiveBusy}
              className="inline-flex items-center gap-1.5 rounded border border-border px-2.5 py-1.5 text-xs text-text-muted hover:text-text disabled:opacity-40"
            >
              <Glyph d={GLYPH_RESTORE} size={13} />
              Unarchive
            </button>
            {confirmDelete ? (
              <>
                <button
                  type="button"
                  onClick={deleteConversation}
                  disabled={archiveBusy}
                  className="rounded bg-error px-2.5 py-1.5 text-xs font-semibold text-bg disabled:opacity-40"
                >
                  Confirm delete
                </button>
                <button
                  type="button"
                  onClick={() => setConfirmDelete(false)}
                  disabled={archiveBusy}
                  className="rounded border border-border px-2.5 py-1.5 text-xs text-text-muted hover:text-text"
                >
                  Cancel
                </button>
              </>
            ) : (
              <button
                type="button"
                onClick={() => setConfirmDelete(true)}
                disabled={archiveBusy}
                className="inline-flex items-center gap-1.5 rounded border border-border px-2.5 py-1.5 text-xs text-error hover:bg-bg-input disabled:opacity-40"
              >
                <Glyph d={GLYPH_TRASH} size={13} />
                Delete
              </button>
            )}
          </span>
        </footer>
      ) : (
        // The composer is the dashboard ChatPanel's, verbatim classes
        // included — those classes are already in the dashboard's
        // compiled CSS, which is the stylesheet panels render under.
        // Omitted relative to the dashboard: image attachments and
        // voice (no app backend for either yet).
        <footer className="chat-composer-safe shrink-0 px-2 pt-2 pb-2 sm:px-5">
          {sendError && <p className="mx-1 mb-1 text-xs text-error">{sendError}</p>}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              send();
            }}
            className="flex min-h-[54px] items-center gap-1.5 rounded-lg border border-border bg-bg-card/95 px-2 py-1.5 shadow-lg backdrop-blur-sm transition-colors focus-within:border-accent/60 sm:min-h-[58px] sm:gap-3 sm:px-4 sm:py-2"
          >
            <textarea
              ref={inputRef}
              value={draft}
              onChange={(e) => {
                setDraft(e.target.value);
                const el = e.target as HTMLTextAreaElement;
                el.style.height = "auto";
                el.style.height = Math.min(el.scrollHeight, 144) + "px";
              }}
              onKeyDown={(e) => {
                // Enter sends, Shift+Enter newline — dashboard convention.
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  send();
                }
              }}
              rows={1}
              style={{ lineHeight: "20px", minHeight: "36px" }}
              placeholder={connected ? "Message the agent…" : "Reconnecting — messages still send"}
              className="block min-w-0 flex-1 resize-none bg-transparent py-2 text-base text-text placeholder:text-text-dim focus:outline-none sm:text-sm"
              autoFocus={
                typeof window !== "undefined" &&
                window.matchMedia("(hover: hover) and (pointer: fine)").matches
              }
            />
            <button
              type="submit"
              disabled={sending || !draft.trim()}
              className="touch-target flex h-11 w-11 shrink-0 items-center justify-center rounded-full bg-accent text-bg transition-all disabled:cursor-not-allowed disabled:opacity-20 enabled:hover:bg-accent-hover enabled:active:scale-95 sm:h-9 sm:w-9"
              aria-label="Send"
              title="Send (Enter)"
            >
              <svg
                viewBox="0 0 20 20"
                className="w-4 h-4"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.5"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <path d="M10 17V3" />
                <path d="M5 8l5-5 5 5" />
              </svg>
            </button>
          </form>
        </footer>
      )}

      <DetailsDialog
        open={detailsOpen}
        conversation={conversation}
        agents={agents}
        onClose={() => setDetailsOpen(false)}
        onChanged={onChanged}
        onRemoved={onRemoved}
      />
    </section>
  );
}

// ─── inbox tab ───────────────────────────────────────────────────────

function priorityBadge(item: InboxItem): { label: string; cls: string } {
  const kind = item.message.component_kind;
  const severity = item.message.severity;
  if (kind === "approval") return { label: "approval", cls: "bg-accent text-bg" };
  if (kind === "alert" && severity === "error") return { label: "error", cls: "bg-error text-bg" };
  if (kind === "alert" && severity === "warn") return { label: "warn", cls: "bg-warn text-bg" };
  if (kind === "alert") return { label: "alert", cls: "bg-bg-hover text-text-muted" };
  return { label: "report", cls: "bg-bg-hover text-text-muted" };
}

function InboxTab({
  onOpenConversation,
}: {
  onOpenConversation: (conversationID: string) => void;
}) {
  const [items, setItems] = useState<InboxItem[]>([]);
  const [statuses, setStatuses] = useState<CurrentStatus[]>([]);
  const [note, setNote] = useState("");

  const load = useCallback(async () => {
    try {
      const [inbox, current] = await Promise.all([
        apiGet<InboxItem[]>("/inbox?limit=100"),
        apiGet<CurrentStatus[]>("/current-statuses"),
      ]);
      setItems(inbox);
      setStatuses(current);
      setNote(`${inbox.length} item${inbox.length === 1 ? "" : "s"}`);
    } catch (err) {
      setNote(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    load();
    const interval = window.setInterval(load, 15000);
    return () => window.clearInterval(interval);
  }, [load]);

  const act = async (messageId: number, actionId: string, actionNote: string) => {
    await apiPost(`/message-action`, { message_id: messageId, action_id: actionId, note: actionNote });
    load();
  };

  const dismiss = async (messageId: number) => {
    try {
      await apiPost(`/message-dismiss`, { message_id: messageId });
      load();
    } catch (err) {
      setNote(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <div className="flex-1 min-h-0 flex flex-col">
      {statuses.length > 0 && (
        <div className="shrink-0 border-b border-border px-4 py-2 flex flex-wrap gap-2">
          {statuses.map((s) => (
            <span
              key={s.agent_id}
              className="text-xs px-2 py-1 rounded bg-bg-card border border-border text-text-muted"
              title={`agent ${s.agent_id}`}
            >
              {s.message.content}
            </span>
          ))}
        </div>
      )}
      <div className="flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-3">
        {items.length === 0 ? (
          <div className="flex-1 flex flex-col items-center justify-center gap-3 text-text-muted">
            <span className="text-text-dim">
              <Glyph d={GLYPH_INBOX} size={32} />
            </span>
            <p className="text-sm">Inbox zero.</p>
            <p className="text-xs text-text-dim text-center">
              Approvals, alerts, and reports from agents and apps land here, most urgent first.
            </p>
          </div>
        ) : (
          items.map((item) => {
            const badge = priorityBadge(item);
            return (
              <div key={item.message.id} className="rounded-md border border-border bg-bg p-0.5">
                <div className="flex items-center gap-2 px-2.5 pt-2 text-xs">
                  <span className={`px-1.5 py-0.5 rounded ${badge.cls}`}>{badge.label}</span>
                  <span className="text-text-dim">{relTime(item.message.created_at)}</span>
                  <span className="ml-auto flex items-center gap-1">
                    <button
                      type="button"
                      onClick={() => onOpenConversation(item.message.conversation_id)}
                      className="px-1.5 py-0.5 rounded text-text-muted hover:text-text hover:bg-bg-input"
                      title="Open the conversation"
                    >
                      <Glyph d={GLYPH_CHAT} size={13} />
                    </button>
                    {item.message.component_kind !== "approval" && (
                      <button
                        type="button"
                        onClick={() => dismiss(item.message.id)}
                        className="px-1.5 py-0.5 rounded text-text-muted hover:text-text hover:bg-bg-input"
                        title="Dismiss"
                      >
                        <Glyph d={GLYPH_X} size={13} />
                      </button>
                    )}
                  </span>
                </div>
                <div className="p-2">
                  <MessageCards message={item.message} onAction={act} />
                </div>
              </div>
            );
          })
        )}
      </div>
      <footer className="shrink-0 border-t border-border px-4 py-2 text-xs text-text-muted">
        {note}
      </footer>
    </div>
  );
}

// ─── root panel ──────────────────────────────────────────────────────

export default function ConversationsPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<"chats" | "inbox">("chats");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [unread, setUnread] = useState<Map<string, number>>(new Map());
  const [selectedId, setSelectedId] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [agents, setAgents] = useState<AgentInfo[] | null>(null);
  const [agentsError, setAgentsError] = useState("");

  const loadConversations = useCallback(async () => {
    try {
      const [chats, unreadEntries] = await Promise.all([
        apiGet<Conversation[]>(`/chats${showArchived ? "?archived=1" : ""}`),
        apiGet<UnreadEntry[]>("/unread-summary"),
      ]);
      const scoped = projectId
        ? chats.filter((c) => !c.project_id || c.project_id === projectId)
        : chats;
      setConversations(scoped);
      setUnread(new Map(unreadEntries.map((e) => [e.conversation_id, e.unread])));
      setSelectedId((current) =>
        current && scoped.some((c) => c.id === current) ? current : (scoped[0]?.id ?? ""),
      );
    } catch {
      /* transient */
    }
  }, [projectId, showArchived]);

  useEffect(() => {
    loadConversations();
    const interval = window.setInterval(loadConversations, 8000);
    return () => window.clearInterval(interval);
  }, [loadConversations]);

  // Agent directory, fetched lazily the first time a dialog needs it.
  const ensureAgents = useCallback(() => {
    if (agents !== null) return;
    apiGet<AgentInfo[]>(`/agents${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ""}`).then(
      (list) => {
        setAgents(list);
        setAgentsError("");
      },
      (err) => setAgentsError(err instanceof Error ? err.message : String(err)),
    );
  }, [agents, projectId]);

  // Opening a conversation marks it seen with the latest known id.
  useEffect(() => {
    if (!selectedId) return;
    const entryUnread = unread.get(selectedId) ?? 0;
    if (entryUnread === 0) return;
    apiPost(`/seen`, { chat_id: selectedId, last_seen_id: Number.MAX_SAFE_INTEGER >> 1 }).then(
      () => loadConversations(),
      () => {},
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedId]);

  const selected = useMemo(
    () => conversations.find((c) => c.id === selectedId) ?? null,
    [conversations, selectedId],
  );

  const openConversation = (conversationID: string) => {
    setTab("chats");
    setSelectedId(conversationID);
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">Conversations</h1>
          <p className="text-xs text-text-muted">Chat with agents; approvals and reports in one inbox.</p>
        </div>
        <nav className="ml-auto flex items-center gap-1">
          {(
            [
              { id: "chats", label: "Chats", glyph: GLYPH_CHAT },
              { id: "inbox", label: "Inbox", glyph: GLYPH_INBOX },
            ] as const
          ).map((entry) => (
            <button
              key={entry.id}
              type="button"
              onClick={() => setTab(entry.id)}
              className={`px-3 py-1.5 rounded text-xs inline-flex items-center gap-1.5 ${
                tab === entry.id
                  ? "bg-bg-input text-text"
                  : "text-text-muted hover:text-text hover:bg-bg-input"
              }`}
            >
              <Glyph d={entry.glyph} size={14} />
              {entry.label}
            </button>
          ))}
        </nav>
      </header>

      {tab === "inbox" ? (
        <InboxTab onOpenConversation={openConversation} />
      ) : (
        <main className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[320px_minmax(0,1fr)]">
          <aside className="border-r border-border min-h-0 flex flex-col">
            <div className="shrink-0 border-b border-border p-3 flex items-center gap-2">
              <button
                type="button"
                onClick={() => {
                  ensureAgents();
                  setNewOpen(true);
                }}
                className="inline-flex items-center gap-1.5 rounded bg-accent px-2.5 py-1.5 text-xs font-semibold text-bg"
              >
                <Glyph d={GLYPH_PLUS} size={13} />
                New conversation
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowArchived((v) => !v);
                  setSelectedId("");
                }}
                className={`ml-auto inline-flex h-8 w-8 items-center justify-center rounded ${
                  showArchived
                    ? "bg-bg-input text-text"
                    : "text-text-muted hover:bg-bg-input hover:text-text"
                }`}
                aria-label="Archived conversations"
                title={showArchived ? "Back to active conversations" : "Archived conversations"}
              >
                <Glyph d={GLYPH_ARCHIVE} size={15} />
              </button>
            </div>
            {showArchived && (
              <div className="shrink-0 border-b border-border px-3 py-1.5 text-xs text-text-muted">
                Archived
              </div>
            )}
            <div className="flex-1 min-h-0 overflow-auto">
            {conversations.length === 0 ? (
              <div className="p-6 text-sm text-text-muted flex flex-col items-center gap-3 text-center">
                <span className="text-text-dim">
                  <Glyph d={showArchived ? GLYPH_ARCHIVE : GLYPH_CHAT} size={28} />
                </span>
                <p>{showArchived ? "No archived conversations." : "No conversations yet."}</p>
                {!showArchived && (
                  <p className="text-xs text-text-dim">
                    Start one with New conversation, or wait for an external channel to come in.
                  </p>
                )}
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {conversations.map((c) => {
                  const unreadCount = unread.get(c.id) ?? 0;
                  return (
                    <li key={c.id}>
                      <button
                        type="button"
                        onClick={() => setSelectedId(c.id)}
                        className={`w-full text-left px-4 py-3 hover:bg-bg-input ${c.id === selectedId ? "bg-bg-input" : ""}`}
                      >
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium text-text truncate">{c.title}</span>
                          {unreadCount > 0 && c.id !== selectedId && (
                            <span className="ml-auto shrink-0 text-xs px-1.5 py-0.5 rounded-full bg-accent text-bg">
                              {unreadCount}
                            </span>
                          )}
                        </div>
                        <div className="mt-1 flex items-center gap-2 text-xs text-text-dim">
                          <span className="truncate">
                            {c.lead_agent_name || `agent ${c.lead_agent_id}`}
                          </span>
                          {c.origin !== "web" && (
                            <span className="px-1.5 py-0.5 rounded bg-bg border border-border">
                              {c.origin}
                            </span>
                          )}
                          {c.kind === "room" && (
                            <span className="px-1.5 py-0.5 rounded bg-bg border border-border">room</span>
                          )}
                          <span className="ml-auto shrink-0">{relTime(c.updated_at)}</span>
                        </div>
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
            </div>
          </aside>
          {selected ? (
            <ChatColumn
              conversation={selected}
              archived={showArchived}
              agents={agents}
              onEnsureAgents={ensureAgents}
              onActed={loadConversations}
              onChanged={loadConversations}
              onRemoved={() => {
                setSelectedId("");
                loadConversations();
              }}
            />
          ) : (
            <section className="flex-1 flex flex-col items-center justify-center gap-3 text-text-muted">
              <span className="text-text-dim">
                <Glyph d={GLYPH_CHECK} size={32} />
              </span>
              <p className="text-sm">Select a conversation.</p>
            </section>
          )}
        </main>
      )}

      <NewConversationDialog
        open={newOpen}
        projectId={projectId}
        agents={agents}
        agentsError={agentsError}
        onClose={() => setNewOpen(false)}
        onCreated={(conversation) => {
          setNewOpen(false);
          setSelectedId(conversation.id);
          // Leaving archived view retriggers the load via the effect;
          // calling loadConversations here too would race the stale
          // archived query against the fresh one.
          if (showArchived) setShowArchived(false);
          else loadConversations();
        }}
      />
    </div>
  );
}
