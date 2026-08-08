import { describe, expect, test } from "bun:test";
import { scheduleLabel, selectGroups, type Task } from "./taskShared";

function task(overrides: Partial<Task>): Task {
  return {
    id: "task-1",
    agent_id: 7,
    project_id: "project-a",
    title: "Work",
    state: "queued",
    assigned_thread_id: "opaque-1",
    created_at: "2026-08-08T10:00:00Z",
    updated_at: "2026-08-08T10:00:00Z",
    ...overrides,
  };
}

describe("Tasks app UI model", () => {
  test("keeps one-time schedules distinct from recurring schedules", () => {
    expect(
      scheduleLabel(
        task({
          schedule_kind: "once",
          next_run_at: "2026-08-08T11:00:00Z",
        }),
      ),
    ).toStartWith("One time ·");
    expect(
      scheduleLabel(
        task({ schedule_kind: "interval", schedule_expression: "10m" }),
      ),
    ).toBe("Repeats every 10m");
  });

  test("groups app-owned work into active, upcoming, and recent without counting occurrence rows twice", () => {
    const groups = selectGroups([
      task({ id: "active", state: "running", progress: 25 }),
      task({
        id: "schedule",
        state: "waiting",
        schedule_kind: "interval",
        schedule_expression: "10m",
        schedule_enabled: true,
      }),
      task({ id: "done", state: "completed", progress: 100 }),
      task({
        id: "run",
        parent_task_id: "schedule",
        state: "completed",
      }),
    ]);
    expect(groups.active.map((item) => item.id)).toEqual(["active"]);
    expect(groups.upcoming.map((item) => item.id)).toEqual(["schedule"]);
    expect(groups.recent.map((item) => item.id)).toEqual(["done"]);
  });
});
