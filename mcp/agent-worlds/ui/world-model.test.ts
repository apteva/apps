import {describe,expect,test} from "bun:test";
import {villageDestinations,buildWorld,projectWorld,resolveFocus,rootFocus} from "./world-model";
import type {Scene} from "./world-model";
import {fixture} from "./test-fixtures";

describe("shared world projection",()=>{
 test("global root includes empty authorized projects and preserves ownership",()=>{
  const world=buildWorld(fixture,fixture.events), root=projectWorld(fixture,world,rootFocus(fixture));
  expect(root.nodes).toHaveLength(3);
  expect(root.nodes.find(n=>n.id==="product")?.agents.map(a=>a.id)).toEqual(["9"]);
  expect(root.nodes.find(n=>n.id==="empty")?.agents).toEqual([]);
 });
 test("only real same-project handoffs create observed teams",()=>{
  const world=buildWorld(fixture,[...fixture.events,{...fixture.events[0],id:"cross",target_agent_id:"9"},{...fixture.events[0],id:"unknown",target_agent_id:"secret"}]);
  expect(world.teams.map(t=>t.agentIds)).toEqual([["1","2"]]);
  const focus={projectId:"growth",teamId:world.teams[0].id};
  const team=projectWorld(fixture,world,focus);
  expect(team.agents.map(a=>a.id)).toEqual(["1","2"]);
  expect(team.destinations.map(d=>d.name)).toEqual(["CRM"]);
 });
 test("project installations cannot navigate to an instance root",()=>{
  const scene={...fixture,scope:"project" as const,projects:[fixture.projects![0]],agents:fixture.agents.slice(0,8)};
  const world=buildWorld(scene,scene.events);
  expect(resolveFocus(scene,world,{})).toEqual({projectId:"growth"});
  expect(resolveFocus(scene,world,{projectId:"product",agentId:"9"})).toEqual({projectId:"growth"});
 });
 test("replay removes future relationships without inventing teams",()=>{
  const world=buildWorld(fixture,[]);
  expect(world.teams).toHaveLength(0);
  expect(resolveFocus(fixture,world,{projectId:"growth",teamId:"gone",agentId:"2"})).toEqual({projectId:"growth",agentId:"2"});
 });
});

test("village buildings represent apps and integrations, not generic tool activity",()=>{
 const other={...fixture.destinations[0],id:"growth:other:tools",kind:"other" as const,name:"Other tools",call_count:100};
 const result=villageDestinations([...fixture.destinations,other],"",fixture.events);
 expect(result.map(d=>d.id)).toEqual(expect.arrayContaining(fixture.destinations.map(d=>d.id)));
 expect(result.some(d=>d.kind==="other")).toBe(false);
});

test('app labels use display names by installation without changing identity or remote names', async()=>{
 const {withAppDisplayNames}=await import('./app-display-names');
 const scene={...fixture,destinations:[
  {id:'growth:app:17',project_id:'growth',kind:'app' as const,name:'a2a',status:'running',tool_count:0,call_count:0},
  {id:'ops:app:18',project_id:'ops',kind:'app' as const,name:'a2a',status:'running',tool_count:0,call_count:0},
  {id:'growth:app:19',project_id:'growth',kind:'app' as const,name:'custom',status:'running',tool_count:0,call_count:0},
 ]};
 const metadata=[{install_id:17,project_id:'growth',display_name:'Agent to Agent'},{install_id:18,project_id:'ops',display_name:'Operations Messenger'},{install_id:19,project_id:'another-project',display_name:'Unrelated app'}];
 const labeled=withAppDisplayNames(scene,metadata)!;
 expect(labeled.destinations.map(app=>app.name)).toEqual(['Agent to Agent','Operations Messenger','custom']);
 expect(labeled.destinations.map(app=>app.id)).toEqual(scene.destinations.map(app=>app.id));
 expect(scene.destinations[0].name).toBe('a2a');
 expect(withAppDisplayNames({...scene,source:{...scene.source,kind:'remote-runtime'}},metadata)!.destinations[0].name).toBe('a2a');
 expect(withAppDisplayNames(scene,[])!.destinations[0].name).toBe('a2a');
});
