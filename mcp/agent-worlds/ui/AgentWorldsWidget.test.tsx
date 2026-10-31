import {afterAll,expect,test} from "bun:test";
import {GlobalRegistrator} from "@happy-dom/global-registrator";
import React,{act} from "react";
import {createRoot} from "react-dom/client";
import {fixture} from "./test-fixtures";
import {THEMES} from "./themes";
import Widget,{worldPageLink,worldWidgetPreferences} from "./AgentWorldsWidget";
GlobalRegistrator.register({url:"http://localhost:5280"});
(globalThis as any).IS_REACT_ACT_ENVIRONMENT=true;
const requests:string[]=[];
const originalFetch=globalThis.fetch;
globalThis.fetch=((url:string)=>{requests.push(url);return Promise.resolve({ok:true,json:async()=>url.startsWith("/api/apps?")||url==="/api/apps"?[]:structuredClone(fixture)});}) as any;
const host=document.createElement("div");document.body.append(host);
const root=createRoot(host as any);
afterAll(async()=>{await act(async()=>root.unmount());globalThis.fetch=originalFetch;await GlobalRegistrator.unregister();});

test("settings fall back to the village of the main source",()=>{
 expect(worldWidgetPreferences()).toEqual({view:"village",source:"main",height:"standard"});
 expect(worldWidgetPreferences({view:"nope",source:" ",height:"huge"})).toEqual({view:"village",source:"main",height:"standard"});
 expect(worldWidgetPreferences({view:"graph",source:"remote",height:"tall"})).toEqual({view:"graph",source:"remote",height:"tall"});
});

test("the page link opens the same view and source",()=>{
 expect(worldPageLink({projectId:"p1",installId:7},worldWidgetPreferences({view:"station",source:"remote"}))).toBe("/apps/agent-worlds/page?view=station&source=remote&project_id=p1&install_id=7");
});

test("every view renders the scene inside one link",async()=>{
 for(const theme of THEMES){
  await act(async()=>root.render(React.createElement(Widget,{projectId:"p1",installId:7,widgetSettings:{view:theme.id,source:"main"}})));
  await act(async()=>{await new Promise(resolve=>setTimeout(resolve,0));});
  const card=host.querySelector("[data-world-view]")!;
  expect(card.getAttribute("data-world-view")).toBe(theme.id);
  const stage=host.querySelector(".aw-widget-stage")!;
  expect(stage,`stage for ${theme.id}`).toBeTruthy();
  expect(stage.querySelector("canvas,svg"),`renderer for ${theme.id}`).toBeTruthy();
  expect(stage.hasAttribute("inert")).toBe(true);
  expect(host.querySelector(`a[aria-label="Open Agent Worlds · ${theme.name}"]`)!.getAttribute("href")).toContain(`view=${theme.id}`);
 }
 expect(requests.some(url=>url.startsWith("/api/apps/agent-worlds/api/scene?source=main")&&url.includes("project_id=p1")&&url.includes("install_id=7"))).toBe(true);
});
