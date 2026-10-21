import {describe,expect,test} from "bun:test";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import VillageStage from "./village-stage";
import {fixture} from "./test-fixtures";
import {buildWorld,projectWorld} from "./world-model";

const now=Date.parse(fixture.at);
const base={refNode:{current:null},portrait:false,live:true,now,onSelect:()=>{},onSelectDestination:()=>{},events:fixture.events};
const render=(props:Partial<React.ComponentProps<typeof VillageStage>>={})=>renderToStaticMarkup(<VillageStage {...base} {...props}/>);

describe("procedural open-house village",()=>{
 test("every house is open, furnished, and drawn without image assets",()=>{
  const html=render({agents:fixture.agents.slice(0,6),destinations:fixture.destinations});
  expect((html.match(/data-cutaway="open"/g)||[]).length).toBe(2);
  expect(html).toContain("OPEN HOUSES");
  expect(html).toContain('data-renderer="procedural-pixel"');
  expect(html).toContain('data-view="top-down"');
  expect(html).not.toMatch(/<image\b|<img\b|data:image|https?:\/\//);
  const combined=html+render({agents:fixture.agents.slice(0,2),destinations:fixture.destinations});
  const ids=[...combined.matchAll(/<pattern id="([^"]+)"/g)].map(m=>m[1]);
  expect(ids.length).toBeGreaterThan(0);
  expect(new Set(ids).size).toBe(ids.length);
  expect(html).not.toMatch(/skew|<filter|<linearGradient/);
 });
 test("a visiting agent is painted above the open room rather than hidden behind it",()=>{
  const html=render({now:now+12000,agents:fixture.agents.slice(0,2),destinations:fixture.destinations});
  expect(html).toMatch(/data-agent-id="2"[^>]*data-visit-destination="growth:app:crm"/);
  expect(html.indexOf('data-agent-id="2"')).toBeGreaterThan(html.indexOf('aria-label="Inspect destination CRM"'));
 });
 test("districts and teams use the same open houses and focus semantics",()=>{
  const world=buildWorld(fixture,fixture.events);
  const root=projectWorld(fixture,world,{}), project=projectWorld(fixture,world,{projectId:"growth"});
  const html=render({nodes:root.nodes,destinations:fixture.destinations,onFocus:()=>{}});
  expect((html.match(/data-cutaway="open"/g)||[]).length).toBe(fixture.destinations.length);
  expect(html).toContain('data-view="project-villages"');
  expect(html).toContain('aria-label="Zoom into project Growth"');
  expect(render({nodes:project.nodes.slice(0,6),onFocus:()=>{}})).toContain('aria-label="Zoom into team Coordinator + 1"');
 });
 test("each project contains its own actual village, buildings and residents",()=>{
  const world=buildWorld(fixture,fixture.events),root=projectWorld(fixture,world,{});
  const html=render({nodes:root.nodes,destinations:fixture.destinations});
  expect((html.match(/data-project-village=/g)||[]).length).toBe(3);
  expect((html.match(/data-agent-id=/g)||[]).length).toBe(fixture.agents.length);
  const block=(id:string)=>{
   const start=html.indexOf(`data-project-village="${id}"`),end=html.indexOf('data-world-node="project"',start+1);
   return html.slice(start,end<0?undefined:end);
  };
  expect(block("growth")).toContain('data-destination-id="growth:app:crm"');
  expect(block("growth")).not.toContain('data-destination-id="product:app:code"');
  expect(block("product")).toContain('data-agent-id="9"');
  expect(block("product")).not.toContain('data-agent-id="2"');
  expect(block("empty")).not.toContain('data-cutaway="open"');
  expect(block("empty")).not.toContain('data-agent-id=');
  expect(html).not.toContain('aria-label="Inspect agent');
  expect(html).not.toContain('aria-label="Inspect destination');
  expect((html.match(/aria-label="Zoom into project/g)||[]).length).toBe(3);
  expect(html).toContain("Each village is a project");
  const narrow=render({nodes:root.nodes,destinations:fixture.destinations,portrait:true,live:false});
  expect(narrow).toContain('viewBox="0 0 540 960"');
  expect(narrow).toContain('data-motion="false"');
  expect((narrow.match(/data-project-village=/g)||[]).length).toBe(3);
 });
 test("paused replay disables ambient motion and works in portrait",()=>{
  const html=render({live:false,portrait:true,agents:fixture.agents.slice(0,2),destinations:fixture.destinations});
  expect(html).toContain('data-motion="false"');
  expect(html).toContain('data-paused="true"');
  expect(html).toContain('viewBox="0 0 540 960"');
  expect(html).toContain("REPLAY");
 });
});
