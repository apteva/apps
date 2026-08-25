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

export function agentConversationWidgetLayout(wide: boolean) {
  return {
    gridTemplateColumns: wide ? "250px minmax(0,1fr)" : "minmax(0,1fr)",
    gridTemplateRows: wide
      ? "minmax(0,1fr)"
      : "minmax(150px,34%) minmax(0,1fr)",
  };
}
