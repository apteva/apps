import type { ReactNode } from "react";
import AgentWidget from "./AgentConversationsWidget";
import { ConversationChat as Thread, type Conversation } from "./ConversationsPanel";
import { ConversationsProvider } from "./context";
import type { ConversationsClient } from "./client";
export { ConversationsProvider } from "./context";

export interface ChatProps {
  conversations: ConversationsClient;
  agentId: number;
  showNewConversation?: boolean;
  className?: string;
}
function Surface({ conversations, children, className = "" }: { conversations: ConversationsClient; children: ReactNode; className?: string }) {
  return <div className={`apteva-conversations ${className}`} style={{height:"100%",minHeight:0}}>
    <ConversationsProvider conversations={conversations} key={conversations.storageKey}>{children}</ConversationsProvider>
  </div>;
}
export function ConversationChat({ conversations, agentId, showNewConversation = true, className }: ChatProps) {
  return <Surface conversations={conversations} className={className}><AgentWidget
    appName="conversations" projectId={conversations.projectId} installId={conversations.installId ?? 0}
    instanceId={agentId} widgetSettings={{display_mode:"single",show_new_conversation:showNewConversation}}
  /></Surface>;
}
export function AgentConversations({ conversations, agentId, showNewConversation = true, className }: ChatProps) {
  return <Surface conversations={conversations} className={className}><AgentWidget
    appName="conversations" projectId={conversations.projectId} installId={conversations.installId ?? 0}
    instanceId={agentId} widgetSettings={{display_mode:"browser",show_new_conversation:showNewConversation}}
  /></Surface>;
}
export function ConversationThread({ conversations, conversation, onChanged = () => {} }: {
  conversations: ConversationsClient; conversation: Conversation; onChanged?: () => void;
}) {
  if (conversation.project_id !== conversations.projectId) throw new Error("Conversation project does not match the host scope");
  return <Surface conversations={conversations}><Thread key={conversation.id} conversation={conversation}
    archived={Boolean(conversation.archived_at)} onActed={onChanged} onRemoved={onChanged}/></Surface>;
}

import { useEffect, useState } from "react";
import InboxWidget from "./InboxWidget";
import Panel, { ApprovalCard as ApprovalView, ReportCard as ReportView, AlertCard as AlertView } from "./ConversationsPanel";
import type { Message } from "./types";
export function Inbox({ conversations, agentId, conversationsHref, eventRevision, size = "full" }: {
  conversations: ConversationsClient; agentId?: number; conversationsHref?: string; eventRevision?: number; size?: "half" | "full";
}) {
  return <Surface conversations={conversations}><InboxWidget key={agentId} agentId={agentId} projectId={conversations.projectId} installId={conversations.installId}
    conversationsHref={conversationsHref} eventRevision={eventRevision} widgetSize={size}/></Surface>;
}
export function ConversationsPanel({ conversations, agentId }: { conversations: ConversationsClient; agentId?: number }) {
  return <Surface conversations={conversations}><Panel appName="conversations" projectId={conversations.projectId}
    installId={conversations.installId ?? 0} instanceId={agentId}/></Surface>;
}
export function ApprovalCard({ conversations, message, onChanged }: { conversations: ConversationsClient; message: Message; onChanged?: (message: Message) => void }) {
  const [current,setCurrent]=useState(message);
  useEffect(()=>setCurrent(message),[message]);
  return <Surface conversations={conversations}><ApprovalView message={current} onAction={async(id,action,note)=>{
    const result=await conversations.act(id,action,note);setCurrent(result.message);onChanged?.(result.message);
  }}/></Surface>;
}
export function ReportCard({message}:{message:Message}) {return <div className="apteva-conversations"><ReportView message={message}/></div>;}
export function AlertCard({message}:{message:Message}) {return <div className="apteva-conversations"><AlertView message={message}/></div>;}

/** Register these local exports under the existing manifest names. Inject the
 * trusted conversations client separately from message/settings props. */
export const conversationsComponents = {
  "agent-conversations": AgentConversations,
  "inbox-overview": Inbox,
};
