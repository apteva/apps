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
window.fetch = (async (url: unknown, init?: RequestInit) => {
  const path = String(url).split("?")[0];
  if (path === "/api/agents")
    return Response.json(
      location.search.includes("no_agents")
        ? []
        : [{ id: 7, name: "Weather agent" }],
    );
  if (init?.method === "PUT" || init?.method === "POST") {
    const body = JSON.parse(String(init.body));
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
    return Response.json({ direct_runs: [], runs: [] });
  if (path.endsWith("/assignments")) return Response.json({ assignments: [] });
  if (path.endsWith("/weather"))
    return Response.json({
      process,
      versions: [{ version: process.version, definition: process }],
    });
  return Response.json({ processes: [process] });
}) as typeof fetch;
createRoot(document.getElementById("root")!).render(<Panel projectId="test" />);
