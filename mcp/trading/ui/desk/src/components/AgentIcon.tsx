// Per-agent identity icon. Replaces the early "emoji per agent"
// stand-in with proper line glyphs that fit a financial-terminal feel.
//
// New agents add a case here + a matching `icon` field on the
// portfolio record.

export type AgentIconName = "vault" | "bolt" | "target" | "layers";

export function AgentIcon({
  name,
  size = 18,
  className,
}: {
  name: AgentIconName;
  size?: number;
  className?: string;
}) {
  const common = {
    width: size,
    height: size,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.6,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    className,
  };
  switch (name) {
    case "vault":
      // Long-term equity — a vault. Conservative, capital-preservation feel.
      return (
        <svg {...common}>
          <rect x="3" y="4" width="18" height="16" rx="2" />
          <circle cx="12" cy="12" r="3.4" />
          <circle cx="12" cy="12" r="0.9" fill="currentColor" stroke="none" />
          <path d="M12 8.6V7.2M12 16.8v-1.4M8.6 12H7.2M16.8 12h-1.4" />
        </svg>
      );
    case "bolt":
      // Crypto momentum — a lightning bolt. Speed, energy.
      return (
        <svg {...common}>
          <path d="M13 2 4 14h6l-1 8 9-12h-6l1-8z" />
        </svg>
      );
    case "target":
      // Macro predictions — a crosshair. Precision, conviction.
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="9" />
          <circle cx="12" cy="12" r="5.5" />
          <circle cx="12" cy="12" r="2" />
          <path d="M12 1.5V4M12 20v2.5M1.5 12H4M20 12h2.5" />
        </svg>
      );
    case "layers":
      // Multi-asset discretionary — stacked layers.
      return (
        <svg {...common}>
          <path d="M12 2.5 2.5 7.5 12 12.5 21.5 7.5 12 2.5z" />
          <path d="m2.5 12 9.5 5 9.5-5" />
          <path d="m2.5 16.5 9.5 5 9.5-5" />
        </svg>
      );
  }
}
