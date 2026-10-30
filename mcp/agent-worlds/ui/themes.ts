import type { PixelTheme } from "./pixel/theme";
import { station } from "./pixel/themes/station";

// A theme pairs a layout (graph, board or map) with a look. The village is
// drawn by the isometric engine; other map themes share the top-down pixel
// engine and differ only in their painters.
export type ThemeEntry = {
  id: string;
  name: string;
  detail: string;
  layout: "graph" | "board" | "map";
  pixel?: PixelTheme;
  iso?: boolean;
};

export const THEMES: ThemeEntry[] = [
  { id: "graph", name: "System graph", detail: "Agents, apps and integrations", layout: "graph" },
  { id: "operations", name: "Operations", detail: "Live status board", layout: "board" },
  { id: "village", name: "Pixel village", detail: "Agents walking into the buildings of the apps they use", layout: "map", iso: true },
  { id: "station", name: "Space station", detail: "Agents crewing app modules", layout: "map", pixel: station },
];

export const themeById = (id: string) => THEMES.find(theme => theme.id === id) ?? THEMES[2];
