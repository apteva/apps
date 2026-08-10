import { describe, expect, test } from "bun:test";
import { canCreateTrackingSource, trackingSourceCreateInput } from "./AdsPanel";

describe("tracking source UI helpers", () => {
  test("shows creation only when account capabilities allow it", () => {
    expect(canCreateTrackingSource({
      capabilities: { tracking_source: { create: true } },
    } as any)).toBe(true);
    expect(canCreateTrackingSource({
      capabilities: { tracking_source: { create: false } },
    } as any)).toBe(false);
    expect(canCreateTrackingSource(null)).toBe(false);
  });

  test("builds a safe reuse-first create request", () => {
    expect(trackingSourceCreateInput(12, "  Website conversions  ", true)).toEqual({
      ad_account_id: 12,
      name: "Website conversions",
      set_default: true,
      reuse_existing: true,
    });
  });
});
