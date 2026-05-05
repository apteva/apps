// ScreenshotCard — chat-attached annotated screenshot. Renders the
// image with SoM (Set-of-Mark) badges as an SVG overlay so the
// operator sees the same numbered targets the agent picked from.
// Manifest name "screenshot-with-som" — the file is named
// ScreenshotCard.tsx because the build glob picks up *Card.tsx.
//
// Self-contained. No data fetch — everything is in props.

import { useState } from "react";
import { Card, CardHeader } from "@apteva/ui-kit";

interface SoMItem {
  label: number;
  x: number;
  y: number;
  w: number;
  h: number;
  /** Visual hint, drives badge color. */
  kind?: "input" | "button" | "link" | "select" | string;
}

interface Props {
  screenshot_url: string;
  som?: SoMItem[];
  caption?: string;
  preview?: boolean;
}

const KIND_COLORS: Record<string, string> = {
  input: "#3b82f6",
  button: "#22c55e",
  link: "#a855f7",
  select: "#f59e0b",
  default: "#ef4444",
};

function badgeColor(kind?: string) {
  return KIND_COLORS[kind ?? "default"] ?? KIND_COLORS.default;
}

export default function ScreenshotCard(props: Props) {
  const [hovered, setHovered] = useState<number | null>(null);
  const som = props.preview ? PREVIEW_SOM : props.som ?? [];
  const src = props.preview ? PREVIEW_SCREENSHOT : props.screenshot_url;

  // Natural image dimensions are unknown until load; we render the
  // SVG with viewBox matching the SoM coordinates — the agent's SoM
  // pipeline uses display-pixel coordinates, so 0..displayWidth maps
  // 1:1 once we know it. Until first load, fall back to 1280x800
  // (the local backend's default DisplaySize).
  const [dims, setDims] = useState<{ w: number; h: number }>({ w: 1280, h: 800 });

  return (
    <Card>
      {props.caption && <CardHeader title={props.caption} />}
      <div className="relative inline-block w-full">
        <img
          src={src}
          alt={props.caption ?? "screenshot"}
          className="block w-full h-auto rounded border border-zinc-200 dark:border-zinc-700"
          onLoad={(e) => {
            const img = e.currentTarget;
            setDims({ w: img.naturalWidth, h: img.naturalHeight });
          }}
        />
        {som.length > 0 && (
          <svg
            viewBox={`0 0 ${dims.w} ${dims.h}`}
            preserveAspectRatio="none"
            className="absolute inset-0 w-full h-full pointer-events-auto"
          >
            {som.map((m) => {
              const fill = badgeColor(m.kind);
              const isHover = hovered === m.label;
              return (
                <g
                  key={m.label}
                  onMouseEnter={() => setHovered(m.label)}
                  onMouseLeave={() => setHovered(null)}
                  style={{ cursor: "pointer" }}
                >
                  <rect
                    x={m.x}
                    y={m.y}
                    width={m.w}
                    height={m.h}
                    fill={isHover ? fill : "none"}
                    fillOpacity={isHover ? 0.18 : 0}
                    stroke={fill}
                    strokeWidth={isHover ? 3 : 2}
                  />
                  <circle
                    cx={m.x + 12}
                    cy={m.y + 12}
                    r={11}
                    fill={fill}
                    stroke="white"
                    strokeWidth={2}
                  />
                  <text
                    x={m.x + 12}
                    y={m.y + 16}
                    textAnchor="middle"
                    fontSize={13}
                    fontWeight={700}
                    fill="white"
                    style={{ userSelect: "none" }}
                  >
                    {m.label}
                  </text>
                </g>
              );
            })}
          </svg>
        )}
      </div>
      {som.length > 0 && (
        <p className="mt-2 text-xs text-zinc-500 dark:text-zinc-400">
          {som.length} marked element{som.length === 1 ? "" : "s"} · hover a badge to highlight
        </p>
      )}
    </Card>
  );
}

const PREVIEW_SCREENSHOT =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 800"><rect width="1280" height="800" fill="#fafafa"/><rect x="380" y="280" width="520" height="48" fill="white" stroke="#d4d4d8"/><rect x="380" y="348" width="520" height="48" fill="white" stroke="#d4d4d8"/><rect x="380" y="420" width="200" height="44" fill="#0070f3"/><text x="480" y="448" text-anchor="middle" fill="white" font-family="sans-serif" font-size="16">Sign in</text><text x="640" y="220" text-anchor="middle" fill="#27272a" font-family="sans-serif" font-size="32" font-weight="600">Welcome back</text></svg>`,
  );

const PREVIEW_SOM: SoMItem[] = [
  { label: 1, x: 380, y: 280, w: 520, h: 48, kind: "input" },
  { label: 2, x: 380, y: 348, w: 520, h: 48, kind: "input" },
  { label: 3, x: 380, y: 420, w: 200, h: 44, kind: "button" },
];
