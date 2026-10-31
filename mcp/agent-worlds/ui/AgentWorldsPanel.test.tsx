import {afterAll,expect,test} from "bun:test";
import {GlobalRegistrator} from "@happy-dom/global-registrator";
import React,{act} from "react";
import {createRoot} from "react-dom/client";
import {fixture} from "./test-fixtures";
import Panel from "./AgentWorldsPanel";
GlobalRegistrator.register({url:"http://localhost:5280"});
const win = window as any;
(globalThis as any).IS_REACT_ACT_ENVIRONMENT=true;
let latest=structuredClone(fixture);
const pending:{resolve:(response:any)=>void;signal:AbortSignal}[]=[];
let timerId=0;
const timers=new Map<number,{callback:()=>void;delay:number}>();
win.setTimeout=((callback:()=>void,delay:number)=>{timers.set(++timerId,{callback,delay});return timerId}) as any;
win.clearTimeout=((id:number)=>timers.delete(id)) as any;
win.setInterval=win.setTimeout;win.clearInterval=win.clearTimeout;
const originalFetch=globalThis.fetch;
globalThis.fetch=((url:string,options:any)=>url.startsWith("/api/ui-layout/")?Promise.resolve({ok:true,headers:{get:()=>'"1"'},json:async()=>({value:[]})}):url.startsWith("/api/apps?") || url === "/api/apps" ? Promise.resolve({ok:true,json:async()=>[]}) : url.includes("/sources")?Promise.resolve({ok:true,json:async()=>[fixture.source]}):new Promise(resolve=>pending.push({resolve,signal:options.signal}))) as any;
const response=()=>({ok:true,json:async()=>structuredClone(latest)});
const host=win.document.createElement("div");win.document.body.append(host);
const root=createRoot(host as any);
const click=async(label:string)=>act(async()=>{
 const el=[...host.querySelectorAll("button,[role=button]")].find(el=>el.getAttribute("aria-label")===label||el.textContent===label);
 if(el){el.dispatchEvent(new win.MouseEvent("click",{bubbles:true}));return;}
 const select=host.querySelector('select[aria-label="Visualization"]') as HTMLSelectElement;
 const option=[...select.options].find(option=>option.textContent===label);expect(option,`control ${label}`).toBeTruthy();
 select.value=option!.value;select.dispatchEvent(new win.Event("change",{bubbles:true}));
});
const crumb=()=>host.querySelector(".aw-breadcrumbs")!.textContent;
const camera=()=>{const v=host.querySelector('.aw-canvas')!;return [v.getAttribute('data-camera-zoom'),v.getAttribute('data-camera-x'),v.getAttribute('data-camera-y')].join();};
const agentButtons=()=>host.querySelectorAll('.aw-canvas [aria-label^="Inspect agent"], .aw-canvas .aw-sr [data-agent-id]').length;
const poll=async()=>act(async()=>{const entry=[...timers].find(([,t])=>t.delay===2500);expect(entry).toBeTruthy();timers.delete(entry![0]);entry![1].callback();});
const THEMES=["System graph","Operations","Pixel village","Space station"];
afterAll(async()=>{await act(async()=>root.unmount());globalThis.fetch=originalFetch;await GlobalRegistrator.unregister();});

test("Themes share focus, camera, inspection and replay over one model",async()=>{
 await act(async()=>root.render(React.createElement(Panel,{installId:1})));
 expect(pending.length).toBe(1);
 await act(async()=>pending[0].resolve(response()));
 expect([...host.querySelectorAll('select[aria-label="Visualization"] option')].map(o=>o.textContent)).toEqual(THEMES);
 // Default map theme: every project is its own village on one canvas.
 const pixel=host.querySelector('canvas[data-renderer="pixel"]')!;
 expect(pixel.getAttribute('data-theme')).toBe('village');
 expect(pixel.getAttribute('data-view')).toBe('project-villages');
 expect(host.querySelectorAll('.aw-sr button[aria-label],.aw-sr button').length).toBeGreaterThan(0);
 expect([...host.querySelectorAll('.aw-sr button')].map(b=>b.textContent)).toContain('Zoom into project Growth');
 const viewport=host.querySelector('.aw-canvas')!;
 viewport.getBoundingClientRect=()=>({left:0,top:0,width:1280,height:760,right:1280,bottom:760,x:0,y:0,toJSON:()=>({})});
 const wheel=new win.WheelEvent('wheel',{bubbles:true,cancelable:true,deltaY:-100,clientX:640,clientY:380});
 Object.defineProperties(wheel,{clientX:{value:640},clientY:{value:380}});
 await act(async()=>{viewport.dispatchEvent(wheel)});
 expect(wheel.defaultPrevented).toBe(true);expect(Number(viewport.getAttribute('data-camera-zoom'))).toBeGreaterThan(1);
 const zoomed=camera();
 const pointer=(type:string,x:number,y:number)=>new win.PointerEvent(type,{bubbles:true,cancelable:true,pointerId:1,button:0,clientX:x,clientY:y});
 await act(async()=>{viewport.dispatchEvent(pointer('pointerdown',600,380));viewport.dispatchEvent(pointer('pointermove',700,400));viewport.dispatchEvent(pointer('pointerup',700,400));});
 expect(camera()).not.toBe(zoomed);
 // A drag ending over a project cannot navigate it; the next click still can.
 await click("Zoom into project Growth");expect(crumb()).not.toContain('Growth');
 await click('Fit visualization');expect(viewport.getAttribute('data-camera-zoom')).toBe('1');
 await click("System graph");
 expect(host.querySelectorAll('[data-world-node="project"]').length).toBe(3);
 await click("Zoom into project Growth");
 expect(crumb()).toContain("Growth");
 for(const theme of THEMES){
  await click(theme);const beforeSelection=crumb(),view=camera();
  await click('Inspect agent Writer');
  expect(host.querySelector('[data-agent-details="3"]')).toBeTruthy();
  expect(host.querySelector('aside[aria-label="Agent details"] h2')!.textContent).toBe('Writer');
  expect(crumb()).toBe(beforeSelection);expect(camera()).toBe(view);
  await click('Close panel');expect(host.querySelector('.aw-float')).toBeNull();
 }
 // Observed teams navigate in graph views; maps then show just that team.
 await click("System graph");
 await click("Zoom into team Coordinator + 1");
 expect(crumb()).toContain("Observed team");
 await click("Pixel village");
 expect(host.querySelector('canvas[data-renderer="pixel"]')!.getAttribute('data-view')).toBe('village');
 expect(agentButtons()).toBe(2);
 const before=crumb();
 await click('Zoom in visualization');const teamCamera=camera();
 await click("Inspect agent Researcher");
 expect(crumb()).toBe(before);
 expect(host.querySelector('[data-agent-details="2"]')).toBeTruthy();
 expect(host.querySelector('[data-block="worlds:now"]')!.textContent).toContain('running');
 expect(host.querySelector('[data-block="worlds:activity"]')!.textContent).toContain('called crm_search');
 expect(host.querySelector('[data-block="worlds:activity"]')!.textContent).not.toContain('sent a message');
 expect(camera()).toBe(teamCamera);
 await poll(); // a request already in flight when the user pauses
 const clockBeforePause=host.querySelector('canvas[data-renderer="pixel"]')!.getAttribute("data-clock");
 await click("Live");
 expect(host.querySelector('canvas[data-renderer="pixel"]')!.getAttribute("data-clock")).toBe(clockBeforePause);
 const range=host.querySelector('input[type="range"]')!;
 for(const theme of THEMES){
  await click(theme);expect(crumb()).toBe(before);expect(host.querySelector('input[type="range"]')).toBe(range);
  expect(host.querySelector('[data-agent-details="2"]')).toBeTruthy();
  expect(agentButtons()).toBe(2);
  await click('Inspect agent Coordinator');expect(host.querySelector('[data-agent-details="1"]')).toBeTruthy();
  expect(host.querySelector('aside[aria-label="Agent details"] h2')!.textContent).toBe('Coordinator');
  await click('Inspect agent Researcher');
 }
 expect(host.querySelector('canvas[data-renderer="pixel"]')!.getAttribute("data-clock")).toBe(clockBeforePause);
 // Late network responses may not mutate a paused world or reset its cursor.
 latest={...latest,events:[],agents:[{...fixture.agents[0],name:"Changed while paused"}]};
 await act(async()=>pending[1].resolve(response()));
 expect(crumb()).toBe(before);expect(host.textContent).not.toContain("Changed while paused");
 const count=pending.length;await poll();expect(pending.length).toBe(count);
 await act(async()=>window.dispatchEvent(new win.KeyboardEvent('keydown',{key:'Escape',bubbles:true})));
 expect(host.querySelector('.aw-float')).toBeNull();expect(crumb()).toBe(before);
 expect(camera()).toBe(teamCamera);
 await click("Growth");
 await click("Paused");
 latest={...fixture,events:[]};await poll();await act(async()=>pending.at(-1)!.resolve(response()));
 // Graph and board views page; map views show the whole project at once.
 await click("System graph");
 expect(host.querySelectorAll('[data-world-node="team"]').length).toBe(0);
 expect(host.querySelectorAll('svg [aria-label^="Inspect agent"]').length).toBe(6);
 await click("Next");expect(host.querySelectorAll('svg [aria-label^="Inspect agent"]').length).toBe(2);
 await click('Inspect agent Agent 7');expect(host.querySelector('[data-agent-details="7"]')).toBeTruthy();
 await click("Operations");expect(host.querySelector(".aw-pages")!.textContent).toContain("2 / 2");
 expect(host.querySelector('[data-agent-details="7"]')).toBeTruthy();
 await click("Pixel village");expect(host.querySelector(".aw-pages")).toBeNull();
 expect(agentButtons()).toBe(8);
 expect(host.querySelector('[data-agent-details="7"]')).toBeTruthy();
 // Switching installation aborts the previous source and resets its scope.
 await poll();const inflight=pending.at(-1)!;
 await act(async()=>root.render(React.createElement(Panel,{installId:2,projectId:"growth"})));
 expect(inflight.signal.aborted).toBe(true);
 latest={...fixture,scope:"project",projects:[fixture.projects![0]],agents:fixture.agents.slice(0,8)};
 await act(async()=>pending.at(-1)!.resolve(response()));
 expect(crumb()).not.toContain("Instance");expect(crumb()).toContain("Growth");expect(host.querySelector(".aw-float")).toBeNull();
});
