import { useState } from "react";
import type { Message } from "./types";
import { useConversationLocalization } from "./i18n";
import { reportSectionsText } from "./messageContent";
import createDOMPurify from "dompurify";
import { marked } from "marked";
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

const GLYPH_ALERT = "M12 9v4 M12 17h.01 M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z";
const GLYPH_REPORT = "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8";
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!card) return null;
  const status = String(card.props.status ?? "pending");
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
    <div className="rounded-md border border-border bg-bg-card p-3">
      <div className="flex items-center gap-2 text-xs text-text-muted">
        <span className="text-warn">
          <Glyph d={GLYPH_ALERT} size={14} />
        </span>
        <span className="font-semibold uppercase tracking-wide">{t("card.approval")}</span>
      </div>
      <p className="mt-1.5 text-sm font-medium text-text">{String(card.props.title ?? "")}</p>
      {card.props.body ? (
        <p className="mt-1 text-sm text-text-muted whitespace-pre-wrap">{String(card.props.body)}</p>
      ) : null}
      {status !== "pending" && (
        <div className="mt-2.5">
          <span className={`inline-flex max-w-full items-start gap-1.5 rounded px-2.5 py-1.5 text-xs font-semibold ${approvalActionColors(selectedAction?.style, selectedIndex)}`}>
            <svg className="shrink-0 mt-0.5" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="12" cy="12" r="9" />
              <path d="m8 12 3 3 5-6" />
            </svg>
            <span className="min-w-0 whitespace-pre-wrap break-words">
              {t("approval.decision", { decision: selectedAction?.label || statusLabel(status) })}
            </span>
          </span>
        </div>
      )}
      {status === "pending" && (
        <div className="mt-2.5 flex flex-col gap-2">
          <input
            type="text"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder={t("approval.notePlaceholder")}
            className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs text-text"
          />
          <div className="flex items-center gap-2">
            {actions.map((a, index) => (
              <button
                key={a.id}
                type="button"
                disabled={busy}
                onClick={() => act(a.id)}
                className={`px-3 py-1.5 rounded text-xs font-semibold disabled:opacity-50 ${approvalActionColors(a.style, index)} ${
                  a.style === "primary"
                    ? "hover:opacity-90"
                    : a.style === "danger" || (!a.style && index === 0)
                      ? "hover:bg-accent/10"
                      : "hover:bg-bg-input"
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
        <p className="mt-1.5 text-xs text-text-dim">{t("approval.note")} {String(card.props.note)}</p>
      ) : null}
    </div>
  );
}

export function ReportCard({ message }: { message: Message }) {
  const { t } = useConversationLocalization();
  const card = message.components.find((c) => c.name === "report-card");
  if (!card) return null;
  return (
    <div className="rounded-md border border-border bg-bg-card p-3">
      <div className="flex items-center gap-2 text-xs text-text-muted">
        <span className="text-info">
          <Glyph d={GLYPH_REPORT} size={14} />
        </span>
        <span className="font-semibold uppercase tracking-wide">{t("card.report")}</span>
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

export function AlertCard({ message }: { message: Message }) {
  const { statusLabel } = useConversationLocalization();
  const card = message.components.find((c) => c.name === "alert-card");
  if (!card) return null;
  const severity = String(card.props.severity ?? "info");
  const tone =
    severity === "error" ? "text-error" : severity === "warn" ? "text-warn" : "text-info";
  return (
    <div className="rounded-md border border-border bg-bg-card p-3">
      <div className={`flex items-center gap-2 text-xs ${tone}`}>
        <Glyph d={GLYPH_ALERT} size={14} />
        <span className="font-semibold uppercase tracking-wide">{statusLabel(severity)}</span>
      </div>
      <p className="mt-1.5 text-sm text-text whitespace-pre-wrap">{String(card.props.text ?? "")}</p>
    </div>
  );
}

