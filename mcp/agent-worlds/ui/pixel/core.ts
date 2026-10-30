// Shared pixel-art primitives. Everything is drawn at native resolution on an
// integer grid and scaled up by whole numbers, so edges stay crisp.

export type Ctx = CanvasRenderingContext2D;
export type Point = { x: number; y: number };

export const TILE = 16;

export function seedFor(value: string) {
  return [...value].reduce((seed, char) => (Math.imul(seed, 31) + char.charCodeAt(0)) >>> 0, 7);
}

// xorshift32: deterministic per seed, so a project's village never reshuffles.
export function rng(seed: number) {
  let state = (seed >>> 0) || 0x9e3779b9;
  return () => {
    state ^= state << 13;
    state ^= state >>> 17;
    state ^= state << 5;
    return (state >>> 0) / 4294967296;
  };
}

export function pick<T>(items: readonly T[], random: () => number): T {
  return items[Math.floor(random() * items.length) % items.length];
}

export function makeCanvas(width: number, height: number): [HTMLCanvasElement, Ctx] {
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.ceil(width));
  canvas.height = Math.max(1, Math.ceil(height));
  const ctx = canvas.getContext("2d")!;
  ctx.imageSmoothingEnabled = false;
  return [canvas, ctx];
}

export function rect(ctx: Ctx, x: number, y: number, w: number, h: number, color: string) {
  ctx.fillStyle = color;
  ctx.fillRect(Math.round(x), Math.round(y), Math.round(w), Math.round(h));
}

export function px(ctx: Ctx, x: number, y: number, color: string) {
  ctx.fillStyle = color;
  ctx.fillRect(Math.round(x), Math.round(y), 1, 1);
}

// Pixel-snapped filled disc; used for canopies, shadows and glows.
export function disc(ctx: Ctx, cx: number, cy: number, r: number, color: string, squash = 1) {
  ctx.fillStyle = color;
  const rows = Math.ceil(r * squash);
  for (let dy = -rows; dy <= rows; dy++) {
    const span = Math.floor(Math.sqrt(Math.max(0, r * r - (dy / squash) * (dy / squash))));
    ctx.fillRect(Math.round(cx - span), Math.round(cy + dy), span * 2 + 1, 1);
  }
}

// Template sprites: one character per pixel, "." is transparent.
export function stamp(ctx: Ctx, rows: readonly string[], colors: Record<string, string>, x: number, y: number, flip = false) {
  const width = rows[0]?.length || 0;
  rows.forEach((row, dy) => {
    for (let dx = 0; dx < row.length; dx++) {
      const color = colors[row[dx]];
      if (!color) continue;
      ctx.fillStyle = color;
      ctx.fillRect(Math.round(x + (flip ? width - 1 - dx : dx)), Math.round(y + dy), 1, 1);
    }
  });
}

export function shadow(ctx: Ctx, cx: number, cy: number, rx: number, alpha = 0.28) {
  ctx.save();
  ctx.globalAlpha = alpha;
  disc(ctx, cx, cy, rx, "#1d1a24", 0.38);
  ctx.restore();
}

// 3x5 bitmap font. Labels render on the same pixel grid as the world.
const GLYPHS: Record<string, string> = {
  A: "010101111101101", B: "110101110101110", C: "011100100100011", D: "110101101101110",
  E: "111100110100111", F: "111100110100100", G: "011100101101011", H: "101101111101101",
  I: "111010010010111", J: "001001001101010", K: "101101110101101", L: "100100100100111",
  M: "101111111101101", N: "110101101101101", O: "010101101101010", P: "110101110100100",
  Q: "010101101110011", R: "110101110101101", S: "011100010001110", T: "111010010010010",
  U: "101101101101111", V: "101101101101010", W: "101101111111101", X: "101101010101101",
  Y: "101101010010010", Z: "111001010100111", "0": "111101101101111", "1": "010110010010111",
  "2": "110001010100111", "3": "110001010001110", "4": "101101111001001", "5": "111100110001110",
  "6": "011100111101111", "7": "111001010010010", "8": "111101111101111", "9": "111101111001110",
  ".": "000000000000010", "·": "000000010000000", "-": "000000111000000", "_": "000000000000111", ":": "000010000010000",
  "/": "001001010100100", "'": "010010000000000", "!": "010010010000010", "?": "110001010000010",
  "+": "000010111010000", "&": "010101010101011", "(": "001010010010001", ")": "100010010010100",
  "#": "101111101111101", ",": "000000000010100", "@": "111101111100011", " ": "000000000000000",
};

export function normalizeText(text: string) {
  return text.normalize("NFD").replace(/[̀-ͯ]/g, "").toUpperCase();
}

export function textWidth(text: string, scale = 1) {
  const length = normalizeText(text).length;
  return length ? (length * 4 - 1) * scale : 0;
}

export function text(ctx: Ctx, value: string, x: number, y: number, color: string, scale = 1) {
  ctx.fillStyle = color;
  let cursor = Math.round(x);
  for (const char of normalizeText(value)) {
    const glyph = GLYPHS[char] ?? GLYPHS["?"];
    for (let i = 0; i < 15; i++) {
      if (glyph[i] === "1") ctx.fillRect(cursor + (i % 3) * scale, Math.round(y) + Math.floor(i / 3) * scale, scale, scale);
    }
    cursor += 4 * scale;
  }
}

export function fitText(value: string, maxWidth: number, scale = 1) {
  let out = normalizeText(value.trim());
  if (textWidth(out, scale) <= maxWidth) return out;
  while (out.length > 1 && textWidth(out + ".", scale) > maxWidth) out = out.slice(0, -1);
  return out + ".";
}

// A label plate: dark body, 1px light rim, notched pixel corners.
export function plate(ctx: Ctx, value: string, cx: number, top: number, scale: number, colors: { bg: string; rim: string; text: string }) {
  const width = textWidth(value, scale) + 6 * scale;
  const height = 9 * scale;
  const x = Math.round(cx - width / 2), y = Math.round(top);
  rect(ctx, x + scale, y, width - 2 * scale, height, colors.rim);
  rect(ctx, x, y + scale, width, height - 2 * scale, colors.rim);
  rect(ctx, x + scale, y + scale, width - 2 * scale, height - 2 * scale, colors.bg);
  text(ctx, value, x + 3 * scale, y + 2 * scale, colors.text, scale);
  return { x, y, width, height };
}
