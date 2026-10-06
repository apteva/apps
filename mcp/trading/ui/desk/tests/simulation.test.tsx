import { afterEach, expect, test } from "bun:test";
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { BacktestRunDetail, SimulationControls, CreatePortfolioForm } from "../../TradingPanel.tsx";

let root: Root;
let container: HTMLElement;
afterEach(async()=>{ if(root) await act(async()=>root.unmount());container?.remove(); });
function mount(){container=document.createElement("div");document.body.append(container);root=createRoot(container);}
const run:any={id:1,name:"Hourly event replay",run_kind:"strategy",status:"running",symbols:["AAPL"],current_step:2,total_steps:10,starting_cash:1000,summary:{engine_version:"event-sim/1",processed_events:4,input_events:20,simulation_time:"2026-01-05T15:00:00Z",benchmark_symbol:"AAPL"}};

test("running event replay shows progress and leaves pause/cancel usable",async()=>{
 mount();const actions:string[]=[];
 await act(async()=>root.render(<BacktestRunDetail run={run} events={[]} liveEvents={[]} performance={null} busy={false} onAction={(_,op)=>actions.push(op)}/>));
 expect(container.textContent).toContain("4/20 events");
 const buttons=Array.from(container.querySelectorAll("button"));
 const pause=buttons.find(b=>b.textContent==="Pause")!;
 const cancel=buttons.find(b=>b.textContent==="Cancel")!;
 expect(pause.disabled).toBe(false);expect(cancel.disabled).toBe(false);
 await act(async()=>pause.click());await act(async()=>cancel.click());
 expect(actions).toEqual(["pause","cancel"]);
});

test("editing a queued simulation preserves imported feature inputs",async()=>{
 mount();const extra=[{id:"sentiment",type:"feature.sentiment",symbol:"AAPL",event_time:"2026-01-05T10:00:00Z",available_at:"2026-01-05T10:05:00Z",data:{score:0.8}}];
 const writes:any[]=[];
 const api:any=async(method:string,path:string,q:any,body:any)=>{if(method==="GET")return{config:{seed:7,benchmark_symbol:"AAPL",submission_latency_ms:10},inputs:extra};writes.push(body);return{updated:true}};
 await act(async()=>root.render(<SimulationControls run={{...run,status:"queued"}} api={api} onChange={async()=>{}} setError={(error)=>{if(error)throw Error(error)}}/>));
 expect(container.querySelector("textarea")!.value).toContain("feature.sentiment");
 const save=Array.from(container.querySelectorAll("button")).find(b=>b.textContent==="Save simulation settings")!;
 await act(async()=>save.click());
 expect(writes).toHaveLength(1);expect(writes[0].inputs).toEqual(extra);expect(writes[0].simulation.seed).toBe(7);
});


test("broker paper excludes live-only crypto providers", async()=>{
 mount();
 const brokers=[{slug:"binance-trading",bound:true,paper_supported:false,asset_classes:["crypto"]},{slug:"alpaca-trading",bound:true,paper_supported:true,asset_classes:["equity","etf","crypto"]}];
 const api:any=async()=>({brokers});
 await act(async()=>root.render(<CreatePortfolioForm api={api} onCreated={()=>{}} onCancel={()=>{}} setError={()=>{}}/>));
 const radios=container.querySelectorAll<HTMLInputElement>('input[type="radio"]');
 await act(async()=>radios[2]!.click());
 expect(container.querySelector("select")!.textContent).toContain("binance-trading");
 await act(async()=>radios[1]!.click());
 expect(container.querySelector("select")!.textContent).toContain("alpaca-trading");
 expect(container.querySelector("select")!.textContent).not.toContain("binance-trading");
});


test("agent event run shows decision waiting and remains controllable",async()=>{
 mount();const actions:string[]=[];
 await act(async()=>root.render(<BacktestRunDetail run={{...run,run_kind:"agent",source_agent_id:123,summary:{...run.summary,decision_mode:"agent",agent_waiting:true}}} events={[]} liveEvents={[]} performance={null} busy={false} onAction={(_,op)=>actions.push(op)}/>));
 expect(container.textContent).toContain("Agent deciding");
 expect(container.textContent).toContain("simulated clock paused");
 const pause=Array.from(container.querySelectorAll("button")).find(b=>b.textContent==="Pause")!;
 expect(pause.disabled).toBe(false);await act(async()=>pause.click());expect(actions).toEqual(["pause"]);
});
