import { describe, expect, test } from "bun:test";
import { globalProjects, widgetConfig, widgetQueryRequest } from "./standalone-config";

describe("standalone widget filters", () => {
  test("a distinct trend counts the configured Distinct field", () => {
    const selections = { topic: "page_view", filter: "", window: "7d" };
    const fromForm = widgetConfig({ aggregation: "distinct", group_by: "props.visitor_id" }, "trend", selections);
    expect(fromForm.by).toBe("props.visitor_id");
    expect(fromForm.value).toBeUndefined();
    const legacy = widgetConfig({ aggregation: "distinct", value: "props.visitor_id" }, "trend", selections);
    expect(legacy.by).toBe("props.visitor_id");
    const count = widgetConfig({ aggregation: "count", group_by: "props.visitor_id" }, "trend", selections);
    expect(count.by).toBeUndefined();
  });

  test("scopes All to configured filter options", () => {
    const config = widgetConfig(
      {
        app: "content",
        topic: "page_view",
        filter_field: "props.host",
        filter_options: "marcoschwartz.com, makecademy.com",
      },
      "trend",
      { topic: "page_view", filter: "", window: "30d" },
    );
    expect(config.where).toEqual({
      "props.host": ["marcoschwartz.com", "makecademy.com"],
    });
  });

  test("a selected value overrides the configured All scope", () => {
    const config = widgetConfig(
      {
        filter_field: "props.host",
        filter_options: "marcoschwartz.com,makecademy.com",
      },
      "ranking",
      { topic: "page_view", filter: "makecademy.com", window: "30d" },
    );
    expect(config.where).toEqual({ "props.host": "makecademy.com" });
  });
});

describe("standalone widget scope", () => {
  const config = { app: "site", topic: "page_view", aggregation: "count", window: "7d", by: "props.country", limit: 5, where: {} } as any;

  test("a global widget queries every visible project, or one picked project", () => {
    const all = widgetQueryRequest("/api", "ranking", config, { global: true });
    expect(all?.url).toBe("/api/query-widget?scope=global");
    expect(all?.body).toEqual({ widget: { type: "top", config } });
    const one = widgetQueryRequest("/api", "trend", config, { global: true, selectedProject: "p2" });
    expect(one?.body).toEqual({ project_id: "p2", widget: { type: "timeseries", config } });
  });

  test("a project widget stays on its project and waits without one", () => {
    expect(widgetQueryRequest("/api", "ranking", config, { global: false, projectId: "a b" })?.url).toBe("/api/query-widget?project_id=a%20b");
    expect(widgetQueryRequest("/api", "ranking", config, { global: false })).toBeNull();
  });

  test("the project filter reads the projects the server returned", () => {
    expect(globalProjects({ projects: [{ id: "p1", name: "Alpha" }, { id: "p2" }, { name: "no id" }] })).toEqual([{ id: "p1", name: "Alpha" }, { id: "p2", name: "p2" }]);
    expect(globalProjects(null)).toEqual([]);
  });
});
