import type { KeyboardEvent, ReactNode, RefObject } from "react";

export interface ConversationChatViewProps {
  title: string;
  subtitle: string;
  publicAudience: boolean;
  connected: boolean;
  archived: boolean;
  messageNodes: ReactNode;
  hasMessages: boolean;
  streamNode: ReactNode;
  bottomRef: RefObject<HTMLDivElement | null>;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  draft: string;
  sending: boolean;
  responseActive: boolean;
  breakBusy: boolean;
  breakRequested: boolean;
  sendError: string;
  archiveBusy: boolean;
  confirmDelete: boolean;
  onDraftChange: (value: string, element: HTMLTextAreaElement) => void;
  onComposerKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onSend: () => void;
  onSoftBreak: () => void;
  onOpenDetails?: () => void;
  onUnarchive: () => void;
  onRequestDelete: () => void;
  onCancelDelete: () => void;
  onDelete: () => void;
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

const GLYPH_CHAT =
  "M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z";
const GLYPH_MORE = "M12 12h.01 M19 12h.01 M5 12h.01";
const GLYPH_RESTORE = "M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8 M3 3v5h5";
const GLYPH_TRASH =
  "M3 6h18 M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6 M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2";
const GLYPH_PAUSE = "M9 5v14 M15 5v14";

/**
 * Shared durable-chat surface used by the full Conversations panel and every
 * focused Conversations widget. Transport and persistence remain in the
 * Conversations controller; this component is the single transcript,
 * streaming slot, and composer implementation.
 */
export default function ConversationChatView(props: ConversationChatViewProps) {
  return (
    <section className="min-h-0 flex-1 flex flex-col">
      <div className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            <h2 className="text-sm font-semibold text-text truncate">{props.title}</h2>
            {props.publicAudience && (
              <span className="px-1.5 py-0.5 rounded text-xs bg-accent/15 border border-accent/30 text-accent shrink-0">
                public
              </span>
            )}
          </div>
          <p className="text-xs text-text-muted truncate">{props.subtitle}</p>
        </div>
        <span
          className={`ml-auto shrink-0 w-2 h-2 rounded-full ${props.connected ? "bg-success" : "bg-border"}`}
          title={props.connected ? "Live" : "Reconnecting — the 5s poll keeps history current"}
        />
        {!props.archived && props.onOpenDetails && (
          <button
            type="button"
            onClick={props.onOpenDetails}
            className="shrink-0 inline-flex h-8 w-8 items-center justify-center rounded text-text-muted hover:bg-bg-input hover:text-text"
            aria-label="Conversation details"
            title="Details"
          >
            <Glyph d={GLYPH_MORE} size={16} />
          </button>
        )}
      </div>

      <div className="flex-1 min-h-0 overflow-auto p-4 flex flex-col gap-3">
        {!props.hasMessages && !props.streamNode ? (
          <div className="flex-1 flex flex-col items-center justify-center gap-3 text-text-muted">
            <span className="text-text-dim">
              <Glyph d={GLYPH_CHAT} size={32} />
            </span>
            <p className="text-sm">No messages yet — say something.</p>
          </div>
        ) : (
          <>
            {props.messageNodes}
            {props.streamNode}
          </>
        )}
        <div ref={props.bottomRef} />
      </div>

      {props.archived ? (
        <footer className="shrink-0 border-t border-border p-3 flex items-center gap-2">
          <span className="text-xs text-text-muted">Archived conversation — read only.</span>
          <span className="ml-auto flex items-center gap-2">
            <button
              type="button"
              onClick={props.onUnarchive}
              disabled={props.archiveBusy}
              className="inline-flex items-center gap-1.5 rounded border border-border px-2.5 py-1.5 text-xs text-text-muted hover:text-text disabled:opacity-40"
            >
              <Glyph d={GLYPH_RESTORE} size={13} />
              Unarchive
            </button>
            {props.confirmDelete ? (
              <>
                <button
                  type="button"
                  onClick={props.onDelete}
                  disabled={props.archiveBusy}
                  className="rounded bg-error px-2.5 py-1.5 text-xs font-semibold text-bg disabled:opacity-40"
                >
                  Confirm delete
                </button>
                <button
                  type="button"
                  onClick={props.onCancelDelete}
                  disabled={props.archiveBusy}
                  className="rounded border border-border px-2.5 py-1.5 text-xs text-text-muted hover:text-text"
                >
                  Cancel
                </button>
              </>
            ) : (
              <button
                type="button"
                onClick={props.onRequestDelete}
                disabled={props.archiveBusy}
                className="inline-flex items-center gap-1.5 rounded border border-border px-2.5 py-1.5 text-xs text-error hover:bg-bg-input disabled:opacity-40"
              >
                <Glyph d={GLYPH_TRASH} size={13} />
                Delete
              </button>
            )}
          </span>
        </footer>
      ) : (
        <footer className="chat-composer-safe shrink-0 px-2 pt-2 pb-2 sm:px-5">
          {props.sendError && <p className="mx-1 mb-1 text-xs text-error">{props.sendError}</p>}
          {props.responseActive && (
            <div className="mb-2 flex justify-center">
              <button
                type="button"
                onClick={props.onSoftBreak}
                disabled={props.breakBusy || props.breakRequested}
                className="inline-flex items-center gap-1.5 rounded-full border border-border bg-bg-card px-3 py-1.5 text-xs text-text-muted transition-colors enabled:hover:border-accent/50 enabled:hover:text-text disabled:cursor-default disabled:opacity-60"
                aria-label="Ask the agent to pause and reconsider"
                title="Sends a new request; it does not stop the agent or cancel running work"
              >
                <Glyph d={GLYPH_PAUSE} size={13} />
                {props.breakRequested ? "Break requested" : props.breakBusy ? "Requesting break…" : "Break"}
              </button>
            </div>
          )}
          <form
            onSubmit={(event) => {
              event.preventDefault();
              props.onSend();
            }}
            className="flex min-h-[54px] items-center gap-1.5 rounded-lg border border-border bg-bg-card/95 px-2 py-1.5 shadow-lg backdrop-blur-sm transition-colors focus-within:border-accent/60 sm:min-h-[58px] sm:gap-3 sm:px-4 sm:py-2"
          >
            <textarea
              ref={props.inputRef}
              value={props.draft}
              onChange={(event) => props.onDraftChange(event.target.value, event.target)}
              onKeyDown={props.onComposerKeyDown}
              rows={1}
              style={{ lineHeight: "20px", minHeight: "36px" }}
              placeholder={props.connected ? "Message the agent…" : "Reconnecting — messages still send"}
              className="block min-w-0 flex-1 resize-none bg-transparent py-2 text-base text-text placeholder:text-text-dim focus:outline-none sm:text-sm"
              autoFocus={
                typeof window !== "undefined" &&
                window.matchMedia("(hover: hover) and (pointer: fine)").matches
              }
            />
            <button
              type="submit"
              disabled={props.sending || !props.draft.trim()}
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
    </section>
  );
}
