import { ConversationLocalizationProvider, type ConversationLocalization } from "./i18n";
import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { ConversationsClient } from "./client";

const Context = createContext<{ client: ConversationsClient; legacyDrafts: boolean } | null>(null);
export function ConversationsProvider({ conversations, children, legacyDrafts = false, locale, timeZone, messages }: ConversationLocalization & { conversations: ConversationsClient; children: ReactNode; legacyDrafts?: boolean }) {
  const value = useMemo(() => ({ client: conversations, legacyDrafts }), [conversations, legacyDrafts]);
  return <ConversationLocalizationProvider locale={locale} timeZone={timeZone} messages={messages}><Context.Provider key={conversations.instanceKey} value={value}>{children}</Context.Provider></ConversationLocalizationProvider>;
}
export function useConversationAPI() {
  const context = useContext(Context);
  if (!context) throw new Error("Conversations UI requires a ConversationsProvider");
  const { client: conversationsClient, legacyDrafts } = context;
  return { conversationsClient, legacyDrafts, apiGet: conversationsClient.apiGet, apiPost: conversationsClient.apiPost,
    apiPatch: conversationsClient.apiPatch, apiDelete: conversationsClient.apiDelete };
}
