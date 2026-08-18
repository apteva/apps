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

// ─── fetch helpers ───────────────────────────────────────────────────

async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(`${API}${path}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

async function apiPost<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
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
const GLYPH_SEND = "m22 2-7 20-4-9-9-4Z M22 2 11 13";
const GLYPH_ALERT =
  "M12 9v4 M12 17h.01 M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z";
const GLYPH_CHECK = "M20 6 9 17l-5-5";
const GLYPH_X = "M18 6 6 18 M6 6l12 12";
const GLYPH_REPORT =
  "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8";

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
  onActed,
}: {
  conversation: Conversation;
  onActed: () => void;
}) {
  const { messages, bubble, connected, mergeMessages } = useConversationTransport(conversation.id);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState("");
  const bottomRef = useRef<HTMLDivElement | null>(null);

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

      <footer className="shrink-0 border-t border-border p-3">
        {sendError && <p className="mb-2 text-xs text-error">{sendError}</p>}
        <div className="flex items-end gap-2">
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                send();
              }
            }}
            rows={2}
            placeholder="Message the agent…"
            className="flex-1 resize-none bg-bg-input border border-border rounded-md px-3 py-2 text-sm text-text focus:outline-none focus:border-accent"
          />
          <button
            type="button"
            onClick={send}
            disabled={sending || !draft.trim()}
            className="shrink-0 h-9 w-9 inline-flex items-center justify-center rounded-md bg-accent text-bg disabled:opacity-50"
            aria-label="Send"
          >
            <Glyph d={GLYPH_SEND} size={16} />
          </button>
        </div>
      </footer>
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

  const loadConversations = useCallback(async () => {
    try {
      const [chats, unreadEntries] = await Promise.all([
        apiGet<Conversation[]>("/chats"),
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
  }, [projectId]);

  useEffect(() => {
    loadConversations();
    const interval = window.setInterval(loadConversations, 8000);
    return () => window.clearInterval(interval);
  }, [loadConversations]);

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
          <aside className="border-r border-border min-h-0 overflow-auto">
            {conversations.length === 0 ? (
              <div className="p-6 text-sm text-text-muted flex flex-col items-center gap-3 text-center">
                <span className="text-text-dim">
                  <Glyph d={GLYPH_CHAT} size={28} />
                </span>
                <p>No conversations yet.</p>
                <p className="text-xs text-text-dim">
                  Conversations appear when you message an agent or an external channel comes in.
                </p>
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
          </aside>
          {selected ? (
            <ChatColumn conversation={selected} onActed={loadConversations} />
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
    </div>
  );
}
