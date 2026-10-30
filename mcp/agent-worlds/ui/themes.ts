import type { PixelTheme } from "./pixel/theme";
import { village } from "./pixel/themes/village";
import { station } from "./pixel/themes/station";

// A theme pairs a layout (graph, board or map) with a look. Map themes share
// the pixel engine and differ only in their painters.
export type ThemeEntry = {
  id: string;
  name: string;
  detail: string;
  layout: "graph" | "board" | "map";
  pixel?: PixelTheme;
};

export const THEMES: ThemeEntry[] = [
  { id: "graph", name: "System graph", detail: "Agents, apps and integrations", layout: "graph" },
  { id: "operations", name: "Operations", detail: "Live status board", layout: "board" },
  { id: "village", name: "Pixel village", detail: "Agents visiting apps and integrations", layout: "map", pixel: village },
  { id: "station", name: "Space station", detail: "Agents crewing app modules", layout: "map", pixel: station },
];

export const themeById = (id: string) => THEMES.find(theme => theme.id === id) ?? THEMES[2];
