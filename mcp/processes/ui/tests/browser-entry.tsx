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
let processVersions = JSON.parse(sessionStorage.getItem("process-versions") || "null") || [{version:process.version,definition:structuredClone(process)}];
const controlFixture = location.search.includes("step_control");
let controlRun: any;
if (controlFixture) {
  process.status = "active";
  process.assignments = [
    {
      id: "manual",
      process_id: "weather",
      revision: 1,
      name: "Manual test",
      owner_agent_id: 7,
      procedure_version: 1,
      follow_latest: true,
      parameters: {},
      status: "active",
      sync_pending: false,
    },
  ];
}
const missingAgent = location.search.includes("missing_agent");
if (missingAgent) {
  process.assignments = [
    {
      id: "barcelona",
      process_id: "weather",
      revision: 4,
      name: "Barcelona weather",
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

if (location.search.includes("activity")) {
  const listeners = new Set<(event:any)=>void>();
  (window as any).__aptevaTelemetryBus = {subscribe:(_id:number, fn:(event:any)=>void)=>{listeners.add(fn);return ()=>listeners.delete(fn);}};
  (window as any).__emitWorkerTelemetry = (event:any)=>listeners.forEach(fn=>fn(event));
  process.approval_requirements = "Separate operator approval is required before notification.";
  process.completion_criteria = "Keep the exact receipt after explicit approval.";
}
const originalFetch = window.fetch.bind(window);
window.fetch = (async (url: unknown, init?: RequestInit) => {
  const path = String(url).split("?")[0];
  if(location.search.includes("compact_history") && path.startsWith("/api/apps/processes/processes"))return originalFetch(String(url),init);
  if (
    path === "/api/apps/processes/processes" &&
    location.search.includes("map")
  )
    return originalFetch("/fixture/map-processes", init);
  if (path.endsWith("/runs") && location.search.includes("map"))
    return originalFetch(`/fixture/map-runs/${path.split("/").at(-2)}`, init);
  if (path.endsWith("/overview")) return originalFetch(String(url), init);
  if (path === "/api/telemetry") return originalFetch("/fixture/telemetry",init);
  if (path === "/api/apps") return Response.json([{name:"processes",display_name:"Processes",icon:"/fixture/process-icon.svg",icon_style:"monochrome",surfaces:{mcp_tool_names:["processes_step_claim"]}}]);
  if (path === "/api/connections") return Response.json([]);
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
    const lifecycle = path.match(/\/processes\/weather\/(activate|pause|draft)$/)?.[1];
    if (lifecycle) {
      process.status = lifecycle === "activate" ? "active" : lifecycle === "pause" ? "paused" : "draft";
      sessionStorage.setItem("process", JSON.stringify(process));
      return Response.json(process);
    }
    if (controlFixture && path.endsWith("/start")) {
      sessionStorage.setItem("submitted-start", JSON.stringify(body));
      controlRun = {
        id: "controlled-run",
        process_id: "weather",
        version: 1,
        workflow: true,
        state: "waiting",
        created_at: new Date().toISOString(),
        control_mode: body.control_mode,
        waiting_for_advance: true,
        assignment: process.assignments[0],
        steps: ["alpha", "beta", "approve"].map((key, i) => ({
          id: key,
          run_id: "controlled-run",
          key,
          definition: {
            ...step(key, key, i === 2 ? ["alpha", "beta"] : []),
            role: i === 2 ? "operator" : "worker",
          },
          executor: {
            kind: i === 2 ? "human" : "agent",
            agent_id: i === 2 ? undefined : 7,
          },
          state: i === 2 ? "pending" : "ready",
          progress: 0,
          output: "",
          error: "",
        })),
        eligible_steps: [
          { id: "alpha", key: "alpha" },
          { id: "beta", key: "beta" },
        ],
      };
      return Response.json({ run: controlRun });
    }
    if (controlFixture && path.endsWith("/advance")) {
      const releases = JSON.parse(sessionStorage.getItem("releases") || "[]");
      releases.push(body);
      sessionStorage.setItem("releases", JSON.stringify(releases));
      const selected = controlRun.steps.find((s: any) => s.id === body.step_id);
      selected.released_at = new Date().toISOString();
      selected.state =
        selected.executor.kind === "human" ? "waiting" : "completed";
      selected.output =
        selected.executor.kind === "human"
          ? ""
          : `Exact receipt ${selected.id}.png`;
      controlRun.eligible_steps = controlRun.eligible_steps.filter(
        (s: any) => s.id !== selected.id,
      );
      if (
        controlRun.steps.slice(0, 2).every((s: any) => s.state === "completed")
      ) {
        const human = controlRun.steps[2];
        if (human.state === "pending") {
          human.state = "ready";
          controlRun.eligible_steps.push({ id: human.id, key: human.key });
        }
      }
      return Response.json({ step_id: selected.id });
    }
    if (controlFixture && path.includes("/steps/approve")) {
      sessionStorage.setItem("approval", JSON.stringify(body));
      controlRun.steps[2].state = "completed";
      controlRun.steps[2].output = body.output;
      controlRun.state = "completed";
      return Response.json({});
    }
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
    if (location.search.includes("fail_save") && init?.method === "PUT" && !sessionStorage.getItem("save-failed-once")) {
      sessionStorage.setItem("save-failed-once", "true");
      return new Response("Temporary save failure", {status:500});
    }
    if (init?.method === "PUT" && body.expected_version !== process.version) return new Response("procedure changed; reload before saving", {status:409});
    sessionStorage.setItem(
      "submitted-definition",
      JSON.stringify(body.definition),
    );
    process = {
      ...(init?.method === "POST"
        ? { id: "weather", status: "draft", assignments: [] }
        : process),
      ...body.definition,
      ...(body.save_as_draft ? {status:"draft"} : {}),
      version: process.version + 1,
    };
    processVersions.push({version:process.version,definition:structuredClone(process)});
    sessionStorage.setItem("process-versions",JSON.stringify(processVersions));
    sessionStorage.setItem("process", JSON.stringify(process));
    return Response.json(process);
  }
  if (controlFixture && path.endsWith("/runs"))
    return Response.json({
      direct_runs: controlRun ? [controlRun] : [],
      runs: path.endsWith("/processes/runs") && controlRun ? [controlRun] : [],
    });
  if (path.endsWith("/runs")) {
    if (!location.search.includes("live")) return Response.json({direct_runs: [], runs: []});
    const response = await originalFetch("/fixture/runs");
    if (!path.endsWith("/processes/runs")) return response;
    const fixture = await response.json();
    return Response.json({runs: [...fixture.runs, ...fixture.direct_runs]});
  }
  if (path.endsWith("/assignments"))
    return Response.json({ assignments: process.assignments });
  if (path.endsWith("/weather"))
    return Response.json({
      process,
      versions: processVersions,
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
