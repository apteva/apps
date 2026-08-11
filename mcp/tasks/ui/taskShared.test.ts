import { describe, expect, test } from "bun:test";
import {
  isPendingSchedule,
  normalizeTaskQueueFilters,
  scheduleLabel,
  selectGroups,
  selectTaskQueue,
  taskQueueRank,
  taskRowSummary,
  taskEventLabel,
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

  test("orders the Home widget as one operational priority queue", () => {
    const queue = selectTaskQueue([
      task({ id: "cancelled", state: "cancelled" }),
      task({ id: "completed", state: "completed" }),
      task({
        id: "later",
        state: "waiting",
        schedule_kind: "interval",
        schedule_enabled: true,
        next_run_at: "2026-08-08T12:00:00Z",
      }),
      task({
        id: "sooner",
        state: "waiting",
        schedule_kind: "once",
        schedule_enabled: true,
        next_run_at: "2026-08-08T11:00:00Z",
      }),
      task({
        id: "paused",
        state: "waiting",
        schedule_kind: "interval",
        schedule_enabled: false,
      }),
      task({ id: "queued", state: "queued" }),
      task({ id: "running", state: "running" }),
      task({ id: "blocked", state: "blocked" }),
      task({ id: "failed", state: "failed" }),
      task({ id: "occurrence", parent_task_id: "later", state: "running" }),
    ]);

    expect(queue.map((item) => item.id)).toEqual([
      "failed",
      "blocked",
      "running",
      "queued",
      "sooner",
      "later",
      "paused",
      "completed",
      "cancelled",
    ]);
    expect(taskQueueRank(queue[0])).toBeLessThan(taskQueueRank(queue[1]));
  });

  test("keeps failures visible with active work and applies recent limits only to history", () => {
    const queue = selectTaskQueue(
      [
        task({ id: "failed", state: "failed", error: "CRM unavailable" }),
        task({ id: "done-new", state: "completed", updated_at: "2026-08-08T12:00:00Z" }),
        task({ id: "done-old", state: "completed", updated_at: "2026-08-08T11:00:00Z" }),
      ],
      {
        showActive: true,
        showUpcoming: true,
        showRecent: true,
        recentLimit: 1,
      },
    );
    expect(queue.map((item) => item.id)).toEqual(["failed", "done-new"]);
    expect(taskRowSummary(queue[0])).toBe("CRM unavailable");
  });

  test("combines scheduled and recurring filters without losing queue priority", () => {
    const queue = selectTaskQueue(
      [
        task({ id: "running", state: "running" }),
        task({
          id: "once",
          state: "waiting",
          schedule_kind: "once",
          schedule_enabled: true,
          next_run_at: "2026-08-08T11:00:00Z",
        }),
        task({
          id: "recurring",
          state: "waiting",
          schedule_kind: "interval",
          schedule_enabled: true,
          next_run_at: "2026-08-08T12:00:00Z",
        }),
        task({ id: "done", state: "completed" }),
      ],
      undefined,
      ["scheduled", "recurring"],
    );
    expect(queue.map((item) => item.id)).toEqual(["once", "recurring"]);
  });

  test("normalizes persisted task filters", () => {
    expect(
      normalizeTaskQueueFilters(["recurring", "invalid", "scheduled", "recurring"]),
    ).toEqual(["recurring", "scheduled"]);
    expect(normalizeTaskQueueFilters("recurring")).toEqual([]);
  });

  test("uses terminal results instead of stale execution steps in compact rows", () => {
    expect(
      taskRowSummary(
        task({
          state: "completed",
          current_step: "Reviewing leads",
          result: "No newly active leads found.",
        }),
      ),
    ).toBe("No newly active leads found.");
  });

  test("shows task progress percentages in the event timeline", () => {
    expect(
      taskEventLabel({
        id: "event-running",
        task_id: "task-1",
        event_type: "updated",
        from_state: "running",
        to_state: "running",
        data: { progress: 70, current_step: "Messages classified" },
        created_at: "2026-08-08T10:01:00Z",
      }),
    ).toBe("Running · 70%");
    expect(
      taskEventLabel({
        id: "event-complete",
        task_id: "task-1",
        event_type: "state_changed",
        from_state: "running",
        to_state: "completed",
        data: { progress: 100, current_step: "Completed" },
        created_at: "2026-08-08T10:02:00Z",
      }),
    ).toBe("Completed · 100%");
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
