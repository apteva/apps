// qr.mjs — minimal byte-mode QR encoder for live-link.
//
// Scope:
//   - byte mode only (we always encode URLs)
//   - ECC level M (~15% recovery — comfortable margin at typical
//     screen+camera distances; phones lock on quickly)
//   - versions 1-10 (max ~210 byte chars, more than any tunnel URL
//     we'd ever produce — trycloudflare URLs are ~50 chars, named-mode
//     hostnames maybe 60)
//   - mask 0 (rule: (row + col) % 2 == 0) chosen unconditionally —
//     skip per-mask penalty scoring; mask 0 produces readable codes
//     for URL-shaped (high-entropy) input across every scanner we've
//     tested. If we ever need to support shorter or more structured
//     payloads, add scoring then.
//
// Output: an SVG string ready for `dangerouslySetInnerHTML`. Single
// path with one rect per dark module — small, scales crisply, prints
// at any size.
//
// Public-domain implementation written for live-link to avoid pulling
// any QR library through the dashboard's importmap. ~250 LOC.

// ─── ECC tables (level M only, versions 1-10) ─────────────────────
const M_DATA_CODEWORDS = [16, 28, 44, 64, 86, 108, 124, 154, 182, 216];
const M_ECC_PER_BLOCK  = [10, 16, 26, 18, 24, 16,  18,  22,  22,  26];
const M_BLOCKS         = [ 1,  1,  1,  2,  2,  4,   4,   4,   5,   5];

// Alignment pattern centers per version. Versions 2+ only.
const ALIGN = [
  null,
  [],
  [6, 18], [6, 22], [6, 26], [6, 30], [6, 34],
  [6, 22, 38], [6, 24, 42], [6, 26, 46], [6, 28, 50],
];

// ─── Galois field GF(256) with primitive 0x11d ────────────────────
const GF_EXP = new Array(512);
const GF_LOG = new Array(256);
(function initGF() {
  let x = 1;
  for (let i = 0; i < 255; i++) {
    GF_EXP[i] = x;
    GF_LOG[x] = i;
    x <<= 1;
    if (x & 0x100) x ^= 0x11d;
  }
  for (let i = 255; i < 512; i++) GF_EXP[i] = GF_EXP[i - 255];
})();

const gfMul = (a, b) => (a === 0 || b === 0) ? 0 : GF_EXP[GF_LOG[a] + GF_LOG[b]];

// Generator polynomial: ∏ (x - α^i) for i in 0..n-1, expressed as
// coefficients [g_0, g_1, ..., g_n] of degree n.
function genPoly(n) {
  let p = [1];
  for (let i = 0; i < n; i++) {
    const next = new Array(p.length + 1).fill(0);
    for (let j = 0; j < p.length; j++) {
      next[j]     ^= p[j];
      next[j + 1] ^= gfMul(p[j], GF_EXP[i]);
    }
    p = next;
  }
  return p;
}

// Reed-Solomon remainder: divides data * x^ecLen by genPoly(ecLen),
// returns the ecLen-coefficient remainder (the ECC codewords).
function rsRemainder(data, ecLen) {
  const poly = genPoly(ecLen);
  const r = new Array(ecLen).fill(0);
  for (const b of data) {
    const factor = b ^ r[0];
    r.shift();
    r.push(0);
    if (factor !== 0) {
      for (let i = 0; i < ecLen; i++) r[i] ^= gfMul(poly[i + 1], factor);
    }
  }
  return r;
}

// ─── Encoding pipeline ────────────────────────────────────────────

function pickVersion(byteLen) {
  for (let v = 1; v <= 10; v++) {
    const charCountBits = v >= 10 ? 16 : 8;
    const headerBits    = 4 + charCountBits;
    const dataBits      = M_DATA_CODEWORDS[v - 1] * 8;
    if (headerBits + byteLen * 8 + 4 <= dataBits) return v;
  }
  throw new Error("URL too long for QR (max ~210 bytes at ECC M, version 10)");
}

function encodeData(bytes, version) {
  const charCountBits = version >= 10 ? 16 : 8;
  const totalBits     = M_DATA_CODEWORDS[version - 1] * 8;

  let bits = "0100"; // mode indicator: byte
  bits += bytes.length.toString(2).padStart(charCountBits, "0");
  for (const b of bytes) bits += b.toString(2).padStart(8, "0");

  // Terminator: up to 4 zero bits.
  bits += "0".repeat(Math.min(4, totalBits - bits.length));
  // Pad to byte boundary.
  while (bits.length % 8 !== 0) bits += "0";
  // Pad with alternating 0xEC / 0x11 until full.
  const pad = ["11101100", "00010001"];
  let p = 0;
  while (bits.length < totalBits) { bits += pad[p]; p ^= 1; }

  const out = [];
  for (let i = 0; i < bits.length; i += 8) out.push(parseInt(bits.substr(i, 8), 2));
  return out;
}

// Split data into blocks per spec, compute ECC for each, and
// interleave column-by-column for placement.
function buildCodewords(dataCodewords, version) {
  const blocks = M_BLOCKS[version - 1];
  const ecLen  = M_ECC_PER_BLOCK[version - 1];
  const total  = dataCodewords.length;

  const baseLen   = Math.floor(total / blocks);
  const remainder = total % blocks;
  const numShort  = blocks - remainder;

  const dBlocks = [];
  const eBlocks = [];
  let off = 0;
  for (let i = 0; i < blocks; i++) {
    const len = i < numShort ? baseLen : baseLen + 1;
    const blk = dataCodewords.slice(off, off + len);
    off += len;
    dBlocks.push(blk);
    eBlocks.push(rsRemainder(blk, ecLen));
  }

  const out = [];
  const maxData = baseLen + (remainder > 0 ? 1 : 0);
  for (let col = 0; col < maxData; col++) {
    for (let b = 0; b < blocks; b++) {
      if (col < dBlocks[b].length) out.push(dBlocks[b][col]);
    }
  }
  for (let col = 0; col < ecLen; col++) {
    for (let b = 0; b < blocks; b++) out.push(eBlocks[b][col]);
  }
  return out;
}

// Format info BCH(15,5): ECC level (2 bits) + mask (3 bits) → 15-bit
// codeword XORed with 0x5412 (per spec, hides patterns of all-zeros).
function formatInfo(eccLevel, mask) {
  const data = (eccLevel << 3) | mask;       // 5-bit
  let bch = data << 10;
  for (let i = 14; i >= 10; i--) {
    if ((bch >> i) & 1) bch ^= 0x537 << (i - 10);
  }
  return ((data << 10) | (bch & 0x3ff)) ^ 0x5412;
}

// ─── Matrix construction ──────────────────────────────────────────

function buildMatrix(codewords, version) {
  const size = 17 + 4 * version;
  const m = Array.from({ length: size }, () => new Array(size).fill(0));
  const reserved = Array.from({ length: size }, () => new Array(size).fill(false));

  // Finder patterns + 1-module separators.
  const finder = (cx, cy) => {
    for (let dy = -1; dy <= 7; dy++) {
      for (let dx = -1; dx <= 7; dx++) {
        const x = cx + dx, y = cy + dy;
        if (x < 0 || y < 0 || x >= size || y >= size) continue;
        // Outer ring (perimeter of the 7×7) or 3×3 center → dark.
        const ring   = (dx >= 0 && dx <= 6 && (dy === 0 || dy === 6)) ||
                       (dy >= 0 && dy <= 6 && (dx === 0 || dx === 6));
        const center = (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4);
        m[y][x] = (ring || center) ? 1 : 0;
        reserved[y][x] = true;
      }
    }
  };
  finder(0, 0);
  finder(size - 7, 0);
  finder(0, size - 7);

  // Timing patterns (alternating runs along row 6 + col 6).
  for (let i = 8; i < size - 8; i++) {
    const v = (i & 1) === 0 ? 1 : 0;
    m[6][i] = v; reserved[6][i] = true;
    m[i][6] = v; reserved[i][6] = true;
  }

  // Alignment patterns (versions 2+).
  for (const ay of ALIGN[version]) {
    for (const ax of ALIGN[version]) {
      // Skip slots that overlap finder patterns.
      const overlapsFinder =
        (ax === 6 && ay === 6) ||
        (ax === 6 && ay === size - 7) ||
        (ax === size - 7 && ay === 6);
      if (overlapsFinder) continue;
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          const ring   = Math.abs(dx) === 2 || Math.abs(dy) === 2;
          const center = dx === 0 && dy === 0;
          m[ay + dy][ax + dx] = (ring || center) ? 1 : 0;
          reserved[ay + dy][ax + dx] = true;
        }
      }
    }
  }

  // Reserve format-info modules (filled after data placement).
  for (let i = 0; i <= 8; i++) {
    if (i !== 6) { reserved[8][i] = true; reserved[i][8] = true; }
  }
  reserved[8][size - 8] = true;
  for (let i = size - 8; i < size; i++) reserved[8][i] = true;
  for (let i = size - 7; i < size; i++) reserved[i][8] = true;
  // Dark module — always 1, always at (size-8, 8).
  m[size - 8][8] = 1;
  reserved[size - 8][8] = true;

  // Place data in the standard zigzag, top-right → bottom-left,
  // skipping reserved cells. Mask 0: invert when (row + col) is even.
  let bitIdx = 0;
  const nextBit = () => {
    const byte = bitIdx >> 3;
    if (byte >= codewords.length) return 0;
    return (codewords[byte] >> (7 - (bitIdx & 7))) & 1;
  };

  let upward = true;
  for (let xRight = size - 1; xRight > 0; xRight -= 2) {
    if (xRight === 6) xRight--; // skip the timing column
    for (let step = 0; step < size; step++) {
      const y = upward ? size - 1 - step : step;
      for (let dx = 0; dx < 2; dx++) {
        const x = xRight - dx;
        if (reserved[y][x]) continue;
        let bit = nextBit();
        bitIdx++;
        if (((y + x) & 1) === 0) bit ^= 1; // mask 0
        m[y][x] = bit;
      }
    }
    upward = !upward;
  }

  // Format info: ECC M = 0b00, mask = 0.
  const fmt = formatInfo(0b00, 0);
  for (let i = 0; i < 15; i++) {
    const b = (fmt >> i) & 1;
    // Copy 1: around top-left finder (vertical strip on col 8, then
    // horizontal strip on row 8).
    let x, y;
    if (i < 6)       { x = 8; y = i; }
    else if (i === 6) { x = 8; y = 7; }
    else if (i === 7) { x = 8; y = 8; }
    else if (i === 8) { x = 7; y = 8; }
    else              { x = 14 - i; y = 8; }
    m[y][x] = b;

    // Copy 2: across the bottom-right of top-right + top of
    // bottom-left finder.
    if (i < 8)        { x = size - 1 - i; y = 8; }
    else              { x = 8; y = size - 15 + i; }
    m[y][x] = b;
  }

  return m;
}

// ─── Public entrypoint ────────────────────────────────────────────

export function qrSVG(text, opts = {}) {
  const px      = opts.size   ?? 192;
  const margin  = opts.margin ?? 4;
  const dark    = opts.dark   ?? "#000";
  const light   = opts.light  ?? "transparent";

  const bytes = Array.from(new TextEncoder().encode(text));
  const ver   = pickVersion(bytes.length);
  const data  = encodeData(bytes, ver);
  const cws   = buildCodewords(data, ver);
  const mat   = buildMatrix(cws, ver);

  const n     = mat.length;
  const total = n + margin * 2;

  let path = "";
  for (let y = 0; y < n; y++) {
    for (let x = 0; x < n; x++) {
      if (mat[y][x]) path += `M${x + margin} ${y + margin}h1v1h-1z`;
    }
  }

  return (
    `<svg xmlns="http://www.w3.org/2000/svg" ` +
    `viewBox="0 0 ${total} ${total}" width="${px}" height="${px}" ` +
    `shape-rendering="crispEdges">` +
    `<rect width="${total}" height="${total}" fill="${light}"/>` +
    `<path d="${path}" fill="${dark}"/>` +
    `</svg>`
  );
}
