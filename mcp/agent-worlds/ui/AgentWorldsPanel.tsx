import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { PANEL_STYLES, WORLD_NAV_STYLES } from "./panel-styles";
import SceneViewport from "./scene-viewport";
import { withAppDisplayNames, type AppMetadata } from "./app-display-names";
import { HierarchyStage, OperationsStage, SystemGraph } from "./clean-stages";
import { clock, eventColor, short, statusColor, usePalette } from "./palette";
import PixelStage, { MAX_RESIDENTS } from "./pixel/PixelStage";
import IsoStage from "./pixel/iso/IsoStage";
import { THEMES, themeById } from "./themes";
import { buildWorld, rootFocus, resolveFocus, projectWorld, contentsForAgents, PAGE_SIZE } from "./world-model";
import type { Source, Scene, Focus } from "./world-model";
type Props = { installId: number; projectId?: string };

// Buffer one polling cycle so newly observed calls begin before their walking
// animation reaches the event, rather than jumping forward on every refresh.
const VILLAGE_DELAY_MS = 3500;
const API = "/api/apps/agent-worlds/api";

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

export default function AgentWorldsPanel({ installId, projectId = "" }: Props) {
  const palette = usePalette();
  const [sourceId, setSourceId] = useState("main");
  const [themeId, setThemeId] = useState("village");
  const theme = themeById(themeId);
  const isMap = theme.layout === "map";
  const [mapSize, setMapSize] = useState({ width: 1280, height: 760 });
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
  const stageRef = useRef<HTMLCanvasElement | SVGSVGElement | null>(null);
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
  // Map themes animate on a shared clock; other themes are static between polls.
  useEffect(() => {
    if (!live || !isMap) return;
    setAnimationNow(Date.now() - VILLAGE_DELAY_MS);
    const motion = typeof matchMedia === "function" ? matchMedia("(prefers-reduced-motion: reduce)") : null;
    const timer = window.setInterval(() => {
      if (document.visibilityState !== "hidden" && !motion?.matches) setAnimationNow(Date.now() - VILLAGE_DELAY_MS);
    }, 80);
    return () => window.clearInterval(timer);
  }, [live, isMap]);

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
  // Maps show the whole place at once; graph and board views page through it.
  const totalItems = projection?.nodes.length || visibleAgents.length;
  const pages = isMap ? 1 : Math.max(1,Math.ceil(totalItems/PAGE_SIZE));
  const currentPage = Math.min(page,pages-1);
  const stageAgents = isMap ? visibleAgents.slice(0, MAX_RESIDENTS) : visibleAgents.slice(currentPage*PAGE_SIZE,(currentPage+1)*PAGE_SIZE);
  const teams = useMemo(() => new Map((world?.teams || []).flatMap(team => team.agentIds.map(id => [id, team.id] as const))), [world]);
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

  // Canvas themes export directly; SVG themes are rasterised through an image.
  const serializeStage = useCallback(async (): Promise<CanvasImageSource> => {
    if (!stageRef.current) throw new Error("Scene is not ready");
    if (stageRef.current instanceof HTMLCanvasElement) return stageRef.current;
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
      canvas.height = portrait ? 1920 : isMap ? Math.round(1600 * mapSize.height / mapSize.width) : 700;
      const context = canvas.getContext("2d")!;
      context.fillStyle = palette.bg;
      context.fillRect(0, 0, canvas.width, canvas.height);
      context.drawImage(image, 0, 0, canvas.width, canvas.height);
      const a = document.createElement("a");
      a.download = `agent-worlds-${theme.id}.png`;
      a.href = canvas.toDataURL("image/png");
      a.click();
    } catch (caught) { setError((caught as Error).message); }
  }, [portrait, theme.id, isMap, mapSize, serializeStage, palette.bg]);

  const recordClip = useCallback(async () => {
    setError("");
    if (!HTMLCanvasElement.prototype.captureStream || !window.MediaRecorder) { setError("Video recording is not supported in this browser"); return; }
    setRecording(true);
    try {
      const canvas = document.createElement("canvas");
      canvas.width = portrait ? 720 : 1280;
      canvas.height = portrait ? 1280 : isMap ? Math.round(1280 * mapSize.height / mapSize.width) : 560;
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
      a.download = `agent-worlds-${theme.id}.webm`;
      a.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 60000);
    } catch (caught) { setError((caught as Error).message); }
    finally { setRecording(false); }
  }, [portrait, theme.id, isMap, mapSize, serializeStage, palette.bg]);

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
        <select className="aw-visualization" aria-label="Visualization" value={theme.id} onChange={event => setThemeId(event.target.value)}>{THEMES.map(item => <option key={item.id} value={item.id} title={item.detail}>{item.name}</option>)}</select>
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
        <div className={`aw-content ${portrait ? "is-portrait" : ""} ${isMap ? "is-map" : ""}`}>
          <section className="aw-scene" aria-label={theme.name}>
            <SceneViewport hostRef={canvasHost} stageRef={stageRef} width={isMap ? mapSize.width : portrait ? 540 : 1280} height={isMap ? mapSize.height : portrait ? 960 : 560} sceneKey={JSON.stringify([installId,projectId,sourceId,currentFocus,currentPage,isMap])} status={`${visibleAgents.length} agents · ${runningCount} running`}>
              {camera => !scene ? <div className="aw-loading">Connecting to the scene…</div>
                : theme.iso ? <IsoStage stageRef={stageRef} camera={camera} agents={stageAgents} nodes={projection?.nodes || []} destinations={destinations} events={visibleEvents} teams={teams} portrait={portrait} now={animationNow} live={live} selectedAgent={selectedAgent} selectedDestination={selectedDestination} onSelect={setSelectedAgent} onSelectDestination={inspectDestination} onFocus={navigate} onWorldSize={setMapSize} />
                : theme.pixel ? <PixelStage theme={theme.pixel} stageRef={stageRef} camera={camera} agents={stageAgents} nodes={projection?.nodes || []} destinations={destinations} events={visibleEvents} teams={teams} portrait={portrait} now={animationNow} live={live} selectedAgent={selectedAgent} selectedDestination={selectedDestination} onSelect={setSelectedAgent} onSelectDestination={inspectDestination} onFocus={navigate} onWorldSize={setMapSize} />
                : projection?.nodes.length ? <HierarchyStage refNode={stageRef} nodes={projection.nodes.slice(currentPage*PAGE_SIZE,(currentPage+1)*PAGE_SIZE)} preset={theme.layout === "graph" ? "graph" : "operations"} label={theme.name} portrait={portrait} destinations={destinations} events={visibleEvents} selectedAgent={selectedAgent} onSelect={setSelectedAgent} onFocus={navigate} palette={palette} />
                : theme.layout === "graph" ? <SystemGraph refNode={stageRef} source={scene.source} agents={stageAgents} destinations={destinations} selectedDestination={selectedDestination} events={stageEvents} portrait={portrait} selectedAgent={selectedAgent} onSelect={setSelectedAgent} onSelectDestination={inspectDestination} palette={palette} />
                : <OperationsStage refNode={stageRef} agents={stageAgents} events={stageEvents} portrait={portrait} selectedAgent={selectedAgent} onSelect={setSelectedAgent} palette={palette} />}
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
