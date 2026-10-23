import { useEffect } from "react";

/** Structural subset of the shared AppHost contract. No runtime UI-kit import
 * or unreleased SDK dependency is needed to run on older hosts. */
export interface ConversationHost {
  capabilities: { ui: { chatAssistantLauncher: boolean } };
  ui: {
    request(settings: { chatAssistant: { launcher: "auto" | "hidden_on_compact" | "hidden" } }): () => void;
  };
}

// A mounted conversation pane owns this request. Returning to the mobile list
// unmounts that pane and releases it; selecting a saved ID alone does not hide
// the launcher. The host owns viewport and foreground eligibility decisions.
export function useConversationHost(host?: ConversationHost) {
  useEffect(() => {
    if (!host?.capabilities.ui.chatAssistantLauncher) return;
    return host.ui.request({ chatAssistant: { launcher: "hidden_on_compact" } });
  }, [host]);
}
