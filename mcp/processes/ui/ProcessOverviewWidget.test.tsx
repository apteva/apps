import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, {
  overviewLink,
  assignmentRunState,
  rowAgentNames,
  GLOBAL_REFRESH_MS,
  projectColor,
  overviewQueue,
  executionStatus,
  currentStepLabel,
  stepActivity,
  actionLabel,
  duration,
  type Data,
} from "./ProcessOverviewWidget";
import { overviewFixture } from "./tests/overview-fixture";
const originalFetch = globalThis.fetch;
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window,
  document: window.document,
  HTMLElement: window.HTMLElement,
  IS_REACT_ACT_ENVIRONMENT: true,
});
let root: Root;
afterEach(async () => {
  await act(async () => root?.unmount());
  document.body.innerHTML = "";
  globalThis.fetch = originalFetch;
});
test("overview shows simultaneous steps, names, schedules and scoped links; refresh clears failures", async () => {
  let data = structuredClone(overviewFixture),
    offline = false;
  const reads: string[] = [];
  globalThis.fetch = (async (input: unknown) => {
    const url = String(input);
    reads.push(url);
    if (url.startsWith("/api/agents"))
      return Response.json([{ id: 7, name: "Weather agent" }]);
    return offline
      ? new Response("offline", { status: 503 })
      : Response.json(data);
  }) as typeof fetch;
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  const render = async (revision: number) =>
    act(async () =>
      root.render(
        <Widget
          projectId="project-a"
          installId={77}
          eventRevision={revision}
        />,
      ),
    );
  await render(0);
  expect(
    reads.some(
      (u) =>
        u ===
        "/api/apps/processes/processes/overview?project_id=project-a&install_id=77",
    ),
  ).toBe(true);
  expect(el.querySelectorAll(".po-row").length).toBe(3);
  expect(document.querySelector(".po-detail")).toBeNull();
  expect(el.querySelector("progress")).toBeNull();
  expect(el.querySelector(".po-stats")).toBeNull();
  await act(async () =>
    el.querySelector<HTMLButtonElement>(".po-row")!.click(),
  );
  expect(document.querySelector(".po-detail")!.textContent).toContain(
    "Weather agent",
  );
  expect(document.querySelector(".po-detail")!.textContent).toContain(
    "Send notification",
  );
  expect(document.querySelector(".po-detail")!.textContent).toContain(
    "1/3 steps complete",
  );
  expect(
    document.querySelector('a[href*="run_id=weather-run"]')?.getAttribute("href"),
  ).toContain(
    "/apps/processes/page?project_id=project-a&install_id=77&process_id=weather&run_id=weather-run",
  );
  offline = true;
  await render(1);
  expect(el.querySelector('[role="alert"]')?.textContent).toContain(
    "last received snapshot",
  );
  offline = false;
  data.active[0].steps_completed = 2;
  await render(2);
  expect(el.querySelector('[role="alert"]')).toBeNull();
  expect(document.querySelector(".po-detail")!.textContent).toContain(
    "2/3 steps complete",
  );
  const summary = el.querySelector(".po-event-summary")!;
  expect(summary.textContent).toContain("1 assignment waiting for events");
  expect(summary.textContent).toContain("1 need attention");
  await act(async () => (summary as HTMLButtonElement).click());
  expect(el.querySelector('.po-filters button[aria-pressed="true"]')!.textContent).toBe("Assignments");
  const rows = Array.from(el.querySelectorAll(".po-assignment-row"));
  expect(rows.length).toBe(2);
  expect(rows[0].textContent).toContain("Hourly forecast");
  expect(rows[0].textContent).toContain("Every hour");
  expect(rows[0].getAttribute("href")).toContain("assignment_id=barcelona");
  expect(rows[1].textContent).toContain("Conversation message received");
  expect(rows[1].textContent).toContain("not listening");
  expect(rows[1].textContent).toContain("Never run");
  expect(document.querySelector(".po-detail")).toBeNull();
  expect(reads.filter((u) => u.startsWith("/api/agents")).length).toBe(1);
});
test("run links stay in Processes and assignment links target assignments", () => {
  expect(
    overviewLink(
      { projectId: "p", installId: 77 },
      { ...overviewFixture.active[0], backend: "tasks" },
    ),
  ).toBe("/apps/processes/page?project_id=p&install_id=77&process_id=weather&run_id=weather-run");
  expect(
    overviewLink(
      { projectId: "p", installId: 77 },
      { ...overviewFixture.upcoming[0], project_id: "other-project" },
      true,
    ),
  ).toContain("project_id=other-project");
  expect(
    overviewLink(
      { projectId: "p", installId: 77 },
      overviewFixture.upcoming[0],
      true,
    ),
  ).toContain("assignment_id=barcelona");
});

test("global overview loads without a project, labels results and filters by visible project", async () => {
  const data: Data = {
    ...structuredClone(overviewFixture),
    scope: "global",
    projects: [
      { id: "project-a", name: "Alpha" },
      { id: "project-b", name: "Beta" },
    ],
  };
  data.active[0].project_id = "project-a";
  data.active[0].project_name = "Alpha";
  data.active[0].process_name = "Alpha process";
  data.upcoming[0].project_id = "project-b";
  data.upcoming[0].project_name = "Beta";
  data.active[0].agent_name = "Alpha writer";
  data.assignments![0] = { ...data.assignments![0], project_id: "project-a", project_name: "Alpha", agent_name: "Alpha writer" };
  const reads: string[] = [];
  globalThis.fetch = (async (input: unknown) => {
    const url = String(input);
    reads.push(url);
    if (url.startsWith("/api/agents")) return Response.json([]);
    return Response.json(data);
  }) as typeof fetch;
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  await act(async () =>
    root.render(
      <Widget dashboardScope="global" installId={88} />,
    ),
  );
  expect(
    reads.some(
      (url) => url === "/api/apps/processes/processes/overview?scope=global&install_id=88",
    ),
  ).toBe(true);
  // No project to list agents from: names come from the rows.
  expect(reads.some((url) => url.startsWith("/api/agents"))).toBe(false);
  expect(el.textContent).toContain("Alpha");
  await act(async () => {
    Array.from(el.querySelectorAll("button")).find((b) => b.textContent === "Assignments")!.click();
  });
  const row = el.querySelector(".po-assignment-row")!;
  expect(row.textContent).toContain("Alpha writer");
  // Links carry the row's own project, not the (empty) widget project.
  expect(row.getAttribute("href")).toContain("project_id=project-a");
  expect(el.querySelector('select[aria-label="Filter by project"]')).not.toBeNull();
  await act(async () => {
    const select = el.querySelector<HTMLSelectElement>('select[aria-label="Filter by project"]')!;
    select.value = "project-b";
    select.dispatchEvent(new window.Event("change", { bubbles: true }) as unknown as Event);
  });
  expect(
    reads.some(
      (url) => url === "/api/apps/processes/processes/overview?scope=global&project_id=project-b&install_id=88",
    ),
  ).toBe(true);
});

test("All orders running then schedules then blocked/outcomes, preserves settings and bounds legacy limits", async () => {
  const data = structuredClone(overviewFixture);
  data.active.unshift({
    ...data.active[0],
    id: "blocked",
    state: "blocked",
    created_at: "2026-09-14T00:00:00Z",
  });
  expect(overviewQueue(data).map((x) => x.item.state)).toEqual([
    "running",
    "scheduled",
    "blocked",
    "completed",
  ]);
  expect(
    overviewQueue(data, { show_active: false }).map((x) => x.item.state),
  ).toEqual(["scheduled", "completed"]);
  data.recent = Array.from({ length: 12 }, (_, i) => ({
    ...data.recent[0],
    id: `old-${i}`,
  }));
  globalThis.fetch = (async (input: unknown) =>
    Response.json(
      String(input).startsWith("/api/agents") ? [] : data,
    )) as typeof fetch;
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  await act(async () => root.render(<Widget projectId="test" />));
  expect(el.querySelectorAll(".po-row").length).toBe(4);
  await act(async () =>
    root.render(
      <Widget projectId="test" widgetSettings={{ recent_limit: 12 }} />,
    ),
  );
  expect(el.querySelectorAll(".po-row").length).toBe(6);
});

test("right column uses live step state and accounts for parallel work, review and terminal outcomes", () => {
  const run = structuredClone(overviewFixture.active[0]);
  const entry = { item: run, assignment: false, group: "running" as const };
  expect(executionStatus(entry)).toMatchObject({
    label: "Post conversation",
    state: "running",
    extra: 0,
  });
  run.steps[2].state = "running";
  expect(executionStatus(entry)).toMatchObject({ state: "running", extra: 1 });
  run.state = "blocked";
  expect(executionStatus(entry).state).toBe("blocked");
  run.state = "completed";
  expect(executionStatus(entry)).toMatchObject({
    label: "Finished",
    state: "completed",
    extra: 0,
  });
  run.state = "running";
  run.steps = run.steps.slice(0, 2);
  run.steps[1].executor = { kind: "human", agent_id: 0 };
  run.steps[1].state = "ready";
  expect(executionStatus(entry)).toMatchObject({
    label: "Post conversation",
    state: "ready",
  });
  run.state = "blocked";
  run.steps[1].state = "blocked";
  expect(executionStatus(entry).state).toBe("blocked");
});

test("timed executions appear with schedules and retain a run link", () => {
  const data: Data = structuredClone(overviewFixture);
  data.active[0].state = "scheduled";
  data.active[0].steps = [{ id: "follow-up", name: "Send follow-up", state: "scheduled", progress: 0, kind: "work", origin: "process_step", executor: { kind: "agent", agent_id: 7 }, start_at: new Date(Date.now() + 600_000).toISOString() }];
  const entry = overviewQueue(data).find(x => x.item.id === data.active[0].id)!;
  expect(entry.group).toBe("scheduled");
  expect(entry.assignment).toBe(false);
  expect(executionStatus(entry).label).toContain("Starts in 10 min");
  expect(overviewLink({ projectId: "project-a" }, entry.item)).toContain("run_id=");
});
test("running rows show live activity; details open in a modal with the step's activity; finished rows show the summary", async () => {
  const data = structuredClone(overviewFixture) as any;
  const step = data.active[0].steps[1];
  Object.assign(step, { thread_id: "worker-2", execution_id: "exe-2" });
  data.recent[0].summary = "Sent the forecast to 3 subscribers.";
  const telemetry = [
    { id: "t1", thread_id: "worker-2", type: "llm.thinking", time: "2026-09-13T17:00:01Z", data: { execution_id: "exe-2", iteration: 1, text: "Checking the conversation first." } },
    { id: "t2", thread_id: "worker-2", type: "llm.done", time: "2026-09-13T17:00:02Z", data: { execution_id: "exe-2", iteration: 1 } },
    { id: "t3", thread_id: "worker-2", type: "tool.call", time: "2026-09-13T17:00:03Z", data: { execution_id: "exe-2", id: "c1", name: "conversations_post", reason: "Post the forecast" } },
  ];
  const reads: string[] = [];
  globalThis.fetch = (async (input: unknown) => {
    const url = String(input);
    reads.push(url);
    if (url.startsWith("/api/agents")) return Response.json([{ id: 7, name: "Weather agent" }]);
    if (url.startsWith("/api/telemetry")) return Response.json(telemetry);
    return Response.json(data);
  }) as typeof fetch;
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  await act(async () => root.render(<Widget projectId="project-a" installId={77} />));
  await act(async () => new Promise((r) => setTimeout(r, 20)));
  expect(reads.some((u) => u.includes("/api/telemetry?") && u.includes("thread_id=worker-2"))).toBe(true);
  const row = el.querySelector<HTMLButtonElement>('.po-row[data-state="running"]')!;
  const line = row.querySelector(".po-live")!;
  expect(line.querySelector(".po-live-text")!.textContent).toBe("Post the forecast");
  expect(line.querySelector(".po-marker")!.textContent).toBe("Running");
  expect(line.getAttribute("title")).toContain("“Checking the conversation first.”");
  expect(row.querySelectorAll(".po-summary").length).toBe(0);
  expect(row.querySelector(".po-title-step")!.textContent).toBe("› Post conversation");
  const segments = [...row.querySelectorAll(".po-stepbar > span")].map((s) => s.getAttribute("data-state"));
  expect(segments).toEqual(data.active[0].steps.map((s: any) => s.state));
  expect(currentStepLabel([{ id: "a", name: "Read CRM", state: "running" }, { id: "b", name: "Read deals", state: "running" }, { id: "c", name: "Send", state: "pending" }] as any)).toBe("Read CRM +1");
  expect(currentStepLabel([{ id: "a", name: "Done", state: "completed" }] as any)).toBe("");
  expect(row.querySelector('[role="progressbar"]')!.getAttribute("aria-valuenow")).toBe("1");
  expect(el.querySelector('.po-row[data-state="completed"]')!.textContent).toContain("Sent the forecast to 3 subscribers.");
  await act(async () => row.click());
  await act(async () => new Promise((r) => setTimeout(r, 20)));
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(el.contains(dialog)).toBe(false);
  expect(dialog.querySelector('[aria-pressed="true"]')!.textContent).toContain("Post conversation");
  const feed = dialog.querySelector(".po-feed")!.textContent!;
  expect(feed).toContain("Checking the conversation first.");
  expect(feed).toContain("Post the forecast");
  expect(feed).toContain("conversations post");
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Close details");
  await act(async () => document.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape" }) as any));
  expect(document.querySelector('[role="dialog"]')).toBeNull();
});

test("step activity keeps finished thoughts, labels actions and times them", () => {
  const t = (s: number) => new Date(Date.UTC(2026, 10, 24, 8, 33, 0) + s * 1000).toISOString();
  const x = { execution_ids: ["exe-1"] };
  const events = [
    { id: "1", type: "llm.start", time: t(0.6), data: { iteration: 1 } },
    { id: "2", type: "llm.done", time: t(5.7), data: { iteration: 1, message: "I'll read the CRM totals." } },
    { id: "3", type: "tool.call", time: t(5.7), data: { execution_ids: [], id: "a", name: "search_tools", reason: "Finding CRM statistics", args: {} } },
    { id: "4", type: "tool.result", time: t(5.7), data: { execution_ids: [], id: "a", name: "search_tools", duration_ms: 1, success: true } },
    { id: "5", type: "llm.start", time: t(6), data: { iteration: 2 } },
    { id: "6", type: "llm.done", time: t(12.2), data: { iteration: 2 } },
    { id: "7", type: "tool.call", time: t(12.2), data: { ...x, id: "b", name: "crm_contacts_search", reason: "Reading contact count", args: { limit: 1 } } },
    { id: "8", type: "tool.result", time: t(12.5), data: { ...x, id: "b", duration_ms: 271, success: true, result: "{\"total\":66}" } },
    { id: "9", type: "tool.call", time: t(30), data: { ...x, id: "c", name: "processes_step_update", args: { state: "completed" } } },
    { id: "10", type: "tool.result", time: t(30.1), data: { ...x, id: "c", duration_ms: 91, success: true } },
    { id: "11", type: "llm.start", time: t(30.1), data: { iteration: 3 } },
    { id: "12", type: "llm.cancelled", time: t(30.1), data: { iteration: 3 } },
  ];
  const target = { agentID: 1, threadID: "w", executionID: "exe-1", stepID: "s1", completedAt: t(30.1), label: "Get CRM stats" };
  const a = stepActivity(events as any, target);
  expect(a.thought).toBe("I'll read the CRM totals.");
  expect(a.thinkingSince).toBeUndefined();
  expect(a.actions.map((c) => c.label)).toEqual(["Finding CRM statistics", "Reading contact count", "Completed step"]);
  expect(a.actions[1].durationMs).toBe(271);
  expect(a.toolMs).toBe(363);
  expect(a.modelMs).toBe(11300);
  expect(a.turns.length).toBe(2);
  const live = stepActivity(events.slice(0, 5) as any, { ...target, completedAt: undefined });
  expect(live.thinkingSince).toBe(t(6));
  expect(actionLabel("processes_step_update", "", { state: "blocked" })).toBe("Reported blocker");
  expect(actionLabel("search_tools", "")).toBe("Looked up tools");
  expect([duration(0), duration(91), duration(271), duration(5081), duration(29000), duration(75000)]).toEqual(["<0.01s", "0.09s", "0.3s", "5.1s", "29s", "1m 15s"]);
});

test("assignment badge names the open run's real state", () => {
  const base = { id: "a", process_id: "p", process_name: "P", name: "A", agent_id: 7, start: "event" as const, triggers: [], ongoing_count: 0, needs_attention: false };
  expect(assignmentRunState(base)).toBeNull();
  expect(assignmentRunState({ ...base, ongoing_count: 4, live_run_id: "r", live_run_state: "blocked" })).toEqual({ state: "blocked", label: "Blocked" });
  expect(assignmentRunState({ ...base, ongoing_count: 1, live_run_id: "r", live_run_state: "running" })).toEqual({ state: "running", label: "Running" });
  expect(assignmentRunState({ ...base, ongoing_count: 1, live_run_id: "r", live_run_state: "waiting" })?.label).toBe("Waiting");
  // A 0.25.0 backend sends no state: never claim Running.
  expect(assignmentRunState({ ...base, ongoing_count: 2, live_run_id: "r" })?.label).toBe("2 open runs");
});

test("global rows name their agents and the widget refreshes itself", () => {
  const data: Data = structuredClone(overviewFixture);
  data.active[0].agent_name = "Alpha writer";
  data.assignments![1] = { ...data.assignments![1], agent_id: 9, agent_name: "Beta triage" };
  expect(rowAgentNames(data)).toEqual({ 7: "Alpha writer", 9: "Beta triage" });
  expect(GLOBAL_REFRESH_MS).toBe(15000);
});

test("project colour comes from the dashboard and falls back to none", () => {
  const projects = [
    { id: "project-a", name: "Alpha", color: "#e11d48" },
    { id: "project-b", name: "Beta", color: "" },
    { id: "project-c", name: "Gamma", color: "red;background:url(x)" },
  ];
  expect(projectColor({ widgetContext: { projects } }, "project-a")).toBe("#e11d48");
  expect(projectColor({ widgetContext: { projects } }, "project-b")).toBe("");
  expect(projectColor({ widgetContext: { projects } }, "project-c")).toBe("");
  expect(projectColor({}, "project-a")).toBe("");
  expect(projectColor({ widgetContext: {} }, "project-a")).toBe("");
});

async function renderGlobal(widgetContext?: { projects?: Array<{ id: string; name?: string; color?: string }> }) {
  const data: Data = {
    ...structuredClone(overviewFixture),
    scope: "global",
    projects: [
      { id: "project-a", name: "Alpha" },
      { id: "project-b", name: "Beta" },
    ],
  };
  data.active[0] = { ...data.active[0], project_id: "project-a", project_name: "Alpha" };
  data.upcoming[0] = { ...data.upcoming[0], project_id: "project-b", project_name: "Beta" };
  globalThis.fetch = (async (_input: unknown) => Response.json(data)) as typeof fetch;
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  await act(async () => root.render(<Widget dashboardScope="global" installId={88} widgetContext={widgetContext} />));
  return el;
}

test("All projects rows show a coloured dot next to the project name", async () => {
  const el = await renderGlobal({ projects: [{ id: "project-a", name: "Alpha", color: "#e11d48" }, { id: "project-b", name: "Beta", color: "" }] });
  const alpha = el.querySelector('.po-queue .po-project[data-project="project-a"]')!;
  expect(alpha.textContent).toBe("Alpha");
  expect((alpha.querySelector(".po-project-dot") as HTMLElement).style.backgroundColor).toBeTruthy();
  // Empty colour: name only, neutral as before.
  const beta = el.querySelector('.po-queue .po-project[data-project="project-b"]')!;
  expect(beta.textContent).toBe("Beta");
  expect(beta.querySelector(".po-project-dot")).toBeNull();
});

test("All projects rows keep project names on dashboards without colours", async () => {
  const el = await renderGlobal(undefined);
  const tags = Array.from(el.querySelectorAll(".po-queue .po-project"));
  expect(tags.map((t) => t.textContent)).toEqual(["Alpha", "Beta"]);
  expect(el.querySelector(".po-project-dot")).toBeNull();
});

test("project widget shows no project tags", async () => {
  globalThis.fetch = (async (_input: unknown) => Response.json(structuredClone(overviewFixture))) as typeof fetch;
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  await act(async () => root.render(<Widget projectId="project-a" widgetContext={{ projects: [{ id: "project-a", color: "#e11d48" }] }} />));
  expect(el.querySelector(".po-project")).toBeNull();
});
