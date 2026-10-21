import {describe,expect,test} from "bun:test";
import {agentTrack,villageStreets,type House} from "./village-motion";
import {sitesFor} from "./village-stage";
import {fixture} from "./test-fixtures";
import type {Event} from "./world-model";

const time=Date.parse("2026-10-21T12:00:00Z");
const call=(id:string,target:string,offset=0):Event=>({id,agent_id:"researcher",kind:"tool",type:"tool.call",label:"Using app",target_id:target,time:new Date(time+offset).toISOString()});
const houses:House[]=[{id:"crm",x:300,y:300,scale:1},{id:"mail",x:980,y:300,scale:1},{id:"code",x:300,y:560,scale:1},{id:"search",x:980,y:560,scale:1}];
const delta=(a:{x:number;y:number},b:{x:number;y:number})=>Math.hypot(a.x-b.x,a.y-b.y);

describe("village motion",()=>{
 test("idle agents wander, stop to rest, and keep clear of walls and water",()=>{
  for(const portrait of [false,true]){
   const sites=portrait?houses.map((h,i)=>({...h,x:140+i%2*260,y:302+Math.floor(i/2)*278,scale:.86})):houses;
   const streets=villageStreets(sites,portrait);
   for(const id of ["researcher","writer","coordinator","agent-4","agent-5","agent-6"]){
    const track=agentTrack(id,[],sites,streets);let rests=0,walks=0;const places=new Set();
    for(let t=time;t<time+180000;t+=150){
     const p=track.at(t);expect(streets.clear(p)).toBe(true);expect(delta(p,track.at(t+1))).toBeLessThan(.06);
     if(p.walking)walks++;else rests++;places.add(`${Math.round(p.x/10)}:${Math.round(p.y/10)}`);
    }
    expect(walks).toBeGreaterThan(0);expect(rests).toBeGreaterThan(0);expect(places.size).toBeGreaterThan(10);
   }
  }
 });
 test("a call interrupts a stroll continuously, enters the doorway, rests inside, and returns continuously",()=>{
  const streets=villageStreets(houses,false), track=agentTrack("researcher",[call("call","crm")],houses,streets);
  const visit=track.visits[0];
  expect(delta(track.at(time-1),track.at(time))).toBeLessThan(.06);
  expect(track.at(visit.entered+1000).destination).toBe("crm");
  expect(track.at(visit.entered+1000).inside).toBe(true);expect(track.at(visit.entered+1000).walking).toBe(false);
  expect(delta(track.at(visit.end-1),track.at(visit.end))).toBeLessThan(.08);
  expect(track.at(visit.end+1).destination).toBeUndefined();
  let doorway=false;
  for(let t=time;t<visit.end;t+=50){
   const p=track.at(t);expect(delta(p,track.at(t+1))).toBeLessThan(.08);
   if(!streets.clear(p)){
    expect(Math.abs(p.x-300)).toBeLessThan(87);expect(p.y).toBeGreaterThan(156);
    if(p.y>302&&p.y<327){expect(Math.abs(p.x-300)).toBeLessThan(1);doorway=true;}
   }
  }
  expect(doorway).toBe(true);
 });
 test("new integrations interrupt an existing visit from its current position and results do not start another trip",()=>{
  const streets=villageStreets(houses,false), first=agentTrack("researcher",[call("a","crm")],houses,streets);
  const next=Math.floor(first.visits[0].entered)+1000;
  const result={...call("result","crm",1000),kind:"result",type:"tool.result"};
  const track=agentTrack("researcher",[call("a","crm"),result,call("b","mail",next-time)],houses,streets);
  expect(track.visits.length).toBe(2);expect(delta(first.at(next),track.at(next))).toBeLessThan(.01);
  expect(track.at(track.visits[1].entered+100).destination).toBe("mail");
  expect(track.at(track.visits[1].entered+100).inside).toBe(true);
 });
 test("polling, event ordering, pauses and backward seeks produce the same positions",()=>{
  const events=[call("a","crm"),call("b","mail",20000)],streets=villageStreets(houses,false);
  const first=agentTrack("researcher",events,houses,streets),second=agentTrack("researcher",[...events].reverse(),houses,streets);
  const captured=first.at(time+25000);first.at(time+40000);
  expect(first.at(time+25000)).toEqual(captured);expect(second.at(time+25000)).toEqual(captured);
  expect(first.at(time+25000)).toEqual(first.at(time+25000));
  expect(agentTrack("researcher",[call("unknown","missing")],houses,streets).visits).toHaveLength(0);
 });
 test("busy maps keep all eight app/integration doors reachable on desktop and portrait",()=>{
  const destinations=Array.from({length:8},(_,i)=>({...fixture.destinations[0],id:`destination-${i}`}));
  for(const portrait of [false,true]){
   const sites=sitesFor([],destinations,portrait,""),streets=villageStreets(sites,portrait);
   for(const site of sites){
    const track=agentTrack("researcher",[call("busy",site.id)],sites,streets),visit=track.visits[0];
    expect(visit.inbound.length).toBeGreaterThan(2);
    expect(track.at(visit.entered+100).inside).toBe(true);
    expect(delta(track.at(visit.end-1),track.at(visit.end))).toBeLessThan(.08);
   }
  }
 });

});
