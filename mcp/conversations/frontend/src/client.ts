import { defineAppExtension, type AppHandle, type SubscribeOptions } from "@apteva/web-sdk";
import type { Attachment, AgentInfo, ChangePage, Conversation, ConversationPage, InboxPage, Message, MessageDelivery, ToolActivity, StreamFrame, UnreadEntry } from "./types";

export interface CreateConversation {
  title?: string;
  agent_ids: number[];
  lead_agent_id: number;
  audience?: "operator" | "public";
  conversation_key?: string;
  directive?: string;
}
export interface SendMessage {
  content: string;
  client_message_id: string;
  attachments?: Message["attachments"];
  intent?: "soft_break";
  target_agent_ids?: number[];
  target_call_id?: string;
}
export interface ListConversations {
  agent_id?: number;
  lead_agent_id?: number;
  archived?: boolean;
  query?: string;
  cursor?: string;
  limit?: number;
}
export interface ConversationSubscription {
  onMessage?: (message: Message) => void;
  onFrame?: (frame: StreamFrame) => void;
  onResync?: () => void;
  onOpen?: () => void;
  onError?: (error: unknown) => void;
  signal?: AbortSignal;
}
const query = (path: string, values: Record<string, string | number | boolean | undefined>) => {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined) params.set(key, typeof value === "boolean" ? (value ? "1" : "0") : String(value));
  }
  return `${path}?${params}`;
};

// getRandomValues also works on local HTTP dashboards without randomUUID.
const instanceKey = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2,"0")).join("");

/** App-owned contract. AppHandle supplies authentication, routing and cancellation. */
export class ConversationsClient {
  readonly instanceKey = instanceKey();
  readonly projectId: string;
  readonly installId: number | undefined;
  readonly storageKey: string;

  constructor(readonly app: AppHandle, storageKey?: string, readonly audience?: "public" | "operator") {
    if (app.name !== "conversations" || !app.projectId) throw new Error("Conversations requires a project-scoped conversations app handle");
    this.projectId = app.projectId;
    this.installId = app.installId;
    // Hosts may provide a stable, non-secret user key. Default isolates client instances.
    this.storageKey = `${app.projectId}:${app.installId ?? "default"}:${storageKey ?? instanceKey()}`;
  }

  list = (options: ListConversations = {}, init?: RequestInit) =>
    this.app.get<ConversationPage>(query("/chats", { ...options, page: 1 }), init);
  get = (id: string, init?: RequestInit) => this.app.get<Conversation>(query("/chats", { id }), init);
  create = (input: CreateConversation, init?: RequestInit) =>
    this.app.post<Conversation>("/chats", { ...input, audience: this.audience ?? input.audience, project_id: this.projectId }, init);
  update = (id: string, input: { title?: string; directive?: string; archived?: boolean }, init?: RequestInit) =>
    this.app.patch<Conversation>(query("/chats", { id }), input, init);
  remove = (id: string, init?: RequestInit) => this.app.del(query("/chats", { id }), init);
  history = (id: string, options: { before?: number; limit?: number } = {}, init?: RequestInit) =>
    this.app.get<ChangePage>(query("/messages", { chat_id: id, page: 1, ...options }), init);
  changes = (id: string, cursor: number, init?: RequestInit) =>
    this.app.get<ChangePage>(query("/changes", { chat_id: id, cursor }), init);
  send = (id: string, input: SendMessage, init?: RequestInit) =>
    this.app.post<Message>(query("/messages", { chat_id: id }), input, init);
  upload = (chat:string,id:string,name:string,content_base64:string) => this.app.post<Attachment>(query("/attachments",{chat_id:chat}),{id,name,content_base64});
  attachment = (chat:string,id:string) => this.app.get<{attachment:Attachment;content_base64:string}>(query("/attachments",{chat_id:chat,id}));
  markSeen = (id: string, lastSeenId: number, init?: RequestInit) =>
    this.app.post("/seen", { chat_id: id, last_seen_id: lastSeenId }, init);
  unread = (agentId?: number, init?: RequestInit) =>
    this.app.get<UnreadEntry[]>(query("/unread-summary", { agent_id: agentId }), init);
  activity = (id: string, init?: RequestInit) => this.app.get<ToolActivity[]>(query("/activity", { chat_id: id }), init);
  agents = (init?: RequestInit) => this.app.get<AgentInfo[]>("/agents", init);
  inbox = (options: { agent_id?: number; cursor?: string; limit?: number } = {}, init?: RequestInit) =>
    this.app.get<InboxPage>(query("/inbox", { page: 1, ...options }), init);
  act = (messageId: number, actionId: string, note = "", init?: RequestInit) =>
    this.app.post<{ message: Message }>("/message-action", { message_id: messageId, action_id: actionId, note }, init);
  dismiss = (messageId: number, init?: RequestInit) => this.app.post("/message-dismiss", { message_id: messageId }, init);
  deliveries = (id: string, init?: RequestInit) => this.app.get<MessageDelivery[]>(query("/deliveries", { chat_id: id }), init);
  retryDelivery = (id: number, init?: RequestInit) => this.app.post("/delivery-failures", { id }, init);

  subscribe = (id: string, handlers: ConversationSubscription) => {
    const options: SubscribeOptions = {
      eventTypes: ["message", "stream", "resync"],
      signal: handlers.signal,
      onOpen: handlers.onOpen,
      onError: handlers.onError,
    };
    // REST change pages own the durable cursor; ephemeral SSE IDs never advance it.
    return this.app.subscribe<Message | StreamFrame>(query("/stream", { chat_id: id }), (event, meta) => {
      if (meta.event === "resync") handlers.onResync?.();
      else if (meta.event === "stream") {
        if ((event as StreamFrame).chat_id === id) handlers.onFrame?.(event as StreamFrame);
      } else if ((event as Message).conversation_id === id) handlers.onMessage?.(event as Message);
    }, options);
  };

  // Shared adapter for administrative UI routes; scope comes exclusively from the handle.
  private checkProject(projectId: string) {
    if (projectId !== this.projectId) throw new Error("Conversation project does not match the host scope");
  }
  apiGet = <T>(path: string, projectId = this.projectId): Promise<T> => {
    this.checkProject(projectId); return this.app.get<T>(path);
  };
  apiPost = <T>(path: string, body: unknown, projectId = this.projectId): Promise<T> => {
    this.checkProject(projectId);
    if (path === "/chats" && this.audience && body && typeof body === "object") body = { ...body, audience: this.audience };
    return this.app.post<T>(path, body);
  };
  apiPatch = <T>(path: string, body: unknown, projectId = this.projectId): Promise<T> => {
    this.checkProject(projectId); return this.app.patch<T>(path, body);
  };
  apiDelete = <T>(path: string, projectId = this.projectId): Promise<T> => {
    this.checkProject(projectId); return this.app.del<T>(path);
  };
}

export function conversationsExtension(options: { storageKey?: string; audience?: "public" | "operator" } = {}) {
  return defineAppExtension({ app: "conversations", create: ({ app }) => new ConversationsClient(app, options.storageKey, options.audience) });
}
