import { describe, expect, test } from "bun:test";
import { DEFAULT_RINGBACK_COUNTRY, RINGBACK_PATTERNS, ringbackPattern, ringbackTimeline } from "./ringback";

const round = (value: number) => Math.round(value * 1000) / 1000;

describe("ringback patterns", () => {
  test("defaults to the French cadence and accepts lowercase or padded codes", () => {
    expect(DEFAULT_RINGBACK_COUNTRY).toBe("FR");
    expect(ringbackPattern()).toEqual(RINGBACK_PATTERNS.FR);
    expect(ringbackPattern(" gb ")).toEqual(RINGBACK_PATTERNS.GB);
    expect(ringbackPattern("ZZ")).toEqual(RINGBACK_PATTERNS.FR);
    expect(RINGBACK_PATTERNS.FR).toEqual({ country: "FR", frequencies: [440], cadence: [1.5, 3.5] });
  });

  test("every pattern has an even cadence and audible frequencies", () => {
    for (const pattern of Object.values(RINGBACK_PATTERNS)) {
      expect(pattern.cadence.length % 2).toBe(0);
      expect(pattern.cadence.every((value) => value > 0)).toBe(true);
      expect(pattern.frequencies.every((value) => value >= 380 && value <= 500)).toBe(true);
    }
  });

  test("timeline repeats the cadence within the horizon", () => {
    const segments = ringbackTimeline(RINGBACK_PATTERNS.GB, 5.2).map((s) => [round(s.start), round(s.end)]);
    expect(segments).toEqual([[0, 0.4], [0.6, 1], [3, 3.4], [3.6, 4]]);
    const french = ringbackTimeline(RINGBACK_PATTERNS.FR, 6).map((s) => [round(s.start), round(s.end)]);
    expect(french).toEqual([[0, 1.5], [5, 6]]);
    expect(ringbackTimeline(RINGBACK_PATTERNS.FR, 0)).toEqual([]);
  });
});
