import { afterEach, expect, test } from "bun:test";
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ValidationControls } from "../../ValidationControls.tsx";
let root:Root;let container:HTMLElement;
afterEach(async()=>{if(root)await act(async()=>root.unmount());container?.remove()});
function mount(){container=document.createElement("div");document.body.append(container);root=createRoot(container)}
const button=(text:string)=>Array.from(container.querySelectorAll("button")).find(b=>b.textContent===text)!;
const emptySuite:any={id:9,name:"Check",status:"queued",config:{mode:"walk_forward",candidates:[{name:"Source"}],max_agent_decisions:1000},plan:[],cases:[],report:{total:6,completed:0,failed:0,groups:{}}};

test("validation creation queues a suite without starting agent inference",async()=>{
 mount();const writes:any[]=[];
 const api:any=async(m:string,p:string,q:any,b:any)=>{if(m==="POST"){writes.push({p,b});return{suite:emptySuite}};return p==="/validations"?{suites:[]}:{suite:emptySuite}};
 await act(async()=>root.render(<ValidationControls sourceId={17} agent api={api} setError={e=>{if(e)throw Error(e)}}/>));
 await act(async()=>button("New validation suite").click());
 expect(container.textContent).toContain("can incur model costs");
 await act(async()=>button("Create queued suite").click());
 expect(writes).toHaveLength(1);expect(writes[0].p).toBe("/validations");expect(writes[0].b.source_backtest_id).toBe(17);expect(writes[0].b.config.mode).toBe("walk_forward");expect(writes[0].b.config.max_agent_decisions).toBe(1000);
 expect(button("Run / resume suite").disabled).toBe(false);
});

test("running validation reports agent progress and exposes pause and cancel",async()=>{
 mount();const calls:string[]=[];
 const running:any={...emptySuite,status:"running",plan:[{ordinal:0,fold:0,phase:"test",shock:{name:"baseline"}}],cases:[{ordinal:0,run_id:41,selected_candidate:0,status:"running",summary:{agent_waiting:true,processed_events:2,input_events:10}}]};
 const api:any=async(m:string,p:string)=>{if(m==="POST")calls.push(p);return p==="/validations"?{suites:[{id:9,name:"Check",status:"running"}]}:{suite:running}};
 await act(async()=>root.render(<ValidationControls sourceId={17} agent api={api} setError={e=>{if(e)throw Error(e)}}/>));
 const select=container.querySelector<HTMLSelectElement>('[aria-label="Validation suites"]')!;
 await act(async()=>{select.value="9";select.dispatchEvent(new Event("change",{bubbles:true}))});
 expect(container.textContent).toContain("Agent deciding");expect(container.textContent).toContain("2/10");
 expect(button("Download validation artifact").disabled).toBe(true);
 await act(async()=>button("Pause suite").click());await act(async()=>button("Cancel suite").click());
 expect(calls).toEqual(["/validations/9/pause","/validations/9/cancel"]);
});

test("validation reports explain conditional Monte Carlo frequencies",async()=>{
 mount();const api:any=async()=>({suites:[]});
 await act(async()=>root.render(<ValidationControls sourceId={17} agent={false} api={api} setError={()=>{}}/>));
 await act(async()=>button("New validation suite").click());
 const select=container.querySelector<HTMLSelectElement>('[aria-label="Validation method"]')!;
 await act(async()=>{select.value="monte_carlo";select.dispatchEvent(new Event("change",{bubbles:true}))});
 expect(container.textContent).toContain("conditional execution-risk estimates");expect(container.querySelector('[aria-label="Monte Carlo samples"]')).not.toBeNull();
});
