import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, { overviewLink } from "./ProcessOverviewWidget";
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
  expect(el.textContent).toContain("Weather agent");
  expect(el.textContent).toContain("Post conversation");
  expect(el.textContent).toContain("Send notification");
  expect(
    el.querySelector('a[href*="run_id=weather-run"]')?.getAttribute("href"),
  ).toContain(
    "/apps/processes/page?project_id=project-a&install_id=77&process_id=weather&run_id=weather-run",
  );

  expect(el.querySelector("details summary")?.textContent).toContain("1/3");
  await act(async () =>
    Array.from(el.querySelectorAll("button"))
      .find((b) => b.textContent?.startsWith("Scheduled"))
      ?.click(),
  );
  expect(el.textContent).toContain("Hourly forecast");
  expect(el.querySelector(".react-flow")).toBeNull();
  offline = true;
  await render(1);
  expect(el.querySelector('[role="alert"]')?.textContent).toContain(
    "last received snapshot",
  );
  offline = false;
  data.active[0].steps_completed = 2;
  await render(2);
  expect(el.querySelector('[role="alert"]')).toBeNull();
  await act(async () =>
    Array.from(el.querySelectorAll("button"))
      .find((b) => b.textContent?.startsWith("Active"))
      ?.click(),
  );
  expect(el.querySelector("summary")?.textContent).toContain("2/3");
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
