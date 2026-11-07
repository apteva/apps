import { featuredDestinations, type Agent, type Destination, type Event, type Focus, type Source, type WorldNode } from "./world-model";
import { clock, latestByAgent, short, statusColor, type Palette } from "./palette";

type SvgRef = React.MutableRefObject<HTMLCanvasElement | SVGSVGElement | null>;

function SvgText({ x, y, children, size = 14, fill = "currentColor", anchor = "start", weight = 400 }: { x: number; y: number; children: React.ReactNode; size?: number; fill?: string; anchor?: "start" | "middle" | "end"; weight?: number }) { return <text x={x} y={y} fill={fill} fontSize={size} fontWeight={weight} textAnchor={anchor}>{children}</text>; }

export function SystemGraph({ refNode, source, agents, destinations, selectedDestination, selectedAgent, events, portrait, onSelect, onSelectDestination, palette }: { refNode: SvgRef; source: Source; agents: Agent[]; destinations: Destination[]; selectedDestination: string; selectedAgent: string; events: Event[]; portrait: boolean; onSelect: (id: string) => void; onSelectDestination: (id: string) => void; palette: Palette }) {
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
  return <svg ref={node => { refNode.current = node; }} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="group" aria-label="Agent and destination graph" style={{ color: palette.text, background: palette.subtle, fontFamily: palette.font }}>
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

export function OperationsStage({ refNode, agents, selectedAgent, events, portrait, onSelect, palette }: { refNode: SvgRef; agents: Agent[]; selectedAgent: string; events: Event[]; portrait: boolean; onSelect: (id:string) => void; palette: Palette }) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
  const latest = latestByAgent(events), columns = portrait ? 1 : 2, cardWidth = portrait ? 480 : 592, cardHeight = portrait ? 104 : 96;
  return <svg ref={node => { refNode.current = node; }} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="group" aria-label="Agent operations board" style={{ color: palette.text, background: palette.subtle, fontFamily: palette.font }}><rect width={width} height={height} fill={palette.subtle} /><SvgText x={30} y={48} size={22} weight={700}>Operations</SvgText><SvgText x={30} y={72} size={11} fill={palette.dim}>{agents.length} AGENTS · CURRENT STATUS</SvgText><line x1={30} y1={88} x2={width-30} y2={88} stroke={palette.border} />{agents.slice(0, portrait ? 7 : 8).map((agent, index) => { const x = 30 + (index % columns) * (cardWidth + 36), y = 110 + Math.floor(index / columns) * (cardHeight + 16), item = latest.get(agent.id); return <g key={agent.id} data-selected={selectedAgent===agent.id} aria-pressed={selectedAgent===agent.id} {...svgAction(`Inspect agent ${agent.name}`, () => onSelect(agent.id))}><rect x={x} y={y} width={cardWidth} height={cardHeight} rx="7" fill={palette.card} stroke={palette.border} /><rect x={x} y={y} width="4" height={cardHeight} fill={statusColor(agent.status, palette)} /><SvgText x={x + 20} y={y + 27} size={15} weight={700}>{short(agent.name, 25)}</SvgText><circle cx={x + cardWidth - 80} cy={y + 22} r="4" fill={statusColor(agent.status, palette)} /><SvgText x={x + cardWidth - 17} y={y + 26} size={10} fill={statusColor(agent.status, palette)} anchor="end">{agent.status.toUpperCase()}</SvgText><line x1={x + 20} y1={y + 40} x2={x + cardWidth - 20} y2={y + 40} stroke={palette.border} /><SvgText x={x + 20} y={y + 64} size={12} fill={palette.muted}>{short(item?.label || "No recent activity", 47)}</SvgText><SvgText x={x + 20} y={y + 86} size={10} fill={palette.dim}>{item ? `${clock(item.time)} · ${item.thread_id || "main"}` : agent.model || "Waiting for events"}</SvgText></g>; })}{!agents.length && <SvgText x={width / 2} y={height / 2} size={15} fill={palette.dim} anchor="middle">No agents in this source yet</SvgText>}</svg>;
}

function svgAction(label: string, action: () => void) {
 return {role:"button",tabIndex:0,"aria-label":label,onClick:action,onKeyDown:(event:React.KeyboardEvent<SVGGElement>) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); action(); } },style:{cursor:"pointer"}};
}

// Every representation receives the same semantic nodes and focus callback.
// Geometry belongs to the renderer; membership, visibility and paging do not.
export function HierarchyStage({refNode,nodes,preset,label,portrait,destinations,events,onFocus,palette,selectedAgent,onSelect}:{refNode:SvgRef;nodes:WorldNode[];preset:"graph"|"operations";label:string;portrait:boolean;destinations:Destination[];events:Event[];selectedAgent:string;onSelect:(id:string)=>void;onFocus:(focus:Focus)=>void;palette:Palette}) {
 const width = portrait ? 540 : 1280, height = portrait ? 960 : 560;
 const columns = portrait ? 2 : 3, cellW = (width-40)/columns, cellH = portrait ? 290 : 240;
 return <svg ref={node => { refNode.current = node; }} viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="group" aria-label={`${label} · projects and observed teams`} style={{background:palette.subtle,fontFamily:palette.font,color:palette.text}}>
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
