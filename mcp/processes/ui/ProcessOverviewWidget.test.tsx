import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, { overviewLink, overviewQueue } from "./ProcessOverviewWidget";
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
test("task links target Tasks without the Processes install, assignment links target assignments", () => {
  expect(
    overviewLink(
      { projectId: "p", installId: 77 },
      { ...overviewFixture.active[0], backend: "tasks" },
    ),
  ).toBe("/apps/tasks/page?project_id=p&task_id=weather-run");
  expect(
    overviewLink(
      { projectId: "p", installId: 77 },
      overviewFixture.upcoming[0],
      true,
    ),
  ).toContain("assignment_id=barcelona");
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
