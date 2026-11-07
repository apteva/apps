import { useEffect, useState } from "react";
import type { Event } from "./world-model";

// Dashboard theme tokens, read from CSS variables so SVG views follow the host theme.
export const clock = (value: string) => new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
export const short = (value: string, length = 28) => value.length > length ? value.slice(0, length - 1) + "…" : value;
export type Palette = { font: string; bg: string; card: string; subtle: string; input: string; text: string; muted: string; dim: string; border: string; strong: string; accent: string; success: string; warn: string; error: string; info: string };
const fallback: Palette = { font: "JetBrains Mono, ui-monospace, monospace", bg: "#0a0a0a", card: "#141414", subtle: "#101010", input: "#111111", text: "#e8e8e8", muted: "#aaaaaa", dim: "#888888", border: "#2a2a2a", strong: "#4a4a4a", accent: "#f97316", success: "#22c55e", warn: "#eab308", error: "#ef4444", info: "#3b82f6" };
function readPalette(): Palette {
  if (typeof window === "undefined") return fallback;
  const css = getComputedStyle(document.documentElement);
  const color = (token: string, fallbackColor: string) => css.getPropertyValue(`--${token}`).trim() || fallbackColor;
  return { font: color("font-base", fallback.font), bg: color("bg", fallback.bg), card: color("bg-card", fallback.card), subtle: color("bg-subtle", fallback.subtle), input: color("bg-input", fallback.input), text: color("text", fallback.text), muted: color("text-muted", fallback.muted), dim: color("text-dim", fallback.dim), border: color("border", fallback.border), strong: color("border-strong", fallback.strong), accent: color("accent", fallback.accent), success: color("success", fallback.success), warn: color("warn", fallback.warn), error: color("error", fallback.error), info: color("info", fallback.info) };
}
export function usePalette() {
  const [palette, setPalette] = useState<Palette>(readPalette);
  useEffect(() => {
    const update = () => setPalette(readPalette());
    update();
    const observer = new MutationObserver(update);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "data-mode", "class"] });
    return () => observer.disconnect();
  }, []);
  return palette;
}
export const statusColor = (status: string, palette: Palette) => status === "running" || status === "active" ? palette.success : status === "error" || status === "failed" ? palette.error : palette.warn;
export const eventColor = (kind: string, palette: Palette) => ({ tool: palette.info, message: palette.accent, error: palette.error, thinking: palette.warn, spawn: palette.success, done: palette.success } as Record<string, string>)[kind] || palette.dim;

export function latestByAgent(events: Event[]) {
  const latest = new Map<string, Event>();
  for (const event of events) latest.set(event.agent_id, event);
  return latest;
}

