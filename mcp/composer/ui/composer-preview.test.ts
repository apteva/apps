import { describe, expect, test } from "bun:test";
import { previewCameraAt, previewCropStyle, previewEase } from "./composer-preview";

describe("Composer preview geometry", () => {
  test("matches renderer easing equations", () => {
    expect(previewEase(0.25, "ease_in")).toBeCloseTo(0.0625);
    expect(previewEase(0.25, "ease_out")).toBeCloseTo(0.4375);
    expect(previewEase(0.25, "ease_in_out")).toBeCloseTo(0.125);
  });

  test("interpolates source-space camera keyframes", () => {
    const camera = previewCameraAt({
      x: 0.5,
      y: 0.5,
      scale: 1,
      keyframes: [{ time: 2, x: 0.75, y: 0.25, scale: 2, easing: "linear" }],
    }, 1);
    expect(camera).toEqual({ x: 0.625, y: 0.375, scale: 1.5 });
  });

  test("maps normalized crop before camera motion", () => {
    expect(previewCropStyle({ x: 0.25, y: 0.1, width: 0.5, height: 0.8 }, "cover", { x: 0.5, y: 0.5, scale: 1.25 })).toMatchObject({
      width: "200%",
      height: "125%",
      left: "-50%",
      top: "-12.5%",
      objectFit: "cover",
      transform: "scale(1.25)",
      transformOrigin: "50% 50%",
    });
  });
});
