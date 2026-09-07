export const SOFT_BREAK_INTENT = "soft_break" as const;
export const SOFT_BREAK_CONTENT = "Pause here and reconsider before continuing.";

export interface ActiveConversationResponse {
  callId: string;
  agentId?: number;
}

export interface SoftBreakMessageInput {
  content: string;
  intent: typeof SOFT_BREAK_INTENT;
  client_message_id: string;
  target_agent_ids: number[];
  target_call_id: string;
}

export function softBreakMessageInput(
  leadAgentId: number,
  response: ActiveConversationResponse,
  clientMessageId: string,
): SoftBreakMessageInput {
  const targetAgentId = response.agentId && response.agentId > 0
    ? response.agentId
    : leadAgentId;
  if (!Number.isInteger(targetAgentId) || targetAgentId <= 0) {
    throw new Error("soft break requires a target agent");
  }
  if (!response.callId) {
    throw new Error("soft break requires an active response");
  }
  if (!clientMessageId) {
    throw new Error("soft break requires an idempotency key");
  }
  return {
    content: SOFT_BREAK_CONTENT,
    intent: SOFT_BREAK_INTENT,
    client_message_id: clientMessageId,
    target_agent_ids: [targetAgentId],
    target_call_id: response.callId,
  };
}

export function isSoftBreakMetadata(metadata?: Record<string, unknown>): boolean {
  return metadata?.intent === SOFT_BREAK_INTENT;
}
