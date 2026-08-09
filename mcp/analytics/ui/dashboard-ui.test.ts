import { describe, expect, test } from "bun:test";
import { batchResultsByID, formatMetric, formatObjectivePeriod, formatObjectiveValue, isCurrentRequest, objectiveMonthBounds, objectiveProgressWidth, partitionDashboardWidgets, resolvedWindow, scopedAppURL } from "./dashboard-ui";

describe("dashboard UI helpers", () => {
  test("formats normalized Patreon money with an explicit USD unit", () => {
    expect(formatMetric(1715.62, { format: "currency", currency: "USD" })).toBe("$1,715.62");
  });

  test("resolves window placeholders from current dashboard filters", () => {
    expect(resolvedWindow({ window: "$filters.window" }, { window: "30d" })).toBe("30d");
  });

  test("rejects stale request sequences", () => {
    expect(isCurrentRequest(4, 5)).toBe(false);
    expect(isCurrentRequest(5, 5)).toBe(true);
  });

  test("adds project scope to both plain and parameterized app routes", () => {
    expect(scopedAppURL("/api/apps/analytics/dashboards/7", "H Sites")).toBe("/api/apps/analytics/dashboards/7?project_id=H%20Sites");
    expect(scopedAppURL("/api/apps/analytics/feed?limit=50", "p1")).toBe("/api/apps/analytics/feed?limit=50&project_id=p1");
  });

  test("preserves per-widget errors instead of converting them to zero", () => {
    expect(batchResultsByID([{ widget_id: 7, error: "amount contains non-numeric rows" }])).toEqual({
      7: { error: "amount contains non-numeric rows" },
    });
  });

  test("separates top indicators from the two-column chart area", () => {
    const widgets = [
      ...Array.from({ length: 3 }, () => ({ type: "stat" })),
      ...Array.from({ length: 4 }, () => ({ type: "timeseries" })),
    ];
    const { stats, charts } = partitionDashboardWidgets(widgets);
    expect(stats).toHaveLength(3);
    expect(charts).toHaveLength(4);
  });

  test("builds an exclusive UTC month range for objective targets", () => {
    expect(objectiveMonthBounds("2026-08")).toEqual({
      start: Date.UTC(2026, 7, 1),
      end: Date.UTC(2026, 8, 1),
    });
    expect(() => objectiveMonthBounds("2026-13")).toThrow("invalid month");
  });

  test("clamps objective progress bars while preserving the real percentage elsewhere", () => {
    expect(objectiveProgressWidth(142)).toBe(100);
    expect(objectiveProgressWidth(-4)).toBe(0);
    expect(objectiveProgressWidth(undefined)).toBe(0);
  });

  test("formats target units explicitly", () => {
    expect(formatObjectiveValue(25000, "money", "USD")).toBe("$25,000.00");
    expect(formatObjectiveValue(12.5, "percent")).toBe("12.5%");
  });

  test("renders an exclusive UTC month end without leaking into the local timezone", () => {
    const { start, end } = objectiveMonthBounds("2026-08");
    expect(formatObjectivePeriod(start, end, "UTC")).toBe("Aug 1, 2026 - Aug 31, 2026");
  });
});
