import { expect, type Page } from "@playwright/test";
// The host's terminal and clean palettes, in both modes. Keep fixtures local so
// app browser tests also run outside the development workspace.
export const flowThemes = [
  {
    name: "terminal-dark",
    bg: "#0a0a0a",
    panel: "#141414",
    text: "#e8e8e8",
    muted: "#aaaaaa",
    border: "#2a2a2a",
    accent: "#f97316",
    success: "#22c55e",
    warn: "#eab308",
    error: "#ef4444",
    radius: "4px",
  },
  {
    name: "terminal-light",
    bg: "#faf8f3",
    panel: "#f1ede4",
    text: "#1a1a1a",
    muted: "#555555",
    border: "#d4ccb8",
    accent: "#c2410c",
    success: "#15803d",
    warn: "#a16207",
    error: "#b91c1c",
    radius: "4px",
  },
  {
    name: "clean-dark",
    bg: "#0f1419",
    panel: "#1a2128",
    text: "#e6e8eb",
    muted: "#a3aab3",
    border: "#2a3340",
    accent: "#6e8aff",
    success: "#4ade80",
    warn: "#fbbf24",
    error: "#f87171",
    radius: "8px",
  },
  {
    name: "clean-light",
    bg: "#ffffff",
    panel: "#f7f8fa",
    text: "#15171a",
    muted: "#4b5563",
    border: "#e5e7eb",
    accent: "#4f5fdb",
    success: "#047857",
    warn: "#b45309",
    error: "#b91c1c",
    radius: "8px",
  },
];
export async function applyFlowTheme(
  page: Page,
  t: (typeof flowThemes)[number],
) {
  await page.addStyleTag({
    content: `body{background:${t.bg};color-scheme:${t.name.endsWith("light") ? "light" : "dark"}}.ap-processes {--color-bg:${t.bg};--color-bg-card:${t.panel};--color-text:${t.text};--color-text-muted:${t.muted};--color-border:${t.border};--color-accent:${t.accent};--color-success:${t.success};--color-warn:${t.warn};--color-error:${t.error};--radius-md:${t.radius};--radius-lg:${t.name.startsWith("terminal") ? "6px" : "12px"};font-family:${t.name.startsWith("terminal") ? "ui-monospace,monospace" : "system-ui,sans-serif"}}`,
  });
}
export async function expectBorderContrast(page: Page, selector: string) {
  const ratios = await page.locator(selector).evaluateAll((elements) =>
    elements.map((el) => {
      const s = getComputedStyle(el),
        c = document.createElement("canvas");
      c.width = c.height = 1;
      const ctx = c.getContext("2d")!;
      const luminance = (color: string) => {
        ctx.clearRect(0, 0, 1, 1);
        ctx.fillStyle = color;
        ctx.fillRect(0, 0, 1, 1);
        const rgb = [...ctx.getImageData(0, 0, 1, 1).data]
          .slice(0, 3)
          .map((x) => {
            x /= 255;
            return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
          });
        return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
      };
      const a = luminance(s.borderTopColor),
        b = luminance(s.backgroundColor);
      return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
    }),
  );
  // Decorative outlines should sit between the faint host hairlines and the
  // overly bright v0.11.2 treatment. Text and active-state colors stay intact.
  for (const ratio of ratios) {
    expect(ratio).toBeGreaterThanOrEqual(1.7);
    expect(ratio).toBeLessThanOrEqual(3.2);
  }
}
