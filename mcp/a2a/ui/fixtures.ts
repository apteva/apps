// Synthetic data used only by UI tests and the isolated visual preview.
export const agents = [
  {
    id: 41,
    address: "agent:41",
    name: "Research",
    description:
      "Finds evidence, compares sources, and turns open questions into useful research briefs.",
    peer_id: "local",
    peer_name: "Northstar studio",
    kind: "local",
    status: "running",
    skills: ["web-research", "source-analysis"],
    card: {
      name: "Research",
      description: "Research and analysis",
      version: "1.0",
      skills: [
        {
          id: "web-research",
          name: "Web research",
          description: "Investigate a topic and return a brief with sources.",
          examples: ["Compare the top three suppliers for our next launch."],
        },
      ],
      defaultInputModes: ["text/plain"],
      defaultOutputModes: ["text/plain", "application/json"],
    },
  },
  {
    id: 42,
    address: "agent:42",
    name: "Operations",
    description:
      "Coordinates projects, maintains records, and keeps everyday work moving.",
    peer_id: "local",
    peer_name: "Northstar studio",
    kind: "local",
    status: "running",
    skills: ["planning", "crm"],
  },
  {
    id: 43,
    address: "agent:43",
    name: "Editorial",
    description:
      "Creates clear, consistent writing from approved research and campaign plans.",
    peer_id: "local",
    peer_name: "Northstar studio",
    kind: "local",
    status: "stopped",
    skills: ["copywriting", "editing"],
  },
  {
    address: "a2a:remote_strategy",
    name: "Strategy",
    description:
      "Reviews market opportunities and recommends the next actions for the team.",
    peer_id: "partner",
    peer_name: "Partner workspace",
    kind: "node",
    status: "cached",
    skills: ["market-analysis", "strategy"],
    fetched_at: "2026-09-15T10:20:00Z",
  },
  {
    address: "a2a:remote_finance",
    name: "Finance",
    description:
      "Models scenarios and helps quantify the commercial impact of a decision.",
    peer_id: "partner",
    peer_name: "Partner workspace",
    kind: "node",
    status: "cached",
    skills: ["forecasting", "financial-analysis"],
    fetched_at: "2026-09-15T10:20:00Z",
  },
  {
    address: "a2a:remote_weather",
    name: "Weather intelligence",
    description:
      "Forecasts and weather context for logistics and event planning.",
    peer_id: "weather",
    peer_name: "Weather intelligence",
    kind: "agent_card",
    status: "cached",
    skills: ["forecasting", "weather"],
    fetched_at: "2026-09-15T10:20:00Z",
  },
];
export const connections = [
  {
    id: "partner",
    name: "Partner workspace",
    kind: "node",
    base_url: "https://partner.example/api/apps/a2a",
    managed_by: "operator",
    authenticated: true,
    agents: ["Strategy", "Finance"],
    discover_agents: ["41", "42"],
    invoke_agents: ["41"],
    protocol_version: "1.0",
  },
  {
    id: "weather",
    name: "Weather intelligence",
    kind: "agent_card",
    base_url: "https://weather.example",
    card_url: "https://weather.example/.well-known/agent-card.json",
    managed_by: "operator",
    authenticated: false,
    agents: ["Weather intelligence"],
    protocol_version: "0.3.0",
  },
];
export const tasks = [
  {
    id: 128,
    kind: "ask",
    status: "working",
    from_agent_id: 42,
    from_agent_name: "Operations",
    to_agent_id: 41,
    to_agent_name: "Research",
    direction: "local",
    preview:
      "Compare the three shortlisted suppliers. Focus on reliability, lead times, and total cost.",
    message_count: 2,
    from_thread_id: "launch-plan",
    to_thread_id: "supplier-research",
    created_at: "2026-09-15T10:00:00Z",
    updated_at: "2026-09-15T10:25:00Z",
  },
  {
    id: 127,
    kind: "ask",
    status: "input_required",
    from_agent_id: 41,
    from_agent_name: "Research",
    to_agent_id: 0,
    to_agent_name: "Strategy",
    direction: "outbound",
    peer_id: "partner",
    preview:
      "Review the expansion brief and recommend which market we should prioritize next quarter.",
    message_count: 3,
    created_at: "2026-09-15T09:30:00Z",
    updated_at: "2026-09-15T10:15:00Z",
    last_synced_at: "2026-09-15T10:15:00Z",
  },
  {
    id: 126,
    kind: "ask",
    status: "completed",
    from_agent_id: 42,
    from_agent_name: "Operations",
    to_agent_id: 0,
    to_agent_name: "Weather intelligence",
    direction: "outbound",
    peer_id: "weather",
    preview:
      "Check the forecast for the outdoor launch event in Barcelona this Friday.",
    message_count: 2,
    created_at: "2026-09-15T09:15:00Z",
    updated_at: "2026-09-15T10:00:00Z",
    artifacts: [
      {
        name: "Event weather brief",
        description: "Forecast and planning recommendations",
        parts: [
          {
            text: "Clear skies expected. Plan for warm conditions in the afternoon.",
          },
          {
            file: {
              name: "forecast.pdf",
              uri: "https://weather.example/forecast.pdf",
            },
          },
        ],
      },
    ],
  },
  {
    id: 125,
    kind: "ask",
    status: "failed",
    from_agent_id: 41,
    from_agent_name: "Research",
    to_agent_id: 0,
    to_agent_name: "Finance",
    direction: "outbound",
    peer_id: "partner",
    preview:
      "Model the expected return for a six-month pilot across our three candidate markets.",
    message_count: 2,
    created_at: "2026-09-15T08:40:00Z",
    updated_at: "2026-09-15T09:50:00Z",
  },
  {
    id: 124,
    kind: "message",
    status: "completed",
    from_agent_id: 43,
    from_agent_name: "Editorial",
    to_agent_id: 42,
    to_agent_name: "Operations",
    direction: "local",
    preview:
      "The launch copy is ready for review. All three variants are attached to the campaign.",
    message_count: 1,
    created_at: "2026-09-15T08:15:00Z",
    updated_at: "2026-09-15T09:20:00Z",
  },
];
export function fixture(path: string) {
  const u = new URL(path, "http://localhost");
  if (u.pathname.endsWith("/network/card"))
    return {
      card: {
        ...agents[0].card,
        name: "Strategy",
        description: "Market analysis for your next decision.",
      },
    };
  if (u.pathname.endsWith("/network"))
    return {
      agents,
      node: { node_id: "node_studio", display_name: "Northstar studio" },
      warnings: [],
    };
  if (u.pathname.endsWith("/connections")) return { connections };
  if (u.pathname.endsWith("/check"))
    return {
      ok: true,
      agents: 2,
      message: "Discovery verified. Sending a task has not been tested.",
      checked_at: new Date().toISOString(),
      latency_ms: 126,
    };
  if (u.pathname.endsWith("/overview"))
    return {
      total: 128,
      active: 4,
      input_required: 1,
      attention: 2,
      failed: 1,
      completed: 121,
      pending_delivery: 0,
      as_of: new Date().toISOString(),
    };
  if (u.pathname.endsWith("/messages")) {
    const id = Number(u.pathname.split("/").at(-2));
    const task = tasks.find((t) => t.id === id) || tasks[0];
    return {
      task,
      messages: [
        {
          id: 1,
          from_agent_id: task.from_agent_id,
          body: task.preview,
          created_at: task.created_at,
        },
        {
          id: 2,
          from_agent_id: task.to_agent_id,
          body:
            task.status === "input_required"
              ? "I can compare these markets. What is the maximum budget for the initial pilot?"
              : task.status === "failed"
                ? "The financial dataset is unavailable. I could not complete this model."
                : task.status === "completed"
                  ? "The brief is ready. You can find the forecast and recommendations in the returned artifacts."
                  : "I’m reviewing delivery records and independent customer feedback for all three suppliers. I’ll include a cost comparison and highlight any operational risks.",
          status_after: task.status,
          created_at: task.updated_at,
        },
      ],
    };
  }
  const status = u.searchParams.get("status"),
    peer = u.searchParams.get("peer"),
    agent = Number(
      u.searchParams.get("agent_id") ||
        u.searchParams.get("agent_address")?.replace("agent:", ""),
    ),
    q = u.searchParams.get("q")?.toLowerCase();
  const address = u.searchParams.get("agent_address");
  const remoteName = address?.startsWith("a2a:")
    ? agents.find((a) => a.address === address)?.name
    : null;
  const filtered = tasks.filter(
    (t) =>
      (!status ||
        (status === "attention" &&
          ["failed", "input_required"].includes(t.status)) ||
        (status === "open" &&
          ["working", "input_required"].includes(t.status)) ||
        (status === "active" && t.status === "working") ||
        t.status === status) &&
      (!peer ||
        (peer === "local" ? t.direction === "local" : t.peer_id === peer)) &&
      (!remoteName ||
        t.from_agent_name === remoteName ||
        t.to_agent_name === remoteName) &&
      (!agent || t.from_agent_id === agent || t.to_agent_id === agent) &&
      (!q ||
        `${t.preview} ${t.from_agent_name} ${t.to_agent_name}`
          .toLowerCase()
          .includes(q)),
  );
  return {
    tasks: filtered.slice(
      Number(u.searchParams.get("offset") || 0),
      Number(u.searchParams.get("offset") || 0) +
        Number(u.searchParams.get("limit") || 30),
    ),
    total: filtered.length,
    offset: 0,
    limit: 30,
  };
}
