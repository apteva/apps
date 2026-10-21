import type {Scene} from "./world-model";
export const fixture: Scene = {
 source:{id:"main",kind:"main",label:"Main server",status:"running",agents:9},scope:"instance",
 projects:[{id:"growth",name:"Growth"},{id:"product",name:"Product"},{id:"empty",name:"Empty project"}],
 agents:Array.from({length:9},(_,i)=>({id:String(i+1),name:["Coordinator","Researcher","Writer"][i] || `Agent ${i+1}`,status:i===2?"waiting":"running",project_id:i<8?"growth":"product"})),
 apps:[],destinations:[{id:"growth:app:crm",project_id:"growth",agent_ids:["1","2"],kind:"app",name:"CRM",status:"running",tool_count:1,call_count:1,tools:["crm_search"]},{id:"product:app:code",project_id:"product",agent_ids:["9"],kind:"app",name:"Code",status:"running",tool_count:1,call_count:0}],
 events:[{id:"handoff",agent_id:"1",target_agent_id:"2",thread_id:"main",kind:"message",type:"thread.message",label:"sent a message",time:"2026-10-21T12:00:00Z"},{id:"tool",agent_id:"2",thread_id:"main",kind:"tool",type:"tool.call",label:"called crm_search",target:"crm_search",target_id:"growth:app:crm",time:"2026-10-21T12:00:01Z"}],at:"2026-10-21T12:00:02Z"
};
