import { describe, expect, test } from "bun:test";
import { parseServerTimestamp } from "./mediaPanelLogic";

describe("parseServerTimestamp", () => {
  test("reads SQLite CURRENT_TIMESTAMP as UTC", () => {
    expect(parseServerTimestamp("2026-11-24 10:00:05")).toBe(Date.UTC(2026, 10, 24, 10, 0, 5));
  });
  test("keeps explicit zones", () => {
    expect(parseServerTimestamp("2026-11-24T10:00:05Z")).toBe(Date.UTC(2026, 10, 24, 10, 0, 5));
    expect(parseServerTimestamp("2026-11-24T12:00:05+02:00")).toBe(Date.UTC(2026, 10, 24, 10, 0, 5));
  });
  test("returns 0 for missing or bad values", () => {
    expect(parseServerTimestamp("")).toBe(0);
    expect(parseServerTimestamp(undefined)).toBe(0);
    expect(parseServerTimestamp("not a date")).toBe(0);
  });
});
