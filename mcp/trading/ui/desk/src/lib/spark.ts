// Deterministic 30-point pseudo-walk per symbol. Used only for the
// watchlist sparkline + the chart's synthesized history view. Keeping
// it client-side avoids a roundtrip per symbol on first paint and means
// switching between symbols is instant. The numbers don't drive any
// real logic — they're decoration.

export function spark(seed: string, bias: number): number[] {
  let s = 0;
  for (let i = 0; i < seed.length; i++) s = (s * 31 + seed.charCodeAt(i)) | 0;
  const rng = () => {
    s = (s * 1664525 + 1013904223) | 0;
    return ((s >>> 0) / 0xffffffff);
  };
  const out: number[] = [];
  let v = 100 - bias * 8;
  for (let i = 0; i < 30; i++) {
    v += (rng() - 0.5) * 1.6 + bias * 0.18;
    out.push(v);
  }
  return out;
}
