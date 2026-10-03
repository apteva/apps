import { describe, expect, test } from "bun:test";
import { widgetConfig } from "./standalone-config";

describe("standalone widget filters", () => {
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
