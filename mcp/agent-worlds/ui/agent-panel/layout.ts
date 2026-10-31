import { useCallback, useEffect, useRef, useState } from "react";

// The side panel is a list of blocks. Built-in blocks are "worlds:<name>";
// app blocks are "<app>:<component>" from the dashboard.agent_detail slot,
// stored in the same shape the dashboard uses so the server validates their
// settings against each app's schema.
export type BlockHeight = "auto" | "short" | "tall";
export type Block = { id: string; component: string; settings?: Record<string, unknown>; height?: BlockHeight };
export type PanelKind = "agent" | "place";

export const BUILTIN_BLOCKS: Record<PanelKind, { component: string; label: string }[]> = {
  agent: [
    { component: "worlds:now", label: "Now" },
    { component: "worlds:activity", label: "Activity" },
    { component: "worlds:threads", label: "Threads" },
    { component: "worlds:apps", label: "Apps used" },
    { component: "worlds:handoffs", label: "Handoffs" },
  ],
  place: [
    { component: "worlds:visitors", label: "Who is here" },
    { component: "worlds:calls", label: "Recent calls" },
    { component: "worlds:tools", label: "Most used tools" },
  ],
};

export const SURFACES: Record<PanelKind, string> = { agent: "agent-worlds.agent_panel", place: "agent-worlds.place_panel" };
export const BLOCK_HEIGHTS: Record<BlockHeight, number | undefined> = { auto: undefined, short: 220, tall: 460 };

let sequence = 0;
export const newBlockId = (component: string) => `${component}:${Date.now().toString(36)}${(sequence++).toString(36)}`;

// Conversations sits under Now and Activity when it is installed, so a new
// panel already lets you talk to the agent.
export function defaultBlocks(kind: PanelKind, appComponents: string[]): Block[] {
  if (kind === "place") return BUILTIN_BLOCKS.place.map(block => ({ id: block.component, component: block.component }));
  const blocks: Block[] = [{ id: "worlds:now", component: "worlds:now" }, { id: "worlds:activity", component: "worlds:activity", height: "short" }];
  if (appComponents.includes("conversations:agent-conversations")) blocks.push({ id: "conversations:agent-conversations", component: "conversations:agent-conversations", height: "tall" });
  return blocks;
}

// Saved values come from storage that other versions or the dashboard may
// have written; anything that is not a block is dropped.
export function parseBlocks(value: unknown): Block[] | null {
  if (!Array.isArray(value) || !value.length) return null;
  const blocks = value.filter((item): item is Block => !!item && typeof item === "object" && typeof (item as Block).id === "string" && typeof (item as Block).component === "string" && (item as Block).component.includes(":"))
    .map(block => ({ id: block.id, component: block.component, ...(block.settings && typeof block.settings === "object" ? { settings: block.settings } : {}), ...(block.height && block.height in BLOCK_HEIGHTS ? { height: block.height } : {}) }));
  return blocks.length ? blocks : null;
}

const surfaceURL = (projectId: string, surface: string) => `/api/ui-layout/projects/${encodeURIComponent(projectId)}/surfaces/${surface}`;
const localKey = (surface: string) => `agent-worlds:${surface}`;
function readLocal(surface: string) {
  try { return parseBlocks(JSON.parse(window.localStorage.getItem(localKey(surface)) || "null")); } catch { return null; }
}
function writeLocal(surface: string, blocks: Block[]) {
  try { window.localStorage.setItem(localKey(surface), JSON.stringify(blocks)); } catch {}
}

// Per user and project on the server, so the panel follows you across
// devices. Without a project (instance-wide scenes) it stays in this browser.
export function useBlockLayout(kind: PanelKind, projectId: string, appComponents: string[]) {
  const surface = SURFACES[kind];
  const [saved, setSaved] = useState<Block[] | null>(null);
  const [error, setError] = useState("");
  const revision = useRef<string>("");
  useEffect(() => {
    setSaved(null); setError(""); revision.current = "";
    if (!projectId) { setSaved(readLocal(surface)); return; }
    const controller = new AbortController();
    fetch(surfaceURL(projectId, surface), { signal: controller.signal, credentials: "same-origin" })
      .then(async response => {
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        revision.current = response.headers.get("ETag") || "";
        setSaved(parseBlocks((await response.json()).value));
      })
      .catch(caught => { if (!controller.signal.aborted) { setSaved(readLocal(surface)); setError((caught as Error).message); } });
    return () => controller.abort();
  }, [projectId, surface]);

  const save = useCallback(async (blocks: Block[]) => {
    setSaved(blocks); setError("");
    writeLocal(surface, blocks);
    if (!projectId) return;
    const put = (match: string) => fetch(surfaceURL(projectId, surface), {
      method: "PATCH", credentials: "same-origin",
      headers: { "Content-Type": "application/json", ...(match ? { "If-Match": match } : {}) },
      body: JSON.stringify({ value: blocks }),
    });
    try {
      let response = await put(revision.current);
      // Another tab changed the user's layout document; this surface is ours,
      // so retry once against the new revision.
      if (response.status === 409) response = await put(response.headers.get("ETag") || "");
      if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
      revision.current = response.headers.get("ETag") || "";
    } catch (caught) { setError(`Layout not saved: ${(caught as Error).message}`); }
  }, [projectId, surface]);

  return { blocks: saved ?? defaultBlocks(kind, appComponents), customised: !!saved, save, error };
}
