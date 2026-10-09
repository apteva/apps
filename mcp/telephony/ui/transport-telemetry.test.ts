import {test,expect} from "bun:test";
import {TransportTelemetry,TRANSPORT_METRICS,transportMetrics,transportStates} from "./transport-telemetry";
const epoch=Date.UTC(2026,9,9);
const timestamp=(ms:number)=>new Date(epoch+ms).toISOString();
test("bounded history, pending reports and bandwidth; absent metrics stay absent and secrets are excluded",()=>{
 const t=new TransportTelemetry("websocket");
 const metrics=Object.fromEntries(TRANSPORT_METRICS.map(k=>[k,1e12]));
 for(let i=0;i<1000;i++)t.observe({...metrics,dropped_ms:i,url:"wss://secret",rtt_ms:NaN},{protocol:"udp",address:"private"},i*1000,timestamp(i*1000));
 expect(t.recent()).toHaveLength(24);
 let count=0;for(let batch=t.drain();batch.length;batch=t.drain()){count+=batch.length;expect(batch.length).toBeLessThanOrEqual(2);expect(JSON.stringify(batch).length).toBeLessThan(8192);expect(JSON.stringify(batch)).not.toContain("secret");expect(JSON.stringify(batch)).not.toContain("private");}
 expect(count).toBe(8);expect(transportMetrics({rtt_ms:NaN,queue_ms:-1,extra:3})).toEqual({queue_ms:0});expect(transportStates({muted:"false",protocol:"garbage"})).toEqual({muted:"false"});
});
test("short incident is preserved with pre-incident timestamp until next bounded emission",()=>{
 const t=new TransportTelemetry("websocket");
 t.observe({dropped_ms:0},{},0,timestamp(0));t.drain();
 t.observe({dropped_ms:0},{},1000,timestamp(1000));
 t.observe({dropped_ms:20},{},2000,timestamp(2000));
 t.observe({dropped_ms:20},{},5000,timestamp(5000));
 const samples=t.drain();expect(samples.map(s=>s.reason)).toEqual(["incident_context","incident"]);
 expect(samples[0].timestamp).toBe(timestamp(1000));expect(samples[1].timestamp).toBe(timestamp(2000));expect(samples[1].window_ms).toBe(1000);
});
test("mute and clock/counter resets never invent lost audio; observer mutations cannot alter queued wire reports",()=>{
 const t=new TransportTelemetry("websocket");
 t.observe({capture_muted_ms:100,capture_dropped_ms:0},{muted:"true"},0,timestamp(0));t.drain();
 t.observe({capture_muted_ms:5100,capture_dropped_ms:0},{muted:"true"},5000,timestamp(5000));
 const publicHistory=t.recent();publicHistory[1].metrics.capture_dropped_ms=1000;
 expect(t.drain()[0].metrics.capture_dropped_ms).toBe(0);
 t.observe({capture_muted_ms:0,capture_dropped_ms:0},{muted:"false"},1,timestamp(6000));
 expect(t.drain()[0].reason).toBe("periodic");
 t.observe({capture_muted_ms:0,capture_dropped_ms:0},{muted:"false"},6001,timestamp(12000));expect(t.drain()[0].reason).toBe("periodic");
});

test("rich monitoring is split, paced, bounded and isolated from send failures",async()=>{
 const {transportSampleParts,TransportTelemetrySender}=await import("./transport-telemetry");
 const sample={id:"s1",timestamp:timestamp(0),transport:"webrtc" as const,reason:"periodic" as const,window_ms:1000,metrics:Object.fromEntries(TRANSPORT_METRICS.map(k=>[k,1e12])),states:{protocol:"udp",local_candidate:"relay"}};
 const parts=transportSampleParts(sample);expect(parts.length).toBeGreaterThan(1);expect(parts.length).toBeLessThanOrEqual(16);
 const merged={metrics:{},states:{}};parts.forEach((part,i)=>{expect(JSON.stringify(part).length).toBeLessThanOrEqual(768);expect(part.part_index).toBe(i);expect(part.part_count).toBe(parts.length);Object.assign(merged.metrics,part.metrics);Object.assign(merged.states,part.states);});
 expect(merged).toEqual({metrics:sample.metrics,states:sample.states});
 const sent:any[]=[];let blocked=true,throws=false;
 const sender=new TransportTelemetrySender(p=>{if(throws)throw Error("monitor unavailable");if(blocked)return false;sent.push(p);return true;});
 try{sender.enqueue([sample]);sender.tick();expect(sent).toHaveLength(0);blocked=false;sender.tick();expect(sent).toHaveLength(1);throws=true;sender.tick();expect(sent).toHaveLength(1);throws=false;for(let i=1;i<parts.length;i++)sender.tick();expect(sent).toHaveLength(parts.length);
  for(let i=0;i<100;i++)sender.enqueue([sample]);expect((sender as any).pending.length).toBeLessThanOrEqual(32);
 }finally{sender.stop();}
});

test("WebRTC summaries and history share one pacing slot; latest summary replaces stale pending state",async()=>{
 const {TransportTelemetrySender}=await import("./transport-telemetry");const sent:string[]=[];
 const sender=new TransportTelemetrySender(()=>{sent.push("history");return true;},r=>{sent.push(String(r));return true;});
 try{sender.enqueue([{id:"s1",timestamp:timestamp(0),transport:"webrtc",reason:"periodic",window_ms:1000,metrics:{rtt_ms:20},states:{}}]);
  sender.enqueueReport("old-summary");sender.enqueueReport("latest-summary");sender.tick();expect(sent).toEqual(["latest-summary"]);
  sender.tick();expect(sent).toEqual(["latest-summary","history"]);
 }finally{sender.stop();}
});
