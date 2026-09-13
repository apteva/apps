export interface Attachment {id?:string;type:string;data_url?:string;name?:string;mime_type?:string;size?:number;file_id?:number;storage_app?:string}
export interface Conversation {
  id: string;
  project_id: string;
  lead_agent_id: number;
  lead_agent_name?: string;
  title: string;
  kind: "direct" | "room";
  origin: string;
  audience?: string;
  directive?: string;
  created_at: string;
  updated_at: string;
  archived_at?: string | null;
}

export interface CardComponent {
  app: string;
  name: string;
  props: Record<string, unknown>;
}

export interface Message {
 revision?: number;
  id: number;
  conversation_id: string;
  role: "user" | "agent" | "system";
  content: string;
  agent_id?: number;
  component_kind?: string;
  severity?: string;
  components: CardComponent[];
  attachments?: Attachment[];
  client_message_id?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

export interface ToolActivity {
  id: number; chat_id: string; agent_id: number; thread_id: string; call_id: string;
  name: string; reason: string; status: "running" | "completed" | "failed" | "interrupted";
  started_at: string; ended_at: string; revision: number;
}
export interface StreamFrame {
  tool_activity?: ToolActivity;
 after_message_id?: number;
 run_id?: string;
  chat_id: string;
  agent_id?: number;
  thread_id?: string;
  call_id: string;
  text: string;
  phase?: string;
  done: boolean;
}

export interface InboxPage {items:InboxItem[];total:number;next_cursor:string;attention:Record<string,number>}

export interface InboxItem {
  message: Message;
  priority: number;
}

export interface UnreadEntry {
  conversation_id: string;
  latest_id: number;
  unread: number;
}

export interface AgentInfo {
  id: number;
  name: string;
  status: string;
  attached: boolean;
}


export interface ChangePage { messages: Message[]; cursor: number; has_more: boolean; before: number }
export interface ConversationPage { conversations: Conversation[]; next_cursor: string }
export interface MessageDelivery { id: number; message_id: number; status: string; target: string; last_error?: string; attempts: number }
