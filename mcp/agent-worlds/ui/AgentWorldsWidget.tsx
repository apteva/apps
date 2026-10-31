import { useEffect, useMemo, useRef, useState } from "react";
import { FIT_CAMERA, frameCamera, type Camera } from "./camera";
import { withAppDisplayNames, type AppMetadata } from "./app-display-names";
import { usePalette } from "./palette";
import { CANVAS_STYLES } from "./panel-styles";
import SceneViewport from "./scene-viewport";
import { MAX_RESIDENTS } from "./pixel/PixelStage";
import { useAnimationClock, usePolledJSON } from "./scene-data";
import { themeById } from "./themes";
import WorldStage from "./world-stage";
import { buildWorld, PAGE_SIZE, projectWorld, resolveFocus, type Scene } from "./world-model";

type Props = { installId?: number; projectId?: string; appName?: string; widgetSize?: "half" | "full"; widgetSettings?: Record<string, unknown> };

// The page's canvas and camera rules, sized to the widget instead of the window.
const WIDGET_STYLES = CANVAS_STYLES + `.aw-widget button {cursor:pointer;font:inherit}.aw-widget button:disabled {opacity:.4;cursor:default}.aw-widget .aw-canvas {height:100%;min-height:0}.aw-widget .aw-camera-controls button {height:28px;min-width:30px}.aw-widget .aw-camera-hint {bottom:8px;left:8px}.aw-widget .aw-camera-controls {right:8px;bottom:8px}`;
export const WIDGET_HEIGHTS = { compact: 240, standard: 340, tall: 520 } as const;

// Settings come from the dashboard as saved; anything unknown falls back to
// the village of the main source at the standard height.
export function worldWidgetPreferences(settings: Record<string, unknown> = {}) {
  const view = themeById(typeof settings.view === "string" ? settings.view : "village").id;
  const source = typeof settings.source === "string" && settings.source.trim() ? settings.source.trim() : "main";
  const height = typeof settings.height === "string" && settings.height in WIDGET_HEIGHTS ? settings.height as keyof typeof WIDGET_HEIGHTS : "standard";
  return { view, source, height };
}

export function worldPageLink(props: Props, preferences: ReturnType<typeof worldWidgetPreferences>) {
  const query = new URLSearchParams({ view: preferences.view });
  if (preferences.source !== "main") query.set("source", preferences.source);
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  return `/apps/${encodeURIComponent(props.appName || "agent-worlds")}/page?${query.toString()}`;
}

// A live window onto one world view with the page's pan and zoom. Clicking an
// agent or building opens the full page on the same view; nothing animates or
// polls while off screen.
export default function AgentWorldsWidget(props: Props) {
  const installId = props.installId || 0, projectId = props.projectId || "";
  const preferences = useMemo(() => worldWidgetPreferences(props.widgetSettings), [props.widgetSettings]);
  const theme = themeById(preferences.view);
  const isMap = theme.layout === "map";
  const palette = usePalette();
  const host = useRef<HTMLDivElement | null>(null), viewport = useRef<HTMLDivElement | null>(null);
  const [mapSize, setMapSize] = useState({ width: 1280, height: 760 });
  // The village opens framed on its islands rather than the sea around them.
  const [fit, setFit] = useState<Camera>(FIT_CAMERA);
  const stageRef = useRef<HTMLCanvasElement | SVGSVGElement | null>(null);
  const [visible, setVisible] = useState(true);
  useEffect(() => {
    if (!host.current || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(entries => setVisible(entries.some(entry => entry.isIntersecting)));
    observer.observe(host.current);
    return () => observer.disconnect();
  }, []);

  const sceneData = usePolledJSON<Scene>(`/scene?source=${encodeURIComponent(preferences.source)}`, installId, projectId, 2500, visible);
  const appData = usePolledJSON<AppMetadata[]>("/api/apps", 0, projectId, 60000, visible);
  const [now] = useAnimationClock(visible && isMap);
  const scene = useMemo(() => withAppDisplayNames(sceneData.data, appData.data || []), [sceneData.data, appData.data]);
  const world = useMemo(() => scene ? buildWorld(scene, scene.events) : null, [scene]);
  const projection = scene && world ? projectWorld(scene, world, resolveFocus(scene, world, {})) : null;
  const agents = projection?.agents || [];
  const stageAgents = agents.slice(0, isMap ? MAX_RESIDENTS : PAGE_SIZE);
  const ids = new Set(agents.map(agent => agent.id)), stageIds = new Set(stageAgents.map(agent => agent.id));
  const events = (scene?.events || []).filter(event => ids.has(event.agent_id));
  const teams = useMemo(() => new Map((world?.teams || []).flatMap(team => team.agentIds.map(id => [id, team.id] as const))), [world]);
  const running = agents.filter(agent => agent.status === "running" || agent.status === "active").length;
  const link = worldPageLink(props, preferences);
  const ignore = () => {};
  const open = () => window.location.assign(link);
  const frameLand = (box: { x: number; y: number; w: number; h: number }) =>
    setFit(previous => { const next = frameCamera(box); return Math.abs(next.zoom - previous.zoom) < 1e-3 && Math.abs(next.x - previous.x) < 1e-3 && Math.abs(next.y - previous.y) < 1e-3 ? previous : next; });

  return <div className="aw-widget flex h-full flex-col overflow-hidden rounded-lg border border-border bg-bg-card" data-world-view={theme.id}>
    <style>{WIDGET_STYLES}</style>
    <div className="flex items-center gap-2 border-b border-border px-3 py-2">
      <span className="text-xs font-semibold text-text">Agent Worlds</span>
      <span className="truncate text-xs text-text-muted">{theme.name}</span>
      <span className="ml-auto shrink-0 text-xs text-text-dim">{scene ? `${agents.length} agents · ${running} running` : sceneData.error ? "" : "Connecting…"}</span>
      <a href={link} className="shrink-0 text-xs text-text-muted hover:text-text">Open →</a>
    </div>
    <div ref={host} className="relative overflow-hidden bg-bg" style={{ height: WIDGET_HEIGHTS[preferences.height] }}>
      {sceneData.error && !scene ? <div className="flex h-full items-center justify-center px-4 text-center text-xs text-text-muted" role="alert">{sceneData.error}</div>
        : !scene ? <div className="flex h-full items-center justify-center text-xs text-text-dim">Connecting to the scene…</div>
        : <SceneViewport hostRef={viewport} stageRef={stageRef} width={isMap ? mapSize.width : 1280} height={isMap ? mapSize.height : 560} sceneKey={JSON.stringify([installId, projectId, preferences.source, theme.id])} status="" fitCamera={theme.iso ? fit : FIT_CAMERA} wheel="modifier">
          {camera => <WorldStage theme={theme} scene={scene} camera={camera} palette={palette} stageRef={stageRef} agents={stageAgents} nodes={projection?.nodes || []} destinations={projection?.destinations || []} events={events} stageEvents={events.filter(event => stageIds.has(event.agent_id))} teams={teams} portrait={false} now={now} live page={0} selectedAgent="" selectedDestination="" onSelect={open} onSelectDestination={open} onFocus={ignore} onWorldSize={setMapSize} onLandBounds={frameLand} />}
        </SceneViewport>}
    </div>
  </div>;
}
