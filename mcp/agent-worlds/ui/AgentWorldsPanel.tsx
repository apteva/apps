import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { PANEL_STYLES } from "./panel-styles";

type Source = { id: string; label: string; kind: string; status: string; agents: number };
type Agent = { id: string; name: string; status: string; model?: string; main_thread?: string };
type AppNode = { id: string; name: string; status: string };
type Event = { id: string; agent_id: string; thread_id?: string; type: string; kind: string; label: string; target?: string; time: string };
type Scene = { source: Source; agents: Agent[]; apps: AppNode[]; events: Event[]; at: string };
type Preset = "graph" | "operations" | "village" | "conversation";
type Props = { installId: number; projectId: string };

const API = "/api/apps/agent-worlds/api";
const presets: { id: Preset; name: string; detail: string }[] = [
  { id: "graph", name: "System graph", detail: "Agents, threads and tools" },
  { id: "operations", name: "Operations", detail: "Live status board" },
  { id: "village", name: "Pixel village", detail: "A playful agent world" },
  { id: "conversation", name: "Conversation", detail: "Messages and handoffs" },
];

function apiURL(path: string, installId: number, projectId: string) {
  const url = new URL(API + path, window.location.origin);
  if (installId) url.searchParams.set("install_id", String(installId));
  if (projectId) url.searchParams.set("project_id", projectId);
  return url.pathname + url.search;
}

async function getJSON<T>(path: string, installId: number, projectId: string): Promise<T> {
  const res = await fetch(apiURL(path, installId, projectId));
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${res.status}`);
  }
  return res.json();
}

const clock = (value: string) => new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const short = (value: string, length = 28) => value.length > length ? value.slice(0, length - 1) + "…" : value;
type Palette = { font: string; bg: string; card: string; subtle: string; input: string; text: string; muted: string; dim: string; border: string; strong: string; accent: string; success: string; warn: string; error: string; info: string };
const fallback: Palette = { font: "JetBrains Mono, ui-monospace, monospace", bg: "#0a0a0a", card: "#141414", subtle: "#101010", input: "#111111", text: "#e8e8e8", muted: "#aaaaaa", dim: "#888888", border: "#2a2a2a", strong: "#4a4a4a", accent: "#f97316", success: "#22c55e", warn: "#eab308", error: "#ef4444", info: "#3b82f6" };
function readPalette(): Palette {
  if (typeof window === "undefined") return fallback;
  const css = getComputedStyle(document.documentElement);
  const color = (token: string, fallbackColor: string) => css.getPropertyValue(`--${token}`).trim() || fallbackColor;
  return { font: color("font-base", fallback.font), bg: color("bg", fallback.bg), card: color("bg-card", fallback.card), subtle: color("bg-subtle", fallback.subtle), input: color("bg-input", fallback.input), text: color("text", fallback.text), muted: color("text-muted", fallback.muted), dim: color("text-dim", fallback.dim), border: color("border", fallback.border), strong: color("border-strong", fallback.strong), accent: color("accent", fallback.accent), success: color("success", fallback.success), warn: color("warn", fallback.warn), error: color("error", fallback.error), info: color("info", fallback.info) };
}
function usePalette() {
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
const statusColor = (status: string, palette: Palette) => status === "running" || status === "active" ? palette.success : status === "error" || status === "failed" ? palette.error : palette.warn;
const eventColor = (kind: string, palette: Palette) => ({ tool: palette.info, message: palette.accent, error: palette.error, thinking: palette.warn, spawn: palette.success, done: palette.success } as Record<string, string>)[kind] || palette.dim;

function latestByAgent(events: Event[]) {
  const latest = new Map<string, Event>();
  for (const event of events) latest.set(event.agent_id, event);
  return latest;
}

export default function AgentWorldsPanel({ installId, projectId }: Props) {
  const palette = usePalette();
  const [sources, setSources] = useState<Source[]>([]);
  const [sourceId, setSourceId] = useState("main");
  const [scene, setScene] = useState<Scene | null>(null);
  const [preset, setPreset] = useState<Preset>("graph");
  const [selectedAgent, setSelectedAgent] = useState("");
  const [live, setLive] = useState(true);
  const [replayIndex, setReplayIndex] = useState(0);
  const [portrait, setPortrait] = useState(false);
  const [error, setError] = useState("");
  const [recording, setRecording] = useState(false);
  const [animationNow, setAnimationNow] = useState(Date.now());
  const stageRef = useRef<SVGSVGElement | null>(null);
  const seenAtRef = useRef(new Map<string, number>());
  const loadSources = useCallback(async () => {
    try {
      const next = await getJSON<Source[]>("/sources", installId, projectId);
      setSources(next);
      if (!next.some(item => item.id === sourceId)) setSourceId("main");
    } catch (caught) { setError((caught as Error).message); }
  }, [installId, projectId, sourceId]);
  const loadScene = useCallback(async () => {
    try {
      const next = await getJSON<Scene>(`/scene?source=${encodeURIComponent(sourceId)}`, installId, projectId);
      const seenAt = seenAtRef.current;
      const valid = new Set(next.events.map(event => event.id));
      for (const key of seenAt.keys()) if (!valid.has(key)) seenAt.delete(key);
      for (const event of next.events) if (!seenAt.has(event.id)) seenAt.set(event.id, Date.now());
      setScene(next);
      setReplayIndex(next.events.length);
      setError("");
    } catch (caught) { setError((caught as Error).message); }
  }, [installId, projectId, sourceId]);

  useEffect(() => { void loadSources(); const timer = window.setInterval(loadSources, 15000); return () => window.clearInterval(timer); }, [loadSources]);
  useEffect(() => { setScene(null); setSelectedAgent(""); void loadScene(); }, [loadScene]);
  useEffect(() => { if (!live) return; const timer = window.setInterval(loadScene, 2500); return () => window.clearInterval(timer); }, [live, loadScene]);
  useEffect(() => { if (!live || preset !== "village") return; const timer = window.setInterval(() => setAnimationNow(Date.now()), 120); return () => window.clearInterval(timer); }, [live, preset]);

  const visibleEvents = useMemo(() => {
    const items = live ? scene?.events || [] : (scene?.events || []).slice(0, replayIndex);
    return selectedAgent ? items.filter(item => item.agent_id === selectedAgent) : items;
  }, [scene, selectedAgent, live, replayIndex]);
  const visibleAgents = useMemo(() => selectedAgent ? (scene?.agents || []).filter(agent => agent.id === selectedAgent) : scene?.agents || [], [scene, selectedAgent]);
  const lastEvent = visibleEvents[visibleEvents.length - 1];

  const serializeStage = useCallback(async () => {
    if (!stageRef.current) throw new Error("Scene is not ready");
    const clone = stageRef.current.cloneNode(true) as SVGSVGElement;
    clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
    const blob = new Blob([new XMLSerializer().serializeToString(clone)], { type: "image/svg+xml;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    try {
      const image = new Image();
      await new Promise<void>((resolve, reject) => { image.onload = () => resolve(); image.onerror = () => reject(new Error("Could not render scene")); image.src = url; });
      return image;
    } finally { URL.revokeObjectURL(url); }
  }, []);

  const savePNG = useCallback(async () => {
    try {
      const image = await serializeStage();
      const canvas = document.createElement("canvas");
      canvas.width = portrait ? 1080 : 1600;
      canvas.height = portrait ? 1920 : 700;
      const context = canvas.getContext("2d")!;
      context.fillStyle = palette.bg;
      context.fillRect(0, 0, canvas.width, canvas.height);
      context.drawImage(image, 0, 0, canvas.width, canvas.height);
      const a = document.createElement("a");
      a.download = `agent-worlds-${preset}.png`;
      a.href = canvas.toDataURL("image/png");
      a.click();
    } catch (caught) { setError((caught as Error).message); }
  }, [portrait, preset, serializeStage, palette.bg]);

  const recordClip = useCallback(async () => {
    if (!HTMLCanvasElement.prototype.captureStream || !window.MediaRecorder) { setError("Video recording is not supported in this browser"); return; }
    setRecording(true);
    try {
      const canvas = document.createElement("canvas");
      canvas.width = portrait ? 720 : 1280;
      canvas.height = portrait ? 1280 : 560;
      const context = canvas.getContext("2d")!;
      const mime = MediaRecorder.isTypeSupported("video/webm;codecs=vp9") ? "video/webm;codecs=vp9" : "video/webm";
      const chunks: BlobPart[] = [];
      const recorder = new MediaRecorder(canvas.captureStream(20), { mimeType: mime });
      recorder.ondataavailable = event => { if (event.data.size) chunks.push(event.data); };
      const finished = new Promise<void>(resolve => { recorder.onstop = () => resolve(); });
      let drawing = false;
      const draw = async () => {
        if (drawing) return;
        drawing = true;
        try {
          const image = await serializeStage();
          context.fillStyle = palette.bg;
          context.fillRect(0, 0, canvas.width, canvas.height);
          context.drawImage(image, 0, 0, canvas.width, canvas.height);
        } finally { drawing = false; }
      };
      await draw();
      recorder.start(1000);
      const timer = window.setInterval(() => { void draw(); }, 250);
      await new Promise(resolve => window.setTimeout(resolve, 12000));
      window.clearInterval(timer);
      recorder.stop();
      await finished;
      const url = URL.createObjectURL(new Blob(chunks, { type: mime }));
      const a = document.createElement("a");
      a.href = url;
      a.download = `agent-worlds-${preset}.webm`;
      a.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 60000);
    } catch (caught) { setError((caught as Error).message); }
    finally { setRecording(false); }
  }, [portrait, preset, serializeStage, palette.bg]);

  const activePreset = presets.find(item => item.id === preset)!;
  const threadCount = new Set(visibleEvents.filter(event => event.thread_id).map(event => `${event.agent_id}:${event.thread_id}`)).size;
  const runningCount = visibleAgents.filter(agent => agent.status === "running" || agent.status === "active").length;
  const resources = [...new Set([...(scene?.apps || []).map(app => app.name), ...visibleEvents.filter(event => event.target).map(event => event.target!)])].slice(0, 6);
  return <div className="aw">
    <style>{PANEL_STYLES}</style>
    <header className="aw-header">
      <div className="aw-heading"><div className="aw-title-row"><span className="aw-mark" aria-hidden="true">◈</span><h1>Agent Worlds</h1><span className="aw-beta">LIVE</span></div><p>Live view of your agents across the main server and environments.</p></div>
      <div className="aw-header-actions"><button className="aw-button" onClick={savePNG} disabled={!scene}>Export PNG</button><button className="aw-button" onClick={recordClip} disabled={!scene || recording}>{recording ? "Recording…" : "Record clip"}</button></div>
    </header>
    <main className="aw-main">
      <div className="aw-frame">
        <div className="aw-controls" aria-label="Scene controls">
          <label className="aw-field aw-source"><span>Source</span><select value={sourceId} onChange={event => setSourceId(event.target.value)}>{sources.map(source => <option key={source.id} value={source.id}>{source.label} · {source.agents} agents</option>)}</select></label>
          <label className="aw-field aw-agent"><span>Focus</span><select value={selectedAgent} onChange={event => setSelectedAgent(event.target.value)}><option value="">All agents</option>{scene?.agents.map(agent => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label>
          <div className="aw-control-actions"><button className={`aw-button aw-live ${live ? "is-live" : ""}`} onClick={() => { setLive(!live); setReplayIndex(scene?.events.length || 0); }}><span className="aw-dot" />{live ? "Live" : "Paused"}</button><button className="aw-button" onClick={loadScene}>Refresh</button><label className="aw-portrait"><input type="checkbox" checked={portrait} onChange={event => setPortrait(event.target.checked)} /> Portrait</label></div>
        </div>
        {error && <div className="aw-error" role="alert">{error}</div>}
        <nav className="aw-presets" aria-label="Visualization preset">{presets.map(item => <button key={item.id} type="button" onClick={() => setPreset(item.id)} aria-current={preset === item.id ? "page" : undefined} className={preset === item.id ? "is-active" : ""}>{item.name}</button>)}</nav>
        <div className="aw-summary"><div><strong>{visibleAgents.length}</strong><span>Agents</span></div><div><strong>{runningCount}</strong><span>Running</span></div><div><strong>{threadCount}</strong><span>Threads</span></div><div><strong>{visibleEvents.length}</strong><span>Events</span></div><span className="aw-summary-time">{scene ? `Updated ${clock(scene.at)}` : "Connecting…"}</span></div>
        <div className={`aw-content ${portrait ? "is-portrait" : ""}`}>
          <section className="aw-scene" aria-label={activePreset.name}>
            <div className="aw-scene-head"><div><strong>{activePreset.name}</strong><span>{activePreset.detail}</span></div><div className="aw-scene-source"><span className="aw-stage-indicator" />{scene?.source.label || "Connecting"}</div></div>
            <div className="aw-canvas">
              {scene ? preset === "graph" ? <SystemGraph refNode={stageRef} source={scene.source} agents={visibleAgents} events={visibleEvents} portrait={portrait} onSelect={setSelectedAgent} palette={palette} /> : preset === "village" ? <PixelVillage refNode={stageRef} agents={visibleAgents} events={visibleEvents} portrait={portrait} now={animationNow} seenAt={seenAtRef.current} onSelect={setSelectedAgent} palette={palette} /> : preset === "operations" ? <OperationsStage refNode={stageRef} agents={visibleAgents} events={visibleEvents} portrait={portrait} palette={palette} /> : <ConversationStage refNode={stageRef} agents={visibleAgents} events={visibleEvents} portrait={portrait} palette={palette} /> : <div className="aw-loading">Connecting to the scene…</div>}
            </div>
            <div className="aw-scene-foot"><span>{preset === "graph" ? "Select an agent to focus" : activePreset.detail}</span>{preset === "graph" && <div className="aw-resources"><span>Resources</span>{resources.length ? resources.map(name => <span className="aw-resource" key={name}>{short(name, 18)}</span>) : <em>None observed yet</em>}</div>}</div>
          </section>
          <aside className="aw-sidebar">
            <section className="aw-side-agents"><div className="aw-side-head"><h3>Agents</h3><span>{scene?.agents.length || 0}</span></div><div className="aw-agent-list">{(scene?.agents || []).map(agent => <button key={agent.id} onClick={() => setSelectedAgent(selectedAgent === agent.id ? "" : agent.id)} className={`aw-agent-row ${selectedAgent === agent.id ? "is-selected" : ""}`}><span className="aw-agent-avatar">{agent.name.slice(0,1).toUpperCase()}</span><span className="aw-agent-name"><strong>{agent.name}</strong><small>{agent.model || agent.main_thread || agent.status}</small></span><span className="aw-agent-status" style={{ background: statusColor(agent.status, palette) }} title={agent.status} /></button>)}{!scene?.agents.length && <div className="aw-plain-empty">No agents in this source</div>}</div></section>
            <section className="aw-side-activity"><div className="aw-side-head"><h3>Activity</h3><span>{live ? "Live feed" : "Replay"}</span></div><div className="aw-activity-list">{visibleEvents.slice(-10).reverse().map(event => <div key={event.id} className="aw-event"><span className="aw-event-mark" style={{ background: eventColor(event.kind, palette) }} /><div><div className="aw-event-line"><b>{scene?.agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id}</b> {event.label}</div><div className="aw-event-meta">{clock(event.time)}{event.thread_id ? ` · ${short(event.thread_id, 18)}` : ""}</div></div></div>)}{!visibleEvents.length && <div className="aw-plain-empty">Activity appears as agents work.</div>}</div></section>
          </aside>
        </div>
        {!live && <div className="aw-replay"><div><strong>Replay</strong><span>{lastEvent ? clock(lastEvent.time) : "Start"}</span></div><input type="range" min={0} max={scene?.events.length || 0} value={replayIndex} onChange={event => setReplayIndex(Number(event.target.value))} /></div>}
      </div>
    </main>
  </div>;
}

function SvgText({ x, y, children, size = 14, fill = "currentColor", anchor = "start", weight = 400 }: { x: number; y: number; children: React.ReactNode; size?: number; fill?: string; anchor?: "start" | "middle" | "end"; weight?: number }) { return <text x={x} y={y} fill={fill} fontSize={size} fontWeight={weight} textAnchor={anchor}>{children}</text>; }

function SystemGraph({ refNode, source, agents, events, portrait, onSelect, palette }: { refNode: React.RefObject<SVGSVGElement | null>; source: Source; agents: Agent[]; events: Event[]; portrait: boolean; onSelect: (id: string) => void; palette: Palette }) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
  const latest = latestByAgent(events);
  const shown = agents.slice(0, 12), rows = Math.max(1, Math.ceil(shown.length / 2));
  const cardW = portrait ? 228 : 282;
  const cardH = portrait ? Math.min(82, 620 / rows - 12) : Math.min(88, 480 / rows - 10);
  const positions = shown.map((agent, i) => ({ agent, x: portrait ? (i % 2 ? 286 : 26) : (i % 2 ? 948 : 50), y: portrait ? 280 + Math.floor(i / 2) * (620 / rows) + (620 / rows - cardH) / 2 : 40 + Math.floor(i / 2) * (480 / rows) + (480 / rows - cardH) / 2 }));
  const hubX = width / 2, hubY = portrait ? 162 : 280, hubW = portrait ? 246 : 260, hubH = 104;
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Agent system graph" style={{ color: palette.text, background: palette.subtle, fontFamily: palette.font }}>
    <defs><pattern id="awGraphDots" width="24" height="24" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="1" fill={palette.border} /></pattern></defs>
    <rect width={width} height={height} fill={palette.subtle} /><rect width={width} height={height} fill="url(#awGraphDots)" />
    <circle cx={hubX} cy={hubY} r={portrait ? 112 : 145} fill="none" stroke={palette.border} strokeWidth="1" opacity=".65" /><circle cx={hubX} cy={hubY} r={portrait ? 160 : 205} fill="none" stroke={palette.border} strokeWidth="1" opacity=".3" />
    {positions.map(({agent,x,y},i) => { const left = i % 2 === 0; const sx = portrait ? hubX + (left ? -38 : 38) : hubX + (left ? -hubW/2 : hubW/2); const sy = portrait ? hubY + hubH/2 : hubY; const ex = left ? x + cardW : x; const ey = y + cardH/2; const midX = (sx+ex)/2; return <g key={`edge:${agent.id}`}><path d={`M ${sx} ${sy} C ${midX} ${sy}, ${midX} ${ey}, ${ex} ${ey}`} fill="none" stroke={latest.has(agent.id) ? palette.accent : palette.strong} strokeWidth={latest.has(agent.id) ? 2 : 1.2} strokeDasharray={latest.has(agent.id) ? undefined : "4 6"} opacity={latest.has(agent.id) ? ".9" : ".65"} /><circle cx={ex} cy={ey} r="3" fill={latest.has(agent.id) ? palette.accent : palette.strong} /></g>; })}
    <rect x={hubX-hubW/2} y={hubY-hubH/2} width={hubW} height={hubH} rx="9" fill={palette.card} stroke={palette.accent} strokeWidth="1.8" />
    <rect x={hubX-hubW/2} y={hubY-hubH/2} width="4" height={hubH} fill={palette.accent} />
    <SvgText x={hubX-hubW/2+23} y={hubY-19} size={10} fill={palette.accent} weight={700}>APTEVA · {source.kind.includes("runtime") ? "ENVIRONMENT" : "SERVER"}</SvgText>
    <SvgText x={hubX-hubW/2+23} y={hubY+12} size={20} weight={700}>{short(source.label, 18)}</SvgText>
    <SvgText x={hubX-hubW/2+23} y={hubY+35} size={11} fill={palette.dim}>{agents.length} connected agents</SvgText>
    {positions.map(({agent,x,y}) => <g key={agent.id} onClick={() => onSelect(agent.id)} style={{ cursor: "pointer" }}><rect x={x} y={y} width={cardW} height={cardH} rx="7" fill={palette.card} stroke={palette.border} /><rect x={x} y={y} width="3" height={cardH} fill={statusColor(agent.status,palette)} /><circle cx={x+29} cy={y+30} r="15" fill={palette.subtle} stroke={palette.strong} /><SvgText x={x+29} y={y+35} size={14} fill={palette.accent} anchor="middle" weight={700}>{agent.name.slice(0,1).toUpperCase()}</SvgText><SvgText x={x+55} y={y+30} size={13} weight={700}>{short(agent.name,portrait?17:24)}</SvgText><circle cx={x+cardW-17} cy={y+25} r="4.5" fill={statusColor(agent.status,palette)} /><SvgText x={x+16} y={y+cardH-16} size={11} fill={palette.dim}>{short(latest.get(agent.id)?.label || agent.status,portrait?29:37)}</SvgText></g>)}
    {!agents.length && <SvgText x={hubX} y={portrait?380:400} size={15} fill={palette.dim} anchor="middle">No agents in this source yet</SvgText>}
    {agents.length > shown.length && <SvgText x={width-28} y={height-22} size={11} fill={palette.dim} anchor="end">+{agents.length-shown.length} more · select an agent to focus</SvgText>}
  </svg>;
}

function OperationsStage({ refNode, agents, events, portrait, palette }: { refNode: React.RefObject<SVGSVGElement | null>; agents: Agent[]; events: Event[]; portrait: boolean; palette: Palette }) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
  const latest = latestByAgent(events), columns = portrait ? 1 : 2, cardWidth = portrait ? 480 : 592, cardHeight = portrait ? 104 : 96;
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Agent operations board" style={{ color: palette.text, background: palette.subtle, fontFamily: palette.font }}><rect width={width} height={height} fill={palette.subtle} /><SvgText x={30} y={48} size={22} weight={700}>Operations</SvgText><SvgText x={30} y={72} size={11} fill={palette.dim}>{agents.length} AGENTS · CURRENT STATUS</SvgText><line x1={30} y1={88} x2={width-30} y2={88} stroke={palette.border} />{agents.slice(0, portrait ? 7 : 8).map((agent, index) => { const x = 30 + (index % columns) * (cardWidth + 36), y = 110 + Math.floor(index / columns) * (cardHeight + 16), item = latest.get(agent.id); return <g key={agent.id}><rect x={x} y={y} width={cardWidth} height={cardHeight} rx="7" fill={palette.card} stroke={palette.border} /><rect x={x} y={y} width="4" height={cardHeight} fill={statusColor(agent.status, palette)} /><SvgText x={x + 20} y={y + 27} size={15} weight={700}>{short(agent.name, 25)}</SvgText><circle cx={x + cardWidth - 80} cy={y + 22} r="4" fill={statusColor(agent.status, palette)} /><SvgText x={x + cardWidth - 17} y={y + 26} size={10} fill={statusColor(agent.status, palette)} anchor="end">{agent.status.toUpperCase()}</SvgText><line x1={x + 20} y1={y + 40} x2={x + cardWidth - 20} y2={y + 40} stroke={palette.border} /><SvgText x={x + 20} y={y + 64} size={12} fill={palette.muted}>{short(item?.label || "No recent activity", 47)}</SvgText><SvgText x={x + 20} y={y + 86} size={10} fill={palette.dim}>{item ? `${clock(item.time)} · ${item.thread_id || "main"}` : agent.model || "Waiting for events"}</SvgText></g>; })}{!agents.length && <SvgText x={width / 2} y={height / 2} size={15} fill={palette.dim} anchor="middle">No agents in this source yet</SvgText>}</svg>;
}

function ConversationStage({ refNode, agents, events, portrait, palette }: { refNode: React.RefObject<SVGSVGElement | null>; agents: Agent[]; events: Event[]; portrait: boolean; palette: Palette }) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
  const messages = events.filter(event => ["message", "spawn", "done", "error"].includes(event.kind)).slice(portrait ? -9 : -6);
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Agent conversation stage" style={{ color: palette.text, background: palette.subtle, fontFamily: palette.font }}><rect width={width} height={height} fill={palette.subtle} /><SvgText x={30} y={48} size={22} weight={700}>Conversation</SvgText><SvgText x={30} y={72} size={11} fill={palette.dim}>AGENT HANDOFFS &amp; MILESTONES</SvgText><line x1={30} y1={88} x2={width-30} y2={88} stroke={palette.border} /><line x1={portrait ? 46 : 67} y1={110} x2={portrait ? 46 : 67} y2={height-35} stroke={palette.border} strokeWidth="2" />{messages.map((event, index) => { const bubbleW = portrait ? 430 : 1160, x = portrait ? 74 : 95, y = 112 + index * (portrait ? 88 : 75), name = agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id; return <g key={event.id}><circle cx={portrait ? 46 : 67} cy={y+31} r="6" fill={eventColor(event.kind, palette)} /><rect x={x} y={y} width={bubbleW} height={portrait ? 70 : 64} rx="6" fill={palette.card} stroke={palette.border} /><rect x={x} y={y} width="3" height={portrait ? 70 : 64} fill={eventColor(event.kind, palette)} /><SvgText x={x + 16} y={y + 23} size={11} fill={palette.accent} weight={700}>{short(name, 30)}</SvgText><SvgText x={x + 16} y={y + 46} size={13}>{short(event.label, portrait ? 45 : 95)}</SvgText><SvgText x={x + bubbleW - 15} y={y + 23} size={10} fill={palette.dim} anchor="end">{clock(event.time)}</SvgText></g>; })}{!messages.length && <SvgText x={width / 2} y={height / 2} size={15} fill={palette.dim} anchor="middle">Agent messages and handoffs will appear here</SvgText>}</svg>;
}

function PixelVillage({ refNode, agents, events, portrait, now, seenAt, onSelect, palette }: { refNode: React.RefObject<SVGSVGElement | null>; agents: Agent[]; events: Event[]; portrait: boolean; now: number; seenAt: Map<string, number>; onSelect: (id: string) => void; palette: Palette }) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
  const groundY = portrait ? 625 : 385;
  const houses = portrait ? [{ x: 35, y: 195, label: "LIBRARY", color: "#d6a469" }, { x: 295, y: 195, label: "WORKSHOP", color: "#bd775c" }, { x: 165, y: 430, label: "MAILBOX", color: "#8c80bf" }] : [{ x: 85, y: 145, label: "LIBRARY", color: "#d6a469" }, { x: 530, y: 115, label: "WORKSHOP", color: "#bd775c" }, { x: 972, y: 150, label: "MAILBOX", color: "#8c80bf" }];
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Pixel village of live agents" shapeRendering="crispEdges" style={{ background: "#92c9d1", imageRendering: "pixelated", fontFamily: palette.font }}>
    <rect width={width} height={height} fill="#94cbd4" />
    <path d={`M 0 ${groundY - 110} L ${width * .2} ${groundY - 155} L ${width * .43} ${groundY - 108} L ${width * .68} ${groundY - 170} L ${width} ${groundY - 122} L ${width} ${groundY} L 0 ${groundY} Z`} fill="#78b6a8" />
    <path d={`M 0 ${groundY - 65} L ${width * .25} ${groundY - 95} L ${width * .5} ${groundY - 58} L ${width * .78} ${groundY - 105} L ${width} ${groundY - 63} L ${width} ${groundY} L 0 ${groundY} Z`} fill="#8ec39b" />
    <g fill="#e6f2e4"><rect x={width * .12} y="89" width="74" height="17" /><rect x={width * .12 + 13} y="77" width="48" height="13" /><rect x={width * .64} y="135" width="88" height="17" /><rect x={width * .64 + 17} y="122" width="52" height="13" /></g>
    <rect y={groundY} width={width} height={height - groundY} fill="#6ea66c" /><rect y={groundY + 30} width={width} height="16" fill="#63995f" />
    <circle cx={width - 85} cy="78" r="33" fill="#f8e8a5" /><rect x="0" y={groundY - 24} width={width} height="26" fill="#83b475" />
    {Array.from({ length: portrait ? 20 : 36 }, (_, i) => { const x = (i * 127 + 27) % width, y = groundY + 56 + ((i * 47) % Math.max(80, height - groundY - 80)); return <g key={i}><rect x={x} y={y} width="7" height="3" fill="#e8dda2" /><rect x={x + 3} y={y - 4} width="2" height="4" fill="#477c4c" /></g>; })}
    {houses.map((house, i) => <g key={house.label}><rect x={house.x + 15} y={house.y + 45} width="195" height="135" fill={house.color} stroke="#645b53" strokeWidth="5" /><path d={`M ${house.x} ${house.y + 50} L ${house.x + 112} ${house.y - 18} L ${house.x + 225} ${house.y + 50} Z`} fill={i === 1 ? "#704e5b" : i === 2 ? "#685985" : "#805d57"} stroke="#594d53" strokeWidth="5" /><rect x={house.x + 88} y={house.y + 101} width="45" height="79" fill="#634e49" /><rect x={house.x + 35} y={house.y + 85} width="35" height="35" fill="#d7e9d5" stroke="#5d6661" strokeWidth="5" /><rect x={house.x + 160} y={house.y + 85} width="35" height="35" fill="#d7e9d5" stroke="#5d6661" strokeWidth="5" /><rect x={house.x + 39} y={house.y + 165} width="150" height="22" fill="#e8d3a3" stroke="#61594c" strokeWidth="3" /><SvgText x={house.x + 114} y={house.y + 181} size={14} fill="#3c4b3e" anchor="middle" weight={700}>{house.label}</SvgText></g>)}
    <path d={portrait ? `M 269 625 L 269 900 M 80 698 L 465 698` : `M 0 465 L 1280 465 M 640 385 L 640 560`} stroke="#c3b083" strokeWidth="48" fill="none" />
    {(portrait ? [{ x: 34, y: 750 }, { x: 493, y: 820 }] : [{ x: 36, y: 427 }, { x: 1245, y: 414 }]).map((tree, index) => <g key={index}><rect x={tree.x - 7} y={tree.y - 8} width="14" height="40" fill="#705c49" /><rect x={tree.x - 29} y={tree.y - 50} width="58" height="45" fill="#3e805b" /><rect x={tree.x - 20} y={tree.y - 69} width="40" height="20" fill="#4a9364" /><rect x={tree.x - 35} y={tree.y - 36} width="9" height="20" fill="#34704f" /><rect x={tree.x + 26} y={tree.y - 36} width="9" height="20" fill="#34704f" /><rect x={tree.x - 17} y={tree.y - 42} width="13" height="6" fill="#6db67b" /></g>)}
    {agents.slice(0, portrait ? 8 : 12).map((agent, index) => {
      const history = events.filter(item => item.agent_id === agent.id);
      const event = history[history.length - 1], previous = history[history.length - 2];
      const kind = event?.kind || "idle";
      const place = (eventKind?: string) => eventKind === "tool" || eventKind === "result" ? houses[1] : eventKind === "message" || eventKind === "spawn" ? houses[2] : houses[0];
      const base = place(kind), from = place(previous?.kind);
      const row = Math.floor(index / 4), col = index % 4;
      const targetX = Math.min(width - 30, Math.max(28, base.x + (portrait ? 50 + (index % 2) * 40 : 32 + col * 47)));
      const targetY = Math.min(height - 80, portrait ? groundY + 105 + row * 70 : base.y + 270 + row * 88);
      const startX = Math.min(width - 30, Math.max(28, from.x + (portrait ? 50 + (index % 2) * 40 : 32 + col * 47)));
      const startY = Math.min(height - 80, portrait ? groundY + 105 + row * 70 : from.y + 270 + row * 88);
      const progress = event ? Math.min(1, Math.max(0, (now - (seenAt.get(event.id) || now)) / 1800)) : 1;
      const x = Math.round(startX + (targetX - startX) * progress);
      const y = Math.round(startY + (targetY - startY) * progress);
      const step = progress < 1 && Math.floor(now / 160) % 2 === 0 ? 4 : 0;
      const shirt = ["#536bbc", "#c9685a", "#60a482", "#ab77b7", "#d6a65d"][index % 5];
      return <g key={agent.id} onClick={() => onSelect(agent.id)} style={{ cursor: "pointer" }}>
        <rect x={x - 12} y={y + 34} width="26" height="5" fill="#54765b" opacity=".6" />
        <rect x={x - 7} y={y - 8} width="14" height="14" fill="#efc69b" /><rect x={x - 8} y={y - 10} width="16" height="6" fill="#594e4e" />
        <rect x={x - 10} y={y + 5} width="20" height="19" fill={shirt} />
        <rect x={x - 9} y={y + 24} width="7" height={12 - step} fill="#3b4b66" /><rect x={x + 2} y={y + 24} width="7" height={8 + step} fill="#3b4b66" />
        <rect x={x + 10} y={y + 8} width="6" height="10" fill="#efc69b" /><circle cx={x + 13} cy={y - 16} r="5" fill={statusColor(agent.status, palette)} />
        {kind === "message" && <rect x={x + 14} y={y + 13} width="12" height="8" fill="#f3e8cc" stroke="#7f755a" />}
        <rect x={x - 40} y={y - 53} width="80" height="22" rx="4" fill="#f5efd9" stroke="#6f755e" strokeWidth="2" />
        <SvgText x={x} y={y - 38} size={11} fill="#415449" anchor="middle" weight={700}>{short(agent.name, 11)}</SvgText>
        <SvgText x={x} y={y + 54} size={10} fill="#2f5541" anchor="middle">{kind === "tool" ? "working" : kind === "message" ? "delivering" : kind === "thinking" ? "thinking" : agent.status}</SvgText>
      </g>;
    })}
    {!agents.length && <SvgText x={width / 2} y={groundY + 93} size={17} fill="#315d43" anchor="middle">The village is waiting for agents</SvgText>}
    <rect x="16" y="17" width={portrait ? 508 : 340} height="48" rx="7" fill="#f2ebd5" stroke="#7d8b70" strokeWidth="3" /><SvgText x={31} y={49} size={20} fill="#3d5c51" weight={700}>AGENT VILLAGE</SvgText>
    <rect x="16" y={height - 52} width={width - 32} height="38" rx="5" fill="#f2ebd5" stroke="#7d8b70" strokeWidth="3" /><SvgText x={31} y={height - 28} size={12} fill="#496552">{short(events[events.length - 1]?.label || "A quiet moment in the village", portrait ? 55 : 100)}</SvgText>
  </svg>;
}
