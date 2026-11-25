import { useMemo, useState, type ReactNode } from "react";
import type { Message } from "./types";
import { useCodeBlockLabels, useConversationLocalization } from "./i18n";
import { reportSectionsText } from "./messageContent";
import { renderSafeMarkdown } from "./markdown";
import { alertParts } from "./cardParts";

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
      className="shrink-0"
    >
      <path d={d} />
    </svg>
  );
}

export const GLYPH_ALERT = "M12 9v4 M12 17h.01 M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z";
export const GLYPH_INFO = "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M12 16v-4 M12 8h.01";
const GLYPH_CRITICAL = "M7.86 2h8.28L22 7.86v8.28L16.14 22H7.86L2 16.14V7.86z M12 8v4 M12 16h.01";
export const GLYPH_APPROVAL = "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4";
const GLYPH_REPORT = "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8";
export const GLYPH_QUESTION = "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M9.1 9a3 3 0 0 1 5.8 1c0 2-3 3-3 3 M12 17h.01";
const GLYPH_SOURCE = "M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71 M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71";
const GLYPH_CHEVRON = "m6 9 6 6 6-6";
const GLYPH_CHECK = "M20 6 9 17l-5-5";

export type CardTone = "info" | "warn" | "error" | "accent" | "neutral";

// Literal class names so the stylesheet build keeps every tone.
const TONE_TEXT: Record<CardTone, string> = {
  info: "text-info", warn: "text-warn", error: "text-error", accent: "text-accent", neutral: "text-text-muted",
};
const TONE_EDGE: Record<CardTone, string> = {
  info: "bg-info", warn: "bg-warn", error: "bg-error", accent: "bg-accent", neutral: "bg-border-strong",
};

/** The frame every conversation card shares: message width, the host's
 * card surface, radius and shadow, a kind label and an optional tone edge. */
export function CardShell({
  kind, tone = "neutral", icon, label, meta, title, edge = false, footer, children, className = "", componentId,
}: {
  kind: string;
  componentId?: string;
  tone?: CardTone;
  icon: string;
  label: ReactNode;
  meta?: ReactNode;
  title?: ReactNode;
  edge?: boolean;
  footer?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <section
      data-card={kind}
      data-component-id={componentId}
      className={`conversation-card relative w-full max-w-2xl min-w-0 overflow-hidden rounded-lg border border-border bg-bg-card ${className}`}
    >
      {edge && <span aria-hidden="true" className={`absolute inset-y-0 left-0 w-[3px] ${TONE_EDGE[tone]}`} />}
      <div className="px-4 py-3">
        <div className="flex min-w-0 items-center gap-1.5 text-xs font-medium">
          <span className={`inline-flex min-w-0 items-center gap-1.5 ${TONE_TEXT[tone]}`}>
            <Glyph d={icon} size={14} />
            <span className="truncate">{label}</span>
          </span>
          {meta ? <span className="ml-auto min-w-0 truncate font-normal text-text-dim">{meta}</span> : null}
        </div>
        {title ? <h3 className="mt-1.5 text-[15px] sm:text-sm font-semibold leading-snug text-text break-words">{title}</h3> : null}
        {children}
        {footer ? <div className="mt-3 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 text-xs text-text-dim">{footer}</div> : null}
      </div>
    </section>
  );
}

/** Agent-written card text renders like the agent's replies. */
export function CardMarkdown({ source, muted = false, className = "" }: { source: string; muted?: boolean; className?: string }) {
  const codeLabels = useCodeBlockLabels();
  const html = useMemo(() => (source.trim() ? renderSafeMarkdown(source, codeLabels) : ""), [source, codeLabels]);
  if (!html) return null;
  return (
    <div
      className={`chat-md conversation-card-body mt-1 text-[15px] sm:text-sm leading-relaxed break-words ${muted ? "text-text-muted" : "text-text"} ${className}`}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

function CardTime({ value }: { value: string }) {
  const { relativeTime, dateTime } = useConversationLocalization();
  const date = new Date(value);
  if (!value || !Number.isFinite(date.getTime())) return null;
  return (
    <time dateTime={date.toISOString()} title={dateTime(date)} className="tabular-nums whitespace-nowrap">
      {relativeTime(date)}
    </time>
  );
}

function DismissControl({ dismissed, onDismiss }: { dismissed: boolean; onDismiss?: () => Promise<void> }) {
  const { t } = useConversationLocalization();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (dismissed) {
    return (
      <span className="ml-auto inline-flex items-center gap-1 text-text-dim">
        <Glyph d={GLYPH_CHECK} size={12} />
        {t("card.dismissed")}
      </span>
    );
  }
  if (!onDismiss) return null;
  return (
    <span className="ml-auto inline-flex items-center gap-2">
      {error && <span role="alert" className="text-error">{error}</span>}
      <button
        type="button"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setError("");
          try { await onDismiss(); } catch (err) { setError(err instanceof Error ? err.message : String(err)); } finally { setBusy(false); }
        }}
        className="rounded-md border border-border px-2.5 py-1 text-xs text-text-muted hover:border-border-strong hover:bg-bg-hover hover:text-text focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50"
      >
        {t("common.dismiss")}
      </button>
    </span>
  );
}

function SourceRef({ source }: { source: string }) {
  const { t } = useConversationLocalization();
  if (!source) return null;
  const link = /^https?:\/\//i.test(source);
  return (
    <span className="inline-flex min-w-0 max-w-full items-center gap-1" title={source}>
      <Glyph d={GLYPH_SOURCE} size={12} />
      <span className="shrink-0">{t("card.source")}</span>
      {link
        ? <a href={source} target="_blank" rel="noopener noreferrer" className="min-w-0 truncate text-accent hover:underline">{source.replace(/^https?:\/\//i, "")}</a>
        : <code className="min-w-0 truncate font-mono text-[11px] text-text-muted">{source}</code>}
    </span>
  );
}

function approvalActionColors(style: string | undefined, index: number) {
  if (style === "primary") return "border border-accent bg-accent text-bg";
  if (style === "danger" || (!style && index === 0)) return "border border-accent text-accent";
  return "border border-border text-text";
}

export function ApprovalCard({
  message,
  onAction,
}: {
  message: Message;
  onAction: (messageId: number, actionId: string, note: string) => Promise<void>;
}) {
  const { t, statusLabel } = useConversationLocalization();
  const card = message.components.find((c) => c.name === "approval-card");
  const [note, setNote] = useState("");
  const [noting, setNoting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!card) return null;
  const status = String(card.props.status ?? "pending");
  const pending = status === "pending";
  const actions = Array.isArray(card.props.actions)
    ? (card.props.actions as Array<{ id: string; label: string; style?: string }>)
    : [];
  const selectedIndex = actions.findIndex(action => action.id === status);
  const selectedAction = actions[selectedIndex];

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
    <CardShell
      kind="approval"
      tone={pending ? "accent" : "neutral"}
      edge={pending}
      icon={GLYPH_APPROVAL}
      label={pending ? t("card.approvalNeeded") : t("card.approval")}
      title={String(card.props.title ?? "")}
      footer={<CardTime value={message.created_at} />}
    >
      <CardMarkdown source={String(card.props.body ?? "")} muted />
      {!pending && (
        <div className="mt-2.5">
          <span className={`inline-flex max-w-full items-start gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-semibold ${approvalActionColors(selectedAction?.style, selectedIndex)}`}>
            <svg className="shrink-0 mt-0.5" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="12" cy="12" r="9" />
              <path d="m8 12 3 3 5-6" />
            </svg>
            <span className="min-w-0 whitespace-pre-wrap break-words">
              {t("approval.decision", { decision: selectedAction?.label || statusLabel(status) })}
            </span>
          </span>
          {card.props.note ? (
            <p className="mt-1.5 text-xs text-text-dim break-words">{t("approval.note")} {String(card.props.note)}</p>
          ) : null}
        </div>
      )}
      {pending && (
        <div className="mt-3 flex flex-col gap-2">
          {noting && (
            <input
              type="text"
              value={note}
              autoFocus
              aria-label={t("approval.addNote")}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t("approval.notePlaceholder")}
              className="min-w-0 rounded-md border border-border bg-bg-input px-2.5 py-1.5 text-sm text-text focus-visible:border-accent focus-visible:outline-none"
            />
          )}
          <div className="flex flex-wrap items-center gap-2">
            {actions.map((a, index) => (
              <button
                key={a.id}
                type="button"
                disabled={busy}
                onClick={() => act(a.id)}
                className={`px-3 py-1.5 rounded-md text-xs font-semibold disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent ${approvalActionColors(a.style, index)} ${
                  a.style === "primary"
                    ? "hover:opacity-90"
                    : a.style === "danger" || (!a.style && index === 0)
                      ? "hover:bg-accent/10"
                      : "hover:bg-bg-hover"
                }`}
              >
                {a.label}
              </button>
            ))}
            {!noting && (
              <button
                type="button"
                onClick={() => setNoting(true)}
                className="ml-auto rounded-md px-1.5 py-1 text-xs text-text-dim hover:text-text focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
              >
                {t("approval.addNote")}
              </button>
            )}
          </div>
          {error && <p role="alert" className="text-xs text-error">{error}</p>}
        </div>
      )}
    </CardShell>
  );
}

export function ReportCard({ message, onDismiss }: { message: Message; onDismiss?: () => Promise<void> }) {
  const { t } = useConversationLocalization();
  const card = message.components.find((c) => c.name === "report-card");
  const sections = card ? reportSectionsText(card.props.sections) : "";
  // Short reports read in place; long ones fold behind "Show full report".
  const long = sections.length > 700 || sections.split("\n").length > 14;
  const [open, setOpen] = useState(!long);
  if (!card) return null;
  return (
    <CardShell
      kind="report"
      tone="info"
      icon={GLYPH_REPORT}
      label={t("card.report")}
      meta={card.props.period ? String(card.props.period) : undefined}
      title={String(card.props.title ?? "")}
      footer={<>
        {sections && long ? (
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpen(value => !value)}
            className="inline-flex items-center gap-1 rounded-md text-xs font-medium text-accent hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
          >
            {t(open ? "card.hideReport" : "card.showReport")}
            <span className={open ? "rotate-180" : ""}><Glyph d={GLYPH_CHEVRON} size={12} /></span>
          </button>
        ) : null}
        <CardTime value={message.created_at} />
        <DismissControl dismissed={card.props.dismissed === true} onDismiss={onDismiss} />
      </>}
    >
      <CardMarkdown source={String(card.props.summary ?? "")} muted />
      {sections && open && (
        <CardMarkdown source={sections} className="mt-3 border-t border-border pt-3" />
      )}
    </CardShell>
  );
}

const SEVERITY: Record<string, { tone: CardTone; icon: string; label: "card.severity.info" | "card.severity.warn" | "card.severity.error" | "card.severity.critical" }> = {
  info: { tone: "info", icon: GLYPH_INFO, label: "card.severity.info" },
  warn: { tone: "warn", icon: GLYPH_ALERT, label: "card.severity.warn" },
  warning: { tone: "warn", icon: GLYPH_ALERT, label: "card.severity.warn" },
  error: { tone: "error", icon: GLYPH_ALERT, label: "card.severity.error" },
  critical: { tone: "error", icon: GLYPH_CRITICAL, label: "card.severity.critical" },
};

export function alertSeverity(value: unknown) {
  return SEVERITY[String(value ?? "info").toLowerCase()] ?? SEVERITY.info;
}

export function AlertCard({ message, onDismiss }: { message: Message; onDismiss?: () => Promise<void> }) {
  const { t } = useConversationLocalization();
  const card = message.components.find((c) => c.name === "alert-card");
  if (!card) return null;
  const severity = alertSeverity(card.props.severity ?? message.severity);
  const parts = alertParts(String(card.props.text ?? message.content ?? ""), String(card.props.title ?? ""));
  return (
    <CardShell
      kind="alert"
      tone={severity.tone}
      edge
      icon={severity.icon}
      label={t(severity.label)}
      title={parts.title || undefined}
      footer={<>
        <SourceRef source={parts.source} />
        <CardTime value={message.created_at} />
        <DismissControl dismissed={card.props.dismissed === true} onDismiss={onDismiss} />
      </>}
    >
      <CardMarkdown source={parts.body} muted={Boolean(parts.title)} />
    </CardShell>
  );
}
