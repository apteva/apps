import { AttachmentContent, GenericComponents, reportSectionsText } from "./messageContent";
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

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import createDOMPurify from "dompurify";
import { marked } from "marked";
import ConversationChatView from "./ConversationChatView";
import { isSoftBreakMetadata, softBreakMessageInput } from "./softBreak";

const API = "/api/apps/conversations";

// ─── markdown (the dashboard's safeMarkdown pipeline, bundled) ───────
//
// Agent messages are model-authored and may quote untrusted sources;
// marked preserves raw HTML, so DOMPurify is the security boundary —
// same options as the dashboard's renderSafeMarkdown.
marked.setOptions({ breaks: true, gfm: true });

const SANITIZE_OPTIONS = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ["base", "form", "iframe", "object", "embed", "style", "template"],
  FORBID_ATTR: ["style", "srcdoc"],
};

let purifier: ReturnType<typeof createDOMPurify> | null = null;

function renderSafeMarkdown(source: string): string {
  const parsed = marked.parse(source, { async: false }) as string;
  if (!purifier) purifier = createDOMPurify(window);
  return purifier.isSupported ? purifier.sanitize(parsed, SANITIZE_OPTIONS) : "";
}

// closeOpenMarkdown closes unterminated bold/inline-code/fence/link
// tokens so a partial mid-stream render doesn't flicker while the
// closing pair is still arriving (dashboard ChatPanel's guard).
function closeOpenMarkdown(s: string): string {
  const fence = (s.match(/```/g) || []).length;
  let out = s;
  if (fence % 2 === 1) return out + "\n```";
  const noFences = out.replace(/```[\s\S]*?```/g, "");
  const ticks = (noFences.match(/`/g) || []).length;
  if (ticks % 2 === 1) out += "`";
  const bold = (out.match(/\*\*/g) || []).length;
  if (bold % 2 === 1) out += "**";
  if (/\[[^\]]*\]\([^)]*$/.test(out)) out += ")";
  return out;
}

// ─── types (mirror the app's wire shapes) ────────────────────────────

export interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

export interface Conversation {
  id: string;
  project_id: string;
  lead_agent_id: number;
  lead_agent_name?: string;
  title: string;
  kind: "direct" | "room";
  origin: string;
  audience?: string;
  directive?: string;
  created_at: string;
  updated_at: string;
  archived_at?: string | null;
}

interface CardComponent {
  app: string;
  name: string;
  props: Record<string, unknown>;
}

export interface Message {
 revision?: number;
  id: number;
  conversation_id: string;
  role: "user" | "agent" | "system";
  content: string;
  agent_id?: number;
  component_kind?: string;
  severity?: string;
  components: CardComponent[];
  attachments?: Array<{type:string;data_url?:string;name?:string}>;
  client_message_id?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

interface StreamFrame {
 after_message_id?: number;
 run_id?: string;
  chat_id: string;
  agent_id?: number;
  thread_id?: string;
  call_id: string;
  text: string;
  phase?: string;
  done: boolean;
}

interface InboxPage {items:InboxItem[];total:number;next_cursor:string;attention:Record<string,number>}

interface InboxItem {
  message: Message;
  priority: number;
}

interface UnreadEntry {
  conversation_id: string;
  latest_id: number;
  unread: number;
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

interface TelegramConnectionView {
  connection_id: number;
  name: string;
  status: string;
  project_id: string;
  enabled: boolean;
  bot_id?: string;
  bot_username?: string;
  webhook_url?: string;
  webhook_status?: "healthy" | "drifted" | "error";
  pending_update_count?: number;
  last_webhook_error?: string;
  original_bot_name?: string;
  auto_name_enabled: boolean;
  synced_agent_id?: number;
  synced_bot_name?: string;
  name_sync_error?: string;
  routed_agent_count: number;
  response_feedback: "live" | "typing" | "off";
}

interface TelegramBindingView {
  id: string;
  connection_id: number;
  project_id: string;
  conversation_id: string;
  conversation_title?: string;
  audience?: string;
  chat_id: string;
  chat_type?: string;
  chat_title?: string;
  chat_username?: string;
  require_mention: boolean;
  access_mode: string;
  allowed_user_ids: number[];
  created_at: string;
}

interface TelegramIntakePolicy {
  transport: string;
  connection_id: number;
  project_id: string;
  mode: "pairing" | "public" | "closed";
  default_agent_id: number;
  default_title: string;
  require_group_mention: boolean;
}

interface TelegramAccessRequest {
  id: string;
  connection_id: number;
  external_chat_id: string;
  external_user_id: string;
  chat_type: string;
  display_name: string;
  username?: string;
  chat_title?: string;
  pairing_code: string;
  expires_at: string;
}

// ─── fetch helpers ───────────────────────────────────────────────────

function scopedPath(path: string, projectId: string): string {
  if (!projectId) throw new Error("project context required");
  return `${path}${path.includes("?") ? "&" : "?"}project_id=${encodeURIComponent(projectId)}`;
}

export function agentScopedPath(path: string, instanceId?: number): string {
  if (instanceId === undefined) return path;
  return `${path}${path.includes("?") ? "&" : "?"}agent_id=${encodeURIComponent(instanceId)}`;
}

export async function apiGet<T>(path: string, projectId: string): Promise<T> {
  const res = await fetch(`${API}${scopedPath(path, projectId)}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

async function apiSend<T>(method: string, path: string, body: unknown, projectId: string): Promise<T> {
  const res = await fetch(`${API}${scopedPath(path, projectId)}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export const apiPost = <T,>(path: string, body: unknown, projectId: string) => apiSend<T>("POST", path, body, projectId);
const apiPatch = <T,>(path: string, body: unknown, projectId: string) => apiSend<T>("PATCH", path, body, projectId);
const apiDelete = <T,>(path: string, projectId: string) => apiSend<T>("DELETE", path, undefined, projectId);

// useMediaQuery mirrors the dashboard hook of the same name — the
// context column gates on JS matchMedia (1024px, the lg breakpoint)
// because the compiled stylesheet only carries the responsive classes
// the dashboard itself uses.
function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(
    () => typeof window !== "undefined" && window.matchMedia(query).matches,
  );
  useEffect(() => {
    const mql = window.matchMedia(query);
    const onChange = () => setMatches(mql.matches);
    mql.addEventListener("change", onChange);
    setMatches(mql.matches);
    return () => mql.removeEventListener("change", onChange);
  }, [query]);
  return matches;
}

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

// PublicTag marks conversations whose human side is a product's end
// users (site chatbot visitors behind a gateway) rather than
// operators. Inbox items are structurally refused there.
function PublicTag() {
  return (
    <span className="px-1.5 py-0.5 rounded text-xs bg-accent/15 border border-accent/30 text-accent shrink-0">
      public
    </span>
  );
}

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
      <div className="chat-md" dangerouslySetInnerHTML={{__html:renderSafeMarkdown(reportSectionsText(card.props.sections))}} />
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

// ─── streaming bubble + thinking (dashboard ChatPanel visuals) ───────

// StreamingBubble renders in-progress token text exactly like the
// dashboard: a full-width markdown message growing in place, with
// half-open markdown tokens closed so the render doesn't flicker.
function StreamingBubble({ text }: { text: string }) {
  const html = useMemo(() => renderSafeMarkdown(closeOpenMarkdown(text)), [text]);
  return (
    <div className="flex min-h-[42px] min-w-0 flex-col justify-center shrink-0">
      <div
        className="chat-md text-text text-[15px] sm:text-sm break-words leading-relaxed"
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </div>
  );
}

// ThinkingMessagePlaceholder is the dashboard's thinking row — the
// animated chat-thinking-dots (class from the dashboard stylesheet)
// with the label, occupying transcript space like a message.
function ThinkingMessagePlaceholder() {
  return (
    <div
      className="grid min-h-[42px] min-w-0 shrink-0 grid-cols-[1.9rem_minmax(0,1fr)_auto] items-center gap-2 px-1 py-0.5"
      role="status"
      aria-live="polite"
      aria-label="Thinking"
    >
      <span
        className="chat-thinking-dots inline-flex h-7 w-7 shrink-0 items-center justify-center gap-1"
        aria-hidden="true"
      >
        <span />
        <span />
        <span />
      </span>
      <span className="text-[13px] leading-5 text-text-muted">Thinking…</span>
      <span className="h-4 w-4 shrink-0" aria-hidden="true" />
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
  const [audience, setAudience] = useState<"operator" | "public">("operator");
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
    setAudience("operator");
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
        audience,
        project_id: projectId,
      }, projectId);
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
        <label className="block">
          <span className="mb-1 block text-xs uppercase text-text-muted">Audience</span>
          <select
            value={audience}
            onChange={(e) => setAudience(e.target.value as "operator" | "public")}
            className="w-full rounded border border-border bg-bg-input px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
          >
            <option value="operator">Operator — approvals and internal inbox items allowed</option>
            <option value="public">Public visitor — replies only, no internal inbox items</option>
          </select>
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
  const [directive, setDirective] = useState(conversation.directive || "");
  const [participants, setParticipants] = useState<ParticipantsInfo | null>(null);
  const [addAgentId, setAddAgentId] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setTitle(conversation.title);
    setDirective(conversation.directive || "");
    setAddAgentId("");
    setConfirmDelete(false);
    setError("");
    apiGet<ParticipantsInfo>(`/participants?id=${encodeURIComponent(conversation.id)}`, conversation.project_id).then(
      setParticipants,
      (err) => setError(err instanceof Error ? err.message : String(err)),
    );
  }, [open, conversation.id, conversation.title, conversation.directive]);

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
      await apiPatch(`/chats?id=${encodeURIComponent(conversation.id)}`, { title: next }, conversation.project_id);
      onChanged();
    });
  };

  const saveDirective = () => {
    const next = directive.trim();
    if (next === (conversation.directive || "")) return;
    void run(async () => {
      await apiPatch(`/chats?id=${encodeURIComponent(conversation.id)}`, { directive: next }, conversation.project_id);
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
        conversation.project_id,
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
        conversation.project_id,
      );
      setParticipants(updated);
      onChanged();
    });
  };

  const archive = () => {
    void run(async () => {
      await apiPatch(`/chats?id=${encodeURIComponent(conversation.id)}`, { archived: true }, conversation.project_id);
      onClose();
      onRemoved();
    });
  };

  const remove = () => {
    void run(async () => {
      await apiDelete(`/chats?id=${encodeURIComponent(conversation.id)}`, conversation.project_id);
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
          <div className="mb-1 text-xs uppercase text-text-muted">Conversation instructions</div>
          <textarea
            value={directive}
            onChange={(e) => setDirective(e.target.value)}
            maxLength={8000}
            rows={4}
            placeholder="Optional context or behavior specific to this conversation"
            className="w-full resize-y rounded border border-border bg-bg-input px-2 py-1.5 text-sm text-text focus:border-accent focus:outline-none"
          />
          <div className="mt-1 flex items-center justify-between gap-2">
            <span className="text-[11px] text-text-dim">Platform policy and tool permissions cannot be overridden here.</span>
            <button
              type="button"
              onClick={saveDirective}
              disabled={saving || directive.trim() === (conversation.directive || "")}
              className="rounded border border-border px-2.5 py-1 text-xs text-text-muted hover:text-text disabled:opacity-40"
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

// ─── context column ──────────────────────────────────────────────────

// ContextColumn is the panel's counterpart to the dashboard chat
// page's right-hand ConversationContextPanel — generic for now: the
// conversation's facts and its participant roster, with Manage opening
// the shared details dialog. App contributions / tool activity are the
// dashboard's later, richer fill.
function ContextColumn({
  conversation,
  agents,
  onEnsureAgents,
  onManage,
  refreshHold,
}: {
  conversation: Conversation;
  agents: AgentInfo[] | null;
  onEnsureAgents: () => void;
  onManage: () => void;
  // While the details dialog is open we hold refreshes; when it closes
  // the roster refetches so its edits show up immediately.
  refreshHold: boolean;
}) {
  const [participants, setParticipants] = useState<ParticipantsInfo | null>(null);

  useEffect(() => {
    onEnsureAgents();
  }, [onEnsureAgents]);

  useEffect(() => {
    if (refreshHold) return;
    let cancelled = false;
    apiGet<ParticipantsInfo>(`/participants?id=${encodeURIComponent(conversation.id)}`, conversation.project_id).then(
      (info) => {
        if (!cancelled) setParticipants(info);
      },
      () => {},
    );
    return () => {
      cancelled = true;
    };
  }, [conversation.id, refreshHold]);

  const agentName = (id: number) => agents?.find((a) => a.id === id)?.name || `agent ${id}`;
  const facts: Array<[string, string]> = [
    ["Audience", conversation.audience === "public" ? "public (visitors)" : "operator"],
    ["Origin", conversation.origin],
    ["Type", conversation.kind],
    ["Created", relTime(conversation.created_at) || "—"],
    ["Updated", relTime(conversation.updated_at) || "—"],
  ];

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg">
      <div className="flex h-10 shrink-0 items-center border-b border-border px-4">
        <span className="text-xs font-semibold uppercase text-text-muted">Details</span>
      </div>
      <div className="flex-1 min-h-0 overflow-auto p-4 space-y-5">
        <div>
          <div className="mb-2 text-xs uppercase text-text-dim">Conversation</div>
          <div className="space-y-1.5">
            {facts.map(([label, value]) => (
              <div key={label} className="flex items-center justify-between gap-2 text-xs">
                <span className="text-text-muted">{label}</span>
                <span className="text-text truncate">{value}</span>
              </div>
            ))}
          </div>
        </div>

        <div>
          <div className="mb-2 text-xs uppercase text-text-dim">Participants</div>
          {participants === null ? (
            <p className="text-xs text-text-dim">Loading…</p>
          ) : (
            <div className="space-y-1.5">
              {participants.agent_ids.map((id) => (
                <div key={id} className="flex items-center gap-2 text-xs">
                  <span className="min-w-0 flex-1 truncate text-text-muted">{agentName(id)}</span>
                  {id === participants.lead_agent_id && (
                    <span className="uppercase text-accent">lead</span>
                  )}
                </div>
              ))}
            </div>
          )}
          <button
            type="button"
            onClick={onManage}
            className="mt-3 w-full rounded border border-border px-2 py-1.5 text-xs text-text-muted hover:text-text hover:bg-bg-input"
          >
            Manage conversation
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── transcript ──────────────────────────────────────────────────────

// MessageRow uses the dashboard ChatPanel's role layouts:
//   user   — right-aligned accent-tinted bubble, plain text
//   agent  — full-width markdown (chat-md, the dashboard's own styles)
//   system — centered status line
function MessageRow(props: {message:Message;onAction:(id:number,action:string,note:string)=>Promise<void>}) {
 return <div className="min-w-0 shrink-0 flex flex-col gap-2">
 {props.message.agent_id ? <p className="text-xs text-text-muted">Agent {props.message.agent_id}</p> : null}
 <MessageBody {...props}/><AttachmentContent attachments={props.message.attachments}/><GenericComponents components={props.message.components}/>
 </div>;
}
function MessageBody({
  message,
  onAction,
}: {
  message: Message;
  onAction: (messageId: number, actionId: string, note: string) => Promise<void>;
}) {
  const isMarkdown = message.role !== "user" && message.role !== "system" && !message.component_kind;
  const html = useMemo(
    () => (isMarkdown ? renderSafeMarkdown(message.content) : ""),
    [isMarkdown, message.content],
  );

  if (message.component_kind) {
    return (
      <div className="w-full min-w-0 shrink-0">
        <MessageCards message={message} onAction={onAction} />
      </div>
    );
  }
  if (message.role === "system") {
    return (
      <div className="shrink-0 text-center text-[10px] text-text-muted py-1 break-words">
        {message.content}
      </div>
    );
  }
  if (message.role === "user" && isSoftBreakMetadata(message.metadata)) {
    return (
      <div
        className="flex shrink-0 items-center gap-2 py-1 text-xs text-text-muted"
        role="status"
        title={`${message.content} · ${relTime(message.created_at)}`}
      >
        <span className="h-px flex-1 bg-border" />
        <span>Break requested</span>
        <span className="h-px flex-1 bg-border" />
      </div>
    );
  }
  if (message.role === "user") {
    return (
      <div className="flex justify-end min-w-0 shrink-0">
        <div
          className="bg-accent/15 border border-accent/30 rounded-xl rounded-br-sm px-3 py-2 max-w-[92%] sm:max-w-[80%] min-w-0"
          title={relTime(message.created_at)}
        >
          <p className="text-text text-[15px] sm:text-sm leading-relaxed whitespace-pre-wrap break-words">
            {message.content}
          </p>
        </div>
      </div>
    );
  }
  return (
    <div className="flex min-h-[42px] min-w-0 flex-col justify-center shrink-0" title={relTime(message.created_at)}>
      <div
        className="chat-md text-text text-[15px] sm:text-sm break-words leading-relaxed"
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </div>
  );
}

interface StreamBubbleState {
 afterMessageId?: number;
 runId?: string;
  callId: string;
  agentId?: number;
  text: string;
  phase?: string;
}

// Stream-frame lifecycle constants and helpers — a faithful port of
// the dashboard's chatConnections manager (state/chatConnections.ts):
// same TTLs, same normalization, same suppression rules.
const SETTLED_STREAM_TTL_MS = 30_000;
const STREAM_DONE_GRACE_MS = 1500;
const SETTLED_STREAM_MAX = 20;

function normalizeStreamText(value: string): string {
  return value.replace(/\r\n/g, "\n").trim();
}

interface ChangePage { messages: Message[]; cursor: number; has_more: boolean; before: number }
function useConversationTransport(conversationID: string, projectId: string) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [bubbles, setBubbles] = useState<StreamBubbleState[]>([]);
  const [connected, setConnected] = useState(false);
  const [historyError, setHistoryError] = useState("");
  const [hasOlder, setHasOlder] = useState(false);
  const beforeRef = useRef(0);
  const generationRef = useRef(0);
  const loadingOlderRef = useRef(false);
  const lastUserIdRef=useRef(0);
  const streamRef = useRef(new Map<string, StreamBubbleState & { updatedAt: number }>());
  const settledRef = useRef(new Map<string, number>());
  const publishBubbles = useCallback(() => setBubbles([...streamRef.current.values()]), []);
  const mergeMessages = useCallback((incoming: Message[]) => {
    const valid = incoming.filter(m => m.conversation_id === conversationID);
    if (!valid.length) return;
    for(const m of valid)if(m.role==="user")lastUserIdRef.current=Math.max(lastUserIdRef.current,m.id);
    for (const m of valid) {
      if (m.role !== "agent" || m.component_kind) continue;
      for (const [key, value] of streamRef.current) {
        if (value.agentId !== m.agent_id || m.id <= (value.afterMessageId ?? 0)) continue;
        if (value.phase === "acknowledgement" || (value.text && normalizeStreamText(m.content) === normalizeStreamText(value.text))) {
          settledRef.current.set(key, Date.now()); streamRef.current.delete(key);
        }
      }
    }
    publishBubbles();
    setMessages(current => {
      const byId = new Map(current.filter(m => m.conversation_id === conversationID).map(m => [m.id, m]));
      for (const m of valid) if ((m.revision ?? 0) >= (byId.get(m.id)?.revision ?? 0)) byId.set(m.id, m);
      return [...byId.values()].sort((a,b) => a.id-b.id);
    });
  }, [conversationID, publishBubbles]);
  const loadOlder = useCallback(async () => {
    if (loadingOlderRef.current || !beforeRef.current) return;
    loadingOlderRef.current = true;
    const generation = generationRef.current;
    try {
      const page = await apiGet<ChangePage>(`/messages?chat_id=${encodeURIComponent(conversationID)}&page=1&before=${beforeRef.current}&limit=100`, projectId);
      if (generation !== generationRef.current) return;
      mergeMessages(page.messages); beforeRef.current = page.before; setHasOlder(page.has_more); setHistoryError("");
    } catch (err) { if (generation === generationRef.current) setHistoryError(String(err)); }
    finally { loadingOlderRef.current = false; }
  }, [conversationID, projectId, mergeMessages]);
  useEffect(() => {
    generationRef.current++;
    let cancelled = false, loading = false, initialized = false, cursor = 0;
    setMessages([]); setBubbles([]); setConnected(false); setHistoryError(""); setHasOlder(false);
    streamRef.current.clear(); settledRef.current.clear(); beforeRef.current = 0;lastUserIdRef.current=0;
    const applyFrame = (frame: StreamFrame) => {
      if (cancelled || frame.chat_id !== conversationID) return;
      const key = `${frame.agent_id ?? 0}:${frame.thread_id ?? ""}:${frame.call_id}:${frame.run_id ?? ""}`;
      if (frame.done) {
        for (const [k,v] of streamRef.current) if ((frame.run_id ? k === key : v.callId === frame.call_id) && (!frame.agent_id || v.agentId === frame.agent_id)) {
          streamRef.current.delete(k); settledRef.current.set(k, Date.now());
        }
      } else if (!settledRef.current.has(key)) {
        const afterMessageId=frame.after_message_id ?? Math.max(lastUserIdRef.current,...[...streamRef.current.values()].filter(v=>v.agentId===frame.agent_id).map(v=>v.afterMessageId ?? 0));
        if (frame.phase !== "acknowledgement") for (const [k,v] of streamRef.current) {
          if (v.agentId === frame.agent_id && v.phase === "acknowledgement") streamRef.current.delete(k);
        }
        streamRef.current.set(key, {afterMessageId,runId:frame.run_id,callId: frame.call_id, agentId: frame.agent_id, text:frame.text, phase:frame.phase, updatedAt:Date.now()});
      }
      publishBubbles();
    };
    const load = async () => {
      if (loading || cancelled) return; loading = true;
      try {
        if (!initialized) {
          const page = await apiGet<ChangePage>(`/messages?chat_id=${encodeURIComponent(conversationID)}&page=1&limit=100`, projectId);
          if (cancelled) return;
          mergeMessages(page.messages); cursor=page.cursor; beforeRef.current=page.before; setHasOlder(page.has_more); initialized=true;
        }
        // Only complete durable pages advance the replay cursor, never SSE.
        for (let n=0;n<10 && !cancelled;n++) {
          const page = await apiGet<ChangePage>(`/changes?chat_id=${encodeURIComponent(conversationID)}&cursor=${cursor}`, projectId);
          if (cancelled) return;
          mergeMessages(page.messages); cursor=page.cursor;
          if (!page.has_more) break;
        }
        setHistoryError("");
      } catch (err) { if (!cancelled) setHistoryError(`History refresh failed: ${String(err)}`); }
      finally { loading=false; }
    };
    const es = new EventSource(`${API}${scopedPath(`/stream?chat_id=${encodeURIComponent(conversationID)}`, projectId)}`, {withCredentials:true});
    es.onopen = () => { if (!cancelled) {setConnected(true); void load();} };
    es.onmessage = e => { try { if (!cancelled) mergeMessages([JSON.parse(e.data)]); } catch {} };
    es.addEventListener("stream", e => { try {applyFrame(JSON.parse((e as MessageEvent).data));} catch {} });
    es.addEventListener("resync", () => { void load(); });
    es.onerror = () => { if (!cancelled) { setConnected(false); streamRef.current.clear(); publishBubbles(); } };
    void load();
    const poll = window.setInterval(() => {
      void load();
      for (const [k,v] of streamRef.current) if (Date.now()-v.updatedAt>90_000) streamRef.current.delete(k);
      for (const [k,t] of settledRef.current) if (Date.now()-t>SETTLED_STREAM_TTL_MS) settledRef.current.delete(k);
      publishBubbles();
    }, 5000);
    return () => { cancelled=true; generationRef.current++; window.clearInterval(poll); es.close(); };
  }, [conversationID, projectId, mergeMessages, publishBubbles]);
  return { messages, bubbles, bubble:bubbles[0] ?? null, connected, mergeMessages, hasOlder, loadOlder, historyError };
}

// Refresh every loaded page so deleted/archived rows cannot linger behind page one.
export async function refreshConversationList(path:string, projectId:string, count=100, selectedId="") {
 const out:Conversation[]=[];let cursor="";
 do {
  const page=await apiGet<{conversations:Conversation[];next_cursor:string}>(`${path}${path.includes("?")?"&":"?"}page=1&limit=100&cursor=${encodeURIComponent(cursor)}`,projectId);
  for(const row of page.conversations)if(!out.some(old=>old.id===row.id))out.push(row);
  if(!page.next_cursor||page.next_cursor===cursor)break;cursor=page.next_cursor;
 }while(out.length<Math.max(100,count));
 if(selectedId&&!out.some(row=>row.id===selectedId)) {
  try {const row=await apiGet<Conversation>(`/chats?id=${encodeURIComponent(selectedId)}`,projectId);if(Boolean(row.archived_at)===path.includes("archived=1"))out.unshift(row);}catch{}
 }
 return out;
}

interface MessageDelivery {id:number;message_id:number;status:string;target:string;last_error?:string;attempts:number}
export function MoreConversations({path,projectId,rows,onRows}: {path:string;projectId:string;rows:Conversation[];onRows:(rows:Conversation[])=>void}) {
 const [busy,setBusy]=useState(false),[error,setError]=useState("");
 const scopeRef=useRef("");scopeRef.current=projectId+":"+path;
 const more=async()=>{if(busy||!rows.length)return;const scope=scopeRef.current;setBusy(true);try{
  const last=rows.at(-1)!;
  const cursor=btoa(JSON.stringify({updated:last.updated_at,id:last.id})).replace(/=+$/g,"").replace(/\+/g,"-").replace(/\//g,"_");
  const page=await apiGet<{conversations:Conversation[];next_cursor:string}>(`${path}${path.includes("?")?"&":"?"}page=1&cursor=${encodeURIComponent(cursor)}`,projectId);
  if(scope!==scopeRef.current)return;
  if(!page.conversations.length){setError("No earlier conversations");return}
  onRows([...rows,...page.conversations.filter(c=>!rows.some(old=>old.id===c.id))]);setError("");
 }catch(err){setError(String(err));}finally{setBusy(false)}};
 return <div className="p-2 text-center text-xs"><button type="button" disabled={busy||!rows.length} className="text-accent" onClick={more}>{busy?"Loading…":"Load earlier conversations"}</button>{error&&<p role="status">{error}</p>}</div>;
}

export function ConversationChat({
  conversation,
  archived,
  onOpenDetails,
  headerActions,
  onActed,
  onRemoved,
}: {
  conversation: Conversation;
  archived: boolean;
  onOpenDetails?: () => void;
  headerActions?: ReactNode;
  onActed: () => void;
  onRemoved: () => void;
}) {
  const { messages, bubble, bubbles, connected, mergeMessages, hasOlder, loadOlder, historyError } = useConversationTransport(conversation.id, conversation.project_id);
  const storageKey = `conversations:draft:${conversation.project_id}:${conversation.id}`;
  const [draft, setDraft] = useState(() => { try { return sessionStorage.getItem(storageKey) ?? ""; } catch { return ""; } });
  const pendingSendRef = useRef<{ content:string; client_message_id:string } | null>(null);
  const mountedRef = useRef(true);
  useEffect(() => { mountedRef.current=true; return () => {mountedRef.current=false;}; }, []);
  useEffect(() => {try {sessionStorage.setItem(storageKey,draft);} catch {}},[storageKey,draft]);
  const [sending, setSending] = useState(false);
  const [deliveries,setDeliveries]=useState<MessageDelivery[]>([]);
  const refreshDeliveries=useCallback(async()=>{try{setDeliveries(await apiGet<MessageDelivery[]>(`/deliveries?chat_id=${encodeURIComponent(conversation.id)}`,conversation.project_id));}catch{}},[conversation.id,conversation.project_id]);
  useEffect(()=>{void refreshDeliveries();const timer=window.setInterval(refreshDeliveries,5000);return ()=>window.clearInterval(timer);},[refreshDeliveries]);
  const retryDelivery=async(delivery:MessageDelivery)=>{try{await apiPost("/delivery-failures",{id:delivery.id},conversation.project_id);void refreshDeliveries();}catch(err){setSendError(String(err));}};

  const [breakBusy, setBreakBusy] = useState(false);
  const [breakRequested, setBreakRequested] = useState(false);
  const breakRequestRef = useRef<{ callId: string; agentId?: number; clientId: string } | null>(null);
  const [sendError, setSendError] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [archiveBusy, setArchiveBusy] = useState(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    setConfirmDelete(false);
    breakRequestRef.current = null;
    setBreakBusy(false);
    setBreakRequested(false);
  }, [conversation.id]);

  useEffect(() => {
    if (bubble) return;
    breakRequestRef.current = null;
    setBreakBusy(false);
    setBreakRequested(false);
  }, [bubble]);

  const setArchived = async (next: boolean) => {
    if (archiveBusy) return;
    setArchiveBusy(true);
    try {
      await apiPatch(`/chats?id=${encodeURIComponent(conversation.id)}`, { archived: next }, conversation.project_id);
      onRemoved();
    } catch (err) {
      setSendError(String(err));
    } finally {
      setArchiveBusy(false);
    }
  };

  const deleteConversation = async () => {
    if (archiveBusy) return;
    setArchiveBusy(true);
    try {
      await apiDelete(`/chats?id=${encodeURIComponent(conversation.id)}`, conversation.project_id);
      onRemoved();
    } catch (err) {
      setSendError(String(err));
    } finally {
      setArchiveBusy(false);
    }
  };

  const nearBottomRef = useRef(true);
  useEffect(() => {
    const bottom=bottomRef.current, scroller=bottom?.parentElement;
    if (!bottom || !scroller) return;
    let lastMarked=0;
    const update=() => {
      nearBottomRef.current=scroller.scrollHeight-scroller.scrollTop-scroller.clientHeight<80;
      const latest=messages.at(-1)?.id ?? 0;
      if (!archived && scroller.clientHeight>0 && bottom.getClientRects().length>0 && bottom.getBoundingClientRect().top>=0 && bottom.getBoundingClientRect().bottom<=window.innerHeight && nearBottomRef.current && document.visibilityState === "visible" && bottom.getBoundingClientRect().bottom <= scroller.getBoundingClientRect().bottom+5 && latest>lastMarked) {
        lastMarked=latest;
        void apiPost("/seen",{chat_id:conversation.id,last_seen_id:latest},conversation.project_id).then(onActed,() => {lastMarked=0;});
      }
    };
    if (nearBottomRef.current) bottom.scrollIntoView({block:"end"});
    update(); scroller.addEventListener("scroll",update);document.addEventListener("visibilitychange",update);
    return () => {scroller.removeEventListener("scroll",update);document.removeEventListener("visibilitychange",update);};
  },[messages, conversation.id, conversation.project_id, archived]);

  const send = async () => {
    const content=draft.trim(); if (!content || sending) return;
    let request=pendingSendRef.current;
    if (!request) { try { request=JSON.parse(sessionStorage.getItem(storageKey+":pending") ?? "null"); } catch {} }
    if (!request) request={content,client_message_id:newClientMessageId()};
    pendingSendRef.current=request;
    try {sessionStorage.setItem(storageKey+":pending",JSON.stringify(request));} catch {}
    setSending(true);setSendError("");
    try {
      const row=await apiPost<Message>(`/messages?chat_id=${encodeURIComponent(conversation.id)}`,request,conversation.project_id);
      pendingSendRef.current=null;try {sessionStorage.removeItem(storageKey+":pending"); if ((sessionStorage.getItem(storageKey) ?? "").trim() === request.content) sessionStorage.removeItem(storageKey);} catch {}
      if (!mountedRef.current) return;
      mergeMessages([row]);setDraft(current => current.trim()===request!.content ? "" : current);
      inputRef.current?.focus();
    } catch(err) {if(mountedRef.current)setSendError(`Send was not confirmed. Retry will reuse the same message: ${String(err)}`);}
    finally {if(mountedRef.current)setSending(false);}
  };

  const requestSoftBreak = async () => {
    if (!bubble || breakBusy || breakRequested) return;
    // Keep the complete first request stable across a lost HTTP response.
    // The visible stream may move from the synthetic ack id to a provider
    // call id while the retry is pending; changing the idempotency key or
    // target_call_id at that point could queue a duplicate break.
    const request = breakRequestRef.current ?? {
      callId: bubble.callId,
      agentId: bubble.agentId,
      clientId: newClientMessageId(),
    };
    breakRequestRef.current = request;
    setBreakBusy(true);
    setSendError("");
    try {
      const row = await apiPost<Message>(`/messages?chat_id=${encodeURIComponent(conversation.id)}`,
        softBreakMessageInput(conversation.lead_agent_id, {
          callId: request.callId,
          agentId: request.agentId,
        }, request.clientId),
        conversation.project_id,
      );
      mergeMessages([row]);
      setBreakRequested(true);
    } catch (err) {
      setSendError(err instanceof Error ? err.message : String(err));
    } finally {
      setBreakBusy(false);
    }
  };

  const onAction = async (messageId: number, actionId: string, note: string) => {
    const result = await apiPost<{ message: Message }>(`/message-action`, {
      message_id: messageId,
      action_id: actionId,
      note,
    }, conversation.project_id);
    mergeMessages([result.message]);
    onActed();
  };

  return (
    <ConversationChatView
      title={conversation.title}
      subtitle={`${conversation.lead_agent_name || `agent ${conversation.lead_agent_id}`}${conversation.origin !== "web" ? ` · via ${conversation.origin}` : ""}`}
      publicAudience={conversation.audience === "public"}
      connected={connected}
      archived={archived}
      messageNodes={<>
        {hasOlder && <button type="button" className="text-sm text-accent" onClick={loadOlder}>Load earlier messages</button>}
        {messages.map(message => <div key={message.id}><fieldset disabled={archived} className="min-w-0"><MessageRow message={message} onAction={onAction}/></fieldset>
 {deliveries.filter(d=>d.message_id===message.id).map(d=><div key={d.id} role="status" className="mt-1 text-xs text-text-muted">{d.status === "processing" ? "Sending" : d.status === "pending" ? (d.attempts ? "Retrying" : "Queued") : d.status} · {d.target.split(":")[0]} {d.last_error && <span>{d.last_error}</span>}{["failed","ambiguous"].includes(d.status) && <button type="button" className="ml-2 text-accent" onClick={()=>retryDelivery(d)}>{d.status==="ambiguous"?"Retry (may duplicate)":"Retry delivery"}</button>}</div>)}
 </div>)}
      </>}
      hasMessages={messages.length > 0}
      streamNode={bubbles.length ? <>{bubbles.map(b => <div key={`${b.agentId}:${b.callId}:${b.runId}`}><p className="text-xs text-text-muted">Agent {b.agentId}</p>{b.text ? <StreamingBubble text={b.text} /> : <ThinkingMessagePlaceholder />}</div>)}</> : null}
      headerActions={headerActions}
      bottomRef={bottomRef}
      inputRef={inputRef}
      draft={draft}
      sending={sending}
      responseActive={Boolean(bubble)}
      breakBusy={breakBusy}
      breakRequested={breakRequested}
      sendError={sendError || historyError}
      archiveBusy={archiveBusy}
      confirmDelete={confirmDelete}
      onDraftChange={(value, element) => {
        setDraft(value);
        element.style.height = "auto";
        element.style.height = Math.min(element.scrollHeight, 144) + "px";
      }}
      onComposerKeyDown={(event) => {
        if (event.key === "Enter" && !event.shiftKey) {
          event.preventDefault();
          send();
        }
      }}
      onSend={send}
      onSoftBreak={requestSoftBreak}
      onOpenDetails={onOpenDetails}
      onUnarchive={() => setArchived(false)}
      onRequestDelete={() => setConfirmDelete(true)}
      onCancelDelete={() => setConfirmDelete(false)}
      onDelete={deleteConversation}
    />
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
  projectId,
  instanceId,
}: {
  onOpenConversation: (conversationID: string) => void;
  projectId: string;
  instanceId?: number;
}) {
  const [items, setItems] = useState<InboxItem[]>([]);
  const [note, setNote] = useState("");
 const [cursor,setCursor]=useState("");
 const expandedRef=useRef(false);

  const load = useCallback(async () => {
    try {
      const inbox = await apiGet<InboxPage>(
        agentScopedPath("/inbox?page=1&limit=100", instanceId),
        projectId,
      );
      setItems(inbox.items);setCursor(inbox.next_cursor);expandedRef.current=false;
      setNote(`${inbox.total} item${inbox.total === 1 ? "" : "s"}`);
    } catch (err) {
      setNote(err instanceof Error ? err.message : String(err));
    }
  }, [projectId, instanceId]);

  useEffect(() => {
    load();
    const interval = window.setInterval(()=>{if(!expandedRef.current)void load();}, 15000);
    return () => window.clearInterval(interval);
  }, [load]);

  const loadMore=async()=>{try{const page=await apiGet<InboxPage>(agentScopedPath(`/inbox?page=1&limit=100&cursor=${encodeURIComponent(cursor)}`,instanceId),projectId);setItems(current=>[...current,...page.items.filter(i=>!current.some(old=>old.message.id===i.message.id))]);setCursor(page.next_cursor);expandedRef.current=true;}catch(err){setNote(String(err));}};
  const act = async (messageId: number, actionId: string, actionNote: string) => {
    await apiPost(`/message-action`, { message_id: messageId, action_id: actionId, note: actionNote }, projectId);
    load();
  };

  const dismiss = async (messageId: number) => {
    try {
      await apiPost(`/message-dismiss`, { message_id: messageId }, projectId);
      load();
    } catch (err) {
      setNote(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <div className="flex-1 min-h-0 flex flex-col">
      <div className="flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-3">
 <div className="flex gap-3 text-xs"><button className="text-accent" onClick={load}>Refresh inbox</button>{cursor&&<button className="text-accent" onClick={loadMore}>Load more items</button>}</div>
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

// ─── Telegram transport tab ─────────────────────────────────────────

function TelegramTab({ projectId, conversations }: { projectId: string; conversations: Conversation[] }) {
  const [connections, setConnections] = useState<TelegramConnectionView[]>([]);
  const [bindings, setBindings] = useState<TelegramBindingView[]>([]);
  const [policies, setPolicies] = useState<TelegramIntakePolicy[]>([]);
  const [requests, setRequests] = useState<TelegramAccessRequest[]>([]);
  const [blockedRequests, setBlockedRequests] = useState<TelegramAccessRequest[]>([]);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [connectionId, setConnectionId] = useState("");
  const [mode, setMode] = useState<"pairing" | "public" | "closed">("pairing");
  const dirtyPolicyRef=useRef(false);
  const policyConnectionRef=useRef("");
  const [agentId, setAgentId] = useState("");
  const [defaultTitle, setDefaultTitle] = useState("Telegram conversation");
  const [requireMention, setRequireMention] = useState(true);
  const [inviteConversation, setInviteConversation] = useState("");
  const [inviteKind, setInviteKind] = useState<"private" | "group">("private");
  const [inviteURL, setInviteURL] = useState("");
  const [inviteExpiresAt, setInviteExpiresAt] = useState("");
  const [setupStep, setSetupStep] = useState<1 | 2 | 3>(1);
  const [requestTargets, setRequestTargets] = useState<Record<string, string>>({});
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [manualConversation, setManualConversation] = useState("");
  const [manualChat, setManualChat] = useState("");
  const [manualUsers, setManualUsers] = useState("");
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(async () => {
    try {
      const [connectionData, bindingData, policyData, accessData, agentData] = await Promise.all([
        apiGet<{ connections: TelegramConnectionView[] }>("/telegram-connections", projectId),
        apiGet<{ bindings: TelegramBindingView[] }>("/telegram-bindings", projectId),
        apiGet<{ policies: TelegramIntakePolicy[] }>("/telegram-intake", projectId),
        apiGet<{ requests: TelegramAccessRequest[]; blocked: TelegramAccessRequest[] }>("/telegram-access", projectId),
        apiGet<AgentInfo[]>("/agents", projectId),
      ]);
      setConnections(connectionData.connections ?? []);
      setBindings(bindingData.bindings ?? []);
      setPolicies(policyData.policies ?? []);
      setRequests(accessData.requests ?? []);
      setBlockedRequests(accessData.blocked ?? []);
      setAgents(agentData ?? []);
      setConnectionId((current) => current && connectionData.connections.some((item) => String(item.connection_id) === current)
        ? current
        : String(connectionData.connections.find((item) => item.enabled)?.connection_id ?? connectionData.connections[0]?.connection_id ?? ""));
      setManualConversation((current) => current && conversations.some((item) => item.id === current) ? current : conversations[0]?.id ?? "");
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    }
  }, [projectId]);

  useEffect(() => { load(); }, [load]);

  const connection = connections.find((item) => String(item.connection_id) === connectionId) ?? null;
  const policy = policies.find((item) => String(item.connection_id) === connectionId) ?? null;
  const currentBindings = bindings.filter((item) => String(item.connection_id) === connectionId);
  const currentRequests = requests.filter((item) => String(item.connection_id) === connectionId);
  const currentBlocked = blockedRequests.filter((item) => String(item.connection_id) === connectionId);
  const selectableAgents = agents.filter((item) => item.attached !== false);

  useEffect(() => {
    if(policyConnectionRef.current!==connectionId){policyConnectionRef.current=connectionId;dirtyPolicyRef.current=false;}
    if(dirtyPolicyRef.current)return;
    if (policy) {
      setMode(policy.mode);
      setAgentId(String(policy.default_agent_id || ""));
      setDefaultTitle(policy.default_title || "Telegram conversation");
      setRequireMention(policy.require_group_mention);
    } else {
      setMode("pairing");
      setAgentId(String(selectableAgents[0]?.id ?? agents[0]?.id ?? ""));
      setDefaultTitle("Telegram conversation");
      setRequireMention(true);
    }
  }, [connectionId, Boolean(policy), agents.length === 0]);

  useEffect(() => {
    setInviteURL("");
    setInviteExpiresAt("");
  }, [connectionId, inviteConversation, inviteKind]);

  useEffect(() => {
    setSetupStep(!connection?.enabled ? 1 : !policy ? 2 : 3);
  }, [connectionId, connection?.enabled, policy?.connection_id]);

  const run = async (key: string, action: () => Promise<string | void>, refresh = true) => {
    setBusy(key);
    setNotice("");
    try {
      const result = await action();
      if (result) setNotice(result);
      if (refresh) await load();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  };

  const activate = () => connection && run("activate", async () => {
    const result = await apiPost<TelegramConnectionView>("/telegram-connections", { connection_id: connection.connection_id }, projectId);
    setSetupStep(2);
    return result.bot_username ? `@${result.bot_username} is connected.` : "Telegram bot connected.";
  });

  const deactivate = () => connection && run("deactivate", async () => {
    if (!window.confirm(`Disconnect ${connection.bot_username ? `@${connection.bot_username}` : connection.name}? Telegram routing and pending access requests for this bot will be removed.`)) return;
    await apiDelete(`/telegram-connections?id=${connection.connection_id}`, projectId);
    setInviteURL("");
    setInviteExpiresAt("");
    setSetupStep(1);
    return "Telegram bot disconnected from Conversations.";
  });

  const savePolicy = () => run("policy", async () => {
    await apiPost("/telegram-intake", {
      connection_id: Number(connectionId), mode,
      default_agent_id: Number(agentId),
      default_title: defaultTitle, require_group_mention: requireMention,
    }, projectId);
    dirtyPolicyRef.current=false;
    setSetupStep(3);
    return mode === "pairing" ? "Secure pairing is active." : mode === "public" ? "Public intake is active." : "Unknown chats are closed.";
  });

  const setAutoName = (enabled: boolean) => connection && run("auto-name", async () => {
    await apiPatch<TelegramConnectionView>("/telegram-connections", {
      connection_id: connection.connection_id,
      auto_name_enabled: enabled,
    }, projectId);
    return enabled
      ? "Telegram will show the agent name whenever this bot routes to only one agent."
      : `Telegram bot name restored to ${connection.original_bot_name || "its original name"}.`;
  });

  const setResponseFeedback = (mode: "live" | "typing" | "off") => connection && run("response-feedback", async () => {
    await apiPatch<TelegramConnectionView>("/telegram-connections", {
      connection_id: connection.connection_id,
      response_feedback: mode,
    }, projectId);
    return mode === "live" ? "Live Telegram response previews are active."
      : mode === "typing" ? "Telegram will show typing until the final answer arrives."
      : "Telegram response feedback is off.";
  });

  const createInvite = () => run("invite", async () => {
    const result = await apiPost<{ invite_url: string; expires_at: string }>("/telegram-invites", {
      connection_id: Number(connectionId), conversation_id: inviteConversation, chat_type: inviteKind,
    }, projectId);
    setInviteURL(result.invite_url);
    setInviteExpiresAt(result.expires_at);
    return "One-time invite created. It expires in 15 minutes.";
  }, false);

  const resolveRequest = (request: TelegramAccessRequest, action: "approve" | "dismiss" | "block" | "unblock") => run(`${action}-${request.id}`, async () => {
    await apiPost("/telegram-access", { id: request.id, action, conversation_id: action === "approve" ? requestTargets[request.id] ?? "" : "" }, projectId);
    return action === "approve" ? `${request.display_name} is connected.` : action === "block" ? `${request.display_name} was blocked.` : action === "unblock" ? `${request.display_name} can request access again.` : "Request dismissed.";
  });

  const removeBinding = (binding: TelegramBindingView) => run(`remove-${binding.id}`, async () => {
    if (!window.confirm(`Disconnect ${binding.chat_title || "this Telegram chat"} from Conversations?`)) return;
    await apiDelete(`/telegram-bindings?id=${encodeURIComponent(binding.id)}`, projectId);
    return `${binding.chat_title || "Telegram chat"} disconnected.`;
  });

  const createManualBinding = (event: React.FormEvent) => {
    event.preventDefault();
    const allowed = manualUsers.split(",").map((item) => Number(item.trim())).filter((item) => Number.isSafeInteger(item) && item > 0);
    run("manual", async () => {
      await apiPost("/telegram-bindings", { connection_id: Number(connectionId), conversation_id: manualConversation, chat_id: manualChat.trim(), allowed_user_ids: allowed }, projectId);
      setManualChat("");
      setManualUsers("");
      return "Advanced route created.";
    });
  };

  const stepLabels = ["Connect bot", "Choose access", "Invite or receive"] as const;

  return (
    <div className="flex-1 min-h-0 overflow-auto p-4">
      <div className="mx-auto max-w-4xl flex flex-col gap-4">
        <section className="rounded-md border border-border bg-bg-card p-4">
          <div className="flex gap-3">
            <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-accent text-bg"><Glyph d={GLYPH_CHAT} size={19} /></div>
            <div><h2 className="text-base font-semibold text-text">Telegram, without the setup friction</h2><p className="mt-1 text-sm text-text-muted">Connect a bot once. Conversations discovers people and groups, handles secure access, and keeps every message in the same durable conversation system.</p><p className="mt-2 text-xs text-text-dim">No Telegram IDs or API commands. Bot credentials remain in the platform integration connection.</p></div>
          </div>
          <div className="mt-4 flex items-center gap-3 rounded border border-border bg-bg px-3 py-2.5">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-accent text-xs font-semibold text-accent">{setupStep}</div>
            <div className="min-w-0"><p className="text-[10px] font-semibold uppercase tracking-wide text-text-dim">Step {setupStep} of 3</p><p className="text-sm font-medium text-text">{stepLabels[setupStep - 1]}</p></div>
            {setupStep > 1 && <button type="button" onClick={() => { setNotice(""); setSetupStep((setupStep - 1) as 1 | 2); }} disabled={busy !== ""} className="ml-auto rounded border border-border px-3 py-1.5 text-xs text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-40">Back</button>}
          </div>
        </section>

        {setupStep === 1 && <section className="rounded-md border border-border bg-bg-card p-4">
          <div className="flex items-start gap-3"><div><h2 className="text-sm font-semibold text-text">Bot connection</h2><p className="mt-1 text-xs text-text-muted">Choose a Telegram connection bound in the Conversations installation settings.</p></div>{connection?.enabled && <span className={`ml-auto whitespace-nowrap rounded bg-bg-input px-2 py-1 text-xs ${connection.webhook_status === "healthy" ? "text-success" : "text-warn"}`}>{connection.webhook_status === "healthy" ? "Webhook healthy" : connection.webhook_status === "drifted" ? "Webhook replaced" : "Needs attention"}</span>}</div>
          {connections.length === 0 ? <div className="mt-4 rounded border border-border bg-bg px-3 py-3 text-xs text-text-muted">No Telegram connection is bound yet. Add one in the app installation settings, then return here.</div> : <div className="mt-4 grid gap-3 sm:grid-cols-2"><label className="flex flex-col gap-1 text-xs text-text-muted">Telegram bot<select value={connectionId} onChange={(event) => setConnectionId(event.target.value)} className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text">{connections.map((item) => <option key={item.connection_id} value={item.connection_id}>{item.bot_username ? `@${item.bot_username}` : item.name}</option>)}</select></label><div className="flex flex-wrap items-end gap-2"><button type="button" onClick={activate} disabled={busy !== ""} className="rounded bg-accent px-3 py-2 text-xs font-semibold text-bg disabled:opacity-40">{busy === "activate" ? "Connecting…" : connection?.enabled ? "Verify & reconnect" : "Activate bot"}</button>{connection?.bot_username && <a href={`https://t.me/${connection.bot_username}`} target="_blank" rel="noreferrer" className="rounded border border-border px-3 py-2 text-xs text-text-muted hover:bg-bg-input hover:text-text">Open bot ↗</a>}{connection?.enabled && <button type="button" onClick={deactivate} disabled={busy !== ""} className="rounded px-2 py-2 text-xs text-error hover:bg-bg-input disabled:opacity-40">{busy === "deactivate" ? "Disconnecting…" : "Disconnect"}</button>}</div></div>}
          {connection?.last_webhook_error && <p className="mt-3 text-xs text-error">Telegram reports: {connection.last_webhook_error}</p>}
          <p className="mt-3 text-xs text-text-dim">Activation verifies the bot and installs a signed webhook automatically. Reconnecting intentionally reclaims Telegram’s one webhook for this bot.</p>
          {connection?.enabled && <div className="mt-4 flex justify-end"><button type="button" onClick={() => { setNotice(""); setSetupStep(2); }} disabled={busy !== ""} className="rounded bg-accent px-3 py-2 text-xs font-semibold text-bg disabled:opacity-40">Continue</button></div>}
        </section>}

        {setupStep === 2 && connection?.enabled && <section className="rounded-md border border-border bg-bg-card p-4">
          <h2 className="text-sm font-semibold text-text">Choose who can start conversations</h2><p className="mt-1 text-xs text-text-muted">This policy applies to unknown chats. Existing routes keep working.</p>
          <div className="mt-4 grid gap-2 sm:grid-cols-3">{([ ["pairing", "Secure pairing", "Approve each person or group before messages reach an agent."], ["public", "Public intake", "Every new private chat gets its own public conversation."], ["closed", "Invites only", "Ignore unknown chats; one-time invites still work."] ] as const).map(([value, title, description]) => <button key={value} type="button" aria-pressed={mode === value} onClick={() => {dirtyPolicyRef.current=true;setMode(value);}} className={`rounded border p-3 text-left ${mode === value ? "border-accent bg-bg-input" : "border-border bg-bg hover:bg-bg-input"}`}><span className="text-sm font-medium text-text">{title}</span><span className="mt-1 block text-xs leading-relaxed text-text-dim">{description}</span></button>)}</div>
          <div className="mt-4 grid gap-3 sm:grid-cols-2"><label className="flex flex-col gap-1 text-xs text-text-muted">Lead agent for new conversations<select value={agentId} onChange={(event) => {dirtyPolicyRef.current=true;setAgentId(event.target.value);}} className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text"><option value="">Select an agent</option>{(selectableAgents.length ? selectableAgents : agents).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label><label className="flex flex-col gap-1 text-xs text-text-muted">Conversation title prefix<input value={defaultTitle} onChange={(event) => {dirtyPolicyRef.current=true;setDefaultTitle(event.target.value);}} maxLength={120} className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text" /></label></div>
          <label className="mt-4 flex items-start gap-2 text-xs text-text-muted"><input type="checkbox" checked={requireMention} onChange={(event) => {dirtyPolicyRef.current=true;setRequireMention(event.target.checked);}} className="mt-0.5" /><span><span className="font-medium text-text">Require a bot mention in groups</span><span className="mt-0.5 block text-text-dim">Recommended: ordinary group chatter does not trigger the agent.</span></span></label>
          <label className="mt-3 flex items-start gap-2 text-xs text-text-muted"><input type="checkbox" checked={connection.auto_name_enabled} onChange={(event) => setAutoName(event.target.checked)} disabled={busy !== ""} className="mt-0.5" /><span><span className="font-medium text-text">Show the sole agent’s name in Telegram</span><span className="mt-0.5 block text-text-dim">{connection.name_sync_error ? `Name update needs attention: ${connection.name_sync_error}` : connection.synced_bot_name ? `Telegram currently shows “${connection.synced_bot_name}”.` : connection.routed_agent_count > 1 ? `This bot routes to ${connection.routed_agent_count} agents, so Telegram keeps “${connection.original_bot_name || connection.name}”.` : "The original bot name is kept until this connection routes to exactly one agent."}</span></span></label>
          <label className="mt-3 grid gap-1 text-xs text-text-muted sm:max-w-md"><span className="font-medium text-text">Response feedback</span><select value={connection.response_feedback || "live"} onChange={(event) => setResponseFeedback(event.target.value as "live" | "typing" | "off")} disabled={busy !== ""} className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text"><option value="live">Live preview (recommended)</option><option value="typing">Typing only</option><option value="off">Off</option></select><span className="text-text-dim">Private chats get Telegram’s native animated draft; groups show typing until the durable final message arrives.</span></label>
          {mode === "public" && <div className="mt-4 rounded border border-warn bg-bg px-3 py-2 text-xs text-text-muted">Public intake accepts any private Telegram user and creates a public conversation. Pairing is safer for internal bots.</div>}
          <div className="mt-4 flex justify-end"><button type="button" onClick={savePolicy} disabled={busy !== "" || !agentId} className="rounded bg-accent px-3 py-2 text-xs font-semibold text-bg disabled:opacity-40">{busy === "policy" ? "Saving…" : policy ? "Save & continue" : "Continue"}</button></div>
        </section>}

        {setupStep === 3 && policy && <section className="rounded-md border border-border bg-bg-card p-4">
          <h2 className="text-sm font-semibold text-text">Invite without asking for IDs</h2><p className="mt-1 text-xs text-text-muted">Telegram supplies the identity securely when a person opens the link or adds the bot to a group.</p>
          <div className="mt-4 grid gap-3 sm:grid-cols-3"><label className="flex flex-col gap-1 text-xs text-text-muted sm:col-span-2">Destination<select value={inviteConversation} onChange={(event) => setInviteConversation(event.target.value)} className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text"><option value="">Create a new conversation when opened</option>{conversations.map((item) => <option key={item.id} value={item.id}>{item.title} ({item.audience})</option>)}</select></label><label className="flex flex-col gap-1 text-xs text-text-muted">Invite type<select value={inviteKind} onChange={(event) => setInviteKind(event.target.value as "private" | "group")} className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text"><option value="private">Person</option><option value="group">Telegram group</option></select></label></div>
          <div className="mt-3"><button type="button" onClick={createInvite} disabled={busy !== ""} className="rounded bg-accent px-3 py-2 text-xs font-semibold text-bg disabled:opacity-40">{busy === "invite" ? "Creating…" : inviteKind === "group" ? "Create add-to-group link" : "Create Telegram invite"}</button></div>
          {inviteURL ? <div className="mt-4 rounded border border-success bg-bg px-3 py-3" role="region" aria-label="Telegram invite ready"><div className="flex items-center gap-2"><span className="flex h-6 w-6 items-center justify-center rounded-full bg-success text-xs font-bold text-bg">✓</span><div><p className="text-sm font-medium text-text">Invite ready</p><p className="text-xs text-text-dim">One use · expires {inviteExpiresAt ? `at ${new Date(inviteExpiresAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` : "after 15 minutes"}</p></div></div><input readOnly value={inviteURL} onFocus={(event) => event.currentTarget.select()} aria-label="Telegram invite URL" className="mt-3 w-full rounded border border-border bg-bg-input px-3 py-2 text-sm text-text" /><div className="mt-2 flex flex-wrap gap-2"><button type="button" onClick={() => navigator.clipboard.writeText(inviteURL).then(() => setNotice("Invite link copied."), () => setNotice("Select the link and copy it manually."))} className="rounded border border-border px-3 py-2 text-xs text-text-muted hover:bg-bg-input hover:text-text">Copy link</button><a href={inviteURL} target="_blank" rel="noreferrer" className="rounded border border-border px-3 py-2 text-xs text-accent hover:bg-bg-input">Open in Telegram ↗</a><button type="button" onClick={createInvite} disabled={busy !== ""} className="rounded px-3 py-2 text-xs text-text-muted hover:bg-bg-input disabled:opacity-40">Create another</button></div></div> : <p className="mt-2 text-xs text-text-dim">The link will appear here and remain visible until you change the destination or invite type.</p>}
        </section>}

        {notice && <div className="rounded border border-border bg-bg px-3 py-2 text-xs text-text-muted" role="status">{notice}</div>}

        {currentRequests.length > 0 && <section className="rounded-md border border-accent bg-bg-card p-4">
          <div className="flex items-center gap-2"><span className="h-2 w-2 rounded-full bg-accent" /><h2 className="text-sm font-semibold text-text">Pending access requests</h2><span className="rounded bg-bg-input px-1.5 py-0.5 text-xs text-text-muted">{currentRequests.length}</span></div><p className="mt-1 text-xs text-text-muted">Message content stays private until approval; only identity and chat metadata are shown.</p>
          <div className="mt-3 flex flex-col gap-2">{currentRequests.map((request) => <div key={request.id} className="rounded border border-border bg-bg p-3"><div className="flex items-start gap-3"><div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-bg-input text-sm font-semibold text-text">{request.display_name.slice(0, 1).toUpperCase()}</div><div className="min-w-0"><p className="text-sm font-medium text-text truncate">{request.chat_title || request.display_name}</p><p className="text-xs text-text-dim truncate">{request.username ? `@${request.username} · ` : ""}{request.chat_type || "private"} · request {request.pairing_code}</p></div></div><div className="mt-3 flex flex-col gap-2 sm:flex-row"><select value={requestTargets[request.id] ?? ""} onChange={(event) => setRequestTargets((current) => ({ ...current, [request.id]: event.target.value }))} className="min-w-0 flex-1 rounded border border-border bg-bg-input px-2.5 py-2 text-xs text-text"><option value="">Create a new operator conversation</option>{conversations.map((item) => <option key={item.id} value={item.id}>Bind to {item.title}</option>)}</select><button type="button" onClick={() => resolveRequest(request, "approve")} disabled={busy !== ""} className="rounded bg-accent px-3 py-2 text-xs font-semibold text-bg disabled:opacity-40">Approve</button><button type="button" onClick={() => resolveRequest(request, "dismiss")} disabled={busy !== ""} className="rounded border border-border px-3 py-2 text-xs text-text-muted hover:bg-bg-input">Dismiss</button><button type="button" onClick={() => resolveRequest(request, "block")} disabled={busy !== ""} className="rounded px-2 py-2 text-xs text-error hover:bg-bg-input">Block</button></div></div>)}</div>
        </section>}

        {currentBlocked.length > 0 && <section className="rounded-md border border-border bg-bg-card p-4"><h2 className="text-sm font-semibold text-text">Blocked senders</h2><p className="mt-1 text-xs text-text-muted">Blocked chats stay silent until you allow them to request access again.</p><div className="mt-3 flex flex-col gap-2">{currentBlocked.map((request) => <div key={request.id} className="flex items-center gap-3 rounded border border-border bg-bg px-3 py-2"><div className="min-w-0"><p className="text-sm text-text truncate">{request.chat_title || request.display_name}</p><p className="text-xs text-text-dim">{request.username ? `@${request.username} · ` : ""}{request.chat_type || "private"}</p></div><button type="button" onClick={() => resolveRequest(request, "unblock")} disabled={busy !== ""} className="ml-auto rounded border border-border px-3 py-1.5 text-xs text-text-muted hover:bg-bg-input hover:text-text">Allow requests</button></div>)}</div></section>}

        <section className="rounded-md border border-border bg-bg-card p-4"><h2 className="text-sm font-semibold text-text">Connected Telegram chats</h2><p className="mt-1 text-xs text-text-muted">Every route points to an ordinary Conversations conversation.</p>{currentBindings.length === 0 ? <div className="mt-3 rounded border border-border bg-bg px-3 py-3 text-xs text-text-muted">No chats connected yet. Message the bot, approve a request, or share an invite.</div> : <div className="mt-3 flex flex-col gap-2">{currentBindings.map((binding) => <div key={binding.id} className="flex items-center gap-3 rounded border border-border bg-bg px-3 py-2"><div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-bg-input text-accent"><Glyph d={GLYPH_CHAT} size={14} /></div><div className="min-w-0"><p className="text-sm text-text truncate">{binding.chat_title || (binding.chat_username ? `@${binding.chat_username}` : "Telegram chat")}</p><p className="text-xs text-text-dim truncate">{binding.conversation_title || binding.conversation_id} · {binding.audience || "operator"} · {binding.access_mode || "manual"}{binding.require_mention ? " · mention only" : ""}</p></div><button type="button" onClick={() => removeBinding(binding)} disabled={busy !== ""} className="ml-auto inline-flex h-8 w-8 items-center justify-center rounded text-text-muted hover:bg-bg-input hover:text-error" aria-label="Disconnect Telegram chat"><Glyph d={GLYPH_TRASH} size={14} /></button></div>)}</div>}</section>

        {connection?.enabled && <section className="rounded-md border border-border bg-bg-card p-4"><button type="button" onClick={() => setAdvancedOpen((open) => !open)} className="flex w-full items-center justify-between text-left text-sm text-text-muted"><span>Advanced manual routing</span><span>{advancedOpen ? "−" : "+"}</span></button>{advancedOpen && <form onSubmit={createManualBinding} className="mt-4 grid gap-3 sm:grid-cols-2"><p className="text-xs text-text-dim sm:col-span-2">Recovery-only: bind known numeric IDs directly. The guided flow is preferred.</p><label className="flex flex-col gap-1 text-xs text-text-muted">Conversation<select value={manualConversation} onChange={(event) => setManualConversation(event.target.value)} className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text"><option value="">Select a conversation</option>{conversations.map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}</select></label><label className="flex flex-col gap-1 text-xs text-text-muted">Telegram chat ID<input value={manualChat} onChange={(event) => setManualChat(event.target.value)} placeholder="123456789 or -100…" className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text" /></label><label className="flex flex-col gap-1 text-xs text-text-muted sm:col-span-2">Allowed user IDs<input value={manualUsers} onChange={(event) => setManualUsers(event.target.value)} placeholder="Comma-separated; optional for public conversations" className="rounded border border-border bg-bg-input px-2.5 py-2 text-sm text-text" /></label><div className="sm:col-span-2 flex justify-end"><button type="submit" disabled={busy !== "" || !manualConversation || !manualChat.trim()} className="rounded border border-border px-3 py-2 text-xs text-text hover:bg-bg-input disabled:opacity-40">{busy === "manual" ? "Binding…" : "Create manual route"}</button></div></form>}</section>}

      </div>
    </div>
  );
}

// ─── root panel ──────────────────────────────────────────────────────

export default function ConversationsPanel({ projectId, instanceId }: NativePanelProps) {
  const [tab, setTab] = useState<"chats" | "inbox" | "telegram">("chats");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [unread, setUnread] = useState<Map<string, UnreadEntry>>(new Map());
  const [selectedId, setSelectedId] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [agents, setAgents] = useState<AgentInfo[] | null>(null);
  const [agentsError, setAgentsError] = useState("");
  const [inboxItems, setInboxItems] = useState<InboxItem[]>([]);
 const [inboxTotal,setInboxTotal]=useState(0);
const [inboxAttention,setInboxAttention]=useState<Record<string,number>>({});
  // Same breakpoint the dashboard chat page uses for its right-hand
  // context column.
  const hasContextColumn = useMediaQuery("(min-width: 1024px)");

  useEffect(()=>{setConversations([]);setSelectedId("");},[projectId,instanceId,showArchived]);
  const listStateRef=useRef({scope:"",count:0,selected:""});
  const listScope=`${projectId}:${instanceId}:${showArchived}`;
  listStateRef.current=listStateRef.current.scope===listScope ? {scope:listScope,count:conversations.length,selected:selectedId} : {scope:listScope,count:0,selected:""};
  const loadConversations = useCallback(async () => {
    const snapshot={...listStateRef.current};
    try {
      const [chats, unreadEntries, inbox] = await Promise.all([
        refreshConversationList(agentScopedPath(`/chats${showArchived ? "?archived=1" : ""}`, instanceId),projectId,snapshot.count,snapshot.selected),
        apiGet<UnreadEntry[]>(agentScopedPath("/unread-summary", instanceId), projectId),
        apiGet<InboxPage>(agentScopedPath("/inbox?page=1&limit=100", instanceId), projectId),
      ]);
      if(snapshot.scope!==listStateRef.current.scope)return;
      const scoped = projectId
        ? chats.filter((c) => !c.project_id || c.project_id === projectId)
        : chats;
      if(listStateRef.current.count<=Math.max(100,snapshot.count))setConversations(scoped);
      setUnread(new Map(unreadEntries.map((e) => [e.conversation_id, e])));
      setInboxItems(inbox.items);setInboxTotal(inbox.total);setInboxAttention(inbox.attention ?? {});
      setSelectedId((current) =>
        (scoped.some(row=>row.id===current) ? current : scoped[0]?.id) || "",
      );
    } catch {
      /* transient */
    }
  }, [projectId, instanceId, showArchived]);

  useEffect(() => {
    loadConversations();
    const interval = window.setInterval(loadConversations, 8000);
    return () => window.clearInterval(interval);
  }, [loadConversations]);

  // Agent directory, fetched lazily the first time a dialog needs it.
  const ensureAgents = useCallback(() => {
    if (agents !== null) return;
    apiGet<AgentInfo[]>(`/agents`, projectId).then(
      (list) => {
        setAgents(list);
        setAgentsError("");
      },
      (err) => setAgentsError(err instanceof Error ? err.message : String(err)),
    );
  }, [agents, projectId]);

  const selected = useMemo(
    () => conversations.find((c) => c.id === selectedId) ?? null,
    [conversations, selectedId],
  );

  // Pending inbox items become attention markers: a severity dot on
  // the conversation row and a count badge on the Inbox tab. Ranks:
  // error alert > warn alert > approval > info alert; reports count
  // in the badge but never earn a dot.
  const attentionByConv = useMemo(() => new Map(Object.entries(inboxAttention)), [inboxAttention]);
  const inboxHasError = Object.values(inboxAttention).some(rank => rank === 4);
  const attentionDotClass = (r: number) =>
    r >= 4 ? "bg-error" : r === 3 ? "bg-warn" : r === 2 ? "bg-accent" : "bg-info";

  // Selection moved on — close the details dialog so its state can
  // never be reused against the next conversation (the dashboard's
  // ChatMain guards the same way).
  useEffect(() => {
    setDetailsOpen(false);
  }, [selectedId]);

  const openDetails = () => {
    ensureAgents();
    setDetailsOpen(true);
  };

  const openConversation = async (conversationID: string) => {
    try {
      const conv=await apiGet<Conversation>(`/chats?id=${encodeURIComponent(conversationID)}`,projectId);
      setConversations(current => current.some(c=>c.id===conv.id) ? current : [conv,...current]);
      setTab("chats");setSelectedId(conversationID);
    } catch(err) {setAgentsError(String(err));}
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">Conversations</h1>
          <p className="text-xs text-text-muted">Chat, inbox, and optional Telegram delivery in one durable system.</p>
        </div>
        <nav className="ml-auto flex items-center gap-1">
          {(
            [
              { id: "chats", label: "Chats", glyph: GLYPH_CHAT },
              { id: "inbox", label: "Inbox", glyph: GLYPH_INBOX },
              { id: "telegram", label: "Telegram", glyph: GLYPH_CHAT },
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
              {entry.id === "inbox" && inboxTotal > 0 && (
                <span
                  className={`text-xs px-1.5 py-0.5 rounded-full text-bg ${inboxHasError ? "bg-error" : "bg-accent"}`}
                >
                  {inboxTotal}
                </span>
              )}
            </button>
          ))}
        </nav>
      </header>

      {tab === "inbox" ? (
        <InboxTab
          onOpenConversation={openConversation}
          projectId={projectId}
          instanceId={instanceId}
        />
      ) : tab === "telegram" ? (
        <TelegramTab projectId={projectId} conversations={conversations.filter((item) => !item.project_id || item.project_id === projectId)} />
      ) : (
        <main
          className={`flex-1 min-h-0 grid grid-cols-1 md:grid-cols-[260px_minmax(0,1fr)] md:divide-x md:divide-border ${
            hasContextColumn && selected ? "lg:grid-cols-[260px_minmax(0,1fr)_280px]" : ""
          }`}
        >
          <aside className="min-h-0 flex flex-col">
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
                    Start one with New conversation, or wait for an agent or app to open one.
                  </p>
                )}
              </div>
            ) : (
              <ul className="divide-y divide-border">
                <li><MoreConversations path={agentScopedPath(`/chats${showArchived?"?archived=1":""}`,instanceId)} projectId={projectId} rows={conversations} onRows={setConversations}/></li>
                {conversations.map((c) => {
                  const unreadCount = unread.get(c.id)?.unread ?? 0;
                  return (
                    <li key={c.id}>
                      <button
                        type="button"
                        onClick={() => setSelectedId(c.id)}
                        className={`w-full text-left px-4 py-3 border-l-2 transition-colors ${
                          c.id === selectedId
                            ? "border-accent bg-bg-hover"
                            : "border-transparent hover:bg-bg-hover"
                        }`}
                      >
                        <div className="flex items-center gap-2">
                          {(attentionByConv.get(c.id) ?? 0) > 0 && (
                            <span
                              className={`w-2 h-2 rounded-full shrink-0 ${attentionDotClass(attentionByConv.get(c.id) ?? 0)}`}
                              title="Pending inbox item"
                            />
                          )}
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
                          {c.audience === "public" && <PublicTag />}
                          {c.origin !== "web" && c.origin !== "app" && (
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
            <ConversationChat key={`${selected.project_id}:${selected.id}`}
              conversation={selected}
              archived={showArchived}
              onOpenDetails={openDetails}
              onActed={loadConversations}
              onRemoved={() => {
                setConversations(current=>current.filter(c=>c.id!==selectedId));
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
          {hasContextColumn && selected && (
            <div className="h-full min-h-0 overflow-hidden">
              <ContextColumn
                conversation={selected}
                agents={agents}
                onEnsureAgents={ensureAgents}
                onManage={openDetails}
                refreshHold={detailsOpen}
              />
            </div>
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

      {selected && (
        <DetailsDialog
          open={detailsOpen}
          conversation={selected}
          agents={agents}
          onClose={() => setDetailsOpen(false)}
          onChanged={loadConversations}
          onRemoved={() => {
            setConversations(current=>current.filter(c=>c.id!==selectedId));
            setSelectedId("");
            loadConversations();
          }}
        />
      )}
    </div>
  );
}
