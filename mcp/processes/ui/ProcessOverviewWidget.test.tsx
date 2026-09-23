import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, {
  overviewLink,
  overviewQueue,
  executionStatus,
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
  expect(el.querySelector(".po-detail")).toBeNull();
  expect(el.querySelector("progress")).toBeNull();
  expect(el.querySelector(".po-stats")).toBeNull();
  await act(async () =>
    el.querySelector<HTMLButtonElement>(".po-row")!.click(),
  );
  expect(el.querySelector(".po-detail")!.textContent).toContain(
    "Weather agent",
  );
  expect(el.querySelector(".po-detail")!.textContent).toContain(
    "Send notification",
  );
  expect(el.querySelector(".po-detail")!.textContent).toContain(
    "1/3 steps complete",
  );
  expect(
    el.querySelector('a[href*="run_id=weather-run"]')?.getAttribute("href"),
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
  expect(el.querySelector(".po-detail")!.textContent).toContain(
    "2/3 steps complete",
  );
  await act(async () =>
    Array.from(el.querySelectorAll("button"))
      .find((b) => b.textContent === "Scheduled")!
      .click(),
  );
  expect(el.querySelectorAll(".po-row").length).toBe(1);
  expect(el.querySelector(".po-row")!.textContent).toContain("Hourly forecast");
  expect(el.querySelector(".po-detail")).toBeNull();
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
      (url) => url === "/api/apps/processes/processes/overview?install_id=88",
    ),
  ).toBe(true);
  expect(el.textContent).toContain("Alpha");
  expect(el.querySelector('select[aria-label="Filter by project"]')).not.toBeNull();
  await act(async () => {
    const select = el.querySelector<HTMLSelectElement>('select[aria-label="Filter by project"]')!;
    select.value = "project-b";
    select.dispatchEvent(new window.Event("change", { bubbles: true }) as unknown as Event);
  });
  expect(
    reads.some(
      (url) => url === "/api/apps/processes/processes/overview?project_id=project-b&install_id=88",
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
