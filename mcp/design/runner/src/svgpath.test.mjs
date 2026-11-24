import { expect, test } from "bun:test";
import { parsePath, flattenPath } from "./svgpath.mjs";

const area = (pts) => { let a = 0; for (let i = 0; i < pts.length - 1; i++) a += pts[i][0] * pts[i + 1][1] - pts[i + 1][0] * pts[i][1]; return Math.abs(a / 2); };

test("relative commands and H/V", () => {
  const [sub] = parsePath("m10 10 h20 v10 h-20 z");
  expect(sub.closed).toBe(true);
  expect(sub.segments.map((s) => s.to)).toEqual([[30, 10], [30, 20], [10, 20]]);
});
test("implicit lineto after moveto", () => {
  expect(parsePath("M0 0 10 0 10 10z")[0].segments.length).toBe(2);
});
test("two subpaths", () => {
  expect(parsePath("M0 0H10V10H0Z M2 2H8V8H2Z").length).toBe(2);
});
test("arc circle flattens to pi r^2", () => {
  const [poly] = flattenPath("M-5 0 A5 5 0 1 0 5 0 A5 5 0 1 0 -5 0 Z", 64);
  expect(area(poly.points)).toBeCloseTo(Math.PI * 25, 0);
});
test("compact arc flags", () => {
  const [sub] = parsePath("M0 0a5 5 0 011 1");
  expect(sub.segments[0]).toMatchObject({ type: "A", large: false, sweep: true, to: [1, 1] });
});
test("smooth cubic reflects control", () => {
  const [sub] = parsePath("M0 0 C0 10 10 10 10 0 S20 -10 20 0");
  expect(sub.segments[1].c1).toEqual([10, -10]);
});
test("rejects garbage", () => {
  expect(() => parsePath("M0 0 L 1 x")).toThrow("unexpected character");
  expect(() => parsePath("L 1 1")).toThrow("must start with M");
});
