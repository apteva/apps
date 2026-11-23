import { describe, expect, test } from "bun:test";
import { overviewWindow } from "./CallsOverviewWidget";
import { formatDuration, freeSetting, isWidgetEvent, stringSetting, widgetURL } from "./widget-query";

describe("telephony dashboard widgets", () => {
  test("overview windows", () => {
    const now = new Date(2026, 10, 23, 14, 30, 15, 500);
    const today = overviewWindow("today", now);
    expect(new Date(today.from).getTime()).toBe(new Date(2026, 10, 23).getTime());
    expect(today.until).toBe(new Date(2026, 10, 23, 14, 30, 15).toISOString());
    const week = overviewWindow("7d", now);
    expect(Date.parse(week.until) - Date.parse(week.from)).toBe(7 * 86400000);
    const midnight = overviewWindow("today", new Date(2026, 10, 23, 0, 0, 0));
    expect(Date.parse(midnight.until) - Date.parse(midnight.from)).toBe(3600000);
  });
  test("durations", () => {
    expect(formatDuration(null)).toBe("—");
    expect(formatDuration(65)).toBe("1:05");
    expect(formatDuration(3725)).toBe("1:02:05");
  });
  test("settings and URL", () => {
    expect(stringSetting({ direction: "sideways" }, "direction", ["", "inbound"], "")).toBe("");
    expect(freeSetting({ number: " +331 " }, "number")).toBe("+331");
    const url = widgetURL({ appName: "telephony", installId: 124, projectId: "p1" }, "live", { direction: "inbound", number: "" });
    expect(url).toBe("/api/apps/telephony/audio-health?view=live&project_id=p1&install_id=124&direction=inbound");
  });
  test("events are scoped to the install", () => {
    const host = { appName: "telephony", installId: 124, projectId: "p1" };
    const topics = new Set(["call.answered"]);
    expect(isWidgetEvent(host, topics, { app: "telephony", project_id: "p1", install_id: 124, topic: "call.answered" })).toBe(true);
    expect(isWidgetEvent(host, topics, { app: "telephony", project_id: "p1", install_id: 19, topic: "call.answered" })).toBe(false);
    expect(isWidgetEvent(host, topics, { app: "telephony", project_id: "p1", install_id: 124, topic: "call.offered" })).toBe(false);
  });
});
