// The contract between the generic renderer and a pixel theme. A theme only
// paints; layout, motion, interaction and lighting composition are shared.
import type { Ctx, Point } from "./core";
import type { Layout, Place, Prop } from "./layout";

export type PlaceState = { open: number; active: boolean; error: boolean; selected: boolean; night: number; now: number };
export type Glow = Point & { radius: number; color: string; strength: number };

export interface PixelTheme {
  id: string;
  backdrop: string;
  outline: string;
  // Outfits cover the legs too (overalls, flight suits).
  jumpsuit?: boolean;
  label: { bg: string; rim: string; text: string; dim: string };
  paintGround(ctx: Ctx, layout: Layout<any>): void;
  animateGround(ctx: Ctx, layout: Layout<any>, now: number): void;
  drawProp(ctx: Ctx, prop: Prop, now: number, night: number): void;
  drawPlace(ctx: Ctx, place: Place, state: PlaceState): void;
  // Chimney smoke, antenna blinks and similar per-place effects.
  placeEffects(ctx: Ctx, place: Place, state: PlaceState): void;
  // 0 = full daylight, 1 = darkest; tint is the overlay colour.
  ambient(now: number): { darkness: number; tint: string; warmth: number };
  glows(layout: Layout<any>, places: Map<string, PlaceState>, night: number, now: number): Glow[];
}

// Buildings share one footprint so layout and hit testing stay theme-free.
export const PLACE_SPRITE = { w: 80, h: 66 };
export function placeOrigin(place: Place) {
  return { x: place.x * 16, y: (place.y + place.h) * 16 };
}
