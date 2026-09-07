import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { ConversationsClient } from "./client";

const Context = createContext<{ client: ConversationsClient; legacyDrafts: boolean } | null>(null);
export function ConversationsProvider({ conversations, children, legacyDrafts = false }: { conversations: ConversationsClient; children: ReactNode; legacyDrafts?: boolean }) {
  const value = useMemo(() => ({ client: conversations, legacyDrafts }), [conversations, legacyDrafts]);
  return <Context.Provider key={conversations.instanceKey} value={value}>{children}</Context.Provider>;
}
export function useConversationAPI() {
  const context = useContext(Context);
  if (!context) throw new Error("Conversations UI requires a ConversationsProvider");
  const { client: conversationsClient, legacyDrafts } = context;
  return { conversationsClient, legacyDrafts, apiGet: conversationsClient.apiGet, apiPost: conversationsClient.apiPost,
    apiPatch: conversationsClient.apiPatch, apiDelete: conversationsClient.apiDelete };
}
