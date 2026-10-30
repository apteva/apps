// Residents: 12x20 hand-authored templates in a 16x24 cell, palette-swapped
// per agent so each one is recognisable, with a four-frame walk cycle.
import { makeCanvas, rect, stamp, seedFor, type Ctx } from "./core";
import type { Facing } from "./motion";

export const CELL = { w: 16, h: 24 };

const HEAD_DOWN = [
  "....oooo....",
  "..oohhhhoo..",
  ".ohhhhhhhho.",
  ".ohhhhhhhho.",
  ".ohhsssshho.",
  ".ohssssssho.",
  ".osesssseso.",
  ".osssssssso.",
  "..oSssssSo..",
  "...oSSSSo...",
];
const HEAD_UP = [
  "....oooo....",
  "..oohhhhoo..",
  ".ohhhhhhhho.",
  ".ohhhhhhhho.",
  ".ohhhhhhhho.",
  ".ohhhhhhhho.",
  ".oHhhhhhhHo.",
  ".oHHhhhhHHo.",
  "..osssssso..",
  "...oSSSSo...",
];
const HEAD_SIDE = [
  "....oooo....",
  "...ohhhhoo..",
  "..ohhhhhhho.",
  "..ohhhhhhho.",
  "..ohhhhssso.",
  "..ohhhsssso.",
  "..ohhsssesoo",
  "..ohssssssso",
  "...oSsssSoo.",
  "....oSSSo...",
];
const BODY_FRONT = [
  "..occcccco..",
  ".occcccccco.",
  "occcccccccco",
  "oCccccccccCo",
  "osCccccccCso",
  ".oppppppppo.",
];
const BODY_SIDE = [
  "...occccco..",
  "..occccccco.",
  "..occccccco.",
  "..oCccCccco.",
  "..oCcsCcCo..",
  "..oppppppo..",
];
// Legs per walk frame: [left length, right length] for front/back views.
const STRIDE = [[4, 4], [3, 4], [4, 4], [4, 3]];

// Long hair falls past the shoulders; caps replace the hair top.
function hairStyle(rows: string[], style: number, facing: Facing) {
  const out = [...rows];
  if (style === 1) {
    if (facing === "down") { out[7] = ".oHssssssHo."; out[8] = ".oHSssssSHo."; out[9] = ".oHHSSSSHHo."; }
    if (facing === "up") { out[8] = ".oHhhhhhhHo."; out[9] = ".oHHhhhhHHo."; }
    if (facing === "left" || facing === "right") { out[7] = "..oHssssssso"; out[8] = "..oHSsssSoo."; out[9] = "..oHHSSSo..."; }
  }
  if (style === 2) {
    out[0] = "...oooooo...";
    out[1] = "..occccccoo.";
    out[2] = ".occccccccco";
    out[3] = facing === "up" ? ".oCCCCCCCCo." : ".oCCCCCCCCCo";
  }
  return out;
}

export type Look = { skin: [string, string]; hair: [string, string]; shirt: [string, string]; pants: [string, string]; style: number };

const SKIN: [string, string][] = [["#f2c9a0", "#d9a47a"], ["#e0ac7e", "#c08a5e"], ["#b97d55", "#9a6342"], ["#8a5a3c", "#6e452d"], ["#f5d6b8", "#e0b896"]];
const HAIR: [string, string][] = [["#3b2a20", "#2a1d16"], ["#7a4a2a", "#5c3620"], ["#c9873b", "#a26a2a"], ["#e6c76a", "#c4a44f"], ["#2a2a35", "#1b1b24"], ["#a33b2e", "#7d2b22"], ["#d9d4cc", "#b5afa5"]];
export const OUTFITS: [string, string][] = [["#d9573f", "#b04431"], ["#3f7fc7", "#2f62a0"], ["#4fa35a", "#3b8045"], ["#e0a93b", "#b88a2c"], ["#8e5bb5", "#6f4592"], ["#2fa39b", "#23807a"], ["#e07fa8", "#bd6187"], ["#5a6170", "#454b58"]];
const PANTS: [string, string][] = [["#3b4a6b", "#2d3852"], ["#5a4636", "#45352a"], ["#2f2f3a", "#22222b"], ["#6b7a4a", "#525e38"]];

// A team shares an outfit colour; everything else comes from the agent ID.
export function lookFor(agentId: string, team?: string, jumpsuit = false): Look {
  const seed = seedFor(agentId);
  const outfit = OUTFITS[(team ? seedFor(team) : seed >>> 3) % OUTFITS.length];
  return { skin: SKIN[seed % SKIN.length], hair: HAIR[(seed >>> 5) % HAIR.length], shirt: outfit, pants: jumpsuit ? outfit : PANTS[(seed >>> 9) % PANTS.length], style: (seed >>> 13) % 3 };
}

const cache = new Map<string, HTMLCanvasElement>();

export function characterFrame(look: Look, facing: Facing, frame: number, outline = "#2b2233") {
  const key = `${look.skin}|${look.hair}|${look.shirt}|${look.pants}|${look.style}|${facing}|${frame}|${outline}`;
  const hit = cache.get(key);
  if (hit) return hit;
  const [canvas, ctx] = makeCanvas(CELL.w, CELL.h);
  const colors: Record<string, string> = {
    o: outline, h: look.hair[0], H: look.hair[1], s: look.skin[0], S: look.skin[1], e: "#2b2233",
    c: look.shirt[0], C: look.shirt[1], p: look.pants[0], P: look.pants[1],
  };
  const side = facing === "left" || facing === "right";
  const flip = facing === "left";
  const bob = frame % 2 ? 1 : 0;
  const top = 2 + bob;
  const head = hairStyle(side ? HEAD_SIDE : facing === "up" ? HEAD_UP : HEAD_DOWN, look.style, facing);
  stamp(ctx, head, colors, 2, top, flip);
  stamp(ctx, side ? BODY_SIDE : BODY_FRONT, colors, 2, top + 10, flip);
  drawLegs(ctx, colors, frame, side, flip, top + 16);
  if (cache.size > 2000) cache.clear();
  cache.set(key, canvas);
  return canvas;
}

function drawLegs(ctx: Ctx, colors: Record<string, string>, frame: number, side: boolean, flip: boolean, y: number) {
  const boot = "#3a2a22";
  if (side) {
    // Profile stride: legs spread on steps, together when standing.
    const spread = frame % 2 ? (frame === 1 ? 2 : -2) : 0;
    const legs = spread ? [{ x: 5 - Math.abs(spread) / 2, front: spread > 0 }, { x: 7 + Math.abs(spread) / 2, front: spread < 0 }] : [{ x: 6, front: true }];
    for (const leg of legs) {
      const x = flip ? 15 - leg.x - 2 : leg.x;
      rect(ctx, x - 1, y, 4, 5, colors.o);
      rect(ctx, x, y, 2, 3, leg.front ? colors.p : colors.P);
      rect(ctx, flip ? x - 1 : x, y + 3, 3, 1, boot);
    }
    return;
  }
  const [left, right] = STRIDE[frame % 4];
  for (const [x, length] of [[5, left], [9, right]] as const) {
    rect(ctx, x - 1, y, 4, length + 1, colors.o);
    rect(ctx, x, y, 2, length - 1, colors.p);
    rect(ctx, x, y + length - 1, 2, 1, boot);
  }
}

// Speech-bubble emotes drawn above a resident's head.
export type Emote = "thinking" | "error" | "message" | "working" | "waiting" | "done";
const EMOTE_ICONS: Record<Emote, { rows: string[]; color: string }> = {
  thinking: { rows: [".......", ".......", ".x.x.x.", "......."], color: "#4a4458" },
  error: { rows: ["...x...", "...x...", ".......", "...x..."], color: "#d0412f" },
  message: { rows: ["xxxxxxx", "xx...xx", "x.xxx.x", "xxxxxxx"], color: "#3f7fc7" },
  working: { rows: ["..x.x..", ".xxxxx.", ".xx.xx.", ".xxxxx."], color: "#c7782e" },
  waiting: { rows: ["xxxx...", "..x....", ".x.....", "xxxx..."], color: "#7a6f99" },
  done: { rows: [".....x.", "....x..", "x.xx...", ".x....."], color: "#3b8f4a" },
};

export function drawEmote(ctx: Ctx, emote: Emote, x: number, y: number, blink: boolean) {
  const bx = Math.round(x - 5), by = Math.round(y - 9);
  rect(ctx, bx + 1, by, 9, 7, "#2b2233");
  rect(ctx, bx, by + 1, 11, 5, "#2b2233");
  rect(ctx, bx + 1, by + 1, 9, 5, "#fbf6ea");
  rect(ctx, bx + 4, by + 7, 2, 1, "#2b2233");
  rect(ctx, bx + 4, by + 6, 2, 1, "#fbf6ea");
  rect(ctx, bx + 5, by + 8, 1, 1, "#2b2233");
  const icon = EMOTE_ICONS[emote];
  if (emote === "thinking" && blink) return;
  stamp(ctx, icon.rows, { x: icon.color }, bx + 2, by + 1.5);
}
