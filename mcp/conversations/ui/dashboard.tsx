import type { ComposerOptions } from "../frontend/src/composer";
import { conversationStyleId, conversationStyles } from "./styles";
import { ConversationLocaleRegion, type ConversationLocalization } from "../frontend/src/i18n";
import { useMemo, type ReactNode } from "react";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../frontend/src/client";
import { ConversationsProvider } from "../frontend/src/context";

/** Dashboard supplies cookies and routing; shared components own behavior. */
export function DashboardConversations({ projectId, installId, dashboardScope = "project", children, ...localization }: ConversationLocalization & { composer?:ComposerOptions; projectId?: string; installId?: number; dashboardScope?: "project" | "global"; children: ReactNode }) {
  const global = dashboardScope === "global";
  const conversations = useMemo(() => new AptevaClient({ baseURL: "" }).use(
    conversationsExtension({ storageKey: "dashboard", global }), { projectId: global ? undefined : projectId, installId },
  ), [global, projectId, installId]);
  return <ConversationsProvider key={`${dashboardScope}:${projectId ?? ""}:${installId ?? ""}`} legacyDrafts conversations={conversations} {...localization}><ConversationLocaleRegion><style href={conversationStyleId} precedence="apteva-app">{conversationStyles}</style>{children}</ConversationLocaleRegion></ConversationsProvider>;
}
