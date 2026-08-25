import { describe, expect, test } from "bun:test";
import { areaChartEndMarkerPath, areaChartGeometry, dedupeWidgetGoals, defaultDashboardFilters, selectDashboardID } from "./dashboard-ui";

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

  test("renders each linked goal once, preferring the first visible metric", () => {
    const data = {
      10: { value: 18240, goals: [{ target_id: 173, name: "MRR target" }] },
      11: { series: [], goals: [{ target_id: 173, name: "MRR target" }, { target_id: 174, name: "Members target" }] },
    };
    expect(dedupeWidgetGoals([10, 11], data)).toEqual({
      10: { value: 18240, goals: [{ target_id: 173, name: "MRR target" }] },
      11: { series: [], goals: [{ target_id: 174, name: "Members target" }] },
    });
    expect(data[11].goals).toHaveLength(2);
  });

  test("keeps area-chart endpoints inside the viewbox", () => {
    const geometry = areaChartGeometry([10, 14, 12, 18]);
    expect(geometry.points[0].x).toBe(8);
    expect(geometry.points.at(-1)?.x).toBe(292);
    expect(geometry.areaPath).toEndWith(`L 8 ${geometry.baseline} Z`);
    expect(areaChartEndMarkerPath(geometry.points, geometry.baseline)).toBe("M 292 8 h 0.001");
  });
});
