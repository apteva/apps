import { useEffect, useMemo, useRef } from "react";
import { pixelScale } from "../core";
import type { Camera } from "../../camera";
import type { Agent, Destination, Event, Focus, WorldNode } from "../../world-model";
import { lookFor } from "../characters";
import type { Hit } from "../render";
import { regionsFor } from "../PixelStage";
import { buildIsoLayout } from "./layout";
import { isoTrack, type IsoPose } from "./motion";
import { SEA, paintScene } from "./paint";
import { drawIsoFrame, type IsoResident } from "./render";
import { describeTool } from "./stations";

type Props = {
  stageRef: React.MutableRefObject<HTMLCanvasElement | SVGSVGElement | null>;
  agents: Agent[]; nodes: WorldNode[]; destinations: Destination[]; events: Event[];
  teams: Map<string, string>; camera: Camera; portrait: boolean; now: number; live: boolean;
  selectedAgent: string; selectedDestination: string;
  onSelect: (id: string) => void; onSelectDestination: (id: string) => void; onFocus?: (focus: Focus) => void;
  onWorldSize?: (size: { width: number; height: number }) => void;
  // The islands' bounds as fractions of the world, so a small view can frame land instead of sea.
  onLandBounds?: (box: { x: number; y: number; w: number; h: number }) => void;
};

const STATUS: Record<string, string> = { ok: "#06d6a0", busy: "#ffd166", error: "#ef476f", off: "#adb5bd" };
const statusTone = (status: string) => {
  const value = status.toLowerCase();
  if (/error|fail|crash|unhealthy/.test(value)) return STATUS.error;
  if (/stop|disabled|inactive|off/.test(value)) return STATUS.off;
  if (/start|pending|install|busy/.test(value)) return STATUS.busy;
  return STATUS.ok;
};
const INK = "#342e42", MUTED = "#6c7086";
const ago = (time: number, now: number) => {
  const s = Math.max(0, Math.round((now - time) / 1000));
  return s < 60 ? `${s}s ago` : s < 3600 ? `${Math.round(s / 60)}m ago` : s < 86400 ? `${Math.round(s / 3600)}h ago` : `${Math.round(s / 86400)}d ago`;
};

// The isometric village: one island per project, a building per app or
// integration, agents walking in through the door to work.
export default function IsoStage(props: Props) {
  const { agents, nodes, destinations, events, portrait } = props;
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const hover = useRef("");
  const hits = useRef<Hit[]>([]);
  const transform = useRef({ scale: 1, ox: 0, oy: 0, dpr: 1 });
  const regions = useMemo(() => regionsFor(agents, nodes, destinations, props.selectedDestination), [agents, nodes, destinations, props.selectedDestination]);
  const layoutKey = JSON.stringify([portrait, regions.map(region => [region.id, region.label, region.places.map(place => [place.id, place.name, place.kind]), region.residents])]);
  const layout = useMemo(() => buildIsoLayout(regions, portrait), [layoutKey]);
  const scene = useMemo(() => (typeof document === "undefined" || !document.createElement("canvas").getContext?.("2d") ? null : paintScene(layout)), [layout]);
  const overview = regions.length > 1 || nodes.some(node => node.kind === "project");
  const agentById = useMemo(() => new Map([...agents, ...nodes.flatMap(node => node.agents)].map(agent => [agent.id, agent])), [agents, nodes]);
  const destinationById = useMemo(() => new Map(destinations.map(item => [item.id, item])), [destinations]);
  const residents = useMemo<IsoResident[]>(() => layout.regions.flatMap((region, index) => region.residents.map(id => {
    const agent = agentById.get(id);
    const track = isoTrack(id, events, layout, index);
    return { id, name: agent?.name || id, status: agent?.status || "", region: index, look: lookFor(id, props.teams.get(id)), at: track.at, couriers: track.couriers };
  })), [layout, events, agentById, props.teams]);
  const world = useMemo(() => (scene ? makeWorldCanvas(scene.width, scene.height) : null), [scene]);
  const size = scene ? { width: scene.width, height: scene.height } : { width: 1280, height: 760 };

  useEffect(() => { props.onWorldSize?.(size); }, [size.width, size.height]);
  useEffect(() => {
    if (!scene?.islands.length || !props.onLandBounds) return;
    const left = Math.min(...scene.islands.map(i => i.x)), top = Math.min(...scene.islands.map(i => i.y));
    const right = Math.max(...scene.islands.map(i => i.x + i.w)), bottom = Math.max(...scene.islands.map(i => i.y + i.h));
    props.onLandBounds({ x: left / scene.width, y: top / scene.height, w: (right - left) / scene.width, h: (bottom - top) / scene.height });
  }, [scene]);

  const draw = () => {
    const canvas = canvasRef.current;
    if (!canvas || !world || !scene) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const reducedMotion = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
    const frame = drawIsoFrame(world.ctx, {
      layout, scene, residents, events, now: props.now, hover: hover.current,
      selectedAgent: props.selectedAgent, selectedPlace: props.selectedDestination, reducedMotion,
    });
    hits.current = frame.hits;
    const dpr = window.devicePixelRatio || 1;
    const fit = Math.min(canvas.width / scene.width, canvas.height / scene.height);
    const raw = fit * props.camera.zoom;
    const scale = pixelScale(raw, dpr);
    const ox = Math.round(canvas.width / 2 - props.camera.x * scene.width * scale);
    const oy = Math.round(canvas.height / 2 - props.camera.y * scene.height * scale);
    transform.current = { scale, ox, oy, dpr };
    ctx.imageSmoothingEnabled = false;
    ctx.fillStyle = SEA;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(world.canvas, ox, oy, scene.width * scale, scene.height * scale);
    drawLabels(ctx, frame.hits, frame.poses, { scale, ox, oy, dpr });
  };

  // Clean cards over each building and activity pills over working agents,
  // drawn at screen resolution so text stays sharp at any zoom.
  const drawLabels = (ctx: CanvasRenderingContext2D, list: Hit[], poses: Map<string, IsoPose>, view: { scale: number; ox: number; oy: number; dpr: number }) => {
    const u = view.dpr * Math.min(1.25, Math.max(0.8, view.scale / view.dpr / 2));
    const screen = (x: number, y: number) => ({ x: view.ox + x * view.scale, y: view.oy + y * view.scale });
    const roomy = view.scale / view.dpr >= 1.5;
    const font = (weight: number, size: number) => `${weight} ${size * u}px Inter, ui-sans-serif, system-ui, sans-serif`;
    ctx.textBaseline = "middle";
    for (const hit of list) {
      const focused = hit.id === hover.current || hit.id === props.selectedAgent || hit.id === props.selectedDestination;
      const at = screen(hit.anchor.x, hit.anchor.y);
      if (hit.kind === "region") {
        if (!overview || !hit.label) continue;
        const count = layout.regions.find(region => region.id === hit.id)?.residents.length || 0;
        const text = `${hit.label} · ${count} ${count === 1 ? "agent" : "agents"}`;
        ctx.font = font(700, 14);
        const w = ctx.measureText(text).width + 24 * u, h = 28 * u;
        card(ctx, at.x - w / 2, at.y - h, w, h, h / 2, u, false);
        ctx.fillStyle = INK; ctx.fillText(text, at.x - w / 2 + 12 * u, at.y - h / 2);
      } else if (hit.kind === "place" && (roomy || focused)) {
        const place = layout.places.find(item => item.id === hit.id)!, destination = destinationById.get(hit.id);
        const calls = destination?.call_count ?? place.calls;
        const last = destination?.last_call ? Date.parse(destination.last_call) : NaN;
        const meta = `${calls} ${calls === 1 ? "call" : "calls"}${Number.isFinite(last) ? ` · ${ago(last, props.now)}` : ""}`;
        const name = fit(ctx, place.name, font(700, 13), 170 * u);
        ctx.font = font(700, 13); const nw = ctx.measureText(name).width;
        ctx.font = font(500, 10.5); const mw = ctx.measureText(meta).width;
        const w = Math.max(nw + 16 * u, mw) + 20 * u, h = 36 * u, x = at.x - w / 2, y = at.y - h;
        card(ctx, x, y, w, h, 9 * u, u, focused);
        ctx.fillStyle = place.roof; roundRect(ctx, x, y, 4 * u, h, 2 * u); ctx.fill();
        ctx.fillStyle = statusTone(place.status); ctx.beginPath(); ctx.arc(x + 14 * u, y + 13 * u, 4 * u, 0, Math.PI * 2); ctx.fill();
        ctx.font = font(700, 13); ctx.fillStyle = INK; ctx.fillText(name, x + 24 * u, y + 13 * u);
        ctx.font = font(500, 10.5); ctx.fillStyle = MUTED; ctx.fillText(meta, x + 10 * u, y + 27 * u);
      } else if (hit.kind === "agent") {
        const pose = poses.get(hit.id);
        const working = pose?.inside && !pose.walking;
        if (!focused && !(working && roomy)) continue;
        const resident = residents.find(item => item.id === hit.id);
        const action = working ? lastAction(hit.id, layout.places.find(place => place.id === pose.inside)?.name) : "";
        const name = fit(ctx, hit.label, font(700, 10), 120 * u), text = fit(ctx, action, font(600, 10.5), 180 * u);
        ctx.font = font(700, 10); const nw = ctx.measureText(name).width;
        ctx.font = font(600, 10.5); const tw = text ? ctx.measureText(text).width + 5 * u : 0;
        const w = nw + tw + 30 * u, h = 20 * u, x = at.x - w / 2, y = at.y - h - 4 * u;
        card(ctx, x, y, w, h, h / 2, u, focused);
        ctx.fillStyle = "#ffffff"; ctx.beginPath(); ctx.moveTo(at.x - 4 * u, y + h - 1); ctx.lineTo(at.x, y + h + 5 * u); ctx.lineTo(at.x + 4 * u, y + h - 1); ctx.fill();
        ctx.fillStyle = resident?.look.shirt[0] || MUTED; ctx.beginPath(); ctx.arc(x + 11 * u, y + h / 2, 4.5 * u, 0, Math.PI * 2); ctx.fill();
        ctx.font = font(700, 10); ctx.fillStyle = INK; ctx.fillText(name, x + 20 * u, y + h / 2 + 0.5);
        if (text) { ctx.font = font(600, 10.5); ctx.fillStyle = MUTED; ctx.fillText(text, x + 25 * u + nw, y + h / 2 + 0.5); }
      }
    }
  };
  // What a working agent is doing, in words: "searching contacts".
  const lastAction = (agentId: string, appName = "") => {
    let best: Event | undefined;
    for (const event of events) if (event.agent_id === agentId && event.kind === "tool" && event.target && Date.parse(event.time) <= props.now) best = event;
    return best ? describeTool(best.target!, appName) : "";
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
      ?? hits.current.filter(hit => hit.kind === "place" && inside(hit)).sort((a, b) => (b.y + b.h) - (a.y + a.h))[0]
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
    <canvas ref={canvasRef} className="aw-pixel" data-renderer="pixel" data-theme="village" data-view={overview ? "project-villages" : "village"} data-clock={props.now}
      onPointerMove={pointerMove} onPointerLeave={() => { if (hover.current) { hover.current = ""; draw(); } }} onClick={click}
      role="img" aria-label={overview ? "Projects shown as villages" : "Agents visiting apps and integrations"} />
    <div className="aw-sr">
      {overview && layout.regions.map(region => region.focus ? <button key={region.id} type="button" onClick={() => props.onFocus?.(region.focus as Focus)}>{`Zoom into project ${region.label}`}</button> : null)}
      {layout.places.map(place => <button key={place.id} type="button" aria-pressed={props.selectedDestination === place.id} onClick={() => props.onSelectDestination(place.id)}>{`Inspect destination ${place.name}`}</button>)}
      {residents.map(resident => <button key={resident.id} type="button" data-agent-id={resident.id} aria-pressed={props.selectedAgent === resident.id} onClick={() => props.onSelect(resident.id)}>{`Inspect agent ${resident.name}`}</button>)}
    </div>
  </>;
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r); ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
}
function card(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number, u: number, focused: boolean) {
  ctx.save();
  ctx.shadowColor = "rgba(52,46,66,0.22)"; ctx.shadowBlur = 8 * u; ctx.shadowOffsetY = 2 * u;
  roundRect(ctx, x, y, w, h, r); ctx.fillStyle = "#ffffff"; ctx.fill();
  ctx.restore();
  if (focused) { roundRect(ctx, x, y, w, h, r); ctx.lineWidth = 2 * u; ctx.strokeStyle = "#ffd166"; ctx.stroke(); }
}
function fit(ctx: CanvasRenderingContext2D, value: string, font: string, max: number) {
  ctx.font = font;
  if (ctx.measureText(value).width <= max) return value;
  let text = value;
  while (text.length > 1 && ctx.measureText(`${text}…`).width > max) text = text.slice(0, -1);
  return `${text}…`;
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
