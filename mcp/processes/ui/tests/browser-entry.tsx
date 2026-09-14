import { useState } from "react";
import Widget from "../ProcessOverviewWidget";
import { createRoot } from "react-dom/client";
import Panel from "../ProcessesPanel";

const step = (key: string, name: string, depends_on: string[]) => ({
  key,
  name,
  depends_on,
  role: "weather_agent",
  kind: "work",
  instructions: `${name} using the configured city and the previous step output.`,
  expected_output: "Timestamped report and delivery confirmation.",
});
let process = JSON.parse(sessionStorage.getItem("process") || "null") || {
  id: "weather",
  name: "Hourly weather alerts",
  description:
    "Fetch current weather every hour, then send the report in Conversations and through Pushover.",
  status: "draft",
  version: 1,
  execution_mode: "agent",
  owner_agent_id: 7,
  instructions: "Follow the three steps in dependency order.",
  completion_criteria: "Fresh weather and confirmed notifications.",
  steps: [
    step("fetch_weather", "Fetch current weather", []),
    step("post_conversations", "Post alert in Conversations", [
      "fetch_weather",
    ]),
    step("send_pushover", "Send Pushover notification", [
      "fetch_weather",
      "post_conversations",
    ]),
  ],
  assignments: [],
};
const missingAgent = location.search.includes("missing_agent");
if (missingAgent) {
  process.assignments = [
    {
      id: "barcelona",
      process_id: "weather",
      revision: 4,
      name: "Barcelona weather",
      target: "Barcelona, Spain",
      owner_agent_id: 1104,
      execution_mode: "agent",
      procedure_version: 1,
      follow_latest: true,
      parameters: { city: "Barcelona" },
      schedule: { kind: "cron", cron: "0 * * * *", timezone: "Europe/Madrid" },
      status: "paused",
      sync_pending: false,
      ...(location.search.includes("pinned_role")
        ? { roles: { weather_agent: { kind: "agent", agent_id: 1104 } } }
        : {}),
    },
  ];
}
const originalFetch = window.fetch.bind(window);
window.fetch = (async (url: unknown, init?: RequestInit) => {
  const path = String(url).split("?")[0];
  if (
    path === "/api/apps/processes/processes" &&
    location.search.includes("map")
  )
    return originalFetch("/fixture/map-processes", init);
  if (path.endsWith("/runs") && location.search.includes("map"))
    return originalFetch(`/fixture/map-runs/${path.split("/").at(-2)}`, init);
  if (path.endsWith("/overview")) return originalFetch(String(url), init);
  if (path === "/api/agents")
    return Response.json(
      location.search.includes("no_agents")
        ? []
        : missingAgent
          ? [{ id: 1105, name: "My Test Agent First" }]
          : [
              {
                id: 7,
                name: location.search.includes("long_agent")
                  ? "Barcelona weather operations coordinator"
                  : "Weather agent",
              },
            ],
    );
  if (init?.method === "PUT" || init?.method === "POST") {
    const body = JSON.parse(String(init.body));
    if (path.endsWith("/assignments/barcelona") && body.assignment) {
      if (
        body.assignment.owner_agent_id !== 1105 ||
        Object.values(body.assignment.roles || {}).some(
          (x: any) => x.kind === "agent" && x.agent_id !== 1105,
        )
      )
        return new Response("agent not found or not owned by this user", {
          status: 403,
        });
      sessionStorage.setItem("submitted-assignment", JSON.stringify(body));
      process.assignments = [
        { ...body.assignment, revision: body.expected_revision + 1 },
      ];
      return Response.json(process.assignments[0]);
    }
    if (!body.definition) throw new Error("Unexpected fixture write");
    sessionStorage.setItem(
      "submitted-definition",
      JSON.stringify(body.definition),
    );
    process = {
      ...(init?.method === "POST"
        ? { id: "weather", status: "draft", assignments: [] }
        : process),
      ...body.definition,
      version: process.version + 1,
    };
    sessionStorage.setItem("process", JSON.stringify(process));
    return Response.json(process);
  }
  if (path.endsWith("/runs"))
    return location.search.includes("live")
      ? originalFetch("/fixture/runs")
      : Response.json({ direct_runs: [], runs: [] });
  if (path.endsWith("/assignments"))
    return Response.json({ assignments: process.assignments });
  if (path.endsWith("/weather"))
    return Response.json({
      process,
      versions: [{ version: process.version, definition: process }],
    });
  return Response.json({ processes: [process] });
}) as typeof fetch;
function WidgetFixture() {
  const [revision, setRevision] = useState(0);
  return (
    <div
      style={{
        maxWidth: location.search.includes("full") ? 1000 : 460,
        margin: 12,
      }}
    >
      <button id="host-revision" onClick={() => setRevision((v) => v + 1)}>
        Simulate host event
      </button>
      <Widget
        projectId="test"
        installId={77}
        eventRevision={revision}
        widgetSize={location.search.includes("full") ? "full" : "half"}
      />
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  location.search.includes("widget") ? (
    <WidgetFixture />
  ) : (
    <Panel projectId="test" installId={77} />
  ),
);
