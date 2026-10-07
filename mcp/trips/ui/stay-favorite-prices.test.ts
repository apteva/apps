import { describe, expect, test } from "bun:test";
import { stayNightCount, stayPriceTotal } from "./stay-favorite-prices";

describe("stay selection estimates", () => {
  test("counts overnight stays by calendar date across daylight savings", () => {
    expect(stayNightCount("2027-03-27", "2027-03-29")).toBe(2);
    expect(stayPriceTotal(12500, "per_night", "2027-03-27", "2027-03-29")).toBe(25000);
  });
  test("keeps nightly estimates unknown until dates are chosen", () => {
    expect(stayPriceTotal(12500, "per_night", "", "")).toBeNull();
    expect(stayPriceTotal(12500, "per_night", "2027-03-29", "2027-03-27")).toBeNull();
    expect(stayPriceTotal(null, "total", "2027-03-27", "2027-03-29")).toBeNull();
  });
  test("preserves zero and total prices and rejects overflowing totals", () => {
    expect(stayPriceTotal(0, "total", "", "")).toBe(0);
    expect(stayPriceTotal(12500, "total", "2027-03-27", "2027-03-29")).toBe(12500);
    expect(stayPriceTotal(Number.MAX_SAFE_INTEGER, "per_night", "2027-03-27", "2027-03-29")).toBeNull();
  });
});
