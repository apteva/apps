import type { HealthDetails, ProviderClass } from "../api/health.ts";

// Compact pill showing the live data source for a given asset class.
// Three visual states:
//   live  (green)   — provider name ≠ "mock" and not stale
//   mock  (muted)   — provider name = "mock"; offline or fallback
//   stale (amber)   — last_ok_at is older than the staleness threshold
//
// Hovering the pill reveals errors_60s and last_ok_at as a tooltip.

export function DataSourcePill({
  health,
  cls,
}: {
  health: HealthDetails | null;
  cls: ProviderClass;
}) {
  if (!health || !health.providers || !health.providers[cls]) return null;
  const p = health.providers[cls]!;
  const name = (p.name ?? "mock").replace("-public", "");
  const isLive = name !== "mock" && !p.stale;

  const tone = p.stale ? "warn" : isLive ? "up" : "muted";
  const colorVar =
    tone === "up"   ? "var(--color-up)" :
    tone === "warn" ? "var(--color-warn)" :
                      "var(--text-tertiary)";
  const bgVar =
    tone === "up"   ? "var(--color-up-light)" :
    tone === "warn" ? "var(--color-warn-light)" :
                      "var(--surface-inset)";

  const tooltip = [
    `${cls}: ${p.name ?? "mock"}`,
    p.last_ok_at ? `last_ok_at: ${new Date(p.last_ok_at).toLocaleTimeString()}` : "",
    p.errors_60s ? `errors_60s: ${p.errors_60s}` : "",
  ].filter(Boolean).join(" · ");

  const label = p.stale ? `${cls} · stale` : isLive ? `${cls} · live` : `${cls} · mock`;

  return (
    <span
      className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md text-[10.5px] font-medium tabular mono"
      style={{ background: bgVar, color: colorVar }}
      title={tooltip}
    >
      <span
        className="inline-block w-1.5 h-1.5 rounded-full"
        style={{ background: colorVar }}
      />
      {label}
    </span>
  );
}
