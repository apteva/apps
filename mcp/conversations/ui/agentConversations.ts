export function appendAgentScope(path: string, instanceId: number): string {
  if (!Number.isInteger(instanceId) || instanceId <= 0) {
    throw new Error("A target agent is required.");
  }
  return `${path}${path.includes("?") ? "&" : "?"}agent_id=${encodeURIComponent(instanceId)}`;
}

export function scopedConversationListPath(archived: boolean, instanceId: number): string {
  return appendAgentScope(`/chats${archived ? "?archived=1" : ""}`, instanceId);
}

export function fixedAgentConversationInput(instanceId: number, projectId: string, title: string) {
  if (!Number.isInteger(instanceId) || instanceId <= 0) {
    throw new Error("A target agent is required.");
  }
  return {
    agent_ids: [instanceId],
    lead_agent_id: instanceId,
    title: title.trim() || undefined,
    audience: "operator",
    project_id: projectId,
  };
}
