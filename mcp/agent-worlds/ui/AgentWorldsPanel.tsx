import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePolledJSON, useAnimationClock } from "./scene-data";
import { PANEL_STYLES, WORLD_NAV_STYLES } from "./panel-styles";
import SceneViewport from "./scene-viewport";
import { withAppDisplayNames, type AppMetadata } from "./app-display-names";
import { clock, eventColor, short, statusColor, usePalette } from "./palette";
import { MAX_RESIDENTS } from "./pixel/PixelStage";
import WorldStage from "./world-stage";
import AgentPanel, { FloatingPanel } from "./agent-panel/AgentPanel";
import { agentWidgets, type InstalledApp } from "./agent-panel/app-widgets";
import { THEMES, themeById } from "./themes";
import { buildWorld, rootFocus, resolveFocus, projectWorld, contentsForAgents, PAGE_SIZE } from "./world-model";
import type { Source, Scene, Focus } from "./world-model";
type Props = { installId: number; projectId?: string };

export default function AgentWorldsPanel({ installId, projectId = "" }: Props) {
  const palette = usePalette();
  const [sourceId, setSourceId] = useState(() => new URLSearchParams(window.location.search).get("source") || "main");
  // The dashboard widget links here with its own view, so the page opens on it.
  const [themeId, setThemeId] = useState(() => new URLSearchParams(window.location.search).get("view") || "village");
  const theme = themeById(themeId);
  const isMap = theme.layout === "map";
  const [mapSize, setMapSize] = useState({ width: 1280, height: 760 });
  const [showInspector, setShowInspector] = useState(() => { const query = new URLSearchParams(window.location.search); return query.has("agent") || query.has("place"); });
  const [focus, setFocus] = useState<Focus>({});
  const [page, setPage] = useState(0);
  // The dashboard widget links here with ?agent= or ?place= to open that panel.
  const [selectedAgent, setSelectedAgentId] = useState(() => new URLSearchParams(window.location.search).get("agent") || "");
  const [selectedDestination, setSelectedDestination] = useState(() => new URLSearchParams(window.location.search).get("place") || "");
  const [live, setLive] = useState(true);
  const [replayIndex, setReplayIndex] = useState(0);
  const [portraitChoice, setPortrait] = useState(false);
  const [narrow, setNarrow] = useState(false);
  const canvasHost = useRef<HTMLDivElement|null>(null);
  const portrait = portraitChoice || narrow;
  const [frozenScene, setFrozenScene] = useState<Scene|null>(null);
  const [exportError, setError] = useState("");
  const [recording, setRecording] = useState(false);
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
  // Changing source or project starts over; the first render keeps a selection from the address.
  const scopeKey = JSON.stringify([sourceId, installId, projectId]), lastScope = useRef(scopeKey);
  useEffect(() => {
    if (lastScope.current === scopeKey) return;
    lastScope.current = scopeKey;
    setFocus({}); setPage(0); setLive(true); setFrozenScene(null); setSelectedDestination(""); setSelectedAgentId(""); setShowInspector(false);
  }, [scopeKey]);
  useEffect(() => {
    if (!rawScene) return;
    setReplayIndex(rawScene.events.length);
  }, [rawScene]);
  const [animationNow, setAnimationNow] = useAnimationClock(live && isMap);

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
  useEffect(() => { if (scene && selectedAgent && !scene.agents.some(agent => agent.id === selectedAgent)) closeDetails(); }, [scene, selectedAgent]);
  const zoomOut = () => {
    if (currentFocus.agentId) navigate({projectId:currentFocus.projectId,teamId:currentFocus.teamId});
    else if (currentFocus.teamId) navigate({projectId:currentFocus.projectId});
    else if (scene?.scope === "instance") navigate({});
  };
  useEffect(() => {
    // App widgets in the panel have their own text fields; Escape there is theirs.
    const editing = (target: EventTarget | null) => target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "SELECT", "TEXTAREA"].includes(target.tagName));
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape" && !editing(event.target)) { if (showInspector) closeDetails(); else zoomOut(); } };
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
  const inspectorDestinations = destinations, inspectorEvents = visibleEvents;
  const destination = destinations.find(item => item.id === selectedDestination);
  // Other apps' agent widgets, saved layouts and widget modules are scoped to
  // the agent's project; instance-wide scenes fall back to the agent's own.
  const panelProject = projectId || inspectedAgent?.project_id || destination?.project_id || "";
  const appWidgets = useMemo(() => agentWidgets((appData.data || []) as InstalledApp[], panelProject), [appData.data, panelProject]);
  const stepSelection = (direction: -1 | 1) => {
    const list = inspectedAgent ? visibleAgents.map(agent => agent.id) : destinations.map(item => item.id), current = inspectedAgent ? selectedAgent : selectedDestination;
    if (list.length < 2) return;
    const next = list[(list.indexOf(current) + direction + list.length) % list.length];
    if (inspectedAgent) setSelectedAgent(next); else inspectDestination(next);
  };
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
                : <WorldStage theme={theme} scene={scene} camera={camera} palette={palette} stageRef={stageRef} agents={stageAgents} nodes={projection?.nodes || []} destinations={destinations} events={visibleEvents} stageEvents={stageEvents} teams={teams} portrait={portrait} now={animationNow} live={live} page={currentPage} selectedAgent={selectedAgent} selectedDestination={selectedDestination} onSelect={setSelectedAgent} onSelectDestination={inspectDestination} onFocus={navigate} onWorldSize={setMapSize} />}
            </SceneViewport>
            {pages > 1 && <div className="aw-pages" aria-label="Scene pages"><button className="aw-button" disabled={currentPage === 0} onClick={() => setPage(currentPage-1)}>Previous</button><span>{currentPage+1} / {pages}</span><button className="aw-button" disabled={currentPage+1 === pages} onClick={() => setPage(currentPage+1)}>Next</button></div>}
          </section>
          {showInspector && (inspectedAgent || destination) && <AgentPanel kind={inspectedAgent ? "agent" : "place"} agent={inspectedAgent} place={inspectedAgent ? undefined : destination} palette={palette}
            context={{agents: visibleAgents, destinations, events: visibleEvents, projectId: currentFocus.projectId, projectName: inspectedAgent ? world?.projects.find(project => project.id === world.projectOf(inspectedAgent))?.name : undefined, palette, onSelectAgent: setSelectedAgent, onSelectPlace: inspectDestination}}
            projectId={panelProject} widgets={appWidgets} appBlocks={scene?.source.kind === "main"} onClose={closeDetails} onStep={stepSelection} />}
          {showInspector && !inspectedAgent && !destination && <FloatingPanel title="World details" palette={palette} label="World details" onClose={closeDetails}>
            <section className="aw-side-destinations"><div className="aw-side-head"><h3>Apps &amp; integrations</h3><span>{inspectorDestinations.length}</span></div><div className="aw-destination-list">{inspectorDestinations.map(item => <button key={item.id} className={`aw-destination-row ${selectedDestination === item.id ? "is-selected" : ""}`} onClick={() => inspectDestination(selectedDestination === item.id ? "" : item.id)}><span className={`aw-destination-icon is-${item.kind}`}>{item.kind === "app" ? "▣" : item.kind === "integration" ? "⌁" : "◇"}</span><span><strong>{item.name}</strong><small>{scene?.scope === "instance" && !currentFocus.projectId ? `${world?.projects.find(project => project.id === item.project_id)?.name || item.project_id || "Source"} · ` : ""}{item.kind} · {item.status}</small></span><em>{item.call_count}</em></button>)}{!inspectorDestinations.length && <div className="aw-plain-empty">No apps or integrations available for this source.</div>}</div></section>
            <section className="aw-side-activity"><div className="aw-side-head"><h3>Activity</h3><span>{live ? "Live feed" : "Replay"}</span></div><div className="aw-activity-list">{inspectorEvents.slice(-10).reverse().map(event => <div key={event.id} className="aw-event"><span className="aw-event-mark" style={{ background: eventColor(event.kind, palette) }} /><div><div className="aw-event-line"><b>{scene?.agents.find(agent => agent.id === event.agent_id)?.name || event.agent_id}</b> {event.label}</div><div className="aw-event-meta">{clock(event.time)}{event.thread_id ? ` · ${short(event.thread_id, 18)}` : ""}</div></div></div>)}{!inspectorEvents.length && <div className="aw-plain-empty">Activity appears as agents work.</div>}</div></section>
            <section className="aw-side-agents"><div className="aw-side-head"><h3>Agents</h3><span>{visibleAgents.length}</span></div><div className="aw-agent-list">{visibleAgents.map(agent => <button key={agent.id} onClick={() => setSelectedAgent(agent.id)} className={`aw-agent-row ${selectedAgent === agent.id ? "is-selected" : ""}`}><span className="aw-agent-avatar">{agent.name.slice(0,1).toUpperCase()}</span><span className="aw-agent-name"><strong>{agent.name}</strong><small>{agent.model || agent.main_thread || agent.status}</small></span><span className="aw-agent-status" style={{ background: statusColor(agent.status, palette) }} title={agent.status} /></button>)}{!visibleAgents.length && <div className="aw-plain-empty">No agents in this source</div>}</div></section>
          </FloatingPanel>}
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
