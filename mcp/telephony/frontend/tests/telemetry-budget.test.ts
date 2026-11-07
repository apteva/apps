import {test,expect} from "bun:test";
import {TransportTelemetrySender,TRANSPORT_METRICS,type TransportSample} from "../../ui/transport-telemetry";
import {RTCControlBudget} from "../src/webrtc-quality";

test("production sized monitoring stays fresh at 640B/s including incident bursts",()=>{
 for(const incident of [false,true]){
  let now=0;const budget=new RTCControlBudget();const complete:number[]=[],parts=new Map<string,Set<number>>();let bytes=0;
  const wireBytes=(s:unknown)=>new TextEncoder().encode(JSON.stringify(s)).length+180;
  const sender=new TransportTelemetrySender(s=>{const n=wireBytes(s);if(!budget.consume(n,now))return false;bytes+=n;const p=parts.get(s.id)??new Set();p.add(s.part_index);parts.set(s.id,p);if(p.size===s.part_count)complete.push(now-Date.parse(s.timestamp));return true;},r=>{const n=wireBytes(r);if(!budget.consume(n,now))return false;bytes+=n;return true;},()=>now);
  try{for(now=0;now<=120000;now+=250){
   if(now%5000===0){
    const sample:TransportSample={id:`s${now}`,timestamp:new Date(now).toISOString(),transport:"webrtc",reason:incident?"incident":"periodic",window_ms:5000,metrics:Object.fromEntries(TRANSPORT_METRICS.map(k=>[k,12345.123])),states:{ice:"connected",context:"running",muted:"false"}};
    sender.enqueueReport({text:"s".repeat(470)});sender.enqueueAuxiliary({text:"r".repeat(87)});sender.enqueue(incident?[{...sample,id:sample.id+"context",reason:"incident_context"},sample]:[sample]);
   }sender.tick();
  }
  expect(complete.length).toBeGreaterThan(10);expect(Math.max(...complete)).toBeLessThanOrEqual(12000);expect(bytes).toBeLessThanOrEqual(640*120+1100);
  console.log(JSON.stringify({incident,complete:complete.length,maxAgeMS:Math.max(...complete),skipped:sender.skippedSamples,bytes}));
  }finally{sender.stop()}
 }
});
test("blocked telemetry expires without catchup and reconnect starts with fresh data",()=>{
 let now=0,blocked=true;const sent:any[]=[];
 const sender=new TransportTelemetrySender(s=>{if(blocked)return false;sent.push(s);return true;},()=>!blocked,()=>now);
 const sample=(id:string)=>({id,timestamp:new Date(now).toISOString(),transport:"websocket" as const,reason:"incident" as const,window_ms:0,metrics:{dropped_ms:20},states:{}});
 sender.enqueue([sample("old")]);sender.enqueueReport({state:"old"});sender.enqueueAuxiliary({state:"old"});sender.tick();
 now=13000;sender.tick();expect(sender.skippedSamples).toBe(1);expect(sender.skippedAuxiliary).toBe(2);
 blocked=false;sender.enqueue([sample("fresh")]);sender.tick();expect(sent.map(s=>s.id)).toEqual(["fresh"]);sender.stop();
});
