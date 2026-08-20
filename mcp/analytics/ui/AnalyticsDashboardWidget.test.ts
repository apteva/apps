import { describe, expect, test } from "bun:test";
import { defaultDashboardFilters, selectDashboardID } from "./dashboard-ui";

describe("Analytics dashboard home widget", () => {
  test("prefers configured dashboard, then stored dashboard, then newest", () => {
    const rows = [
      { id: 9, project_id: "p1", name: "Newest" },
      { id: 4, project_id: "p1", name: "Stored" },
    ];
    expect(selectDashboardID(rows, 4, 9)).toBe(4);
    expect(selectDashboardID(rows, 99, 4)).toBe(4);
    expect(selectDashboardID(rows, 99, 98)).toBe(9);
    expect(selectDashboardID([], 4, 9)).toBe(0);
  });

  test("adapts to arbitrary saved dashboard filters", () => {
    expect(defaultDashboardFilters([
      { key: "page_id", type: "select", default: "page-1" },
      { key: "window", type: "date_window" },
      { key: "plan", type: "select" },
    ])).toEqual({ page_id: "page-1", window: "30d", plan: "all" });
  });
});
