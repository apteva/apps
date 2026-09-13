import { expect, test } from "bun:test";
import type { Step } from "./Workflow";
import {
  canConnect,
  connectSteps,
  layoutSteps,
  nextStepKey,
  removeStep,
} from "./flow-model";
const step = (key: string, depends_on: string[] = []): Step => ({
  key,
  name: key,
  role: "worker",
  kind: "work",
  instructions: "Do the work",
  expected_output: "Evidence",
  depends_on,
});
test("branches join after both predecessors and cannot connect back into a loop", () => {
  const steps = [
    step("fetch"),
    step("conversation", ["fetch"]),
    step("push", ["fetch"]),
  ];
  const joined = connectSteps(steps, "conversation", "push");
  expect(joined[2].depends_on).toEqual(["fetch", "conversation"]);
  expect(canConnect(joined, "push", "fetch")).toBe(false);
  expect(canConnect(joined, "push", "conversation")).toBe(false);
  expect(canConnect(joined, "fetch", "push")).toBe(false);
  expect(canConnect(joined, "fetch", "fetch")).toBe(false);
  expect(canConnect(joined, "__start", "push")).toBe(false);
  expect(connectSteps(joined, "push", "fetch")).toBe(joined);
});
test("layout puts parallel work in a shared column and its join after both", () => {
  const arranged = layoutSteps([
    step("start"),
    step("a", ["start"]),
    step("b", ["start"]),
    step("end", ["a", "b"]),
  ]);
  expect(arranged[1].position!.x).toBe(arranged[2].position!.x);
  expect(arranged[1].position!.y).not.toBe(arranged[2].position!.y);
  expect(arranged[3].position!.x).toBeGreaterThan(arranged[2].position!.x);
  expect(arranged[0].position!.x).toBeLessThan(arranged[1].position!.x);
});
test("deleting a step clears references and new keys remain unique", () => {
  const steps = [
    step("step_1"),
    step("step_2", ["step_1"]),
    step("step_3", ["step_2"]),
  ];
  const remaining = removeStep(steps, "step_2");
  expect(remaining[1].depends_on).toEqual([]);
  expect(nextStepKey(remaining)).toBe("step_2");
  expect(steps[2].depends_on).toEqual(["step_2"]);
});
