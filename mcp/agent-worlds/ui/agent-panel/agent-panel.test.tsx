import {afterAll,expect,test} from "bun:test";
import {GlobalRegistrator} from "@happy-dom/global-registrator";
import React,{act} from "react";
import {createRoot} from "react-dom/client";
import AgentPanel from "./AgentPanel";
import {agentWidgets,widgetModuleURL,type InstalledApp} from "./app-widgets";
import {defaultBlocks,parseBlocks} from "./layout";
import type {Agent,Destination,Event} from "../world-model";
GlobalRegistrator.register({url:"http://localhost:5280"});
(globalThis as any).IS_REACT_ACT_ENVIRONMENT=true;

const apps:InstalledApp[]=[
 {name:"conversations",install_id:10,version:"0.24.60",display_name:"Conversations",ui_components:[{name:"agent-conversations",label:"Agent conversations",entry:"/ui/AgentConversationsWidget.mjs",slots:["dashboard.build","dashboard.agent_detail"],visibility:"attached",settings_schema:{properties:{display_mode:{type:"string",title:"Layout",enum:["browser","single"],default:"browser"}}}}]},
 {name:"tasks",install_id:11,ui_components:[{name:"agent-tasks",label:"Agent tasks",entry:"/ui/AgentTasksWidget.mjs",slots:["dashboard.agent_detail"],visibility:"attached"}]},
 {name:"tasks",install_id:12,project_id:"p1",ui_components:[{name:"agent-tasks",label:"Agent tasks",entry:"/ui/AgentTasksWidget.mjs",slots:["dashboard.agent_detail"],visibility:"attached"}]},
 {name:"crm",install_id:13,ui_components:[{name:"customer-inbox",entry:"/ui/CrmInboxWidget.mjs",slots:["dashboard.home"]}]},
];

test("agent widgets come from the agent-detail slot, project installs shadow global ones",()=>{
 const widgets=agentWidgets(apps,"p1");
 expect(widgets.map(widget=>[widget.component,widget.installId])).toEqual([["conversations:agent-conversations",10],["tasks:agent-tasks",12]]);
 expect(agentWidgets(apps,"p2").find(widget=>widget.appName==="tasks")!.installId).toBe(11);
 expect(widgetModuleURL(widgets[0],"p1")).toBe("/api/apps/conversations/ui/AgentConversationsWidget.mjs?v=0.24.60&install_id=10&project_id=p1");
});

test("saved layouts are cleaned, and new panels include Conversations when installed",()=>{
 expect(parseBlocks([{id:"a",component:"worlds:now",height:"huge"},{id:"b"},"legacy",{id:"c",component:"conversations:agent-conversations",height:"tall",settings:{x:1}}]))
  .toEqual([{id:"a",component:"worlds:now"},{id:"c",component:"conversations:agent-conversations",settings:{x:1},height:"tall"}]);
 expect(parseBlocks([])).toBeNull();
 expect(defaultBlocks("agent",[]).map(block=>block.component)).toEqual(["worlds:now","worlds:activity"]);
 expect(defaultBlocks("agent",["conversations:agent-conversations"]).map(block=>block.component)).toEqual(["worlds:now","worlds:activity","conversations:agent-conversations"]);
 expect(defaultBlocks("place",[]).map(block=>block.component)).toEqual(["worlds:visitors","worlds:calls","worlds:tools"]);
});

const agent:Agent={id:"7",name:"Ada",status:"running",project_id:"p1"};
const crm:Destination={id:"p1:app:13",project_id:"p1",kind:"app",name:"CRM",status:"running",tool_count:2,call_count:3,agent_ids:["7"]};
const conversations:Destination={id:"p1:app:10",project_id:"p1",kind:"app",name:"Conversations",status:"attached",tool_count:0,call_count:0,agent_ids:[]};
const events:Event[]=[
 {id:"e1",agent_id:"7",kind:"tool",type:"tool.call",label:"called contacts_search",target:"contacts_search",target_id:"p1:app:13",time:"2026-10-31T12:00:00Z"},
 {id:"e2",agent_id:"8",target_agent_id:"7",kind:"message",type:"thread.message",label:"sent a message",time:"2026-10-31T12:00:01Z"},
];
const palette={font:"",bg:"",card:"",subtle:"",input:"",text:"",muted:"",dim:"",border:"",strong:"",accent:"",success:"#0f0",warn:"",error:"",info:""};
const patches:any[]=[];
let stored:any[]=[];
const originalFetch=globalThis.fetch;
globalThis.fetch=((url:string,options:any={})=>{
 if(options.method==="PATCH"){const body=JSON.parse(options.body);patches.push({url,match:options.headers["If-Match"],value:body.value});stored=body.value;return Promise.resolve({ok:true,status:200,headers:{get:()=>`"${patches.length+1}"`},text:async()=>""});}
 return Promise.resolve({ok:true,status:200,headers:{get:()=>'"1"'},json:async()=>({value:stored})});
}) as any;
const host=document.createElement("div");document.body.append(host);
const root=createRoot(host as any);
afterAll(async()=>{await act(async()=>root.unmount());globalThis.fetch=originalFetch;await GlobalRegistrator.unregister();});
const settle=()=>act(async()=>{await new Promise(resolve=>setTimeout(resolve,0));});
const render=async(props:Partial<React.ComponentProps<typeof AgentPanel>>)=>{
 const context={agents:[agent,{id:"8",name:"Bram",status:"running"}],destinations:[crm,conversations],events,projectId:"p1",palette,onSelectAgent:()=>{},onSelectPlace:()=>{}};
 await act(async()=>root.render(React.createElement(AgentPanel,{kind:"agent",agent,context,palette,projectId:"p1",widgets:agentWidgets(apps,"p1"),appBlocks:true,onClose:()=>{},...props} as any)));
 await settle();
};
const blocks=()=>[...host.querySelectorAll("[data-block]")].map(el=>el.getAttribute("data-block"));
const click=(label:string)=>act(async()=>{(host.querySelector(`[aria-label="${label}"]`) as HTMLElement).click();});

test("the agent panel shows its blocks and explains app blocks it cannot show",async()=>{
 await render({});
 expect(blocks()).toEqual(["worlds:now","worlds:activity","conversations:agent-conversations"]);
 expect(host.querySelector('[data-block="worlds:now"]')!.textContent).toContain("searching contacts");
 expect(host.querySelector('[data-block="conversations:agent-conversations"]')!.textContent).toContain("Conversations is not attached to Ada");
 await render({appBlocks:false});
 expect(host.querySelector('[data-block="conversations:agent-conversations"]')!.textContent).toContain("only available for agents on this server");
});

test("customising adds, moves and removes blocks and saves them per project",async()=>{
 stored=[];patches.length=0;
 await render({});
 await click("Customise panel");
 const add=host.querySelector('select[aria-label="Add block"]') as HTMLSelectElement;
 expect([...add.options].map(option=>option.textContent)).toContain("Agent tasks · tasks");
 await act(async()=>{add.value="worlds:handoffs";add.dispatchEvent(new window.Event("change",{bubbles:true}));});
 await settle();
 expect(blocks().at(-1)).toBe("worlds:handoffs");
 expect(host.querySelector('[data-block="worlds:handoffs"]')!.textContent).toContain("From Bram");
 expect(patches.at(-1).url).toBe("/api/ui-layout/projects/p1/surfaces/agent-worlds.agent_panel");
 expect(patches.at(-1).match).toBe('"1"');
 await act(async()=>{([...host.querySelectorAll('[aria-label="Move up"]')].at(-1) as HTMLElement).click();});
 expect(blocks().slice(-2)).toEqual(["worlds:handoffs","conversations:agent-conversations"]);
 await click("Remove Activity");
 expect(blocks()).toEqual(["worlds:now","worlds:handoffs","conversations:agent-conversations"]);
 expect(patches.at(-1).value.map((block:any)=>block.component)).toEqual(["worlds:now","worlds:handoffs","conversations:agent-conversations"]);
 // A fresh mount reads the saved layout back.
 await act(async()=>root.render(React.createElement("div")));
 await render({});
 expect(blocks()).toEqual(["worlds:now","worlds:handoffs","conversations:agent-conversations"]);
});

test("a building panel lists who visited and which tools they used",async()=>{
 stored=[];
 await render({kind:"place",agent:undefined,place:crm});
 expect(blocks()).toEqual(["worlds:visitors","worlds:calls","worlds:tools"]);
 expect(host.querySelector('[data-block="worlds:visitors"]')!.textContent).toContain("Ada");
 expect(host.querySelector('[data-block="worlds:tools"]')!.textContent).toContain("contacts_search · 1 call");
});
