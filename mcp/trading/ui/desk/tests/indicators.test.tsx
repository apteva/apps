import { afterEach, expect, test } from "bun:test";
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { StrategyPresetPicker, SimulationControls } from "../../TradingPanel.tsx";
let root:Root;let container:HTMLElement;
afterEach(async()=>{if(root)await act(async()=>root.unmount());container?.remove()});
function mount(){container=document.createElement("div");document.body.append(container);root=createRoot(container)}
test("indicator catalog loads watchlist presets into the draft without creating or assigning a strategy",async()=>{
 mount();const selected:any[]=[];const calls:any[]=[];
 const preset={id:"macd",name:"MACD trend",description:"Test",definition:{universe:["BTC-USD","ETH-USD"],rules:[{rank:{where:{all:[{indicator:"macd_hist_12_26_9",operator:">",value:0}]}}}]}};
 const api:any=async(...args:any[])=>{calls.push(args);return{presets:[preset],indicators:[{name:"rsi_wilder_14",description:"Wilder smoothing"}],conditions:"all / any",limitations:"Research template",sources:[]}};
 await act(async()=>root.render(<StrategyPresetPicker api={api} symbols={["BTC-USD","ETH-USD"]} onSelect={p=>selected.push(p)}/>));
 expect(calls[0]).toEqual(["GET","/strategies/catalog",{symbols:"BTC-USD,ETH-USD"}]);
 await act(async()=>container.querySelector("button")!.click());expect(selected).toEqual([preset]);expect(calls.length).toBe(1);expect(container.textContent).toContain("Wilder smoothing");
});
test("next-open timing resets all three latency fields and preserves costs and inputs",async()=>{
 mount();const writes:any[]=[];const inputs=[{id:"news",type:"news.headline"}];const config={submission_latency_ms:250,cancellation_latency_ms:50,latency_jitter_ms:100,costs:{fee_bps:10}};
 const api:any=async(m:string,p:string,q:any,b:any)=>{if(m==="GET")return{config,inputs};writes.push(b);return{}};
 const run:any={id:1,status:"queued",interval:"1h",symbols:["BTC-USD"],summary:{}};
 await act(async()=>root.render(<SimulationControls run={run} api={api} onChange={async()=>{}} setError={e=>{if(e)throw Error(e)}}/>));
 expect(container.textContent).toContain("delay the fill");
 const button=(text:string)=>Array.from(container.querySelectorAll("button")).find(b=>b.textContent===text)!;
 await act(async()=>button("Use idealized next-open timing (zero latency)").click());await act(async()=>button("Save simulation settings").click());
 expect(writes[0].simulation).toEqual({...config,submission_latency_ms:0,cancellation_latency_ms:0,latency_jitter_ms:0});expect(writes[0].inputs).toEqual(inputs);
});
