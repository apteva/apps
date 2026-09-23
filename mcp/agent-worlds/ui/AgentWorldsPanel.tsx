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
const statusColor = (status: string) => status === "running" || status === "active" ? "#43d9a3" : status === "error" || status === "failed" ? "#fb7185" : "#f5bb63";
const eventColor = (kind: string) => ({ tool: "#78a9ff", message: "#a58aff", error: "#fb7185", thinking: "#f5bb63", spawn: "#43d9a3", done: "#43d9a3" } as Record<string, string>)[kind] || "#94a3b8";

function latestByAgent(events: Event[]) {
  const latest = new Map<string, Event>();
  for (const event of events) latest.set(event.agent_id, event);
  return latest;
}

export default function AgentWorldsPanel({ installId, projectId }: Props) {
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
  const latest = useMemo(() => latestByAgent(visibleEvents), [visibleEvents]);
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
      canvas.height = portrait ? 1920 : 1000;
      const context = canvas.getContext("2d")!;
      context.fillStyle = "#10212d";
      context.fillRect(0, 0, canvas.width, canvas.height);
      context.drawImage(image, 0, 0, canvas.width, canvas.height);
      const a = document.createElement("a");
      a.download = `agent-worlds-${preset}.png`;
      a.href = canvas.toDataURL("image/png");
      a.click();
    } catch (caught) { setError((caught as Error).message); }
  }, [portrait, preset, serializeStage]);

  const recordClip = useCallback(async () => {
    if (!HTMLCanvasElement.prototype.captureStream || !window.MediaRecorder) { setError("Video recording is not supported in this browser"); return; }
    setRecording(true);
    try {
      const canvas = document.createElement("canvas");
      canvas.width = portrait ? 720 : 1280;
      canvas.height = portrait ? 1280 : 800;
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
          context.fillStyle = "#10212d";
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
  }, [portrait, preset, serializeStage]);

  return <div className="min-h-full bg-[#0b121d] p-4 text-[#e8eef5] sm:p-6">
    <style>{PANEL_STYLES}</style>
    <div className="mx-auto max-w-[1600px] space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div><div className="mb-1 flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.23em] text-[#76d6c4]"><span className="inline-block h-2 w-2 rounded-full bg-[#43d9a3]" /> Live observatory</div><h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">Agent Worlds</h1><p className="mt-1 text-sm text-[#99aabd]">Watch your agents work across the main server and isolated environments.</p></div>
        <div className="flex flex-wrap gap-2"><button onClick={savePNG} disabled={!scene} className="rounded-lg border border-[#34465a] px-3 py-2 text-xs font-medium hover:bg-[#1a2d3d] disabled:opacity-40">Export PNG</button><button onClick={recordClip} disabled={!scene || recording} className="rounded-lg border border-[#34465a] px-3 py-2 text-xs font-medium hover:bg-[#1a2d3d] disabled:opacity-40">{recording ? "Recording 12s…" : "Record clip"}</button></div>
      </header>

      <div className="grid gap-3 rounded-xl border border-[#283b4c] bg-[#132130] p-3 sm:grid-cols-[minmax(180px,1fr)_minmax(140px,220px)_auto]">
        <label className="text-xs text-[#95a8bb]">Source<select value={sourceId} onChange={event => setSourceId(event.target.value)} className="mt-1 block w-full rounded-md border border-[#3a5263] bg-[#0c1723] px-3 py-2 text-sm text-[#e8eef5]">{sources.map(source => <option key={source.id} value={source.id}>{source.label} · {source.agents} agents</option>)}</select></label>
        <label className="text-xs text-[#95a8bb]">Agent<select value={selectedAgent} onChange={event => setSelectedAgent(event.target.value)} className="mt-1 block w-full rounded-md border border-[#3a5263] bg-[#0c1723] px-3 py-2 text-sm text-[#e8eef5]"><option value="">All agents</option>{scene?.agents.map(agent => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label>
        <div className="flex items-end gap-2"><button onClick={() => { setLive(!live); setReplayIndex(scene?.events.length || 0); }} className={`rounded-md px-3 py-2 text-sm font-medium ${live ? "bg-[#184d43] text-[#74e4ba]" : "bg-[#473927] text-[#f2c177]"}`}>{live ? "● Live" : "Ⅱ Paused"}</button><button onClick={loadScene} className="rounded-md border border-[#3a5263] px-3 py-2 text-sm">Refresh</button></div>
      </div>

      {error && <div className="rounded-lg border border-[#8d4355] bg-[#442534] px-4 py-3 text-sm text-[#ffd1d8]">{error}</div>}

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap gap-1 rounded-xl border border-[#283b4c] bg-[#132130] p-1">{presets.map(item => <button key={item.id} title={item.detail} onClick={() => setPreset(item.id)} className={`rounded-lg px-3 py-2 text-xs font-medium transition ${preset === item.id ? "bg-[#2b5161] text-white shadow" : "text-[#9eb0c0] hover:bg-[#1b3443]"}`}>{item.name}</button>)}</div>
        <label className="flex cursor-pointer items-center gap-2 text-xs text-[#a5b6c7]"><input type="checkbox" checked={portrait} onChange={event => setPortrait(event.target.checked)} className="accent-[#43d9a3]" /> Portrait 9:16</label>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_285px]">
        <section className={`overflow-hidden rounded-2xl border border-[#2b4253] bg-[#10212d] shadow-[0_18px_60px_rgba(0,0,0,.22)] ${portrait ? "mx-auto w-full max-w-[540px]" : "w-full"}`}>
          <div className="flex items-center justify-between border-b border-[#294252] px-4 py-3 text-xs text-[#8ea7b9]"><span className="font-semibold uppercase tracking-[0.15em] text-[#c9dae6]">{presets.find(item => item.id === preset)?.name}</span><span>{scene?.source.label || "Loading…"}</span></div>
          <div className={portrait ? "aspect-[9/16]" : "aspect-[16/10]"}>
            {scene ? preset === "graph" ? <SystemGraph refNode={stageRef} source={scene.source} agents={visibleAgents} apps={scene.apps} events={visibleEvents} portrait={portrait} onSelect={setSelectedAgent} /> : preset === "village" ? <PixelVillage refNode={stageRef} agents={visibleAgents} events={visibleEvents} portrait={portrait} now={animationNow} seenAt={seenAtRef.current} onSelect={setSelectedAgent} /> : preset === "operations" ? <OperationsStage refNode={stageRef} agents={visibleAgents} events={visibleEvents} portrait={portrait} /> : <ConversationStage refNode={stageRef} agents={visibleAgents} events={visibleEvents} portrait={portrait} /> : <div className="flex h-full items-center justify-center text-sm text-[#95aabd]">Connecting to the scene…</div>}
          </div>
        </section>
        <aside className="space-y-4">
          <div className="rounded-xl border border-[#283b4c] bg-[#132130] p-4"><div className="mb-3 text-xs font-semibold uppercase tracking-widest text-[#91aabd]">At a glance</div><div className="grid grid-cols-3 gap-2 text-center"><Metric label="Agents" value={visibleAgents.length} /><Metric label="Threads" value={new Set(visibleEvents.map(event => event.agent_id + ":" + event.thread_id)).size} /><Metric label="Events" value={visibleEvents.length} /></div><div className="mt-3 text-xs text-[#8ca1b4]">{scene ? `Updated ${clock(scene.at)}` : "Waiting for data"}</div></div>
          <div className="rounded-xl border border-[#283b4c] bg-[#132130] p-4"><div className="mb-3 flex items-center justify-between"><span className="text-xs font-semibold uppercase tracking-widest text-[#91aabd]">Live activity</span><span className="text-[10px] text-[#72899c]">Last {Math.min(visibleEvents.length, 10)}</span></div><div className="max-h-[460px] space-y-1 overflow-auto">{visibleEvents.slice(-10).reverse().map(event => <div key={event.id} className="flex gap-2 border-b border-[#223849] py-2 last:border-0"><span className="mt-1.5 h-2 w-2 shrink-0 rounded-full" style={{ background: eventColor(event.kind) }} /><div className="min-w-0"><div className="truncate text-xs text-[#d5e2ec]"><b>{scene?.agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id}</b> {event.label}</div><div className="mt-0.5 text-[10px] text-[#7f95a8]">{clock(event.time)}{event.thread_id ? ` · ${event.thread_id}` : ""}</div></div></div>)}{!visibleEvents.length && <p className="text-xs text-[#7f95a8]">Events will appear as agents work.</p>}</div></div>
        </aside>
      </div>
      {!live && <div className="rounded-xl border border-[#283b4c] bg-[#132130] px-4 py-3"><div className="mb-2 flex justify-between text-xs text-[#9db1c1]"><span>Replay</span><span>{lastEvent ? clock(lastEvent.time) : "Start"}</span></div><input type="range" min={0} max={scene?.events.length || 0} value={replayIndex} onChange={event => setReplayIndex(Number(event.target.value))} className="w-full accent-[#43d9a3]" /></div>}
    </div>
  </div>;
}

function Metric({ label, value }: { label: string; value: number }) { return <div className="rounded-lg bg-[#0c1925] px-2 py-3"><div className="text-lg font-semibold text-[#ecf4f7]">{value}</div><div className="text-[10px] text-[#8299ab]">{label}</div></div>; }

function SvgText({ x, y, children, size = 14, fill = "#d9e7ee", anchor = "start", weight = 400 }: { x: number; y: number; children: React.ReactNode; size?: number; fill?: string; anchor?: "start" | "middle" | "end"; weight?: number }) { return <text x={x} y={y} fill={fill} fontSize={size} fontFamily="Inter, ui-sans-serif, system-ui" fontWeight={weight} textAnchor={anchor}>{children}</text>; }

function SystemGraph({ refNode, source, agents, apps, events, portrait, onSelect }: { refNode: React.RefObject<SVGSVGElement | null>; source: Source; agents: Agent[]; apps: AppNode[]; events: Event[]; portrait: boolean; onSelect: (id: string) => void }) {
  const width = portrait ? 540 : 960, height = portrait ? 960 : 600;
  const latest = latestByAgent(events);
  const toolNames = [...new Set(events.filter(event => event.target).slice(-50).map(event => event.target!))].slice(-6);
  const resources = apps.length ? apps.slice(0, 5).map(app => app.name) : toolNames;
  const centerX = width * .47, centerY = height * .49;
  const radiusX = portrait ? 175 : 300, radiusY = portrait ? 230 : 170;
  const positions = agents.map((agent, i) => ({ agent, x: centerX + Math.cos((i / Math.max(agents.length, 1)) * Math.PI * 2 - Math.PI / 2) * radiusX, y: centerY + Math.sin((i / Math.max(agents.length, 1)) * Math.PI * 2 - Math.PI / 2) * radiusY }));
  const resourcePoint = (index: number) => ({ x: portrait ? 42 + (index % 2) * 250 : 48 + index * 150, y: portrait ? height - 240 + Math.floor(index / 2) * 62 : height - 75 });
  const threads = new Map<string, string[]>();
  for (const agent of agents) threads.set(agent.id, agent.main_thread ? [agent.main_thread] : []);
  for (const event of events) if (event.thread_id && threads.has(event.agent_id)) {
    const list = threads.get(event.agent_id)!;
    if (!list.includes(event.thread_id) && list.length < 3) list.push(event.thread_id);
  }
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Agent system graph" style={{ background: "#101d2b" }}>
    <defs><pattern id="graphGrid" width="28" height="28" patternUnits="userSpaceOnUse"><path d="M 28 0 L 0 0 0 28" fill="none" stroke="#203445" strokeWidth="1" /></pattern><filter id="graphGlow"><feGaussianBlur stdDeviation="5" /></filter></defs>
    <rect width={width} height={height} fill="#101d2b" /><rect width={width} height={height} fill="url(#graphGrid)" />
    <SvgText x={28} y={38} size={12} fill="#80a7b5" weight={700}>LIVE TOPOLOGY</SvgText>
    <SvgText x={width - 28} y={38} size={12} fill="#80a7b5" anchor="end">{agents.length} AGENTS · {toolNames.length + apps.length} RESOURCES</SvgText>
    {resources.flatMap((name, index) => positions.filter(({ agent }) => events.some(event => event.agent_id === agent.id && event.target && (event.target === name || event.target.startsWith(name + "_")))).map(({ agent, x, y }) => { const point = resourcePoint(index); return <line key={`${agent.id}:${name}`} x1={x} y1={y + 34} x2={point.x + (portrait ? 102 : 65)} y2={point.y} stroke="#6089c5" strokeWidth="1" opacity=".45" />; }))}
    <circle cx={centerX} cy={centerY} r={portrait ? 62 : 70} fill="#143b43" stroke="#4ec7b0" strokeWidth="2" />
    <circle cx={centerX} cy={centerY} r={portrait ? 48 : 55} fill="#17333e" />
    <SvgText x={centerX} y={centerY - 5} size={portrait ? 20 : 22} anchor="middle" weight={700}>APTEVA</SvgText>
    <SvgText x={centerX} y={centerY + 18} size={11} fill="#78b9b0" anchor="middle">{source.kind.includes("runtime") ? "ENVIRONMENT" : "LIVE SERVER"}</SvgText>
    {positions.map(({ agent, x, y }) => <line key={`edge-${agent.id}`} x1={centerX} y1={centerY} x2={x} y2={y} stroke={latest.get(agent.id) ? eventColor(latest.get(agent.id)!.kind) : "#416071"} strokeWidth={latest.get(agent.id) ? 2 : 1} strokeDasharray={latest.get(agent.id) ? undefined : "5 7"} opacity=".8" />)}
    {positions.map(({ agent, x, y }) => <g key={agent.id} onClick={() => onSelect(agent.id)} style={{ cursor: "pointer" }}><circle cx={x} cy={y} r="43" fill="#172c3c" stroke={statusColor(agent.status)} strokeWidth="2" /><circle cx={x} cy={y - 12} r="10" fill={statusColor(agent.status)} /><path d={`M ${x - 18} ${y + 10} Q ${x} ${y - 8} ${x + 18} ${y + 10} L ${x + 18} ${y + 19} L ${x - 18} ${y + 19} Z`} fill={statusColor(agent.status)} /><SvgText x={x} y={y + 67} size={13} anchor="middle" weight={700}>{short(agent.name, 19)}</SvgText><SvgText x={x} y={y + 84} size={10} fill="#8aa4b8" anchor="middle">{short(latest.get(agent.id)?.label || agent.status, 28)}</SvgText></g>)}
    {positions.flatMap(({ agent, x, y }) => (threads.get(agent.id) || []).map((thread, index) => { const side = x < centerX ? -1 : 1, tx = x + side * 62, ty = y - 22 + index * 22; return <g key={`${agent.id}:${thread}`}><line x1={x + side * 38} y1={y} x2={tx} y2={ty} stroke="#8b82bb" strokeWidth="1" strokeDasharray="3 3" /><circle cx={tx} cy={ty} r="6" fill="#a58aff" /><SvgText x={tx + side * 9} y={ty + 4} size={9} fill="#bdb3dc" anchor={side === -1 ? "end" : "start"}>{short(thread, 12)}</SvgText></g>; }))}
    {resources.map((name, i) => { const { x, y } = resourcePoint(i); return <g key={name}><rect x={x} y={y} width={portrait ? 205 : 130} height="38" rx="7" fill="#1c3444" stroke="#466172" /><SvgText x={x + 12} y={y + 24} size={11} fill="#bdd2df">{short(name, portrait ? 28 : 16)}</SvgText></g>; })}
    {!agents.length && <SvgText x={width / 2} y={height / 2 + 110} size={16} fill="#8da8ba" anchor="middle">No agents in this source yet</SvgText>}
  </svg>;
}

function OperationsStage({ refNode, agents, events, portrait }: { refNode: React.RefObject<SVGSVGElement | null>; agents: Agent[]; events: Event[]; portrait: boolean }) {
  const width = portrait ? 540 : 960, height = portrait ? 960 : 600;
  const latest = latestByAgent(events), columns = portrait ? 1 : 2, cardWidth = portrait ? 480 : 430, cardHeight = 118;
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Agent operations board" style={{ background: "#101d2b" }}><rect width={width} height={height} fill="#101d2b" /><SvgText x={30} y={48} size={25} weight={700}>Operations board</SvgText><SvgText x={30} y={73} size={12} fill="#89a6b9">Current state across {agents.length} agents</SvgText>{agents.slice(0, portrait ? 6 : 8).map((agent, index) => { const x = 30 + (index % columns) * (cardWidth + 32), y = 104 + Math.floor(index / columns) * (cardHeight + 20), item = latest.get(agent.id); return <g key={agent.id}><rect x={x} y={y} width={cardWidth} height={cardHeight} rx="12" fill="#182f3e" stroke="#355263" /><rect x={x} y={y} width="5" height={cardHeight} rx="2" fill={statusColor(agent.status)} /><circle cx={x + 32} cy={y + 32} r="12" fill={statusColor(agent.status)} /><SvgText x={x + 58} y={y + 29} size={16} weight={700}>{short(agent.name, 25)}</SvgText><SvgText x={x + cardWidth - 18} y={y + 29} size={11} fill={statusColor(agent.status)} anchor="end">{agent.status.toUpperCase()}</SvgText><SvgText x={x + 20} y={y + 66} size={13} fill="#b8cbd8">{short(item?.label || "No recent activity", 47)}</SvgText><SvgText x={x + 20} y={y + 92} size={10} fill="#829bad">{item ? `${clock(item.time)} · ${item.thread_id || "main"}` : agent.model || "Waiting for events"}</SvgText></g>; })}{!agents.length && <SvgText x={width / 2} y={height / 2} size={17} fill="#8da8ba" anchor="middle">No agents in this source yet</SvgText>}</svg>;
}

function ConversationStage({ refNode, agents, events, portrait }: { refNode: React.RefObject<SVGSVGElement | null>; agents: Agent[]; events: Event[]; portrait: boolean }) {
  const width = portrait ? 540 : 960, height = portrait ? 960 : 600;
  const messages = events.filter(event => ["message", "spawn", "done", "error"].includes(event.kind)).slice(portrait ? -9 : -6);
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Agent conversation stage" style={{ background: "#151d30" }}><rect width={width} height={height} fill="#151d30" /><SvgText x={30} y={48} size={25} weight={700}>Conversation stage</SvgText><SvgText x={30} y={74} size={12} fill="#9aa8c5">Agent handoffs and milestones</SvgText>{messages.map((event, index) => { const fromRight = index % 2 === 1, bubbleW = portrait ? 390 : 580, x = fromRight ? width - bubbleW - 34 : 34, y = 112 + index * (portrait ? 88 : 74), name = agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id; return <g key={event.id}><rect x={x} y={y} width={bubbleW} height={portrait ? 72 : 60} rx="13" fill={fromRight ? "#263954" : "#263b48"} stroke={fromRight ? "#4b6792" : "#4d7974"} /><SvgText x={x + 18} y={y + 24} size={12} fill={fromRight ? "#a9c0ff" : "#91dec8"} weight={700}>{short(name, 30)}</SvgText><SvgText x={x + 18} y={y + 47} size={14}>{short(event.label, portrait ? 42 : 68)}</SvgText><SvgText x={x + bubbleW - 15} y={y + 24} size={10} fill="#8498b0" anchor="end">{clock(event.time)}</SvgText></g>; })}{!messages.length && <SvgText x={width / 2} y={height / 2} size={17} fill="#9aa8c5" anchor="middle">Agent messages and handoffs will appear here</SvgText>}</svg>;
}

function PixelVillage({ refNode, agents, events, portrait, now, seenAt, onSelect }: { refNode: React.RefObject<SVGSVGElement | null>; agents: Agent[]; events: Event[]; portrait: boolean; now: number; seenAt: Map<string, number>; onSelect: (id: string) => void }) {
  const width = portrait ? 540 : 960, height = portrait ? 960 : 600;
  const groundY = portrait ? 625 : 405;
  const houses = portrait ? [{ x: 35, y: 195, label: "LIBRARY", color: "#d6a469" }, { x: 295, y: 195, label: "WORKSHOP", color: "#bd775c" }, { x: 165, y: 430, label: "MAILBOX", color: "#8c80bf" }] : [{ x: 65, y: 160, label: "LIBRARY", color: "#d6a469" }, { x: 385, y: 128, label: "WORKSHOP", color: "#bd775c" }, { x: 708, y: 165, label: "MAILBOX", color: "#8c80bf" }];
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="img" aria-label="Pixel village of live agents" shapeRendering="crispEdges" style={{ background: "#92c9d1", imageRendering: "pixelated" }}>
    <rect width={width} height={height} fill="#94cbd4" />
    <path d={`M 0 ${groundY - 110} L ${width * .2} ${groundY - 155} L ${width * .43} ${groundY - 108} L ${width * .68} ${groundY - 170} L ${width} ${groundY - 122} L ${width} ${groundY} L 0 ${groundY} Z`} fill="#78b6a8" />
    <path d={`M 0 ${groundY - 65} L ${width * .25} ${groundY - 95} L ${width * .5} ${groundY - 58} L ${width * .78} ${groundY - 105} L ${width} ${groundY - 63} L ${width} ${groundY} L 0 ${groundY} Z`} fill="#8ec39b" />
    <g fill="#e6f2e4"><rect x={width * .12} y="89" width="74" height="17" /><rect x={width * .12 + 13} y="77" width="48" height="13" /><rect x={width * .64} y="135" width="88" height="17" /><rect x={width * .64 + 17} y="122" width="52" height="13" /></g>
    <rect y={groundY} width={width} height={height - groundY} fill="#6ea66c" /><rect y={groundY + 30} width={width} height="16" fill="#63995f" />
    <circle cx={width - 85} cy="78" r="33" fill="#f8e8a5" /><rect x="0" y={groundY - 24} width={width} height="26" fill="#83b475" />
    {Array.from({ length: portrait ? 20 : 26 }, (_, i) => { const x = (i * 91 + 27) % width, y = groundY + 56 + ((i * 47) % Math.max(80, height - groundY - 80)); return <g key={i}><rect x={x} y={y} width="7" height="3" fill="#e8dda2" /><rect x={x + 3} y={y - 4} width="2" height="4" fill="#477c4c" /></g>; })}
    {houses.map((house, i) => <g key={house.label}><rect x={house.x + 15} y={house.y + 45} width="195" height="135" fill={house.color} stroke="#645b53" strokeWidth="5" /><path d={`M ${house.x} ${house.y + 50} L ${house.x + 112} ${house.y - 18} L ${house.x + 225} ${house.y + 50} Z`} fill={i === 1 ? "#704e5b" : i === 2 ? "#685985" : "#805d57"} stroke="#594d53" strokeWidth="5" /><rect x={house.x + 88} y={house.y + 101} width="45" height="79" fill="#634e49" /><rect x={house.x + 35} y={house.y + 85} width="35" height="35" fill="#d7e9d5" stroke="#5d6661" strokeWidth="5" /><rect x={house.x + 160} y={house.y + 85} width="35" height="35" fill="#d7e9d5" stroke="#5d6661" strokeWidth="5" /><rect x={house.x + 39} y={house.y + 165} width="150" height="22" fill="#e8d3a3" stroke="#61594c" strokeWidth="3" /><SvgText x={house.x + 114} y={house.y + 181} size={14} fill="#3c4b3e" anchor="middle" weight={700}>{house.label}</SvgText></g>)}
    <path d={portrait ? `M 269 625 L 269 900 M 80 698 L 465 698` : `M 0 488 L 960 488 M 481 405 L 481 600`} stroke="#c3b083" strokeWidth="48" fill="none" />
    {(portrait ? [{ x: 34, y: 750 }, { x: 493, y: 820 }] : [{ x: 36, y: 447 }, { x: 927, y: 434 }]).map((tree, index) => <g key={index}><rect x={tree.x - 7} y={tree.y - 8} width="14" height="40" fill="#705c49" /><rect x={tree.x - 29} y={tree.y - 50} width="58" height="45" fill="#3e805b" /><rect x={tree.x - 20} y={tree.y - 69} width="40" height="20" fill="#4a9364" /><rect x={tree.x - 35} y={tree.y - 36} width="9" height="20" fill="#34704f" /><rect x={tree.x + 26} y={tree.y - 36} width="9" height="20" fill="#34704f" /><rect x={tree.x - 17} y={tree.y - 42} width="13" height="6" fill="#6db67b" /></g>)}
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
        <rect x={x + 10} y={y + 8} width="6" height="10" fill="#efc69b" /><circle cx={x + 13} cy={y - 16} r="5" fill={statusColor(agent.status)} />
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
