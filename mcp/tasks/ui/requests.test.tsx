import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { TaskDetails, useTasks, type Task } from "./taskShared";
import TaskCard from "./TaskCard";
import type { TaskBusEvent } from "./taskEvents";

const originalFetch = globalThis.fetch;
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, { window, document: window.document, HTMLElement: window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
let root: Root | undefined;
const listeners = new Set<(event: TaskBusEvent) => void>();
const bridge = { subscribe(_app: string, _project: string, callback: (event: TaskBusEvent) => void) {
  listeners.add(callback); return () => { listeners.delete(callback); };
} };
beforeEach(() => { (window as any).__aptevaAppEvents = bridge; });
function emit(overrides: Partial<TaskBusEvent> = {}) {
  for (const listener of listeners) listener({ app: "tasks", project_id: "a", install_id: 1393, topic: "task.updated", ...overrides });
}
const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
const fixture: Task = { id: "old", agent_id: 7, project_id: "a", title: "Old", state: "queued", assigned_thread_id: "owner", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
function mount() {
  const container = document.createElement("div"); document.body.append(container);
  root = createRoot(container); return container;
}
afterEach(async () => { await act(async () => root?.unmount()); root = undefined; document.body.innerHTML = ""; globalThis.fetch = originalFetch; expect(listeners.size).toBe(0); });

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

import TasksPanel from "./TasksPanel";
import { SchedulePicker } from "./SchedulePicker";

test("Tasks opens the complete inventory so scheduled work is not hidden", async () => {
  const urls: string[] = [];
  globalThis.fetch = ((url: unknown) => {
    urls.push(String(url));
    return Promise.resolve(Response.json(String(url).includes("/tasks?") ? { tasks: [{ ...fixture, state: "waiting", schedule_kind: "once", schedule_enabled: true, next_run_at: "2028-02-29T09:30:00Z" }] } : []));
  }) as typeof fetch;
  const container = mount();
  await act(async () => root!.render(<TasksPanel projectId="a" />));
  await act(async () => sleep(120));
  expect(urls.some(url => url.includes("view=all"))).toBe(true);
  expect(container.querySelector('button[aria-pressed="true"]')?.textContent).toBe("All tasks");
  expect(container.textContent).toContain("scheduled");
  expect(container.textContent).toContain("Next:");
});

test("the themed calendar selects leap days and preserves local time", async () => {
  let selected = "";
  const container = mount();
  await act(async () => root!.render(<SchedulePicker value="2028-02-14T09:30" timezone="Europe/Madrid" onChange={value => { selected = value; }} />));
  expect(container.querySelector('input[type="datetime-local"]')).toBeNull();
  const day = container.querySelector('button[aria-label="February 29, 2028"]') as HTMLButtonElement;
  expect(day).not.toBeNull();
  await act(async () => day.click());
  expect(selected).toBe("2028-02-29T09:30");
  await act(async () => (container.querySelector('[aria-label="Next month"]') as HTMLButtonElement).click());
  expect(container.textContent).toContain("March 2028");
  expect(container.querySelector('button[aria-label="March 31, 2028"]')).not.toBeNull();
});


test("the main page refreshes from all task bus topics, filters scope, and keeps rows visible", async () => {
  let task = { ...fixture }, requests = 0;
  let resolveRefresh: ((response: Response) => void) | undefined;
  globalThis.fetch = ((url: unknown) => {
    if (!String(url).includes("/tasks?")) return Promise.resolve(Response.json([]));
    requests++;
    if (requests === 1) return Promise.resolve(Response.json({ tasks: [task] }));
    return new Promise(resolve => { resolveRefresh = resolve; });
  }) as typeof fetch;
  const container = mount();
  await act(async () => root!.render(<TasksPanel projectId="a" installId={1393} />));
  await act(async () => sleep(120));
  expect(container.textContent).not.toContain("Live");
  expect(container.textContent).toContain("Old");
  await act(async () => {
    emit({ app: "other" }); emit({ project_id: "b" }); emit({ install_id: 8 }); emit({ topic: "unrelated.updated" }); await sleep(120);
  });
  expect(requests).toBe(1);
  await act(async () => { emit({ topic: "task.agent_execution_claimed" }); emit({ topic: "task.future_event" }); await sleep(120); });
  expect(requests).toBe(2);
  expect(container.textContent).toContain("Old");
  expect(container.textContent).not.toContain("Loading tasks");
  await act(async () => resolveRefresh!(Response.json({ tasks: [{ ...task, state: "running", progress: 42, current_step: "Processing records" }] })));
  expect(container.textContent).toContain("In progress");
  expect(container.textContent).toContain("Processing records");
  expect(container.textContent).toContain("42%");
});

test("reconnect backfills changes and bus refresh retains loaded inventory pages", async () => {
  let version = "Old";
  globalThis.fetch = ((url: unknown) => Promise.resolve(Response.json({
    tasks: [{ ...fixture, id: String(url).includes("cursor=second") ? "second" : "first", title: version }],
    next_cursor: String(url).includes("cursor=second") ? "" : "second",
  }))) as typeof fetch;
  function Harness() { const state = useTasks({ projectId: "a", installId: 1393 }); return <div>{state.tasks.map(task => <p key={task.id}>{task.title}</p>)}<button onClick={state.loadMore}>More</button></div>; }
  const container = mount();
  await act(async () => root!.render(<Harness />));
  await act(async () => sleep(120));
  await act(async () => (container.querySelector("button") as HTMLButtonElement).click());
  expect(container.querySelectorAll("p").length).toBe(2);
  version = "Reconnected";
  await act(async () => { window.dispatchEvent(new window.CustomEvent("apteva:app-events-connected", { detail: { projectId: "a" } })); await sleep(120); });
  expect([...container.querySelectorAll("p")].map(p => p.textContent)).toEqual(["Reconnected", "Reconnected"]);
});

test("open task activity refreshes execution state and reasons without losing earlier history or edits", async () => {
  let active = false;
  globalThis.fetch = ((url: unknown) => {
    if (String(url).includes("/events?")) return Promise.resolve(Response.json({ events: [{ id: "earlier", event_type: "created", created_at: "2025-12-31T00:00:00Z" }] }));
    return Promise.resolve(Response.json({ task: { ...fixture, state: active ? "running" : "queued" },
      events: active ? [{ id: "active", event_type: "agent_execution_active", created_at: "2026-01-02T00:00:00Z", data: { reason: "Executing requested analysis" } }] : [{ id: "queued", event_type: "state_changed", to_state: "queued", created_at: fixture.created_at }],
      events_next_cursor: "older",
      agent_executions: [{ source_event_id: "execution", purpose: "task", state: active ? "active" : "claimed", updated_at: fixture.updated_at }],
    }));
  }) as typeof fetch;
  const container = mount();
  await act(async () => root!.render(<TaskDetails props={{ projectId: "a", installId: 1393 }} task={fixture} onClose={() => {}} onChanged={() => {}} />));
  const button = (label: string) => [...container.querySelectorAll("button")].find(button => button.textContent === label)!;
  await act(async () => button("Load older activity").click());
  await act(async () => button("Edit definition").click());
  active = true;
  await act(async () => { emit(); await sleep(120); });
  expect(container.textContent).toContain("Agent executing");
  expect(container.textContent).toContain("Executing requested analysis");
  expect(container.textContent).toContain("Task created");
  expect(container.textContent).toContain("Save changes");
  expect(container.querySelector('[role="log"]')).not.toBeNull();
  expect(container.textContent).not.toContain("Load older activity");
});

test("chat task cards refresh on the bus and open task execution details", async () => {
  let state = "queued";
  globalThis.fetch = (() => Promise.resolve(Response.json({ task: { ...fixture, state }, events: [] }))) as typeof fetch;
  const container = mount();
  await act(async () => root!.render(<TaskCard projectId="a" installId={1393} taskId="old" />));
  state = "completed";
  await act(async () => { emit({ topic: "task.state_changed" }); await sleep(120); });
  expect(container.textContent).toContain("completed");
  await act(async () => (container.querySelector("button") as HTMLButtonElement).click());
  expect(container.querySelector('[role="dialog"]')).not.toBeNull();
  expect(container.textContent).toContain("Execution activity");
});

test("a scheduled run opens its own execution drawer and Escape only closes that drawer", async () => {
  const schedule: Task = { ...fixture, schedule_kind: "interval", schedule_enabled: true, next_run_at: "2028-01-01T00:00:00Z" };
  const run: Task = { ...fixture, id: "run", title: "Occurrence", parent_task_id: "old", scheduled_for: fixture.created_at };
  globalThis.fetch = ((url: unknown) => Promise.resolve(Response.json(String(url).includes("/runs?") ? { runs: [run] } : { task: String(url).includes("/run?") ? run : schedule, events: [] }))) as typeof fetch;
  const container = mount(); let closed = false;
  await act(async () => root!.render(<TaskDetails props={{ projectId: "a" }} task={schedule} onClose={() => { closed = true; }} onChanged={() => {}} />));
  await act(async () => (container.querySelector('button[aria-label^="Open run"]') as HTMLButtonElement).click());
  expect(container.querySelectorAll('[role="dialog"]').length).toBe(2);
  await act(async () => container.querySelector('[aria-label="Task Occurrence"]')!.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(closed).toBe(false);
  expect(container.querySelectorAll('[role="dialog"]').length).toBe(1);
});


test("standalone panels share their fallback bus connection and refresh after reconnect", async () => {
  delete (window as any).__aptevaAppEvents;
  const original = globalThis.EventSource;
  const sources: FakeSource[] = [];
  class FakeSource {
    onmessage?: (event: { data: string }) => void;
    onopen?: () => void;
    closed = false;
    constructor(public url: string) { sources.push(this); }
    close() { this.closed = true; }
  }
  globalThis.EventSource = FakeSource as any;
  let title = "Before";
  globalThis.fetch = (() => Promise.resolve(Response.json({ tasks: [{ ...fixture, title }] }))) as typeof fetch;
  function Harness() { const one = useTasks({ projectId: "a", installId: 1393 }); const two = useTasks({ projectId: "a", installId: 1393 }); return <div>{one.tasks[0]?.title}/{two.tasks[0]?.title}</div>; }
  try {
    const container = mount();
    await act(async () => root!.render(<Harness />));
    await act(async () => sleep(120));
    expect(sources.length).toBe(1);
    expect(sources[0].url).toBe("/api/app-events/_all?project_id=a");
    title = "After";
    await act(async () => { sources[0].onopen!(); await sleep(120); });
    expect(container.textContent).toBe("After/After");
    title = "Event";
    await act(async () => { sources[0].onmessage!({ data: JSON.stringify({ app: "tasks", project_id: "a", install_id: 1393, topic: "task.updated" }) }); await sleep(120); });
    expect(container.textContent).toBe("Event/Event");
    await act(async () => root!.unmount()); root = undefined;
    expect(sources[0].closed).toBe(true);
  } finally { globalThis.EventSource = original; }
});

test("event bursts do not abort a slow refresh and a final refresh catches intervening changes", async () => {
  const pending: Array<(response: Response) => void> = [];
  let requests = 0;
  globalThis.fetch = (() => {
    requests++;
    if (requests === 1) return Promise.resolve(Response.json({ tasks: [fixture] }));
    return new Promise(resolve => pending.push(resolve));
  }) as typeof fetch;
  function Harness() { const state = useTasks({ projectId: "a" }); return <div>{state.tasks[0]?.title}</div>; }
  const container = mount();
  await act(async () => root!.render(<Harness />));
  await act(async () => sleep(120));
  await act(async () => { emit(); await sleep(120); });
  await act(async () => { emit(); emit(); await sleep(120); });
  expect(requests).toBe(2);
  await act(async () => pending[0](Response.json({ tasks: [{ ...fixture, title: "Intermediate" }] })));
  expect(container.textContent).toBe("Intermediate");
  await act(async () => sleep(120));
  expect(requests).toBe(3);
  await act(async () => pending[1](Response.json({ tasks: [{ ...fixture, title: "Latest" }] })));
  expect(container.textContent).toBe("Latest");
});
