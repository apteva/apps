import { expect, test } from "bun:test";
import { isActive, isScheduleDefinition, taskStateLabel, taskQueueRank, type Task } from "./taskShared";

const base: Task = {
  id: "review-task", agent_id: 7, project_id: "project-a", title: "Review fixture",
  state: "waiting", assigned_thread_id: "worker",
  created_at: "2026-09-05T06:00:00Z", updated_at: "2026-09-05T06:10:00Z",
};

test("activated one-time task awaiting a callback remains work, not a paused definition", () => {
  const task: Task = { ...base, schedule_kind: "once", schedule_enabled: false,
    scheduled_for: "2026-09-05T06:05:00Z", accepted_at: "2026-09-05T06:05:01Z" };
  expect({ active: isActive(task), definition: isScheduleDefinition(task), label: taskStateLabel(task) })
    .toEqual({ active: true, definition: false, label: "waiting" });
});

test("a recurring definition with a failed latest run receives failure priority", () => {
  const task: Task = { ...base, schedule_kind: "interval", schedule_enabled: true,
    schedule_expression: "1h", next_run_at: "2026-09-05T07:00:00Z",
    last_occurrence_status: "failed", last_error: "upstream unavailable" };
  expect(taskQueueRank(task)).toBe(0);
});

import { localScheduleISO } from "./TasksPanel";

test("one-time local scheduling resolves the actual local instant", () => {
  expect(localScheduleISO("2026-11-12T09:00")).toBe(new Date(2026, 10, 12, 9, 0).toISOString());
});

test("one-time local scheduling rejects invalid calendar input", () => {
  expect(() => localScheduleISO("2026-02-31T09:00")).toThrow();
  expect(() => localScheduleISO("")).toThrow();
});
