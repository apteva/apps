import { useEffect, useState } from "react";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";

interface ActivitySummary {
  active_conversation_ids: string[];
}

// One small request serves the whole list, including conversations whose
// transcript is not open. Pausing while hidden avoids unnecessary polling.
export function useConversationActivity(projectId: string, agentId?: number, enabled = true): ReadonlySet<string> {
  const { apiGet } = useConversationAPI();
  const [active, setActive] = useState<ReadonlySet<string>>(() => new Set());

  useEffect(() => {
    setActive(new Set());
    if (!projectId || !enabled) return;
    let cancelled = false;
    let pending = false;
    let lastSuccess = 0;
    const path = agentId ? `/activity-summary?agent_id=${encodeURIComponent(agentId)}` : "/activity-summary";
    const refresh = async () => {
      if (cancelled || pending || document.hidden) return;
      pending = true;
      try {
        const summary = await apiGet<ActivitySummary>(path, projectId);
        if (!cancelled) {
          lastSuccess = Date.now();
          setActive(new Set(Array.isArray(summary.active_conversation_ids) ? summary.active_conversation_ids : []));
        }
      } catch {
        // A transient request must not leave a thread glowing indefinitely.
        if (!cancelled && Date.now() - lastSuccess > 10_000) setActive(new Set());
      } finally {
        pending = false;
      }
    };
    const onVisibility = () => {
      if (document.hidden) setActive(new Set());
      else void refresh();
    };
    void refresh();
    const timer = window.setInterval(refresh, 2_500);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [apiGet, projectId, agentId, enabled]);

  return active;
}

export function ConversationActivityIndicator({ active }: { active: boolean }) {
  const { t } = useConversationLocalization();
  if (!active) return null;
  const label = t("chat.working");
  return <span role="img" aria-label={label} title={label} className="chat-thread-working-dot inline-block h-2 w-2 shrink-0 rounded-full bg-accent" />;
}
