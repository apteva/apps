import type { AgentIconName } from "../components/AgentIcon.tsx";
import type { Portfolio } from "../api/types.ts";

// The API doesn't ship a per-portfolio glyph yet, so we derive one
// from the portfolio's allowed asset classes — purely cosmetic.
//
// Single-class polymarket → target. Single-class crypto → bolt.
// Mixed classes → layers. Else → vault (equity / etf default).
export function iconFor(p: Portfolio): AgentIconName {
  const cls = new Set(p.allowed_classes);
  if (cls.size === 1) {
    if (cls.has("polymarket")) return "target";
    if (cls.has("crypto"))     return "bolt";
  }
  if (cls.size >= 3) return "layers";
  return "vault";
}
