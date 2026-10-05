import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { RunSteps, type StepRun } from "./Workflow";
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window,
  document: window.document,
  HTMLElement: window.HTMLElement,
  SVGElement: window.SVGElement,
  ResizeObserver: window.ResizeObserver,
  requestAnimationFrame: window.requestAnimationFrame.bind(window),
  cancelAnimationFrame: window.cancelAnimationFrame.bind(window),
  IS_REACT_ACT_ENVIRONMENT: true,
});
let root: Root;
afterEach(async () => {
  await act(async () => root?.unmount());
  document.body.innerHTML = "";
});
const step = (id: string, human = false, released = false): StepRun => ({
  id,
  run_id: "r",
  key: id,
  definition: {
    key: id,
    name: id,
    role: "worker",
    instructions: "Check exact receipts",
    expected_output: "Evidence",
    depends_on: [],
  },
  executor: { kind: human ? "human" : "agent", agent_id: 7 },
  state: released ? "waiting" : "ready",
  progress: 0,
  output: "",
  error: "",
  updated_by: "",
  updated_at: "",
  ...(released ? { released_at: "now" } : {}),
});
async function mount(steps: StepRun[], api: any, onChanged: any) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root.render(
      <RunSteps
        steps={steps}
        runID="r"
        runState="waiting"
        controlMode="step_by_step"
        waitingForAdvance
        eligibleSteps={steps
          .filter((s) => !s.released_at)
          .map((s) => ({ id: s.id, key: s.key }))}
        agents={[{ id: 7, name: "Owner" }]}
        projectId="p"
        api={api}
        onChanged={onChanged}
      />,
    ),
  );
}
async function click(text: string) {
  const b = Array.from(document.querySelectorAll("button")).find(
    (b) => b.textContent === text,
  )!;
  expect(b).toBeTruthy();
  await act(async () => b.click());
}
test("waiting run releases selected branches with stable keys and refreshes", async () => {
  const calls: any[] = [];
  let refreshes = 0;
  await mount(
    [step("alpha"), step("beta")],
    async (...args: any[]) => {
      calls.push(args);
      return {};
    },
    async () => {
      refreshes++;
    },
  );
  expect(document.body.textContent).toContain("Waiting for you to advance");
  await act(async () => {
    document
      .querySelectorAll<HTMLInputElement>("input[type=checkbox]")
      .forEach((i) => i.click());
  });
  await click("Run selected steps");
  expect(calls.map((c) => c[2].step_id)).toEqual(["alpha", "beta"]);
  expect(
    calls.every(
      (c) =>
        c[0] === "/runs/r/advance" && c[1] === "POST" && c[2].idempotency_key,
    ),
  ).toBe(true);
  expect(calls[0][2].idempotency_key).not.toBe(calls[1][2].idempotency_key);
  expect(refreshes).toBe(1);
});
test("a failed release keeps its key for a safe retry and refreshes partial state", async () => {
  const calls: any[] = [];
  let refreshes = 0;
  await mount(
    [step("alpha")],
    async (...args: any[]) => {
      calls.push(args);
      if (calls.length === 1) throw new Error("Lost reply");
      return {};
    },
    async () => {
      refreshes++;
    },
  );
  await click("Run next step");
  expect(document.body.textContent).toContain("Lost reply");
  await click("Run next step");
  expect(calls[1][2].idempotency_key).toBe(calls[0][2].idempotency_key);
  expect(refreshes).toBe(2);
});
test("human approval is available only after release and records separate evidence", async () => {
  const calls: any[] = [];
  await mount(
    [step("held-human", true), step("released-human", true, true)],
    async (...args: any[]) => {
      calls.push(args);
      return {};
    },
    async () => {},
  );
  expect(
    Array.from(document.querySelectorAll("button")).filter(
      (b) => b.textContent === "Complete human step",
    ),
  ).toHaveLength(1);
  await click("Complete human step");
  const textarea = document.querySelector<HTMLTextAreaElement>(
    'textarea[id="result-released-human"]',
  )!;
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      "value",
    )!.set!;
    setter.call(textarea, "Operator approved exact receipt");
    textarea.dispatchEvent(
      new window.Event("input", { bubbles: true }) as unknown as Event,
    );
  });
  // Submit uses the same HTTP approval path as automatic runs.
  await act(async () =>
    document
      .querySelector("form")!
      .dispatchEvent(
        new window.Event("submit", {
          bubbles: true,
          cancelable: true,
        }) as unknown as Event,
      ),
  );
  expect(calls[0][0]).toBe("/runs/r/steps/released-human");
  expect(calls[0][2].state).toBe("completed");
});
