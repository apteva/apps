import { describe, expect, test } from "bun:test";
import {
  isPendingSchedule,
  scheduleLabel,
  selectGroups,
  taskStateLabel,
  taskOverviewPreferences,
  type Task,
} from "./taskShared";

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
          state: "waiting",
          schedule_kind: "once",
          schedule_enabled: true,
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

  test("moves one-time work from upcoming to active to recent as live snapshots arrive", () => {
    const scheduled = task({
      id: "once",
      state: "waiting",
      schedule_kind: "once",
      schedule_expression: "2026-08-08T11:00:00Z",
      schedule_enabled: true,
      next_run_at: "2026-08-08T11:00:00Z",
    });
    expect(isPendingSchedule(scheduled)).toBe(true);
    expect(taskStateLabel(scheduled)).toBe("scheduled");
    expect(selectGroups([scheduled])).toMatchObject({
      active: [],
      upcoming: [{ id: "once" }],
      recent: [],
    });

    const running = task({
      ...scheduled,
      state: "running",
      schedule_enabled: false,
      next_run_at: undefined,
      scheduled_for: "2026-08-08T11:00:00Z",
      progress: 10,
      current_step: "Reviewing CRM leads",
    });
    expect(taskStateLabel(running)).toBe("running");
    expect(scheduleLabel(running)).toStartWith("One-time run ·");
    expect(selectGroups([running])).toMatchObject({
      active: [{ id: "once" }],
      upcoming: [],
      recent: [],
    });

    const completed = task({
      ...running,
      state: "completed",
      progress: 100,
      completed_at: "2026-08-08T11:01:00Z",
    });
    expect(taskStateLabel(completed)).toBe("completed");
    expect(selectGroups([completed])).toMatchObject({
      active: [],
      upcoming: [],
      recent: [{ id: "once" }],
    });
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
        next_run_at: "2026-08-08T11:00:00Z",
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

  test("normalizes per-instance task overview settings", () => {
    expect(taskOverviewPreferences()).toEqual({
      showActive: true,
      showUpcoming: true,
      showRecent: true,
      recentLimit: 4,
    });
    expect(taskOverviewPreferences({
      show_active: false,
      show_recent: false,
      recent_limit: 99,
    })).toEqual({
      showActive: false,
      showUpcoming: true,
      showRecent: false,
      recentLimit: 12,
    });
  });
});
