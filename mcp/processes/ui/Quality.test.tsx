import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import type { Root } from "react-dom/client";
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window,
  document: window.document,
  HTMLElement: window.HTMLElement,
  SVGElement: window.SVGElement,
  IS_REACT_ACT_ENVIRONMENT: true,
});
// react-dom detects input-event support when it loads, so load it after the DOM globals.
const { createRoot } = await import("react-dom/client");
const { QualityTab, RunRating } = await import("./Quality");
let root: Root;
afterEach(async () => {
  await act(async () => root?.unmount());
  document.body.innerHTML = "";
});
async function render(node: React.ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container as unknown as Element);
  await act(async () => root.render(node));
  await act(async () => new Promise((r) => setTimeout(r, 0)));
  return container;
}
const click = async (el: Element | null | undefined) => {
  if (!el) throw new Error("missing element");
  await act(async () => (el as HTMLElement).click());
  await act(async () => new Promise((r) => setTimeout(r, 0)));
};
const step = { id: "s1", key: "draft", state: "completed", definition: { name: "Draft" }, executor: { kind: "agent", agent_id: 8 } } as any;

test("rating bar requires a reason for low scores and posts a step rating", async () => {
  const posts: any[] = [];
  const api = async (path: string, method = "GET", body?: any) => {
    if (method === "POST") {
      posts.push({ path, body });
      return { saved: true, review_item: { id: "r1" } };
    }
    return { ratings: [], quality: { scale: "stars", tags: ["Wrong output", "Great"], criteria: [{ key: "accuracy", label: "Accuracy", type: "score" }], low_score: 2 } };
  };
  const view = await render(<RunRating api={api} runID="run1" runState="completed" steps={[step]} agentName={(id) => `Agent ${id}`} />);
  expect(view.textContent).toContain("Rate this run");
  const select = view.querySelector("select") as unknown as HTMLSelectElement;
  await act(async () => {
    select.value = "s1";
    select.dispatchEvent(new window.Event("change", { bubbles: true }) as unknown as Event);
  });
  await click(view.querySelector('[aria-label="Rating"] [aria-label="2 stars"]'));
  const save = [...view.querySelectorAll("button")].find((b) => b.textContent === "Save rating") as unknown as HTMLButtonElement;
  expect(save.disabled).toBe(true);
  expect(view.textContent).toContain("Pick at least one reason");
  await click([...view.querySelectorAll('[aria-label="Reason tags"] button')].find((b) => b.textContent === "Wrong output"));
  expect(save.disabled).toBe(false);
  await click(save);
  expect(posts).toHaveLength(1);
  expect(posts[0].path).toBe("/runs/run1/ratings");
  expect(posts[0].body).toMatchObject({ step_id: "s1", score: 2, tags: ["Wrong output"], source: "operator" });
  expect(posts[0].body.idempotency_key).toBeTruthy();
  expect(view.textContent).toContain("A review item was opened");
});

test("rating bar waits for the run to finish and uses thumbs when configured", async () => {
  const live = await render(<RunRating api={async () => ({})} runID="run1" runState="running" steps={[]} agentName={String} />);
  expect(live.textContent).toContain("Rating opens when the run finishes");
  await act(async () => root.unmount());
  const view = await render(<RunRating api={async () => ({ ratings: [], quality: { scale: "thumbs", tags: ["Late"], criteria: [] } })} runID="run2" runState="failed" steps={[]} agentName={String} />);
  expect(view.querySelector('[aria-label="Thumbs up"]')).toBeTruthy();
  expect(view.querySelector('[aria-label="3 stars"]')).toBeNull();
});

test("quality tab shows version quality and resolves a review item", async () => {
  const posts: any[] = [];
  const opened: string[] = [];
  let state = "open";
  const api = async (path: string, method = "GET", body?: any) => {
    if (method === "POST") {
      posts.push({ path, body });
      state = body.state;
      return { saved: true };
    }
    if (path === "/quality")
      return { current_version: 2, scale: "stars", versions: [{ version: 2, finished_runs: 4, rated_runs: 3, coverage: 0.75, average: 3.67, distribution: { 1: 0, 2: 1, 3: 0, 4: 1, 5: 1 } }], tags: [{ tag: "Wrong output", count: 2 }], executors: [{ agent_id: 8, step_ratings: 2, average: 2.5 }], criteria: [], worst_runs: [{ run_id: "bad", procedure_version: 2, score: 2, tags: ["Wrong output"], rated_at: "2026-10-29T10:00:00Z" }], review: { open: 1, claimed: 0 } };
    return { items: state === "open" ? [{ id: "rv", run_id: "bad", procedure_version: 2, reason: "low_score", state: "open", revision: 1, created_at: "2026-10-29T10:00:00Z", run_state: "completed", rating: { average: 2, count: 1, latest: 2, step_ratings: 0 } }] : [], active_count: state === "open" ? 1 : 0 };
  };
  const view = await render(<QualityTab api={api} agentName={(id) => `Agent ${id}`} currentVersion={2} onOpenRun={(id) => opened.push(id)} />);
  expect(view.textContent).toContain("3.67");
  expect(view.textContent).toContain("75%");
  expect(view.textContent).toContain("Agent 8");
  expect(view.textContent).toContain("Low score · v2");
  await click([...view.querySelectorAll("button")].find((b) => b.textContent === "Open run"));
  expect(opened).toEqual(["bad"]);
  const input = view.querySelector('[aria-label="Resolution"]') as unknown as HTMLInputElement;
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
    setter.call(input, "Clarified the brief");
    input.dispatchEvent(new window.Event("input", { bubbles: true }) as unknown as Event);
  });
  await click([...view.querySelectorAll("button")].find((b) => b.textContent === "Resolve"));
  expect(posts[0]).toEqual({ path: "/reviews/rv", body: { state: "resolved", resolution: "Clarified the brief", expected_revision: 1 } });
  expect(view.textContent).toContain("Nothing to review");
});
