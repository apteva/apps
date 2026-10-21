import { memo, useEffect, useMemo, useRef, useState } from "react";
import { agentTrack, sampleRoute, seedFor, villageStreets } from "./village-motion";
import { contentsForNode, villageDestinations } from "./world-model";
import type { Agent, Destination, Event, Focus, WorldNode } from "./world-model";

type Point = { x: number; y: number };
type Site = Point & { id: string; name: string; variant: number; scale: number; selected: boolean; node?: WorldNode; destination?: Destination };
type Props = {
  refNode: React.RefObject<SVGSVGElement | null>;
  agents?: Agent[]; nodes?: WorldNode[]; destinations?: Destination[];
  selectedAgent?: string; selectedDestination?: string; events: Event[]; portrait: boolean;
  now: number; live: boolean;
  onSelect: (id: string) => void; onSelectDestination: (id: string) => void;
  onFocus?: (focus: Focus) => void;
  showChrome?: boolean; overview?: boolean; frame?: { x: number; y: number; width: number; height: number };
};
let nextScene = 0;
const hash = seedFor;
const noise = (i: number, salt = 0) => { const n = Math.sin(i * 127.1 + salt * 311.7) * 43758.5453; return n - Math.floor(n); };
const snap = (n: number) => Math.round(n / 2) * 2;
const trim = (value: string, n: number) => value.length > n ? value.slice(0, n - 1) + "…" : value;
const status = (value: string) => ["running", "active"].includes(value) ? "#609b70" : ["error", "failed"].includes(value) ? "#c66655" : "#a69a6d";
function action(label: string, callback: () => void) {
  return { role: "button", tabIndex: 0, "aria-label": label, onClick: callback, onKeyDown: (event: React.KeyboardEvent<SVGGElement>) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); callback(); } }, style: { cursor: "pointer" } };
}
function Text({ x, y, children, size = 14, fill = "#334d46", anchor = "middle" }: { x: number; y: number; children: React.ReactNode; size?: number; fill?: string; anchor?: "middle" | "start" | "end" }) {
  return <text x={x} y={y} textAnchor={anchor} fontSize={size} fill={fill}>{children}</text>;
}
function useReducedMotion() {
  const [reduced, setReduced] = useState(() => typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches);
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const query = matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(query.matches);
    query.addEventListener("change", update); return () => query.removeEventListener("change", update);
  }, []);
  return reduced;
}

// Compile palette-indexed pixel art into one path per color. No image assets,
// filters or perspective transforms; every tile uses the same square pixel grid.
function pixelPaths(rows: string[], unit: number, offsetY = 0) {
  const colors = new Map<string, string>();
  rows.forEach((row, r) => {
    for (let c = 0; c < row.length;) {
      const color = row[c]; let end = c + 1;
      while (end < row.length && row[end] === color) end++;
      if (color !== ".") colors.set(color, (colors.get(color) || "") + `M${c * unit} ${r * unit + offsetY}h${(end - c) * unit}v${unit}h-${(end - c) * unit}Z`);
      c = end;
    }
  });
  return [...colors];
}
const TREE = pixelPaths([
  ".....dddd.....", "...ddmmmmd....", "..dmmllllmmd..", ".dmlllllmlmmd.",
  ".dmllmlmmmmlmd", "dmmlllmlmmmlmd", "dmllmmmmmmlmmd", "dmmmmllmmmmmd.",
  ".dmmmmlmmmmmdd", "..ddmmmmmmddd.", "...ddddddddd..", ".....trt......", ".....trt......",
], 5);
const Tree = memo(function Tree({ x, y, autumn = false, small = false }: Point & { autumn?: boolean; small?: boolean }) {
  const palette: Record<string, string> = autumn ? { d: "#63764d", m: "#b09b57", l: "#dfbd70", t: "#6c6246", r: "#a18c5b" } : { d: "#3f704f", m: "#63925b", l: "#93b96c", t: "#6c6246", r: "#a18c5b" };
  return <g transform={`translate(${x} ${y})${small ? " scale(.8)" : ""}`}><rect x="-24" y="-5" width="52" height="14" fill="#527e4c"/>
    <g transform="translate(-35 -65)">{TREE.map(([color, d]) => <path key={color} d={d} fill={palette[color]}/>)}</g>
  </g>;
});
function Flower({ x, y, color = "#d899a3" }: Point & { color?: string }) {
  return <g transform={`translate(${x} ${y})`}><path d="M0 4V-3M0 2H-4" stroke="#4f7f4d" strokeWidth="2"/><path d="M-2-8H2V-6H5V-2H2V0H-2V-2H-5V-6H-2Z" fill={color}/><rect x="-1" y="-5" width="2" height="2" fill="#f4da8e"/></g>;
}
const Terrain = memo(function Terrain({ width: w, height: h, portrait, id }: { width: number; height: number; portrait: boolean; id: string }) {
  const waterY = h - 80;
  return <g aria-hidden="true" pointerEvents="none">
    <defs>
      <pattern id={`${id}-grass`} width="32" height="32" patternUnits="userSpaceOnUse"><rect width="32" height="32" fill="#91ae70"/><path d="M5 6h4v2H5ZM24 23h4v2h-4ZM14 17h2v4h-2Z" fill="#86a566"/><path d="M18 4h4v2h-4ZM5 26h3v2H5Z" fill="#a2ba7a"/></pattern>
      <pattern id={`${id}-path`} width="32" height="24" patternUnits="userSpaceOnUse"><rect width="32" height="24" fill="#d5c398"/><path d="M2 2h12v8H2ZM17 2h13v8H17ZM-5 14H6v8H-5ZM9 14h13v8H9ZM25 14h10v8H25Z" fill="#e1d0a5"/><path d="M2 10h12M17 10h13M9 22h13" stroke="#bdac85" strokeWidth="2"/></pattern>
      <pattern id={`${id}-floor`} width="32" height="16" patternUnits="userSpaceOnUse"><rect width="32" height="16" fill="#d1ad79"/><path d="M0 14H32M16 0V14" stroke="#b48f63" strokeWidth="2"/><path d="M2 2H13M19 2H30" stroke="#e8c594" strokeWidth="2"/></pattern>
    </defs>
    <rect width={w} height={h} fill={`url(#${id}-grass)`}/>
    <path d={`M${w / 2 - (portrait ? 24 : 64)} 60H${w / 2 + (portrait ? 24 : 64)}V${h}H${w / 2 - (portrait ? 24 : 64)}Z`} fill={`url(#${id}-path)`}/>
    {(portrait ? [423, 690] : [374, 636]).map(y => <g key={y}><rect x="28" y={y} width={w - 56} height={portrait ? 44 : 54} fill="#809e62"/><rect x="28" y={y + 4} width={w - 56} height={portrait ? 36 : 46} fill={`url(#${id}-path)`}/></g>)}
    <rect y={waterY - 8} width={w} height="12" fill="#557f55"/><rect y={waterY} width={w} height="8" fill="#c8c49a"/><rect y={waterY + 8} width={w} height="80" fill="#6babb4"/>
    <g className="aw-v-water">{Array.from({ length: portrait ? 16 : 36 }, (_, i) => <path key={i} d={`M${snap(noise(i, 2) * w)} ${waterY + 18 + Math.floor(noise(i, 3) * 45 / 4) * 4}h${8 + (i % 4) * 8}`} stroke={i % 3 ? "#91c4c6" : "#b9d6cf"} strokeWidth="2"/>)}</g>
    <g transform={`translate(${w / 2} ${waterY - 16})`}><rect x="-44" width="88" height="100" fill="#746f51"/><rect x="-36" width="72" height="100" fill="#b19a6c"/>{Array.from({ length: 12 }, (_, i) => <g key={i}><path d={`M-36 ${i * 9}H36`} stroke="#786e50" strokeWidth="2"/><path d={`M-34 ${i * 9 + 2}H34`} stroke="#d3b987" strokeWidth="2"/></g>)}<path d="M-44 0V100M44 0V100" stroke="#665f46" strokeWidth="8"/>{[0, 40, 80].map(y => <g key={y}><rect x="-50" y={y} width="12" height="12" fill="#8f815b"/><rect x="38" y={y} width="12" height="12" fill="#8f815b"/></g>)}</g>
    {Array.from({ length: Math.ceil(w / 60) }, (_, i) => <Tree key={`north${i}`} x={i * 60 + 24} y={78 + (i % 2) * 12} small/>)}
    {[160, 280, 506, 630].map((y, i) => <g key={`edge${i}`}><Tree x={15} y={y} autumn={i === 1}/><Tree x={w - 15} y={y + 28} autumn={i === 3}/></g>)}
    {Array.from({ length: portrait ? 18 : 32 }, (_, i) => {
      const left = i % 2 === 0, x = left ? 52 + noise(i, 7) * 45 : w - 52 - noise(i, 7) * 45;
      return <Flower key={i} x={snap(x)} y={112 + Math.floor(noise(i, 8) * (waterY - 150) / 4) * 4} color={["#e5c575", "#dba3af", "#c3b1d0", "#e4e2bf"][i % 4]}/>;
    })}
    {(portrait ? [250, 555] : [250, 510]).map((y, i) => <g key={`fence${i}`} transform={`translate(${portrait ? 40 : 100} ${y})`}><rect width="32" height="5" fill="#b6a075"/><rect y="12" width="32" height="5" fill="#b6a075"/><path d="M2-4V24M30-4V24" stroke="#877956" strokeWidth="6"/></g>)}
  </g>;
});

const ROOM_COLORS = [
  { wall: "#d2c8a3", roof: "#ae6f65", rug: "#7e9eaa" },
  { wall: "#d9c9a7", roof: "#6b929b", rug: "#b9906e" },
  { wall: "#c7cfab", roof: "#9d83a0", rug: "#92a894" },
  { wall: "#dac4ac", roof: "#879c75", rug: "#b58e9b" },
];
function Plant({ x, y, flower = false }: Point & { flower?: boolean }) {
  return <g transform={`translate(${x} ${y})`}><rect x="-8" y="-2" width="16" height="12" fill="#b47c5c"/><rect x="-10" y="-4" width="20" height="5" fill="#cc9970"/><path d="M-4-4V-16H-10V-22H-2V-14H2V-26H8V-18H14V-12H6V-4Z" fill="#5e8c54"/><path d="M2-22H6V-16H2M-8-20H-4V-14H-8" stroke="#9cbb73" strokeWidth="2"/>{flower && <Flower x={0} y={-17}/>}</g>;
}
function Shelf({ x, y }: Point) {
  const colors = ["#7f9ca7", "#bc7e72", "#d4b475", "#8ca373", "#a18da6"];
  return <g transform={`translate(${x} ${y})`}><rect width="66" height="32" fill="#806748"/><rect x="3" y="3" width="60" height="22" fill="#5c5944"/>{Array.from({ length: 12 }, (_, i) => <g key={i}><rect x={5 + i * 5} y={6 + i % 3} width="4" height={15 - i % 3} fill={colors[i % 5]}/><rect x={6 + i * 5} y="10" width="2" height="2" fill="#e6d4a7"/></g>)}<rect y="25" width="66" height="5" fill="#c3a073"/></g>;
}
function Desk({ x, y, tools = false }: Point & { tools?: boolean }) {
  return <g transform={`translate(${x} ${y})`}><rect x="-30" y="-19" width="60" height="40" fill="#846b4d"/><rect x="-30" y="-23" width="60" height="36" fill="#c6a071"/><path d="M-27-20H27M-27 9H27" stroke="#e4bc87" strokeWidth="2"/>
    {tools ? <g><path d="M-13-14v19M-17-12h10M1 3l14-16" stroke="#677e82" strokeWidth="4"/><rect x="10" y="-16" width="12" height="7" fill="#82979b"/></g> : <g><rect x="-19" y="-14" width="31" height="19" fill="#e7d6ab"/><path d="M-17-12H-6V1H-17ZM-3-12H9V1H-3Z" fill="#f7e9c5"/><path d="M-5-13V4M-15-8h6M0-7h6" stroke="#b5a98a" strokeWidth="2"/><rect x="17" y="-11" width="7" height="7" fill="#657e91"/></g>}
    <rect x="-11" y="26" width="22" height="23" fill="#987854"/><rect x="-8" y="28" width="16" height="13" fill="#aeb39a"/><rect x="-12" y="44" width="24" height="5" fill="#b2956b"/>
  </g>;
}
function Bed({ x, y }: Point) {
  return <g transform={`translate(${x} ${y})`}><rect x="-20" y="-42" width="40" height="79" fill="#977b56"/><rect x="-17" y="-34" width="34" height="65" fill="#c7a0aa"/><rect x="-14" y="-31" width="28" height="17" fill="#eee2bc"/><rect x="-15" y="-10" width="30" height="37" fill="#ad8eaa"/><path d="M-14-2H14M-14 8H14M-14 18H14M-4-8V26M6-8V26" stroke="#d6b7c1" strokeWidth="2"/><rect x="-21" y="-43" width="42" height="8" fill="#bea075"/></g>;
}
const Building = memo(function Building({ x, y, scale, variant, selected, id }: Point & { scale: number; variant: number; selected: boolean; id: string }) {
  const theme = ROOM_COLORS[variant % 4];
  return <g transform={`translate(${x} ${y}) scale(${scale})`} className="aw-v-building" data-cutaway="open">
    <rect x="-114" y="-160" width="234" height="188" fill="#587a52"/>
    {selected && <rect x="-119" y="-184" width="238" height="215" fill="none" stroke="#f1d28d" strokeWidth="4"/>}
    <rect x="-112" y="-178" width="224" height="188" fill="#6d745c"/>
    <rect x="-105" y="-160" width="210" height="166" fill={theme.wall}/>
    <rect x="-96" y="-144" width="192" height="144" fill={`url(#${id}-floor)`}/>
    <rect x="-112" y="-178" width="224" height="12" fill={theme.roof}/><rect x="-112" y="-178" width="224" height="3" fill="#dfc6a1"/>
    {Array.from({ length: 19 }, (_, i) => <rect key={i} x={-108 + i * 12} y="-173" width="7" height="3" fill="#f0d0ae" opacity=".35"/>)}
    <rect x="-105" y="-160" width="210" height="10" fill="#b19b73"/>
    <path d="M-105-144H105M-102-144V-6M102-144V-6" fill="none" stroke="#d9c297" strokeWidth="4"/>
    <rect x="-57" y="-103" width="113" height="79" fill={theme.rug}/><rect x="-51" y="-97" width="101" height="67" fill="none" stroke="#e4d1a7" strokeWidth="3"/>
    <path d="M-9-72H9V-55H-9Z" fill="#e6d1a8" opacity=".6"/>
    <Shelf x={-84} y={-140}/>
    <g transform="translate(30 -158)"><rect width="47" height="22" fill="#7f8b7d"/><rect x="4" y="3" width="39" height="15" fill="#b9d3cf"/><path d="M23 3V18M4 10H43" stroke="#e4d2a8" strokeWidth="3"/></g>
    {variant % 4 === 3 ? <g><Bed x={63} y={-65}/><Desk x={-24} y={-79}/></g> : variant % 4 === 1 ? <g><Desk x={-10} y={-78} tools/><rect x="60" y="-118" width="30" height="49" fill="#7c8b84"/><rect x="64" y="-114" width="22" height="19" fill="#acb2a2"/><rect x="66" y="-88" width="18" height="12" fill="#e2ad79"/><Plant x={73} y={-28}/></g> : <g><Desk x={-9} y={-78}/><Plant x={72} y={-99} flower={variant % 4 === 2}/><Plant x={74} y={-37}/></g>}
    <rect x="-112" y="4" width="84" height="13" fill={theme.wall}/><rect x="28" y="4" width="84" height="13" fill={theme.wall}/>
    <path d="M-112 4H-28M28 4H112" stroke="#e4cd9e" strokeWidth="4"/>
    <path d="M-28 6H28V29H-28Z" fill="#c8bb92"/><path d="M-26 14H26M-26 24H26" stroke="#a39c7c" strokeWidth="3"/>
    <Plant x={-95} y={23} flower/><Plant x={95} y={23}/>
    <rect x="-128" y="-6" width="9" height="30" fill="#867554"/><rect x="-133" y="-13" width="19" height="16" fill="#6c7056"/><rect x="-129" y="-10" width="11" height="10" fill="#e8c985"/>
  </g>;
});

const BODY = [
  "......HHHHH.......", ".....HhhhhhHH.....", "....HhhkkhhhH.....", "....HkSSSSkhH.....",
  "....HSSSSSSkH.....", ".....SSeSSSe......", "......SSSSS.......", "......ccc........",
  "....DDcLLcDD......", "...DDdcLLcddD.....", "...DddcLLcddD.....", "..DDddcLLcdddD....",
  "..DSddcLLcdddSD...", "..SSddcLLcdddSS...", "...DddcLLcdddD....", "...DddccLccddD....",
  "...DdddccdddDD....", "...DddddddddDD....", "....DddDDdddD.....", "....DddDDdddD.....",
  "....Ppp..Ppp......", "....Ppp..Ppp......", "....Ppp..Ppp......", "....Ppp..Ppp......",
  "....BBb..BBb......", "...BBBB..BBBB.....",
];
const TORSO = pixelPaths(BODY.slice(0, 20), 2);
const LEFT_LEG = pixelPaths(BODY.slice(20).map(row => row.slice(0, 8)), 2, 40);
const RIGHT_LEG = pixelPaths(BODY.slice(20).map(row => ".".repeat(8) + row.slice(8)), 2, 40);
const Character = memo(function Character({ x, y, agent, scale = 1, walking = false, step = false, north = false, label = true }: Point & { agent: Agent; scale?: number; walking?: boolean; step?: boolean; north?: boolean; label?: boolean }) {
  const outfit = [["#486276", "#749ba7", "#b5c1ab"], ["#81564c", "#b98064", "#dcad81"], ["#50765c", "#88a777", "#c0c994"], ["#665572", "#9783a4", "#c5a9bc"], ["#827044", "#bea568", "#e3c68e"]][(hash(agent.id) >>> 4) % 5];
  const colors: Record<string, string> = { H: "#37423c", h: "#695b47", k: "#9c8967", S: "#dfba95", e: "#394940", D: outfit[0], d: outfit[1], L: outfit[2], c: "#e6c998", P: "#354a54", p: "#556777", B: "#3a443f", b: "#7b6c50" };
  return <g transform={`translate(${snap(x)} ${snap(y)})`}>
    <rect x={-13 * scale} y="2" width={28 * scale} height="6" fill="#537851"/>
    <g transform={`scale(${scale}) translate(-17 -52)`}>
      <g transform={walking && step ? "translate(0 -2)" : undefined}>{TORSO.map(([color, d]) => <path key={color} d={d} fill={north && ["S", "e"].includes(color) ? colors.h : colors[color]}/>)}</g>
      <g transform={walking && step ? "translate(0 -2)" : undefined}>{LEFT_LEG.map(([color, d]) => <path key={color} d={d} fill={colors[color]}/>)}</g>
      <g transform={walking && !step ? "translate(0 -2)" : undefined}>{RIGHT_LEG.map(([color, d]) => <path key={color} d={d} fill={colors[color]}/>)}</g>
    </g>
    {label && <g pointerEvents="none"><rect x="-76" y={-76 * scale} width="152" height="22" fill="#f0e7cb" stroke="#7e9475" strokeWidth="2"/><rect x="-66" y={-76 * scale + 9} width="5" height="5" fill={status(agent.status)}/><Text x={4} y={-76 * scale + 15} size={12}>{trim(agent.name, 17)}</Text></g>}
  </g>;
});
function Nameplate({ x, y, name, detail, portrait }: Point & { name: string; detail: string; portrait: boolean }) {
  const w = portrait ? 220 : 260;
  return <g transform={`translate(${x} ${y})`} pointerEvents="none" className="aw-v-nameplate"><rect x={-w / 2} width={w} height="39" fill="#f0e7cb" stroke="#7e9475" strokeWidth="2"/><path d={`M${-w / 2 + 3} 3H${w / 2 - 3}`} stroke="#fff2d5" strokeWidth="2"/><Text x={0} y={16} size={portrait ? 13 : 14}>{trim(name, portrait ? 24 : 30)}</Text><Text x={0} y={31} size={10} fill="#6b7c65">{detail}</Text></g>;
}
export function sitesFor(nodes: WorldNode[], destinations: Destination[], portrait: boolean, selected: string): Site[] {
  const items = nodes.length ? nodes : destinations, cols = portrait ? 2 : nodes.length ? 3 : Math.min(items.length > 6 ? 4 : items.length > 4 ? 3 : 2, Math.max(1, items.length));
  return items.map((item, i) => ({
    id: item.id, name: item.name, variant: (hash(item.id) >>> 7) % 4,
    x: portrait ? 140 + i % cols * 260 : nodes.length || cols === 3 ? 220 + i % cols * 420 : cols === 4 ? 190 + i % cols * 300 : cols === 1 ? 640 : i % cols === 0 ? 300 : 980,
    y: portrait ? (items.length > 6 ? 234 : items.length > 4 && !nodes.length ? 260 : 302) + Math.floor(i / cols) * (items.length > 6 ? 186 : nodes.length || items.length > 4 ? 250 : 278) : nodes.length ? 260 + Math.floor(i / cols) * 284 : 300 + Math.floor(i / cols) * 260,
    scale: portrait ? (nodes.length || items.length > 6 ? .65 : items.length > 4 ? .78 : .86) : nodes.length || cols > 2 ? .82 : 1,
    selected: item.id === selected,
    ...(nodes.length ? { node: item as WorldNode } : { destination: item as Destination }),
  }));
}

export default function VillageStage({ refNode, nodes = [], agents = [], destinations = [], selectedDestination = "", selectedAgent = "", events, portrait, now, live, onSelect, onSelectDestination, onFocus, overview = false, showChrome = true, frame }: Props) {
  const idRef = useRef<string | null>(null);
  if (!idRef.current) idRef.current = `aw-village-${++nextScene}`;
  const id = idRef.current, reducedMotion = useReducedMotion();
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 760;
  const sites = sitesFor(nodes, destinations, portrait, selectedDestination);
  const motionKey = JSON.stringify([agents.map(agent => agent.id), sites.map(site => [site.id, site.x, site.y, site.scale]), (agents.length ? events : []).filter(event => event.kind === "tool").map(event => [event.id, event.agent_id, event.target_id, event.time])]);
  const motion = useMemo(() => {
    const streets = villageStreets(sites, portrait);
    return { streets, tracks: new Map(agents.map(agent => [agent.id, agentTrack(agent.id, events, sites, streets)])) };
  }, [motionKey, portrait]);
  if (nodes.length && nodes.every(node => node.kind === "project")) return <ProjectVillages refNode={refNode} nodes={nodes} destinations={destinations} events={events} portrait={portrait} now={now} live={live} onFocus={onFocus} id={id} reducedMotion={reducedMotion} showChrome={showChrome}/>;
  const entities: { key: string; y: number; element: React.ReactNode }[] = [];
  for (const site of sites) {
    const detail = site.node ? `${site.node.kind === "team" ? "OBSERVED TEAM" : site.node.kind.toUpperCase()} · ${site.node.agents.length} AGENTS` : `${site.destination!.kind.toUpperCase()} · ${site.destination!.call_count} CALLS`;
    const activate = () => site.node?.kind === "agent" ? onSelect(site.node.id) : site.node ? onFocus?.(site.node.focus) : onSelectDestination(site.id);
    entities.push({ key: `site:${site.id}`, y: site.y, element: <g data-world-node={site.node?.kind} data-selected={site.node?.kind === "agent" && selectedAgent === site.node.id} data-destination-id={site.destination?.id} {...(!overview ? action(site.node?.kind === "agent" ? `Inspect agent ${site.name}` : site.node ? `Zoom into ${site.node.kind} ${site.name}` : `Inspect destination ${site.name}`, activate) : {})} className="aw-v-site"><title>{site.name}</title><rect x={site.x - 132} y={site.y - 185 * site.scale} width="264" height={185 * site.scale + 90} fill="transparent"/>{site.node?.kind === "project" && <g pointerEvents="none"><Building x={site.x - 68} y={site.y - 43} scale={site.scale * .52} variant={(site.variant + 1) % 4} selected={false} id={id}/><Building x={site.x + 68} y={site.y - 43} scale={site.scale * .52} variant={(site.variant + 3) % 4} selected={false} id={id}/></g>}<Building {...site} id={id}/><Nameplate x={site.x} y={site.y + (portrait ? 34 : 43)} name={site.name} detail={detail} portrait={portrait}/></g> });
    if (site.node) for (const agent of site.node.agents.slice(0, 4)) {
      const seed = seedFor(agent.id), cycle = 16000 + seed % 7000, phase = ((now + seed * 113) % cycle) / cycle;
      const points = [{x:site.x-46*site.scale,y:site.y-18*site.scale},{x:site.x-46*site.scale,y:site.y-58*site.scale},{x:site.x+22*site.scale,y:site.y-58*site.scale},{x:site.x+22*site.scale,y:site.y-18*site.scale},{x:site.x-46*site.scale,y:site.y-18*site.scale}];
      const resting = phase > .78, position = sampleRoute(points, Math.min(1, phase / .78));
      const walking = live && !reducedMotion && !resting;
      entities.push({ key: `resident:${site.id}:${agent.id}`, y: site.y + 1 + position.y / 10000, element: <g pointerEvents="none"><Character {...position} agent={agent} scale={.66} label={false} walking={walking} step={walking && Math.floor(now / 170 + seed) % 2 === 0}/></g> });
    }
  }
  for (const agent of agents) {
    const history = events.filter(event => event.agent_id === agent.id), latest = history[history.length - 1];
    const position = motion.tracks.get(agent.id)!.at(now), site = sites.find(site => site.id === position.destination);
    const walking = live && !reducedMotion && position.walking;
    entities.push({ key: `agent:${agent.id}`, y: site && !motion.streets.clear(position) ? site.y + 1 + position.y / 10000 : position.y, element: <g data-agent-id={agent.id} aria-pressed={!overview ? selectedAgent === agent.id : undefined} data-visit-destination={position.destination} data-inside={!!position.inside} data-walking={walking} data-x={snap(position.x)} data-y={snap(position.y)} {...(!overview ? action(`Inspect agent ${agent.name}`, () => onSelect(agent.id)) : {})}><title>{`${agent.name} · ${position.destination ? `Visiting ${site?.name}` : latest?.label || agent.status}`}</title><rect x={position.x - 28} y={position.y - 57} width="56" height="68" fill="transparent"/>{selectedAgent === agent.id && !overview && <ellipse cx={position.x} cy={position.y+5} rx="20" ry="7" fill="#ffe08a" stroke="#b2732d" strokeWidth="2"/>}<Character {...position} agent={agent} label={!overview} walking={walking} step={walking && Math.floor(now / 160 + hash(agent.id)) % 2 === 0}/></g> });
  }
  entities.sort((a, b) => a.y - b.y || a.key.localeCompare(b.key));
  return <svg ref={refNode} className="aw-village" data-renderer="procedural-pixel" data-view="top-down" data-motion={live && !reducedMotion} data-paused={!live} data-clock={now} viewBox={`0 0 ${width} ${height}`} x={frame?.x} y={frame?.y} width={frame?.width ?? "100%"} height={frame?.height ?? "100%"} role={overview ? "presentation" : "group"} aria-hidden={overview || undefined} aria-label={nodes.length ? "Pixel village · projects and observed teams" : "Pixel village of live agents"} shapeRendering="crispEdges" style={{ background: "#91ae70", fontFamily: "ui-monospace, SFMono-Regular, monospace", ...(frame ? { width: frame.width, height: frame.height, aspectRatio: "auto" } : {}) }}>
    {!overview && <VillageStyles/>}
    <Terrain width={width} height={height} id={id} portrait={portrait}/>
    <g>{entities.map(entity => <g key={entity.key}>{entity.element}</g>)}</g>
    {!overview && showChrome && <g pointerEvents="none"><rect x="16" y="16" width={width - 32} height="36" fill="#f0e7cb" stroke="#7e9475" strokeWidth="2"/><Text x={30} y={40} anchor="start" size={portrait ? 13 : 15}>{nodes.length ? (nodes.some(node => node.kind === "project") ? "WORLD MAP" : "GUILD QUARTER") : "VILLAGE · OPEN HOUSES"}</Text><Text x={width - 30} y={40} anchor="end" size={portrait ? 11 : 13}>{nodes.length ? `${nodes.length} DISTRICTS` : `${agents.length} AGENTS`} · {live ? "LIVE" : "REPLAY"}</Text></g>}
    {!nodes.length && !agents.length && <Text x={width / 2} y={height * .56} size={16}>The village is waiting for agents</Text>}
    {!overview && showChrome && <g pointerEvents="none"><rect x="16" y={height - 38} width={width - 32} height="25" fill="#f0e7cb" stroke="#7e9475" strokeWidth="2"/><Text x={30} y={height - 20} anchor="start" size={portrait ? 11 : 12}>{trim(events[events.length - 1]?.label || (nodes.length ? "Choose a district to zoom in." : "A little world, with everyone at work."), portrait ? 64 : 130)}</Text></g>}
  </svg>;
}

// A project is a complete village, rendered by the same scene as its close-up.
// The outer map only arranges villages and changes the camera on selection;
// ownership, destinations, event history and the clock stay shared.
function ProjectVillage({ node, destinations, events, now, live, portrait, frame, onFocus }: {
  node: WorldNode; destinations: Destination[]; events: Event[]; now: number; live: boolean;
  portrait: boolean; frame: { x: number; y: number; width: number; height: number };
  onFocus?: (focus: Focus) => void;
}) {
  const ref = useRef<SVGSVGElement | null>(null);
  const content = contentsForNode(node, destinations, events);
  const activity = content.events;
  const buildings = villageDestinations(content.destinations, "", activity);
  const residents = node.agents.slice(0, 12);
  const center = frame.x + frame.width / 2;
  return <g data-world-node="project" data-project-village={node.id} className="aw-project-village"
    {...action(`Zoom into project ${node.name}`, () => onFocus?.(node.focus))}>
    <title>{`${node.name} village · ${node.agents.length} agents · ${buildings.length} app and integration buildings`}</title>
    <rect x={frame.x} y={frame.y} width={frame.width} height={frame.height} fill="transparent"/>
    <g pointerEvents="none">
      <path d={`M${center - 12} ${frame.y + frame.height - 30}h24v44h-24Z`} fill="#d5c398"/>
      <VillageStage refNode={ref} agents={residents} destinations={buildings} events={activity}
        now={now} live={live} portrait={portrait} overview
        frame={{ x: frame.x + 12, y: frame.y + 44, width: frame.width - 24, height: frame.height - 52 }}
        onSelect={() => {}} onSelectDestination={() => {}}/>
      <g className="aw-project-sign"><Nameplate x={center} y={frame.y} name={node.name}
        detail={`${node.agents.length} AGENTS · ${buildings.length} BUILDINGS · ZOOM IN`}
        portrait={frame.width < 300}/></g>
    </g>
  </g>;
}
function ProjectVillages({ refNode, nodes, destinations, events, portrait, now, live, onFocus, id, reducedMotion, showChrome }: {
  refNode: React.RefObject<SVGSVGElement | null>; nodes: WorldNode[]; destinations: Destination[];
  events: Event[]; portrait: boolean; now: number; live: boolean; onFocus?: (focus: Focus) => void;
  id: string; reducedMotion: boolean; showChrome: boolean;
}) {
  const width = portrait ? 540 : 1280, height = portrait ? 960 : 760;
  const columns = portrait ? (nodes.length > 3 ? 2 : 1) : Math.min(3, nodes.length);
  const rows = Math.ceil(nodes.length / columns), cellWidth = (width - 64) / columns, cellHeight = (height - 144) / rows;
  const villagePortrait = !portrait && columns === 3 && rows === 1;
  return <svg ref={refNode} className="aw-village" data-renderer="procedural-pixel" data-view="project-villages"
    data-motion={live && !reducedMotion} data-paused={!live} data-clock={now}
    viewBox={`0 0 ${width} ${height}`} width="100%" height="100%" role="group"
    aria-label="Pixel village · projects and observed teams" shapeRendering="crispEdges"
    style={{ background: "#91ae70", fontFamily: "ui-monospace, SFMono-Regular, monospace" }}>
    <VillageStyles/>
    <Terrain width={width} height={height} portrait={portrait} id={id}/>
    {nodes.map((node, index) => <ProjectVillage key={node.id} node={node} destinations={destinations}
      events={events} portrait={villagePortrait} now={now} live={live} onFocus={onFocus}
      frame={{ x: 32 + index % columns * cellWidth, y: 72 + Math.floor(index / columns) * cellHeight, width: cellWidth, height: cellHeight }}/>)}
    {showChrome && <g pointerEvents="none">
      <rect x="16" y="16" width={width - 32} height="36" fill="#f0e7cb" stroke="#7e9475" strokeWidth="2"/>
      <Text x={30} y={40} anchor="start" size={portrait ? 13 : 15}>WORLD · PROJECT VILLAGES</Text>
      <Text x={width - 30} y={40} anchor="end" size={portrait ? 11 : 13}>{nodes.length} VILLAGES · {live ? "LIVE" : "REPLAY"}</Text>
      <rect x="16" y={height - 38} width={width - 32} height="25" fill="#f0e7cb" stroke="#7e9475" strokeWidth="2"/>
      <Text x={width / 2} y={height - 20} size={portrait ? 11 : 12}>Each village is a project · Select a village to zoom in</Text>
    </g>}
  </svg>;
}

function VillageStyles() { return <style>{`
      .aw-village .aw-project-village{outline:none}.aw-village .aw-project-village:hover .aw-project-sign rect,.aw-village .aw-project-village:focus-visible .aw-project-sign rect{stroke:#b98a50;stroke-width:3}.aw-village .aw-v-site{outline:none}.aw-village .aw-v-site:hover .aw-v-nameplate rect,.aw-village .aw-v-site:focus-visible .aw-v-nameplate rect{stroke:#b98a50;stroke-width:3}
      .aw-village [role=button]:focus-visible>rect:first-of-type{stroke:#d9b76a;stroke-width:3;stroke-dasharray:4 4}
      .aw-village [role=button]:focus{outline:none}.aw-village .aw-v-water{animation:aw-v-water 5s steps(4,end) infinite alternate}.aw-village[data-motion=false] *{animation:none!important}
      @keyframes aw-v-water{to{transform:translateX(4px)}}@media(prefers-reduced-motion:reduce){.aw-village *{animation:none!important}}
    `}</style>; }
