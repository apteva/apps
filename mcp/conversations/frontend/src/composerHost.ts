/** Typed host-to-composer contract shared by every Conversations surface. */
export interface ComposerInsertOptions {
  /** Stable key supplied by the host. Repeating it is acknowledged once. */
  requestId?: string;
  projectId?: string;
  agentId?: number;
  conversationId?: string;
  /** Focus the composer after the text is applied. Defaults to true. */
  focus?: boolean;
}

export type ComposerInsertStatus =
  | "applied"
  | "already_applied"
  | "empty_text"
  | "wrong_project"
  | "wrong_agent"
  | "conversation_not_open"
  | "archived"
  | "voice_active";

export interface ComposerInsertResult {
  requestId: string;
  status: ComposerInsertStatus;
  conversationId?: string;
}

export interface ConversationComposerHandle {
  /** Append text to the current draft. This never sends the message. */
  insertText(text: string, options?: ComposerInsertOptions): Promise<ComposerInsertResult>;
}

export interface ComposerSuggestion {
  id: string;
  label: string;
  text: string;
}

export function composerRequestId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return `composer-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
