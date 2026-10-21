import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { PANEL_STYLES, WORLD_NAV_STYLES } from "./panel-styles";
import VillageStage from "./village-stage";
import SceneViewport from "./scene-viewport";
import { withAppDisplayNames, type AppMetadata } from "./app-display-names";

import { buildWorld, rootFocus, resolveFocus, projectWorld, contentsForAgents, PAGE_SIZE, featuredDestinations, villageDestinations } from "./world-model";
import type { Source, Agent, Destination, Event, Scene, Focus, WorldNode } from "./world-model";
type Preset = "graph" | "operations" | "village";
type Props = { installId: number; projectId?: string };

// Buffer one polling cycle so newly observed calls begin before their walking
// animation reaches the event, rather than jumping forward on every refresh.
const VILLAGE_DELAY_MS = 3500;
const API = "/api/apps/agent-worlds/api";
const presets: { id: Preset; name: string; detail: string }[] = [
  { id: "graph", name: "System graph", detail: "Agents, apps and integrations" },
  { id: "operations", name: "Operations", detail: "Live status board" },
  { id: "village", name: "Pixel village", detail: "Agents visiting apps and integrations" },
];

function apiURL(path: string, installId: number, projectId: string = "") {
  const url = new URL(path.startsWith("/api/") ? path : API + path, window.location.origin);
  if (installId) url.searchParams.set("install_id", String(installId));
  if (projectId) url.searchParams.set("project_id", projectId);
  return url.pathname + url.search;
}

// Share one cancellable, sequential polling path for inventory and scenes.
// Pausing stops network refreshes without throwing away the replay snapshot.
function usePolledJSON<T>(path: string, installId: number, projectId: string, interval: number, polling = true) {
  const url = apiURL(path, installId, projectId);
  const [result, setResult] = useState<{ url: string; data: T | null; error: string }>({ url: "", data: null, error: "" });
  const [revision, setRevision] = useState(0);
  const pollingRef = useRef(polling);
  pollingRef.current = polling;
  const refresh = useCallback(() => setRevision(value => value + 1), []);

  useEffect(() => {
    const controller = new AbortController();
    let timer: number;
    const schedule = () => {
      if (controller.signal.aborted) return;
      timer = window.setTimeout(() => {
        if (pollingRef.current && document.visibilityState !== "hidden") void load();
        else schedule();
      }, interval);
    };
    const load = async () => {
      try {
        const response = await fetch(url, { signal: controller.signal });
        const body = await response.json();
        if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
        if (!controller.signal.aborted) setResult({ url, data: body, error: "" });
      } catch (caught) {
        if (!controller.signal.aborted) setResult(previous => ({ url, data: previous.url === url ? previous.data : null, error: (caught as Error).message }));
      } finally { schedule(); }
    };
    void load();
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [url, interval, revision]);

  return { data: result.url === url ? result.data : null, error: result.url === url ? result.error : "", refresh };
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

export default function AgentWorldsPanel({ installId, projectId = "" }: Props) {
  const palette = usePalette();
  const [sourceId, setSourceId] = useState("main");
  const [preset, setPreset] = useState<Preset>("village");
  const [showInspector, setShowInspector] = useState(false);
  const [focus, setFocus] = useState<Focus>({});
  const [page, setPage] = useState(0);
  const [selectedAgent, setSelectedAgentId] = useState("");
  const inspectorRef = useRef<HTMLElement|null>(null);
  const [selectedDestination, setSelectedDestination] = useState("");
  const [live, setLive] = useState(true);
  const [replayIndex, setReplayIndex] = useState(0);
  const [portraitChoice, setPortrait] = useState(false);
  const [narrow, setNarrow] = useState(false);
  const canvasHost = useRef<HTMLDivElement|null>(null);
  const portrait = portraitChoice || narrow;
  const [frozenScene, setFrozenScene] = useState<Scene|null>(null);
  const [exportError, setError] = useState("");
  const [recording, setRecording] = useState(false);
  const [animationNow, setAnimationNow] = useState(Date.now() - VILLAGE_DELAY_MS);
  const stageRef = useRef<SVGSVGElement | null>(null);
  const sourceData = usePolledJSON<Source[]>("/sources", installId, projectId, 15000);
  const sceneData = usePolledJSON<Scene>(`/scene?source=${encodeURIComponent(sourceId)}`, installId, projectId, 2500, live);
  // The host's existing app directory owns display names. One shared lookup
  // per minute serves every renderer; scene polling and agent count add no calls.
  const appData = usePolledJSON<AppMetadata[]>("/api/apps", 0, projectId, 60000);
  const sources = sourceData.data || [];
  const rawScene = live ? sceneData.data : frozenScene;
  const scene = useMemo(() => withAppDisplayNames(rawScene, appData.data || []), [rawScene, appData.data]);
  const error = exportError || sceneData.error || sourceData.error;

  useEffect(() => {
    if (sourceData.data) setSourceId(current => sourceData.data!.some(source => source.id === current) ? current : "main");
  }, [sourceData.data]);
  useEffect(() => { setFocus({}); setPage(0); setLive(true); setFrozenScene(null); setSelectedDestination(""); setSelectedAgentId(""); setShowInspector(false); setAnimationNow(Date.now() - VILLAGE_DELAY_MS); }, [sourceId, installId, projectId]);
  useEffect(() => {
    if (!rawScene) return;
    setReplayIndex(rawScene.events.length);
  }, [rawScene]);
  useEffect(() => { if (!live || preset !== "village") return; setAnimationNow(Date.now() - VILLAGE_DELAY_MS); const motion = typeof matchMedia === "function" ? matchMedia("(prefers-reduced-motion: reduce)") : null; const timer = window.setInterval(() => { if (document.visibilityState !== "hidden" && !motion?.matches) setAnimationNow(Date.now() - VILLAGE_DELAY_MS); }, 80); return () => window.clearInterval(timer); }, [live, preset]);

  // Replay is a frozen source snapshot. Focus, renderer and pagination never
  // reload it or move the replay cursor.
  const timelineEvents = useMemo(() => live ? scene?.events || [] : (scene?.events || []).slice(0, replayIndex), [scene,live,replayIndex]);
  const world = useMemo(() => scene ? buildWorld(scene,timelineEvents) : null, [scene,timelineEvents]);
  const currentFocus = scene && world ? resolveFocus(scene,world,focus) : {};
  const projection = scene && world ? projectWorld(scene,world,currentFocus) : null;
  const visibleAgents = projection?.agents || [];
  const visibleIds = new Set(visibleAgents.map(agent => agent.id));
  useEffect(() => {
    if (!canvasHost.current) return;
    const observer = new ResizeObserver(entries => setNarrow(entries[0].contentRect.width < 680));
    observer.observe(canvasHost.current); return () => observer.disconnect();
  },[]);
  const visibleEvents = timelineEvents.filter(event => visibleIds.has(event.agent_id));
  const lastEvent = visibleEvents[visibleEvents.length - 1];
  const destinations = projection?.destinations || [];
  const totalItems = projection?.nodes.length || visibleAgents.length;
  const pages = Math.max(1,Math.ceil(totalItems/PAGE_SIZE));
  const currentPage = Math.min(page,pages-1);
  const stageAgents = visibleAgents.slice(currentPage*PAGE_SIZE,(currentPage+1)*PAGE_SIZE);
  const stageIds = new Set(stageAgents.map(agent => agent.id));
  const stageEvents = visibleEvents.filter(event => stageIds.has(event.agent_id));
  const inspectedAgent = visibleAgents.find(agent => agent.id === selectedAgent);
  const closeDetails = () => { setShowInspector(false); setSelectedAgentId(""); setSelectedDestination(""); };
  const setSelectedAgent = (id: string) => {
    if (!id) { closeDetails(); return; }
    if (!visibleAgents.some(agent => agent.id === id)) return;
    setSelectedAgentId(id); setSelectedDestination(""); setShowInspector(true);
  };
  const inspectDestination = (id: string) => { setSelectedDestination(id); setSelectedAgentId(""); setShowInspector(true); };
  // An agent is inspected inside the current world. Project/team navigation
  // changes the scene; selection must not filter peers or reset the camera.
  const navigate = (next: Focus) => {
    if (next.agentId) { setSelectedAgent(next.agentId); return; }
    setFocus(next); setPage(0); closeDetails();
  };
  useEffect(() => { if (inspectorRef.current) inspectorRef.current.scrollTop = 0; }, [selectedAgent, selectedDestination]);
  useEffect(() => { if (scene && selectedAgent && !scene.agents.some(agent => agent.id === selectedAgent)) closeDetails(); }, [scene, selectedAgent]);
  const zoomOut = () => {
    if (currentFocus.agentId) navigate({projectId:currentFocus.projectId,teamId:currentFocus.teamId});
    else if (currentFocus.teamId) navigate({projectId:currentFocus.projectId});
    else if (scene?.scope === "instance") navigate({});
  };
  useEffect(() => {
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape" && !(event.target instanceof HTMLInputElement) && !(event.target instanceof HTMLSelectElement)) { if (showInspector) closeDetails(); else zoomOut(); } };
    window.addEventListener("keydown",escape); return () => window.removeEventListener("keydown",escape);
  });

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
    setError("");
    try {
      const image = await serializeStage();
      const canvas = document.createElement("canvas");
      canvas.width = portrait ? 1080 : 1600;
      canvas.height = portrait ? 1920 : preset === "village" ? 950 : 700;
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
    setError("");
    if (!HTMLCanvasElement.prototype.captureStream || !window.MediaRecorder) { setError("Video recording is not supported in this browser"); return; }
    setRecording(true);
    try {
      const canvas = document.createElement("canvas");
      canvas.width = portrait ? 720 : 1280;
      canvas.height = portrait ? 1280 : preset === "village" ? 760 : 560;
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
  const threadCount = new Set([...visibleAgents.flatMap(agent => (agent.threads || []).map(thread => `${agent.id}:${thread.id}`)),...visibleEvents.filter(event => event.thread_id).map(event => `${event.agent_id}:${event.thread_id}`)]).size;
  const runningCount = visibleAgents.filter(agent => agent.status === "running" || agent.status === "active").length;
  const inspectorContent = inspectedAgent ? contentsForAgents([inspectedAgent], destinations, visibleEvents, currentFocus.projectId) : {destinations, events:visibleEvents};
  const inspectorDestinations = inspectorContent.destinations, inspectorEvents = inspectorContent.events;
  const destination = destinations.find(item => item.id === selectedDestination);
  const destinationEvents = visibleEvents.filter(event => event.target_id === selectedDestination);
  return <div className="aw">
    <style>{PANEL_STYLES + WORLD_NAV_STYLES}</style>
    <header className="aw-toolbar">
      <div className="aw-toolbar-main">
        <h1>Agent Worlds</h1>
        <select className="aw-visualization" aria-label="Visualization" value={preset} onChange={event => setPreset(event.target.value as Preset)}>{presets.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
        <nav className="aw-breadcrumbs" aria-label="World focus">
          {scene?.scope === "instance" && <button onClick={() => navigate({})} aria-current={!currentFocus.projectId ? "location":undefined}>Instance</button>}
          {currentFocus.projectId && <><span>/</span><button onClick={() => navigate({projectId:currentFocus.projectId})} aria-current={!currentFocus.teamId ? "location":undefined}>{world?.projects.find(project => project.id === currentFocus.projectId)?.name || currentFocus.projectId}</button></>}
          {projection?.team && <><span>/</span><button onClick={() => navigate({projectId:currentFocus.projectId,teamId:currentFocus.teamId})} aria-current="location">Observed team · {projection.team.name}</button></>}
          {(currentFocus.agentId || currentFocus.teamId || (scene?.scope === "instance" && currentFocus.projectId)) && <button className="aw-zoom-out" onClick={zoomOut}>Zoom out</button>}
        </nav>
      </div>
      <button className={`aw-button aw-live ${live ? "is-live" : ""}`} onClick={() => { if (live) setFrozenScene(scene); setLive(!live); setReplayIndex(scene?.events.length || 0); }}><span className="aw-dot" />{live ? "Live" : "Paused"}</button>
      <button className="aw-button" aria-pressed={showInspector} onClick={() => { if (showInspector) closeDetails(); else setShowInspector(true); }}>Details</button>
      <details className="aw-options"><summary className="aw-button" aria-label="More controls">⋯</summary><div className="aw-options-menu">
        <label className="aw-field"><span>Source</span><select aria-label="Source" value={sourceId} onChange={event => setSourceId(event.target.value)}>{sources.map(source => <option key={source.id} value={source.id}>{source.label} · {source.agents} agents</option>)}</select></label>
        <label className="aw-field"><span>Inspect agent</span><select aria-label="Inspect agent" value={selectedAgent} onChange={event => setSelectedAgent(event.target.value)}><option value="">Choose an agent</option>{visibleAgents.map(agent => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label>
        <button className="aw-button" onClick={sceneData.refresh} disabled={!live}>Refresh</button>
        <label className="aw-portrait"><input type="checkbox" checked={portraitChoice} onChange={event => setPortrait(event.target.checked)} /> Portrait</label>
        <button className="aw-button" onClick={savePNG} disabled={!scene}>Export PNG</button>
        <button className="aw-button" onClick={recordClip} disabled={!scene || recording}>{recording ? "Recording…" : "Record clip"}</button>
        <small>{threadCount} threads · {visibleEvents.length} events<br/>{scene ? `Updated ${clock(scene.at)}` : "Connecting…"}</small>
      </div></details>
    </header>
    <main className="aw-main"><div className="aw-frame">
      {scene?.warnings?.map(warning => <div className="aw-notice" key={warning}>{warning}</div>)}
      {error && <div className="aw-error" role="alert">{error}</div>}
        <div className={`aw-content ${portrait ? "is-portrait" : ""} ${preset === "village" ? "is-village" : ""}`}>
          <section className="aw-scene" aria-label={activePreset.name}>
            <SceneViewport hostRef={canvasHost} stageRef={stageRef} width={portrait ? 540 : 1280} height={portrait ? 960 : preset === "village" ? 760 : 560} sceneKey={JSON.stringify([installId,projectId,sourceId,currentFocus,currentPage])} status={`${visibleAgents.length} agents · ${runningCount} running`}>
              {scene ? projection?.nodes.length ? <HierarchyStage refNode={stageRef} nodes={projection.nodes.slice(currentPage*PAGE_SIZE,(currentPage+1)*PAGE_SIZE)} preset={preset} portrait={portrait} destinations={destinations} events={visibleEvents} selectedAgent={selectedAgent} onSelect={setSelectedAgent} onFocus={navigate} palette={palette} live={live} now={animationNow} /> : preset === "graph" ? <SystemGraph refNode={stageRef} source={scene.source} agents={stageAgents} destinations={destinations} selectedDestination={selectedDestination} events={stageEvents} portrait={portrait} selectedAgent={selectedAgent} onSelect={setSelectedAgent} onSelectDestination={inspectDestination} palette={palette} /> : preset === "village" ? <VillageStage showChrome={false} refNode={stageRef} agents={stageAgents} destinations={villageDestinations(destinations, selectedDestination, stageEvents)} selectedDestination={selectedDestination} events={stageEvents} portrait={portrait} now={animationNow} live={live} selectedAgent={selectedAgent} onSelect={setSelectedAgent} onSelectDestination={inspectDestination} /> : <OperationsStage refNode={stageRef} agents={stageAgents} events={stageEvents} portrait={portrait} selectedAgent={selectedAgent} onSelect={setSelectedAgent} palette={palette} /> : <div className="aw-loading">Connecting to the scene…</div>}

            </SceneViewport>
            {pages > 1 && <div className="aw-pages" aria-label="Scene pages"><button className="aw-button" disabled={currentPage === 0} onClick={() => setPage(currentPage-1)}>Previous</button><span>{currentPage+1} / {pages}</span><button className="aw-button" disabled={currentPage+1 === pages} onClick={() => setPage(currentPage+1)}>Next</button></div>}
          </section>
          {showInspector && <aside ref={inspectorRef} className="aw-sidebar" aria-label={inspectedAgent ? "Agent details" : destination ? "App details" : "World details"}>
            <div className="aw-inspector-head"><h2>{inspectedAgent?.name || destination?.name || "World details"}</h2><button className="aw-close" aria-label="Close details" onClick={closeDetails}>×</button></div>
            {inspectedAgent && <section className="aw-agent-detail" data-agent-details={inspectedAgent.id}>
              <span className="aw-inspected-status" style={{color:statusColor(inspectedAgent.status,palette)}}><span className="aw-dot"/>{inspectedAgent.status}</span>
              <dl><dt>Project</dt><dd>{world?.projects.find(project => project.id === world.projectOf(inspectedAgent))?.name || inspectedAgent.project_id || scene?.source.label}</dd>{inspectedAgent.model && <><dt>Model</dt><dd>{inspectedAgent.model}</dd></>}<dt>Latest activity</dt><dd>{inspectorEvents.at(-1)?.label || "No recent activity"}</dd></dl>
            </section>}
            {inspectedAgent && <section className="aw-side-threads"><div className="aw-side-head"><h3>Threads</h3><span>{inspectedAgent.threads?.length || 0}</span></div>{inspectedAgent.threads?.length ? inspectedAgent.threads.map(thread => <div className="aw-thread-row" key={thread.id}><strong>{thread.id}</strong><span>{thread.status || "Observed"}{thread.parent_id ? ` · parent ${thread.parent_id}`:""}</span></div>) : <div className="aw-plain-empty">No thread inventory available.</div>}</section>}
            <section className="aw-side-destinations"><div className="aw-side-head"><h3>Apps &amp; integrations</h3><span>{inspectorDestinations.length}</span></div><div className="aw-destination-list">{inspectorDestinations.map(item => <button key={item.id} className={`aw-destination-row ${selectedDestination === item.id ? "is-selected" : ""}`} onClick={() => inspectDestination(selectedDestination === item.id ? "" : item.id)}><span className={`aw-destination-icon is-${item.kind}`}>{item.kind === "app" ? "▣" : item.kind === "integration" ? "⌁" : "◇"}</span><span><strong>{item.name}</strong><small>{scene?.scope === "instance" && !currentFocus.projectId ? `${world?.projects.find(project => project.id === item.project_id)?.name || item.project_id || "Source"} · ` : ""}{item.kind} · {item.status}</small></span><em>{item.call_count}</em></button>)}{!inspectorDestinations.length && <div className="aw-plain-empty">No apps or integrations available for this source.</div>}</div>{destination && <div className="aw-destination-detail"><strong>{destination.name}</strong><span>{destination.kind} · {destination.status} · {destinationEvents.filter(event => event.kind === "tool").length} visible calls</span><span>{[...new Set(destinationEvents.map(event => scene?.agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id))].join(", ") || "No agents have visited yet"}</span>{destination.tools?.length ? <small>{destination.tool_count} mapped tools · {destination.tools.slice(0, 3).join(", ")}{destination.tool_count > 3 ? "…" : ""}</small> : null}{destinationEvents.filter(event => event.kind === "tool").slice(-3).reverse().map(event => <small key={event.id}>{clock(event.time)} · {scene?.agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id} · {short(event.target || "tool", 24)}</small>)}</div>}</section>
            <section className="aw-side-activity"><div className="aw-side-head"><h3>Activity</h3><span>{live ? "Live feed" : "Replay"}</span></div><div className="aw-activity-list">{inspectorEvents.slice(-10).reverse().map(event => <div key={event.id} className="aw-event"><span className="aw-event-mark" style={{ background: eventColor(event.kind, palette) }} /><div><div className="aw-event-line"><b>{scene?.agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id}</b> {event.label}</div><div className="aw-event-meta">{clock(event.time)}{event.thread_id ? ` · ${short(event.thread_id, 18)}` : ""}</div></div></div>)}{!inspectorEvents.length && <div className="aw-plain-empty">Activity appears as agents work.</div>}</div></section>
            <section className="aw-side-agents"><div className="aw-side-head"><h3>Agents</h3><span>{visibleAgents.length}</span></div><div className="aw-agent-list">{visibleAgents.map(agent => <button key={agent.id} onClick={() => setSelectedAgent(agent.id)} className={`aw-agent-row ${selectedAgent === agent.id ? "is-selected" : ""}`}><span className="aw-agent-avatar">{agent.name.slice(0,1).toUpperCase()}</span><span className="aw-agent-name"><strong>{agent.name}</strong><small>{agent.model || agent.main_thread || agent.status}</small></span><span className="aw-agent-status" style={{ background: statusColor(agent.status, palette) }} title={agent.status} /></button>)}{!visibleAgents.length && <div className="aw-plain-empty">No agents in this source</div>}</div></section>


          </aside>}
        </div>
        {!live && <div className="aw-replay"><div><strong>Replay</strong><span>{lastEvent ? clock(lastEvent.time) : "Start"}</span></div><input aria-label="Replay position" type="range" min={0} max={scene?.events.length || 0} value={replayIndex} onChange={event => {
          const cursor = Number(event.target.value); setReplayIndex(cursor);
          const eventTime = scene?.events[cursor > 0 ? cursor - 1 : 0]?.time || scene?.at;
          if (eventTime) setAnimationNow(Date.parse(eventTime) - (cursor === 0 ? 1 : 0));
        }} /></div>}
      </div>
    </main>
  </div>;
}

function SvgText({ x, y, children, size = 14, fill = "currentColor", anchor = "start", weight = 400 }: { x: number; y: number; children: React.ReactNode; size?: number; fill?: string; anchor?: "start" | "middle" | "end"; weight?: number }) { return <text x={x} y={y} fill={fill} fontSize={size} fontWeight={weight} textAnchor={anchor}>{children}</text>; }

function SystemGraph({ refNode, source, agents, destinations, selectedDestination, selectedAgent, events, portrait, onSelect, onSelectDestination, palette }: { refNode: React.RefObject<SVGSVGElement | null>; source: Source; agents: Agent[]; destinations: Destination[]; selectedDestination: string; selectedAgent: string; events: Event[]; portrait: boolean; onSelect: (id: string) => void; onSelectDestination: (id: string) => void; palette: Palette }) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
  const shownAgents = agents.slice(0, portrait ? 8 : 9), shownDestinations = featuredDestinations(destinations, portrait ? 8 : 9, selectedDestination);
  const latest = latestByAgent(events);
  const agentX = portrait ? 22 : 42, destinationX = portrait ? 286 : 978;
  const cardW = portrait ? 230 : 258, cardH = portrait ? 68 : 45;
  const rowY = (i: number, count: number) => portrait ? 280 + i * 76 : 75 + i * Math.min(51, 430 / Math.max(1, count));
  const agentPositions = new Map(shownAgents.map((item, i) => [item.id, { x: agentX, y: rowY(i, shownAgents.length) }]));
  const destinationPositions = new Map(shownDestinations.map((item, i) => [item.id, { x: destinationX, y: rowY(i, shownDestinations.length) }]));
  const observed = new Set(events.filter(item => item.kind === "tool" && item.target_id).map(item => `${item.agent_id}|${item.target_id}`));
  const hubX = width / 2, hubY = portrait ? 143 : 280;
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="group" aria-label="Agent and destination graph" style={{ color: palette.text, background: palette.subtle, fontFamily: palette.font }}>
    <defs><pattern id="awGraphDots" width="24" height="24" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="1" fill={palette.border} /></pattern></defs>
    <rect width={width} height={height} fill={palette.subtle} /><rect width={width} height={height} fill="url(#awGraphDots)" />
    <SvgText x={agentX} y={portrait ? 258 : 42} size={11} fill={palette.dim} weight={700}>AGENTS · {agents.length}</SvgText><SvgText x={destinationX} y={portrait ? 258 : 42} size={11} fill={palette.dim} weight={700}>APPS &amp; INTEGRATIONS · {destinations.length}</SvgText>
    {[...observed].map(key => { const [agentID, destinationID] = key.split("|"); const from = agentPositions.get(agentID), to = destinationPositions.get(destinationID); if (!from || !to) return null; const x1 = from.x + cardW, y1 = from.y + cardH/2, x2 = to.x, y2 = to.y + cardH/2; return <path key={key} d={`M ${x1} ${y1} C ${portrait ? 268 : 550} ${y1}, ${portrait ? 272 : 730} ${y2}, ${x2} ${y2}`} fill="none" stroke={palette.info} strokeWidth="1.6" opacity=".7" />; })}
    {!portrait && shownAgents.map(agent => { const pos = agentPositions.get(agent.id)!; return <path key={`hub:${agent.id}`} d={`M ${pos.x+cardW} ${pos.y+cardH/2} L ${hubX-115} ${hubY}`} stroke={palette.border} fill="none" strokeWidth="1" />; })}
    <rect x={hubX-115} y={hubY-49} width="230" height="98" rx="8" fill={palette.card} stroke={palette.accent} strokeWidth="1.7" /><rect x={hubX-115} y={hubY-49} width="4" height="98" fill={palette.accent} />
    <SvgText x={hubX-94} y={hubY-17} size={10} fill={palette.accent} weight={700}>APTEVA · {source.kind.includes("runtime") ? "ENVIRONMENT" : "SERVER"}</SvgText><SvgText x={hubX-94} y={hubY+11} size={17} weight={700}>{short(source.label, 20)}</SvgText><SvgText x={hubX-94} y={hubY+31} size={10} fill={palette.dim}>{agents.length} connected agents</SvgText>
    {shownAgents.map(agent => { const pos=agentPositions.get(agent.id)!; return <g key={agent.id} data-selected={selectedAgent===agent.id} aria-pressed={selectedAgent===agent.id} {...svgAction(`Inspect agent ${agent.name}`, () => onSelect(agent.id))}><rect x={pos.x} y={pos.y} width={cardW} height={cardH} rx="5" fill={palette.card} stroke={palette.border}/><rect x={pos.x} y={pos.y} width="3" height={cardH} fill={statusColor(agent.status,palette)}/><SvgText x={pos.x+14} y={pos.y+20} size={12} weight={700}>{short(agent.name,portrait?22:24)}</SvgText><SvgText x={pos.x+14} y={pos.y+cardH-10} size={10} fill={palette.dim}>{short(latest.get(agent.id)?.label || agent.status,portrait?30:33)}</SvgText></g>; })}
    {shownDestinations.map(item => { const pos=destinationPositions.get(item.id)!; const color=item.kind === "integration" ? palette.info : item.kind === "app" ? palette.accent : palette.dim; return <g key={item.id} {...svgAction(`Inspect destination ${item.name}`, () => onSelectDestination(item.id))}><rect x={pos.x} y={pos.y} width={cardW} height={cardH} rx="5" fill={palette.card} stroke={palette.border}/><rect x={pos.x} y={pos.y} width="3" height={cardH} fill={color}/><SvgText x={pos.x+14} y={pos.y+20} size={12} weight={700}>{short(item.name,portrait?19:22)}</SvgText><SvgText x={pos.x+14} y={pos.y+cardH-10} size={10} fill={palette.dim}>{item.kind} · {item.call_count} calls</SvgText></g>; })}
    {!destinations.length && <SvgText x={destinationX+10} y={portrait?315:135} size={12} fill={palette.dim}>No destinations found</SvgText>}
    {(agents.length>shownAgents.length || destinations.length>shownDestinations.length) && <SvgText x={width-22} y={height-17} size={10} fill={palette.dim} anchor="end">Showing {shownAgents.length} agents · {shownDestinations.length} destinations</SvgText>}
  </svg>;
}

function OperationsStage({ refNode, agents, selectedAgent, events, portrait, onSelect, palette }: { refNode: React.RefObject<SVGSVGElement | null>; agents: Agent[]; selectedAgent: string; events: Event[]; portrait: boolean; onSelect: (id:string) => void; palette: Palette }) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
  const latest = latestByAgent(events), columns = portrait ? 1 : 2, cardWidth = portrait ? 480 : 592, cardHeight = portrait ? 104 : 96;
  return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="group" aria-label="Agent operations board" style={{ color: palette.text, background: palette.subtle, fontFamily: palette.font }}><rect width={width} height={height} fill={palette.subtle} /><SvgText x={30} y={48} size={22} weight={700}>Operations</SvgText><SvgText x={30} y={72} size={11} fill={palette.dim}>{agents.length} AGENTS · CURRENT STATUS</SvgText><line x1={30} y1={88} x2={width-30} y2={88} stroke={palette.border} />{agents.slice(0, portrait ? 7 : 8).map((agent, index) => { const x = 30 + (index % columns) * (cardWidth + 36), y = 110 + Math.floor(index / columns) * (cardHeight + 16), item = latest.get(agent.id); return <g key={agent.id} data-selected={selectedAgent===agent.id} aria-pressed={selectedAgent===agent.id} {...svgAction(`Inspect agent ${agent.name}`, () => onSelect(agent.id))}><rect x={x} y={y} width={cardWidth} height={cardHeight} rx="7" fill={palette.card} stroke={palette.border} /><rect x={x} y={y} width="4" height={cardHeight} fill={statusColor(agent.status, palette)} /><SvgText x={x + 20} y={y + 27} size={15} weight={700}>{short(agent.name, 25)}</SvgText><circle cx={x + cardWidth - 80} cy={y + 22} r="4" fill={statusColor(agent.status, palette)} /><SvgText x={x + cardWidth - 17} y={y + 26} size={10} fill={statusColor(agent.status, palette)} anchor="end">{agent.status.toUpperCase()}</SvgText><line x1={x + 20} y1={y + 40} x2={x + cardWidth - 20} y2={y + 40} stroke={palette.border} /><SvgText x={x + 20} y={y + 64} size={12} fill={palette.muted}>{short(item?.label || "No recent activity", 47)}</SvgText><SvgText x={x + 20} y={y + 86} size={10} fill={palette.dim}>{item ? `${clock(item.time)} · ${item.thread_id || "main"}` : agent.model || "Waiting for events"}</SvgText></g>; })}{!agents.length && <SvgText x={width / 2} y={height / 2} size={15} fill={palette.dim} anchor="middle">No agents in this source yet</SvgText>}</svg>;
}

function svgAction(label: string, action: () => void) {
 return {role:"button",tabIndex:0,"aria-label":label,onClick:action,onKeyDown:(event:React.KeyboardEvent<SVGGElement>) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); action(); } },style:{cursor:"pointer"}};
}

// Every representation receives the same semantic nodes and focus callback.
// Geometry belongs to the renderer; membership, visibility and paging do not.
function HierarchyStage({refNode,nodes,preset,portrait,destinations,events,onFocus,palette,live,now,selectedAgent,onSelect}:{refNode:React.RefObject<SVGSVGElement|null>;nodes:WorldNode[];preset:Preset;portrait:boolean;destinations:Destination[];events:Event[];selectedAgent:string;onSelect:(id:string)=>void;onFocus:(focus:Focus)=>void;palette:Palette;live:boolean;now:number}) {
 const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
 const columns = portrait ? 2 : 3, cellW = (width-40)/columns, cellH = portrait ? 290 : 240;
 if (preset === "village") return <VillageStage showChrome={false} refNode={refNode} nodes={nodes} destinations={destinations} portrait={portrait} events={events} onFocus={onFocus} selectedAgent={selectedAgent} onSelect={onSelect} onSelectDestination={() => {}} live={live} now={now}/>;
 return <svg ref={refNode} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="group" aria-label={`${presets.find(p => p.id === preset)?.name} · projects and observed teams`} style={{background:palette.subtle,fontFamily:palette.font,color:palette.text}}>
  <rect width={width} height={height} fill={palette.subtle}/>
  {nodes.map((node,index) => {
   const x=20+(index%columns)*cellW, y=45+Math.floor(index/columns)*cellH, w=cellW-16, h=cellH-20;
   const ids=new Set(node.agents.map(agent=>agent.id));
   const activity=events.filter(event=>ids.has(event.agent_id));
   const working=node.agents.filter(agent=>["running","active"].includes(agent.status)).length;
   const detail=`${node.agents.length} agents · ${working} running`;
   const latest=activity[activity.length-1];
   return <g key={node.id} data-world-node={node.kind} data-selected={node.kind === "agent" && selectedAgent === node.id} {...svgAction(node.kind === "agent" ? `Inspect agent ${node.name}` : `Zoom into ${node.kind} ${node.name}`,()=>onFocus(node.focus))}>
    <>

     {preset === "graph" ? <ellipse cx={x+w/2} cy={y+h/2} rx={w*.45} ry={h*.45} fill={palette.card} stroke={palette.border}/> : <rect x={x} y={y} width={w} height={h} rx="7" fill={palette.card} stroke={palette.border}/>}
     <SvgText x={x+w/2} y={y+48} size={12} fill={palette.accent} anchor="middle">{node.kind === "team"?"OBSERVED TEAM":node.kind.toUpperCase()}</SvgText>
     <SvgText x={x+w/2} y={y+83} size={portrait?16:19} anchor="middle" weight={700}>{short(node.name,portrait?18:29)}</SvgText>
     <SvgText x={x+w/2} y={y+110} size={12} fill={palette.muted} anchor="middle">{detail}</SvgText>
     {preset !== "graph" && <SvgText x={x+w/2} y={y+142} size={12} fill={palette.muted} anchor="middle">{short(latest?.label || "No recent activity",portrait?22:38)}</SvgText>}
     <SvgText x={x+w/2} y={y+h-28} size={12} fill={palette.accent} anchor="middle">{node.kind === "agent" ? "View details →" : "Zoom in →"}</SvgText>
    </>
   </g>;
  })}
  {!nodes.length && <SvgText x={width/2} y={height/2} anchor="middle" fill={palette.muted}>No projects available in this installation</SvgText>}
 </svg>;
}
