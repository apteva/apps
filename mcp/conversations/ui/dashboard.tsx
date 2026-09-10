import { ConversationLocaleRegion, type ConversationLocalization } from "../frontend/src/i18n";
import { useMemo, type ReactNode } from "react";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../frontend/src/client";
import { ConversationsProvider } from "../frontend/src/context";

/** Dashboard supplies cookies and routing; shared components own behavior. */
export function DashboardConversations({ projectId, installId, children, ...localization }: ConversationLocalization & { projectId: string; installId?: number; children: ReactNode }) {
  const conversations = useMemo(() => new AptevaClient({ baseURL: "" }).use(
    conversationsExtension({ storageKey: "dashboard" }), { projectId, installId },
  ), [projectId, installId]);
  return <ConversationsProvider legacyDrafts conversations={conversations} {...localization} key={`${projectId}:${installId ?? ""}`}><ConversationLocaleRegion>{children}</ConversationLocaleRegion></ConversationsProvider>;
}
