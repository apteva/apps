import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { TaskDetails, useTasks, type Task } from "./taskShared";

const originalFetch = globalThis.fetch;
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, { window, document: window.document, HTMLElement: window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
let root: Root | undefined;
const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
const fixture: Task = { id: "old", agent_id: 7, project_id: "a", title: "Old", state: "queued", assigned_thread_id: "owner", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
function mount() {
  const container = document.createElement("div"); document.body.append(container);
  root = createRoot(container); return container;
}
afterEach(async () => { await act(async () => root?.unmount()); root = undefined; document.body.innerHTML = ""; globalThis.fetch = originalFetch; });

test("late response from a previous scope cannot overwrite the current inventory", async () => {
  const pending: Array<{ resolve: (value: Response) => void; signal?: AbortSignal | null }> = [];
  globalThis.fetch = ((_url: unknown, init?: RequestInit) => new Promise(resolve => pending.push({ resolve, signal: init?.signal }))) as typeof fetch;
  function Harness({ project }: { project: string }) { const state = useTasks({ projectId: project }); return <div>{state.tasks.map(task => task.title).join(",")}</div>; }
  const container = mount();
  await act(async () => root!.render(<Harness project="a" />));
  await act(async () => sleep(120));
  await act(async () => root!.render(<Harness project="b" />));
  await act(async () => sleep(120));
  expect(pending.length).toBe(2);
  expect(pending[0].signal?.aborted).toBe(true);
  await act(async () => { pending[1].resolve(Response.json({ tasks: [{ ...fixture, title: "Current", project_id: "b" }] })); });
  await act(async () => { pending[0].resolve(Response.json({ tasks: [fixture] })); });
  expect(container.textContent).toBe("Current");
});

test("detail load and mutation failures are visible, and Escape restores focus", async () => {
  globalThis.fetch = (() => Promise.resolve(new Response("Service unavailable", { status: 503 }))) as typeof fetch;
  const opener = document.createElement("button"); document.body.append(opener); opener.focus();
  const container = mount(); let closed = false;
  await act(async () => root!.render(<TaskDetails props={{ projectId: "a" }} task={fixture} onClose={() => { closed = true; }} onChanged={() => {}} />));
  expect(container.querySelector('[role="alert"]')?.textContent).toContain("Service unavailable");
  const cancel = Array.from(container.querySelectorAll("button")).find(button => button.textContent === "Cancel task")!;
  await act(async () => cancel.click());
  expect(container.querySelector('[role="alert"]')?.textContent).toContain("Service unavailable");
  expect(closed).toBe(false);
  const dialog = container.querySelector('[role="dialog"]')!;
  dialog.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  expect(closed).toBe(true);
  await act(async () => root!.unmount()); root = undefined;
  expect(document.activeElement).toBe(opener);
});
