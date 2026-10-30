import { useEffect, useMemo, useRef } from "react";
import type { Camera } from "../camera";
import type { Agent, Destination, Event, Focus, WorldNode } from "../world-model";
import { fitText, plate, type Point } from "./core";
import { lookFor } from "./characters";
import { buildLayout, type PlaceInput, type RegionInput } from "./layout";
import { agentTrack } from "./motion";
import { drawFrame, type Hit, type Resident } from "./render";
import type { PixelTheme } from "./theme";

export const MAX_PLACES = 12;
export const MAX_RESIDENTS = 40;

type Props = {
  theme: PixelTheme;
  stageRef: React.MutableRefObject<HTMLCanvasElement | SVGSVGElement | null>;
  agents: Agent[]; nodes: WorldNode[]; destinations: Destination[]; events: Event[];
  teams: Map<string, string>; camera: Camera; portrait: boolean; now: number; live: boolean;
  selectedAgent: string; selectedDestination: string;
  onSelect: (id: string) => void; onSelectDestination: (id: string) => void; onFocus?: (focus: Focus) => void;
  onWorldSize?: (size: { width: number; height: number }) => void;
};

// Places shown in a village: apps and integrations, capped by recent use, then
// ordered by ID so the map stays put as call counts change.
export function placesFor(destinations: Destination[], selected: string): PlaceInput[] {
  const ranked = destinations
    .filter(item => item.kind === "app" || item.kind === "integration")
    .sort((a, b) => (b.id === selected ? 1 : 0) - (a.id === selected ? 1 : 0) || b.call_count - a.call_count || a.id.localeCompare(b.id))
    .slice(0, MAX_PLACES);
  return ranked.sort((a, b) => a.id.localeCompare(b.id)).map(item => ({ id: item.id, name: item.name, kind: item.kind, status: item.status, calls: item.call_count }));
}

export function regionsFor(agents: Agent[], nodes: WorldNode[], destinations: Destination[], selected: string): RegionInput<Focus>[] {
  const projects = nodes.filter(node => node.kind === "project");
  if (projects.length) {
    return projects.map(node => ({
      id: node.id, label: node.name, focus: node.focus,
      places: placesFor(destinations.filter(item => item.project_id === node.id), selected).slice(0, 6),
      residents: node.agents.map(agent => agent.id).slice(0, MAX_RESIDENTS),
    }));
  }
  // Teams and agent nodes all live in one village; teams share an outfit.
  const everyone = nodes.length ? [...new Map(nodes.flatMap(node => node.agents).map(agent => [agent.id, agent])).values()] : agents;
  return [{ id: everyone[0]?.project_id || "village", label: "", places: placesFor(destinations, selected), residents: everyone.map(agent => agent.id).slice(0, MAX_RESIDENTS) }];
}

export default function PixelStage(props: Props) {
  const { theme, agents, nodes, destinations, events, portrait } = props;
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const hover = useRef("");
  const hits = useRef<Hit[]>([]);
  const transform = useRef({ scale: 1, ox: 0, oy: 0, dpr: 1 });
  const regions = useMemo(() => regionsFor(agents, nodes, destinations, props.selectedDestination), [agents, nodes, destinations, props.selectedDestination]);
  const layoutKey = JSON.stringify([portrait, regions.map(region => [region.id, region.places.map(place => place.id), region.residents])]);
  const layout = useMemo(() => buildLayout(regions, portrait), [layoutKey]);
  const overview = regions.length > 1 || nodes.some(node => node.kind === "project");
  const agentById = useMemo(() => new Map([...agents, ...nodes.flatMap(node => node.agents)].map(agent => [agent.id, agent])), [agents, nodes]);
  const names = useMemo(() => new Map(regions.flatMap(region => region.places.map(place => [place.id, place.name]))), [regions]);
  const residents = useMemo<Resident[]>(() => layout.regions.flatMap((region, index) => region.residents.map(id => {
    const agent = agentById.get(id);
    const track = agentTrack(id, events, layout, index);
    return { id, name: agent?.name || id, status: agent?.status || "", region: index, look: lookFor(id, props.teams.get(id), theme.jumpsuit), at: track.at };
  })), [layout, events, agentById, props.teams, theme.jumpsuit]);
  const world = useMemo(() => makeWorldCanvas(layout.width, layout.height), [layout]);

  useEffect(() => { props.onWorldSize?.({ width: layout.width, height: layout.height }); }, [layout.width, layout.height]);

  const draw = () => {
    const canvas = canvasRef.current;
    if (!canvas || !world) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const reducedMotion = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
    hits.current = drawFrame(world.ctx, {
      layout, theme, residents, events, now: props.now, hover: hover.current,
      selectedAgent: props.selectedAgent, selectedPlace: props.selectedDestination, reducedMotion,
    });
    // Whole-number scaling keeps every world pixel the same size on screen.
    const dpr = window.devicePixelRatio || 1;
    const fit = Math.min(canvas.width / layout.width, canvas.height / layout.height);
    const raw = fit * props.camera.zoom;
    const scale = raw >= 1 ? Math.floor(raw + 1e-6) : raw;
    const ox = Math.round(canvas.width / 2 - props.camera.x * layout.width * scale);
    const oy = Math.round(canvas.height / 2 - props.camera.y * layout.height * scale);
    transform.current = { scale, ox, oy, dpr };
    ctx.imageSmoothingEnabled = false;
    ctx.fillStyle = theme.backdrop;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(world.canvas, ox, oy, layout.width * scale, layout.height * scale);
    drawLabels(ctx, hits.current, { scale, ox, oy, dpr }, overview);
  };

  const drawLabels = (ctx: CanvasRenderingContext2D, list: Hit[], view: { scale: number; ox: number; oy: number; dpr: number }, isOverview: boolean) => {
    const ls = Math.max(2, Math.round(2 * view.dpr));
    const screen = (point: Point) => ({ x: view.ox + point.x * view.scale, y: view.oy + point.y * view.scale });
    const roomy = view.scale / view.dpr >= 1.5;
    for (const hit of list) {
      const focused = hit.id === hover.current || hit.id === props.selectedAgent || hit.id === props.selectedDestination;
      let value = "";
      if (hit.kind === "region" && isOverview && hit.label) {
        const count = layout.regions.find(region => region.id === hit.id)?.residents.length || 0;
        value = `${hit.label} · ${count} ${count === 1 ? "agent" : "agents"}`;
      } else if (hit.kind === "place" && (roomy || focused)) value = names.get(hit.id) || hit.label;
      else if (hit.kind === "agent" && focused) value = hit.label;
      if (!value) continue;
      const anchor = screen(hit.anchor);
      const top = anchor.y - (hit.kind === "region" ? -4 * ls : 11 * ls);
      plate(ctx, fitText(value, 60 * ls * 2, ls), Math.round(anchor.x), Math.round(top), ls, focused ? { ...theme.label, rim: "#ffe27a" } : theme.label);
    }
  };

  useEffect(draw);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    props.stageRef.current = canvas;
    const resize = () => {
      const box = canvas.getBoundingClientRect(), dpr = window.devicePixelRatio || 1;
      const width = Math.max(1, Math.round(box.width * dpr)), height = Math.max(1, Math.round(box.height * dpr));
      if (canvas.width !== width || canvas.height !== height) { canvas.width = width; canvas.height = height; draw(); }
    };
    resize();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(resize);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, []);

  const hitAt = (event: React.PointerEvent | React.MouseEvent) => {
    const canvas = canvasRef.current;
    if (!canvas) return undefined;
    const box = canvas.getBoundingClientRect(), view = transform.current;
    const x = ((event.clientX - box.left) * view.dpr - view.ox) / view.scale;
    const y = ((event.clientY - box.top) * view.dpr - view.oy) / view.scale;
    const inside = (hit: Hit) => x >= hit.x && x <= hit.x + hit.w && y >= hit.y && y <= hit.y + hit.h;
    return hits.current.filter(hit => hit.kind === "agent").reverse().find(inside)
      ?? hits.current.find(hit => hit.kind === "place" && inside(hit))
      ?? (overview ? hits.current.find(hit => hit.kind === "region" && inside(hit)) : undefined);
  };
  const pointerMove = (event: React.PointerEvent<HTMLCanvasElement>) => {
    const hit = hitAt(event);
    const id = hit && hit.kind !== "region" ? hit.id : "";
    event.currentTarget.style.cursor = hit ? "pointer" : "";
    if (id !== hover.current) { hover.current = id; draw(); }
  };
  const click = (event: React.MouseEvent<HTMLCanvasElement>) => {
    const hit = hitAt(event);
    if (!hit) return;
    if (hit.kind === "agent") props.onSelect(hit.id);
    else if (hit.kind === "place") props.onSelectDestination(hit.id);
    else { const region = layout.regions.find(item => item.id === hit.id); if (region?.focus) props.onFocus?.(region.focus as Focus); }
  };

  return <>
    <canvas ref={canvasRef} className="aw-pixel" data-renderer="pixel" data-theme={theme.id} data-view={overview ? "project-villages" : "village"} data-clock={props.now}
      onPointerMove={pointerMove} onPointerLeave={() => { if (hover.current) { hover.current = ""; draw(); } }} onClick={click}
      role="img" aria-label={overview ? "Projects shown as villages" : "Agents visiting apps and integrations"} />
    <div className="aw-sr">
      {overview && layout.regions.map(region => region.focus ? <button key={region.id} type="button" onClick={() => props.onFocus?.(region.focus as Focus)}>{`Zoom into project ${region.label}`}</button> : null)}
      {layout.places.map(place => <button key={place.id} type="button" aria-pressed={props.selectedDestination === place.id} onClick={() => props.onSelectDestination(place.id)}>{`Inspect destination ${place.name}`}</button>)}
      {residents.map(resident => <button key={resident.id} type="button" data-agent-id={resident.id} aria-pressed={props.selectedAgent === resident.id} onClick={() => props.onSelect(resident.id)}>{`Inspect agent ${resident.name}`}</button>)}
    </div>
  </>;
}

// Native-resolution world canvas; absent where there is no 2D canvas (SSR, tests).
function makeWorldCanvas(width: number, height: number) {
  if (typeof document === "undefined") return null;
  const canvas = document.createElement("canvas");
  const ctx = canvas.getContext?.("2d");
  if (!ctx) return null;
  canvas.width = width;
  canvas.height = height;
  return { canvas, ctx };
}
