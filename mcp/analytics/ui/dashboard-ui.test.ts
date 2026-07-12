import { describe, expect, test } from "bun:test";
import { batchResultsByID, formatMetric, isCurrentRequest, partitionDashboardWidgets, resolvedWindow, scopedAppURL } from "./dashboard-ui";

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
});
