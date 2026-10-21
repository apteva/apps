import type { Event } from "./world-model";

export type Point = { x: number; y: number };
export type House = Point & { id: string; scale: number };
type Pose = Point & { walking: boolean; north: boolean; destination?: string; inside?: boolean };
type Leg = { from: Point; to: Point; start: number; end: number };
type Visit = { start: number; entered: number; leaving: number; end: number; house: House; inbound: Point[]; outbound: Point[] };
export const seedFor = (value: string) => [...value].reduce((seed, char) => (Math.imul(seed, 31) + char.charCodeAt(0)) >>> 0, 7);
const distance = (a: Point, b: Point) => Math.abs(a.x - b.x) + Math.abs(a.y - b.y);
const length = (points: Point[]) => points.slice(1).reduce((sum, point, i) => sum + distance(points[i], point), 0);
const fraction = (seed: number, index: number) => { let n = Math.imul(seed ^ Math.imul(index + 1, 374761393), 668265263); n = Math.imul(n ^ n >>> 13, 1274126177); return ((n ^ n >>> 16) >>> 0) / 4294967296; };
export function sampleRoute(points: Point[], progress: number): Pose {
 let remaining = Math.max(0, Math.min(1, progress)) * length(points);
 for (let i = 1; i < points.length; i++) {
  const d = distance(points[i - 1], points[i]);
  if (remaining <= d || i === points.length - 1) {
   const t = d ? Math.min(1, remaining / d) : 0;
   return {x: points[i - 1].x + (points[i].x - points[i - 1].x) * t, y: points[i - 1].y + (points[i].y - points[i - 1].y) * t, north: points[i].y < points[i - 1].y, walking: progress > 0 && progress < 1 && d > 0};
  }
  remaining -= d;
 }
 return {...points[0], north:false, walking:false};
}

// A small orthogonal street graph. Expanded walls leave room for a character;
// interiors are reachable only by appending the doorway to an outdoor route.
export function villageStreets(houses: House[], portrait: boolean) {
 const width = portrait ? 540 : 1280, bottom = portrait ? 862 : 646;
 const walls = houses.map(h => ({left:h.x - 132 * h.scale, right:h.x + 132 * h.scale, top:h.y - 184 * h.scale, bottom:h.y + 34 * h.scale}));
 const clear = (p: Point) => p.x >= 64 && p.x <= width - 64 && p.y >= 116 && p.y <= bottom && !walls.some(r => p.x > r.left && p.x < r.right && p.y > r.top && p.y < r.bottom);
 const crosses = (a: Point, b: Point) => walls.some(r => a.x === b.x ? a.x > r.left && a.x < r.right && Math.max(a.y,b.y) > r.top && Math.min(a.y,b.y) < r.bottom : a.y > r.top && a.y < r.bottom && Math.max(a.x,b.x) > r.left && Math.min(a.x,b.x) < r.right);
 // Mostly stroll on streets, with a few small garden detours. Each agent picks
 // its own circuit, pace and resting points; array order never assigns a lane.
 const candidates: Point[] = [];
 const xs = portrait ? [82,196,244,296,348,458] : [150,460,550,640,730,820,1130];
 const ys = portrait ? [360,404,454,638,676,716,786,830] : [354,398,450,608,642];
 for (const y of ys) for (const x of xs) if (clear({x,y})) candidates.push({x,y});
 if (!candidates.length) candidates.push({x:width/2,y:bottom});
 const route = (from: Point, to: Point): Point[] => {
  if (distance(from,to) < .001) return [from,to];
  const xs = [...new Set([64,width-64,from.x,to.x,...walls.flatMap(r=>[r.left,r.right])])].sort((a,b)=>a-b);
  const ys = [...new Set([116,bottom,from.y,to.y,...walls.flatMap(r=>[r.top,r.bottom])])].sort((a,b)=>a-b);
  const points: Point[] = [], index = new Map<string,number>();
  for (const x of xs) for (const y of ys) if (clear({x,y})) {index.set(`${x}:${y}`,points.length);points.push({x,y});}
  const start=index.get(`${from.x}:${from.y}`), end=index.get(`${to.x}:${to.y}`);
  if (start === undefined || end === undefined) return [from];
  const costs=points.map(()=>Infinity), previous=points.map(()=>-1), open=new Set(points.map((_,i)=>i));costs[start]=0;
  while(open.size){
   let current=-1;for(const i of open) if(current<0||costs[i]<costs[current]) current=i;
   if(current===end||!Number.isFinite(costs[current])) break;
   open.delete(current); const p=points[current], xi=xs.indexOf(p.x), yi=ys.indexOf(p.y);
   for(const next of [{x:xs[xi-1],y:p.y},{x:xs[xi+1],y:p.y},{x:p.x,y:ys[yi-1]},{x:p.x,y:ys[yi+1]}]){
    const n=index.get(`${next.x}:${next.y}`);if(n===undefined||!open.has(n)||crosses(p,next))continue;
    const cost=costs[current]+distance(p,next);if(cost<costs[n]){costs[n]=cost;previous[n]=current;}
   }
  }
  if(!Number.isFinite(costs[end]))return [from];
  const result:Point[]=[];for(let i=end;i!==-1;i=previous[i])result.unshift(points[i]);
  return result.filter((p,i)=>i===0||i===result.length-1||!((result[i-1].x===p.x&&p.x===result[i+1].x)||(result[i-1].y===p.y&&p.y===result[i+1].y)));
 };
 return {candidates,route,clear};
}

// Clock-based tracks make the same replay instant reproducible and survive
// polling unchanged. Calls interrupt a stroll at its actual position, enter via
// the door, linger at a work spot, then rejoin the stroll without teleporting.
export function agentTrack(agentId: string, events: Event[], houses: House[], streets: ReturnType<typeof villageStreets>) {
 const seed=seedFor(agentId), speed=38+fraction(seed,0)*10, wanderSpeed=20+fraction(seed,1)*7;
 const chosen:Point[]=[];
 for(let i=0;i<5;i++){
  const point=streets.candidates[Math.floor(fraction(seed,i+3)*streets.candidates.length)];
  const offset={x:point.x+Math.round(fraction(seed,i+30)*20-10),y:point.y+Math.round(fraction(seed,i+40)*16-8)};
  chosen.push(streets.clear(offset)?offset:point);
 }
 const legs:Leg[]=[];let duration=0;
 for(let i=0;i<chosen.length;i++){
  const pause=1800+fraction(seed,i+10)*5200;legs.push({from:chosen[i],to:chosen[i],start:duration,end:duration+pause});duration+=pause;
  const path=streets.route(chosen[i],chosen[(i+1)%chosen.length]);
  for(let j=1;j<path.length;j++){const ms=distance(path[j-1],path[j])/wanderSpeed*1000;legs.push({from:path[j-1],to:path[j],start:duration,end:duration+ms});duration+=ms;}
 }
 const stroll = (time:number):Pose => {
  const t=((time+fraction(seed,2)*duration)%duration+duration)%duration;
  const leg=legs.find(l=>t<l.end)||legs[0];
  return {...sampleRoute([leg.from,leg.to],(t-leg.start)/(leg.end-leg.start)),walking:distance(leg.from,leg.to)>0};
 };
 const visits:Visit[]=[];
 const at = (time:number):Pose => {
  const visit=[...visits].reverse().find(v=>v.start<=time);
  if(!visit||time>=visit.end)return stroll(time);
  const inbound=time<visit.entered, dwell=time>=visit.entered&&time<visit.leaving;
  const pose=dwell?{...visit.inbound[visit.inbound.length-1],walking:false,north:true}:sampleRoute(inbound?visit.inbound:visit.outbound,inbound?(time-visit.start)/(visit.entered-visit.start):(time-visit.leaving)/(visit.end-visit.leaving));
  return {...pose,destination:visit.house.id,inside:Math.abs(pose.x-visit.house.x)<86*visit.house.scale&&pose.y<visit.house.y+12*visit.house.scale&&pose.y>visit.house.y-144*visit.house.scale};
 };
 for(const event of [...events].filter(e=>e.agent_id===agentId&&e.kind==="tool"&&houses.some(h=>h.id===e.target_id)).sort((a,b)=>Date.parse(a.time)-Date.parse(b.time)||a.id.localeCompare(b.id))){
  const start=Date.parse(event.time);if(!Number.isFinite(start))continue;
  const house=houses.find(h=>h.id===event.target_id)!;
  // before/call pairs or bursts for the same house share one physical visit.
  const previous=visits.at(-1);
  if(previous?.house.id===house.id&&start<previous.leaving)continue;
  const origin=at(start), door={x:house.x,y:house.y+34*house.scale};
  const work={x:house.x+(-36+fraction(seedFor(agentId+house.id),0)*60)*house.scale,y:house.y-22*house.scale};
  let inbound:Point[];
  if(!streets.clear(origin)&&previous){
   const exit={x:previous.house.x,y:previous.house.y+34*previous.house.scale};
   inbound=[origin,{x:previous.house.x,y:origin.y},exit,...streets.route(exit,door).slice(1)];
  }else inbound=streets.route(origin,door);
  inbound.push({x:house.x,y:house.y-22*house.scale},work);
  const entered=start+length(inbound)/speed*1000, leaving=entered+4200+fraction(seed,21)*2600;
  // The wandering pace is slower than the returning pace, so this converges to
  // a meeting point on the future stroll. No snap at the end of a visit.
  let end=leaving+10000, outbound:Point[]=[];
  for(let i=0;i<28;i++){
   outbound=[work,{x:house.x,y:work.y},door,...streets.route(door,stroll(end)).slice(1)];
   const next=leaving+length(outbound)/speed*1000;
   if(Math.abs(next-end)<.01){end=next;break;}end=next;
  }
  visits.push({start,entered,leaving,end,house,inbound,outbound});
 }
 return {at,visits,stroll};
}
