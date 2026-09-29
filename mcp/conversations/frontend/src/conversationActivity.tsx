import { useEffect, useState } from "react";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";
import type { StreamFrame } from "./types";

export function applyConversationActivityFrame(current: ReadonlySet<string>, frame: StreamFrame): ReadonlySet<string> {
  if (!frame.snapshot) return current;
  const active = new Set(current);
  const frames = Array.isArray(frame.frames) ? frame.frames : [];
  if (!frame.chat_id) {
    return new Set(frames.filter(item => item.response_progress?.phase !== "idle" && item.response_progress).map(item => item.chat_id));
  }
  if (frames.some(item => item.response_progress && item.response_progress.phase !== "idle")) active.add(frame.chat_id);
  else active.delete(frame.chat_id);
  return active;
}

// The existing user-scoped Conversations SSE stream serves the entire list.
// Its progress snapshots are authoritative on reconnect and on completion.
export function useConversationActivity(projectId: string, agentId?: number, enabled = true): ReadonlySet<string> {
  const { conversationsClient } = useConversationAPI();
  const [active, setActive] = useState<ReadonlySet<string>>(() => new Set());

  useEffect(() => {
    setActive(new Set());
    if (!projectId || !enabled) return;
    let closed = false;
    let subscription: ReturnType<typeof conversationsClient.subscribeActivity> | undefined;
    let staleTimer: ReturnType<typeof setTimeout> | undefined;
    const clearStaleTimer = () => {
      if (staleTimer !== undefined) clearTimeout(staleTimer);
      staleTimer = undefined;
    };
    const open = () => {
      if (closed || document.hidden || subscription) return;
      subscription = conversationsClient.subscribeActivity(agentId, {
        onFrame: frame => setActive(current => applyConversationActivityFrame(current, frame)),
        onOpen: clearStaleTimer,
        onError: () => {
          clearStaleTimer();
          staleTimer = setTimeout(() => setActive(new Set()), 10_000);
        },
      });
    };
    const onVisibility = () => {
      if (document.hidden) {
        subscription?.close();
        subscription = undefined;
        clearStaleTimer();
        setActive(new Set());
      } else open();
    };
    open();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      closed = true;
      subscription?.close();
      clearStaleTimer();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [conversationsClient, projectId, agentId, enabled]);

  return active;
}

export function ConversationActivityIndicator({ active }: { active: boolean }) {
  const { t } = useConversationLocalization();
  if (!active) return null;
  const label = t("chat.working");
  return <span role="img" aria-label={label} title={label} className="chat-thread-working-dot inline-block h-2 w-2 shrink-0 rounded-full bg-accent" />;
}
