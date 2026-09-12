import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Panel from "./ProcessesPanel";
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
const process = {
  id: "p",
  name: "Weekly review",
  description: "Review operations",
  status: "paused",
  version: 1,
  execution_mode: "agent",
  owner_agent_id: 7,
  completion_criteria: "Approved report",
  instructions: "Review",
  sync_pending: false,
};
async function mount(history: object) {
  window.history.replaceState(null, "", "/");
  globalThis.fetch = (async (url: unknown) => {
    const path = String(url).split("?")[0];
    if (path === "/api/agents")
      return Response.json([{ id: 7, name: "Owner" }]);
    if (path.endsWith("/runs")) return Response.json(history);
    if (path.endsWith("/p"))
      return Response.json({
        process,
        versions: [{ version: 1, definition: process }],
      });
    return Response.json({ processes: [process] });
  }) as typeof fetch;
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root.render(<Panel projectId="project-a" />));
  return container;
}
async function click(text: string) {
  const button = Array.from(document.querySelectorAll("button")).find(
    (b) => b.textContent?.trim() === text,
  );
  expect(button).toBeTruthy();
  await act(async () => button!.click());
}
test("execution selector defaults to direct and allows optional Tasks", async () => {
  await mount({});
  await click("+ New process");
  const select = document.querySelector<HTMLSelectElement>("#pc-mode")!;
  expect(select.value).toBe("agent");
  await act(async () => {
    select.value = "tasks";
    select.dispatchEvent(
      new window.Event("change", { bubbles: true }) as unknown as Event,
    );
  });
  expect(document.body.textContent).toContain("Requires Tasks 3.6.0");
});
test("mixed history renders direct evidence and links only Tasks records", async () => {
  await mount({
    direct_runs: [
      {
        id: "direct",
        version: 1,
        state: "completed",
        result: "Direct report approved",
        progress: 100,
        created_at: "2026-09-12T10:00:00Z",
      },
    ],
    runs: [
      {
        version: 1,
        task: {
          id: "task-1",
          title: "Older Tasks run",
          state: "completed",
          result: "Task report",
          created_at: "2026-09-11T10:00:00Z",
        },
      },
    ],
    tasks_error: "offline",
  });
  await click("Weekly review");
  await click("Runs");
  expect(document.body.textContent).toContain("Direct report approved");
  expect(document.body.textContent).toContain(
    "Tasks history unavailable: offline",
  );
  const links = document.querySelectorAll('a[href*="/apps/tasks/"]');
  expect(links.length).toBe(1);
  expect(links[0].getAttribute("href")).toContain("task-1");
});
