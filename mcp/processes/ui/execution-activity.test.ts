import {test,expect} from "bun:test";
import {eventsForStep,thoughtsFromEvents, type TelemetryEvent} from "./execution-activity";
const e=(type:string,sec:number,data:Record<string,unknown>):TelemetryEvent=>({id:`${type}-${sec}`,type,time:`2026-10-06T12:00:${String(sec).padStart(2,"0")}Z`,data});
test("completed step excludes later calls carrying ancestor execution IDs",()=>{
 const events=[e("tool.call",1,{name:"processes_step_claim",args:{step_id:"alpha"},execution_ids:["a"]}),e("tool.call",2,{name:"weather_get",execution_ids:["a"]}),e("llm.done",3,{iteration:1}),e("tool.call",5,{name:"processes_step_claim",args:{step_id:"beta"},execution_ids:["a","b"]}),e("tool.call",6,{name:"notification_send",execution_ids:["a","b"]})];
 expect(eventsForStep(events,"a","alpha","2026-10-06T12:00:04Z").map(e=>e.data?.name || e.type)).toEqual(["processes_step_claim","weather_get","llm.done"]);
 expect(eventsForStep(events,"b","beta").map(e=>e.data?.name || e.type)).toEqual(["processes_step_claim","notification_send"]);
});
test("explicit execution IDs keep unrelated model events out",()=>{
 const events=[e("tool.call",1,{name:"processes_step_claim",args:{step_id:"alpha"},execution_ids:["a"]}),e("llm.thinking",2,{iteration:2,text:"Wrong step",execution_ids:["b"]}),e("llm.thinking",3,{iteration:2,text:"Exact recorded summary",execution_ids:["a"]})];
 expect(eventsForStep(events,"a","alpha").some(e=>e.data?.text==="Wrong step")).toBe(false);
});
test("streamed reasoning and response become one turn without displaying reasoning settings",()=>{
 const turns=thoughtsFromEvents([e("llm.start",1,{iteration:1}),e("llm.thinking",2,{iteration:1,text:"Check "}),e("llm.thinking",3,{iteration:1,text:"the receipt."}),e("llm.chunk",4,{iteration:1,text:"Weather loaded."}),e("llm.done",5,{iteration:1,reasoning:"high"})]);
 expect(turns).toHaveLength(1);expect(turns[0].reasoning).toBe("Check the receipt.");expect(turns[0].response).toBe("Weather loaded.");expect(turns[0].status).toBe("completed");
});
