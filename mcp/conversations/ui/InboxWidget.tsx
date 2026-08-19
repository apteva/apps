// InboxWidget — the conversations app's dashboard.home widget, a
// faithful recreation of the dashboard's AptevaInbox (the operator's
// favorite): tone-coded rows (blue approvals, red/yellow alerts, left
// severity rail), ticking relative timestamps, Review / Open / Dismiss
// actions with detail modals, and the "You're all caught up" empty
// state — backed by this app's own /inbox instead of channel-chat.
//
// House rules: dashboard theme tokens only, inline SVG in
// currentColor, no emojis. Built by
// `bun run scripts/build-panels.ts --app conversations`.

import { useCallback, useEffect, useMemo, useState } from "react";
import createDOMPurify from "dompurify";
import { marked } from "marked";

interface HostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  slot?: string;
  eventRevision?: number;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

const API = "/api/apps/conversations";
const PANEL_HREF = "/apps/conversations/page";

// ─── markdown (same pipeline as the panel / dashboard) ───────────────

marked.setOptions({ breaks: true, gfm: true });
let purifier: ReturnType<typeof createDOMPurify> | null = null;
function renderSafeMarkdown(source: string): string {
  const parsed = marked.parse(source, { async: false }) as string;
  if (!purifier) purifier = createDOMPurify(window);
  return purifier.isSupported
    ? purifier.sanitize(parsed, {
        USE_PROFILES: { html: true },
        FORBID_TAGS: ["base", "form", "iframe", "object", "embed", "style", "template"],
        FORBID_ATTR: ["style", "srcdoc"],
      })
    : "";
}

function InboxMarkdown({ source }: { source: string }) {
  const html = useMemo(() => renderSafeMarkdown(source), [source]);
  return (
    <div
      className="chat-md text-sm text-text-muted leading-relaxed break-words"
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

// flattenPreview: one-line plain-text preview of model-authored
// markdown — raw pipes and heading hashes read as garbage in a row.
function flattenPreview(source: string): string {
  return source
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#*_`>|-]+/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 160);
}

// ─── wire shapes ─────────────────────────────────────────────────────

interface CardComponent {
  name: string;
  props: Record<string, unknown>;
}

interface InboxMessage {
  id: number;
  conversation_id: string;
  agent_id?: number;
  content: string;
  component_kind?: string;
  severity?: string;
  components: CardComponent[];
  created_at: string;
}

interface InboxItem {
  priority: number;
  message: InboxMessage;
}

interface AgentInfo {
  id: number;
  name: string;
}

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

// ─── tones (ported verbatim from the dashboard's AptevaInbox) ────────

interface InboxTone {
  row: string;
  rail: string;
  badge: string;
  action: string;
}

function inboxTone(kind: string, severity: string): InboxTone {
  if (kind === "approval") {
    return {
      row: "border-blue/25 bg-blue/5",
      rail: "bg-blue/80",
      badge: "border-blue/35 bg-blue/10 text-blue",
      action: "border-blue/40 text-blue hover:bg-blue/10",
    };
  }
  if (kind === "alert") {
    const sev = severity.toLowerCase();
    if (sev === "critical" || sev === "error") {
      return {
        row: "border-red/25 bg-red/5",
        rail: "bg-red/80",
        badge: "border-red/35 bg-red/10 text-red",
        action: "border-red/40 text-red hover:bg-red/10",
      };
    }
    if (sev === "warning" || sev === "warn") {
      return {
        row: "border-yellow/25 bg-yellow/5",
        rail: "bg-yellow/80",
        badge: "border-yellow/35 bg-yellow/10 text-yellow",
        action: "border-yellow/40 text-yellow hover:bg-yellow/10",
      };
    }
    return {
      row: "border-border/70 bg-bg-subtle/45",
      rail: "bg-text-muted/70",
      badge: "border-border bg-bg-input text-text-muted",
      action: "border-border text-text-muted hover:text-text hover:border-text-muted",
    };
  }
  return {
    row: "border-accent/25 bg-accent/5",
    rail: "bg-accent/80",
    badge: "border-accent/35 bg-accent/10 text-accent",
    action: "border-accent/35 text-accent hover:bg-accent/10",
  };
}

// ─── time (ticking, exact on hover — donor behavior) ─────────────────

function parseTime(value?: string): Date | null {
  if (!value) return null;
  const normalized = /(?:Z|[+-]\d\d:?\d\d)$/.test(value)
    ? value
    : `${value.replace(" ", "T")}Z`;
  const date = new Date(normalized);
  return Number.isNaN(date.getTime()) ? null : date;
}

function relativeTime(date: Date | null, now: number): string {
  if (!date) return "";
  const seconds = Math.max(0, Math.floor((now - date.getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h ago`;
}

// ─── card prop readers ───────────────────────────────────────────────

function card(message: InboxMessage, name: string): Record<string, unknown> {
  return message.components.find((c) => c.name === name)?.props ?? {};
}

function itemTitle(message: InboxMessage): string {
  const kind = message.component_kind;
  if (kind === "approval") return String(card(message, "approval-card").title ?? message.content);
  if (kind === "report") return String(card(message, "report-card").title ?? message.content);
  return String(card(message, "alert-card").text ?? message.content);
}

function itemBody(message: InboxMessage): string {
  const kind = message.component_kind;
  if (kind === "approval") return String(card(message, "approval-card").body ?? "");
  if (kind === "report") return String(card(message, "report-card").summary ?? "");
  return String(card(message, "alert-card").text ?? "");
}

// ─── overlay (lightweight Modal port, same as the panel's) ───────────

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
        className="relative bg-bg-card border border-border rounded-lg shadow-lg w-full max-w-2xl max-h-full overflow-hidden flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        {children}
      </div>
    </div>
  );
}

// ─── detail modals ───────────────────────────────────────────────────

function DetailModal({
  open,
  label,
  title,
  meta,
  body,
  onClose,
  onDismiss,
}: {
  open: boolean;
  label: string;
  title: string;
  meta?: string;
  body: string;
  onClose: () => void;
  onDismiss?: () => void;
}) {
  return (
    <Overlay open={open} onClose={onClose} ariaLabel={label}>
      <div className="px-5 py-4 border-b border-border flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="text-[10px] uppercase tracking-wide text-accent font-bold">{label}</div>
          <h2 className="mt-1 text-lg font-bold text-text break-words">{title}</h2>
          {meta && <div className="mt-1 text-xs text-text-dim">{meta}</div>}
        </div>
        <div className="shrink-0 flex items-center gap-2">
          {onDismiss && (
            <button
              type="button"
              onClick={() => {
                onDismiss();
                onClose();
              }}
              className="text-text-muted hover:text-text border border-border rounded px-2 py-1 text-xs"
            >
              Dismiss
            </button>
          )}
          <button
            type="button"
            onClick={onClose}
            className="text-text-muted hover:text-text border border-border rounded px-2 py-1 text-xs"
          >
            Close
          </button>
        </div>
      </div>
      <div className="overflow-auto px-5 py-4">
        {body ? <InboxMarkdown source={body} /> : <p className="text-sm text-text-dim">No detail.</p>}
      </div>
    </Overlay>
  );
}

function ApprovalModal({
  open,
  message,
  agentName,
  onClose,
  onActed,
}: {
  open: boolean;
  message: InboxMessage;
  agentName: string;
  onClose: () => void;
  onActed: () => void;
}) {
  const props = card(message, "approval-card");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const actions = Array.isArray(props.actions)
    ? (props.actions as Array<{ id: string; label: string; style?: string }>)
    : [];

  const act = async (actionId: string) => {
    setBusy(true);
    setError("");
    try {
      await apiPost(`/message-action`, { message_id: message.id, action_id: actionId, note });
      onActed();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Overlay open={open} onClose={onClose} ariaLabel="Review approval">
      <div className="px-5 py-4 border-b border-border">
        <div className="text-[10px] uppercase tracking-wide text-blue font-bold">Approval</div>
        <h2 className="mt-1 text-lg font-bold text-text break-words">{String(props.title ?? "")}</h2>
        <div className="mt-1 text-xs text-text-dim">{agentName}</div>
      </div>
      <div className="overflow-auto px-5 py-4 space-y-4">
        {props.body ? <InboxMarkdown source={String(props.body)} /> : null}
        <input
          type="text"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Optional note for the agent"
          className="w-full bg-bg-input border border-border rounded px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
        />
        {error && <p className="text-xs text-error">{error}</p>}
      </div>
      <div className="flex justify-end gap-2 border-t border-border px-5 py-4">
        <button
          type="button"
          onClick={onClose}
          disabled={busy}
          className="rounded border border-border px-3 py-1.5 text-xs text-text-muted hover:text-text"
        >
          Cancel
        </button>
        {actions.map((a) => (
          <button
            key={a.id}
            type="button"
            disabled={busy}
            onClick={() => act(a.id)}
            className={`rounded px-3 py-1.5 text-xs font-semibold disabled:opacity-50 ${
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
    </Overlay>
  );
}

// ─── rows ────────────────────────────────────────────────────────────

function InboxRow({
  item,
  now,
  agentName,
  onChanged,
}: {
  item: InboxItem;
  now: number;
  agentName: string;
  onChanged: () => void;
}) {
  const [detailOpen, setDetailOpen] = useState(false);
  const [approvalOpen, setApprovalOpen] = useState(false);
  const m = item.message;
  const kind = m.component_kind || "report";
  const severity = m.severity || "";
  const tone = inboxTone(kind, severity);
  const label = kind === "approval" ? "Approval" : kind === "report" ? "Report" : "Alert";
  const meta =
    kind === "approval"
      ? String(card(m, "approval-card").status ?? "pending")
      : kind === "report"
        ? String(card(m, "report-card").period ?? "") || "report"
        : severity || "alert";
  const createdAt = parseTime(m.created_at);
  const preview = flattenPreview(itemBody(m));

  const dismiss = async () => {
    try {
      await apiPost(`/message-dismiss`, { message_id: m.id });
      onChanged();
    } catch {
      /* surfaced on next reload */
    }
  };

  return (
    <>
      <article
        className={`h-[78px] rounded-md border px-3 py-2 flex items-center gap-3 overflow-hidden ${tone.row}`}
      >
        <div className={`w-1.5 h-10 rounded-full shrink-0 ${tone.rail}`} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-[10px] uppercase tracking-wide text-text-dim">
            <span className={`rounded border px-1.5 py-0.5 font-bold ${tone.badge}`}>{label}</span>
            {meta && (
              <span className="text-text-muted normal-case tracking-normal truncate">{meta}</span>
            )}
          </div>
          <div className="mt-0.5 text-xs text-text truncate">{itemTitle(m)}</div>
          <div className="mt-0.5 flex items-center gap-2 text-[10px] text-text-dim min-w-0">
            <span className="shrink-0">{agentName}</span>
            {preview && (
              <>
                <span className="text-text-muted">·</span>
                <span className="truncate">{preview}</span>
              </>
            )}
          </div>
        </div>
        <div className="shrink-0 flex items-center gap-3">
          <time
            dateTime={createdAt?.toISOString() || m.created_at}
            title={createdAt ? createdAt.toLocaleString() : m.created_at}
            className="text-[10px] text-text-dim tabular-nums whitespace-nowrap"
          >
            {relativeTime(createdAt, now)}
          </time>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => (kind === "approval" ? setApprovalOpen(true) : setDetailOpen(true))}
              className={`rounded border px-2.5 py-1 text-[11px] ${tone.action}`}
            >
              {kind === "approval" ? "Review" : "Open"}
            </button>
            {kind !== "approval" && (
              <button
                type="button"
                onClick={() => void dismiss()}
                className="rounded border border-border px-2.5 py-1 text-[11px] text-text-dim hover:text-text hover:border-text-muted"
              >
                Dismiss
              </button>
            )}
          </div>
        </div>
      </article>
      {kind === "approval" ? (
        <ApprovalModal
          open={approvalOpen}
          message={m}
          agentName={agentName}
          onClose={() => setApprovalOpen(false)}
          onActed={onChanged}
        />
      ) : (
        <DetailModal
          open={detailOpen}
          label={label}
          title={itemTitle(m)}
          meta={`${agentName}${meta ? ` · ${meta}` : ""}`}
          body={itemBody(m)}
          onClose={() => setDetailOpen(false)}
          onDismiss={() => void dismiss()}
        />
      )}
    </>
  );
}

// ─── widget root ─────────────────────────────────────────────────────

export default function InboxWidget(props: HostProps) {
  const [items, setItems] = useState<InboxItem[]>([]);
  const [agents, setAgents] = useState<Map<number, string>>(new Map());
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const limit = props.widgetSize === "full" ? 12 : 6;

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const inbox = await apiGet<InboxItem[]>("/inbox?limit=100");
      setItems(inbox);
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const poll = window.setInterval(load, 15000);
    return () => window.clearInterval(poll);
    // eventRevision: the host bumps it on app events — reload then too.
  }, [load, props.eventRevision]);

  useEffect(() => {
    const query = props.projectId ? `?project_id=${encodeURIComponent(props.projectId)}` : "";
    apiGet<AgentInfo[]>(`/agents${query}`).then(
      (list) => setAgents(new Map(list.map((a) => [a.id, a.name]))),
      () => {},
    );
  }, [props.projectId]);

  const visible = items.slice(0, limit);
  const agentName = (id?: number) =>
    (id && agents.get(id)) || (id ? `Agent #${id}` : "app");

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <div className="px-4 py-3 border-b border-border flex items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-text text-sm font-bold">Inbox</h2>
            {items.length > 0 && (
              <span className="inline-flex min-w-5 items-center justify-center rounded-full bg-accent/15 px-1.5 py-0.5 text-[10px] font-bold tabular-nums text-accent">
                {items.length}
              </span>
            )}
          </div>
          <p className="text-text-dim text-[11px] mt-0.5">
            Approvals, reports, and alerts from agents
          </p>
        </div>
        <div className="flex items-center gap-3">
          <a href={PANEL_HREF} className="text-[11px] text-text-muted hover:text-text">
            Open conversations →
          </a>
          <button
            type="button"
            onClick={() => void load()}
            disabled={loading}
            className="text-[11px] text-text-muted hover:text-text disabled:opacity-40"
          >
            {loading ? "Loading…" : "Refresh"}
          </button>
        </div>
      </div>

      <div className="min-h-0 flex-1 p-3 space-y-3 overflow-auto">
        {error && (
          <div className="rounded border border-red/30 bg-red/10 px-3 py-2 text-[11px] text-red">
            {error}
          </div>
        )}
        {!loading && !error && visible.length === 0 && (
          <div className="h-full min-h-[88px] flex items-center px-1 text-xs text-text-muted">
            <div>
              <p className="font-medium">You're all caught up</p>
              <p className="mt-1 text-[11px] text-text-dim">
                Approvals, alerts, and new reports will appear here.
              </p>
            </div>
          </div>
        )}
        {visible.map((item) => (
          <InboxRow
            key={item.message.id}
            item={item}
            now={now}
            agentName={agentName(item.message.agent_id)}
            onChanged={() => void load()}
          />
        ))}
        {items.length > visible.length && (
          <a
            href={PANEL_HREF}
            className="flex min-h-9 items-center justify-center rounded-md border border-dashed border-border px-3 text-[11px] text-text-muted transition-colors hover:border-accent hover:text-text"
          >
            {items.length - visible.length} more item
            {items.length - visible.length === 1 ? "" : "s"} →
          </a>
        )}
      </div>
    </section>
  );
}
