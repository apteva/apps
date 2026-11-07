// Isometric pixel primitives. One tile step moves 16px across and 8px down
// (2:1 pixel lines), faces are filled per pixel centre so edges stay crisp,
// and every sprite gets a 1px ink outline. Nothing is anti-aliased.
import { makeCanvas, type Ctx } from "../core";

export const TW = 16, TH = 8;
export const INK: [number, number, number] = [52, 46, 66];

export type Fill = string | ((u: number, v: number, x: number, y: number) => string | null);
export type Tones = { top: Fill; left: Fill; right: Fill };
export type Sprite = { canvas: HTMLCanvasElement; x: number; y: number };

export const hash = (a: number, b: number) => { const s = Math.sin(a * 127.1 + b * 311.7) * 43758.5453; return s - Math.floor(s); };

export function lighten(hex: string, amount: number) {
  const n = parseInt(hex.slice(1), 16), f = (v: number) => Math.max(0, Math.min(255, v + amount));
  return `#${[f(n >> 16), f((n >> 8) & 255), f(n & 255)].map(v => v.toString(16).padStart(2, "0")).join("")}`;
}
export const tone = (hex: string): Tones => ({ top: hex, left: lighten(hex, -22), right: lighten(hex, -44) });

export class Painter {
  constructor(readonly ctx: Ctx, public ox: number, public oy: number) {}

  P(x: number, y: number, z = 0): [number, number] {
    return [this.ox + (x - y) * TW, this.oy + (x + y) * TH - z];
  }

  px(color: string, x: number, y: number, w = 1, h = 1) {
    this.ctx.fillStyle = color;
    this.ctx.fillRect(Math.round(x), Math.round(y), w, h);
  }

  dot(color: string, x: number, y: number) {
    this.ctx.fillStyle = color;
    this.ctx.fillRect(x, y, 1, 1);
  }

  // Fill the parallelogram O + u·A + v·B for u, v in [0, 1).
  para(O: [number, number], A: [number, number], B: [number, number], fill: Fill) {
    const det = A[0] * B[1] - A[1] * B[0];
    if (!det) return;
    const xs = [O[0], O[0] + A[0], O[0] + B[0], O[0] + A[0] + B[0]], ys = [O[1], O[1] + A[1], O[1] + B[1], O[1] + A[1] + B[1]];
    for (let y = Math.floor(Math.min(...ys)); y <= Math.ceil(Math.max(...ys)); y++) {
      for (let x = Math.floor(Math.min(...xs)); x <= Math.ceil(Math.max(...xs)); x++) {
        const dx = x + 0.5 - O[0], dy = y + 0.5 - O[1];
        const u = (dx * B[1] - dy * B[0]) / det, v = (A[0] * dy - A[1] * dx) / det;
        if (u < 0 || u >= 1 || v < 0 || v >= 1) continue;
        const color = typeof fill === "string" ? fill : fill(u, v, x, y);
        if (color) this.dot(color, x, y);
      }
    }
  }

  // A box with its three visible faces. Vertical faces get a light top edge.
  box(x: number, y: number, z: number, w: number, d: number, h: number, tones: Tones) {
    if (h > 0) {
      this.para(this.P(x + w, y, z), [-TW * d, TH * d], [0, -h], edged(tones.right, h));
      this.para(this.P(x, y + d, z), [TW * w, TH * w], [0, -h], edged(tones.left, h));
    }
    this.para(this.P(x, y, z + h), [TW * w, TH * w], [-TW * d, TH * d], tones.top);
  }

  // A vertical rectangle on the plane x = const ("y" axis) or y = const ("x" axis).
  wall(axis: "x" | "y", x: number, y: number, z: number, length: number, h: number, fill: Fill) {
    const along: [number, number] = axis === "x" ? [TW * length, TH * length] : [-TW * length, TH * length];
    this.para(this.P(x, y, z), along, [0, -h], fill);
  }

  // A floor patch on the ground plane.
  floor(x: number, y: number, z: number, w: number, d: number, fill: Fill) {
    this.para(this.P(x, y, z), [TW * w, TH * w], [-TW * d, TH * d], fill);
  }

  // Shaded leafy ball for canopies, bushes and potted plants.
  ball(cx: number, cy: number, r: number, colors: readonly string[], seed = 0) {
    for (let dy = -r - 1; dy <= r + 1; dy++) for (let dx = -r - 1; dx <= r + 1; dx++) {
      const a = Math.atan2(dy, dx), edge = r * (1 + 0.09 * Math.sin(a * 5 + seed) + 0.05 * Math.sin(a * 9 + seed * 2));
      const d = Math.hypot(dx, dy);
      if (d > edge) continue;
      const light = -0.55 * (dx / r) - 0.75 * (dy / r) + 0.35 * (1 - d / edge) + ((dx + dy) & 1 ? 0.06 : 0);
      this.px(light > 0.55 ? colors[0] : light > 0.1 ? colors[1] : light > -0.35 ? colors[2] : colors[3], cx + dx, cy + dy);
    }
  }
}

// Paints straight into an RGBA buffer: the ground is hundreds of thousands of
// pixels, far too many for one fillRect each. Only "#rrggbb" colours.
export class BufferPainter extends Painter {
  private readonly colors = new Map<string, number>();
  constructor(readonly data: Uint8ClampedArray, readonly width: number, readonly height: number, ox: number, oy: number) {
    super(null as unknown as Ctx, ox, oy);
  }
  private rgb(color: string) {
    let value = this.colors.get(color);
    if (value === undefined) this.colors.set(color, (value = parseInt(color.slice(1), 16)));
    return value;
  }
  override dot(color: string, x: number, y: number) {
    if (x < 0 || y < 0 || x >= this.width || y >= this.height) return;
    const value = this.rgb(color), i = (y * this.width + x) * 4;
    this.data[i] = value >> 16; this.data[i + 1] = (value >> 8) & 255; this.data[i + 2] = value & 255; this.data[i + 3] = 255;
  }
  override px(color: string, x: number, y: number, w = 1, h = 1) {
    const x0 = Math.round(x), y0 = Math.round(y);
    for (let j = 0; j < h; j++) for (let i = 0; i < w; i++) this.dot(color, x0 + i, y0 + j);
  }
  // Multiply a pixel's colour, for soft ground shadows.
  shade(x: number, y: number, r: number, g: number, b: number) {
    if (x < 0 || y < 0 || x >= this.width || y >= this.height) return;
    const i = (y * this.width + x) * 4;
    this.data[i] *= r; this.data[i + 1] *= g; this.data[i + 2] *= b;
  }
}

function edged(fill: Fill, h: number): Fill {
  return typeof fill === "string" ? (_u, v) => (v > 1 - 1 / h ? lighten(fill, 16) : fill) : fill;
}

// World-space bounding points of a box, for sizing its sprite.
export function boxCorners(x: number, y: number, z: number, w: number, d: number, h: number): [number, number, number][] {
  return [[x, y, z + h], [x + w, y, z + h], [x, y + d, z + h], [x + w, y + d, z + h], [x + w, y, z], [x, y + d, z], [x + w, y + d, z]];
}

// Draw into a tight sprite canvas positioned in world pixels, then outline it.
export function sprite(origin: { ox: number; oy: number }, corners: [number, number, number][], draw: (p: Painter) => void, pad = 3): Sprite {
  const probe = new Painter(null as unknown as Ctx, origin.ox, origin.oy);
  const points = corners.map(c => probe.P(c[0], c[1], c[2]));
  const minX = Math.floor(Math.min(...points.map(p => p[0]))) - pad, minY = Math.floor(Math.min(...points.map(p => p[1]))) - pad;
  const maxX = Math.ceil(Math.max(...points.map(p => p[0]))) + pad, maxY = Math.ceil(Math.max(...points.map(p => p[1]))) + pad;
  const [canvas, ctx] = makeCanvas(maxX - minX + 1, maxY - minY + 1);
  draw(new Painter(ctx, origin.ox - minX, origin.oy - minY));
  outline(ctx, canvas.width, canvas.height);
  return { canvas, x: minX, y: minY };
}

export function outline(ctx: Ctx, w: number, h: number, color = INK) {
  const source = ctx.getImageData(0, 0, w, h), data = source.data, out = new Uint8ClampedArray(data);
  const alpha = (x: number, y: number) => (x < 0 || y < 0 || x >= w || y >= h ? 0 : data[(y * w + x) * 4 + 3]);
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
    const i = (y * w + x) * 4;
    if (data[i + 3]) continue;
    if (alpha(x - 1, y) || alpha(x + 1, y) || alpha(x, y - 1) || alpha(x, y + 1)) { out[i] = color[0]; out[i + 1] = color[1]; out[i + 2] = color[2]; out[i + 3] = 255; }
  }
  ctx.putImageData(new ImageData(out, w, h), 0, 0);
}
