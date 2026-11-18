import {test,expect} from "bun:test";
import {AutomaticAnswer,transientCheckInFailure,type AutoAnswerSnapshot,type AutoAnswerTiming} from "../src/auto-answer";
import type {Call,TelephonyClient} from "../src/client";

const call=(id="incoming"):Call=>({id,direction:"inbound",status:"pending",peer_kind:"human",answerable:true,from_number:"+1",to_number:"+2",ring_offers:[{id:"offer-"+id,destination_id:"sales",kind:"browser"}]});
async function until(fn:()=>boolean,ms=2000){const end=Date.now()+ms;while(!fn()&&Date.now()<end)await Bun.sleep(2);expect(fn()).toBe(true);}
const httpError=(status:number)=>Object.assign(Error(`Telephony HTTP ${status}`),{status});
type CheckIn=(request:any)=>Promise<{designated:boolean;lease_ms?:number}>;

/** Short timings: check in every 30 ms, a 200 ms claim, retries after 10 then 20 ms. */
function fixture(timing:Partial<AutoAnswerTiming>={},delay=0){
 const checkIns:any[]=[],answers:any[]=[],states:AutoAnswerSnapshot[]=[];let busy=false,preparations=0,releases=0;
 let session:CheckIn=async()=>({designated:true,lease_ms:200});
 const client={
  autoAnswerSession:(v:any)=>{checkIns.push(v);return v.available?session(v):Promise.resolve({designated:false});},
  autoAnswerOffer:async(v:any)=>v.cancel?{canceled:true}:{eligible:true,destination_id:"sales",effective:{mode:"automatic",delay_ms:delay,source:"adviser",allow_adviser_override:true}},
 } as unknown as TelephonyClient;
 const auto=new AutomaticAnswer(client,{busy:()=>busy,prepare:async()=>{preparations++},releasePreparation:()=>{releases++},answer:async(...v)=>{answers.push(v);busy=true},changed:v=>states.push(v)},false,{heartbeatMs:30,retryMs:[10,20],recoveryMs:[40,40],...timing});
 const last=()=>states.at(-1);
 return {auto,checkIns,answers,states,last,setBusy(v:boolean){busy=v},setSession(v:CheckIn){session=v},get preparations(){return preparations},get releases(){return releases}};
}

test("classifies failures that may pass when repeated",()=>{
 expect(transientCheckInFailure(new DOMException("Media request cancelled","AbortError"))).toBe(true);
 expect(transientCheckInFailure(new TypeError("Failed to fetch"))).toBe(true);
 for(const status of [408,429,500,502,503])expect(transientCheckInFailure(httpError(status))).toBe(true);
 for(const status of [400,401,403,404,409])expect(transientCheckInFailure(httpError(status))).toBe(false);
});

test("one failed check-in, then a good one, keeps availability and the offer",async()=>{
 const f=fixture({},150);
 try{
  await f.auto.setAvailable(true);
  let failed=0;
  f.setSession(async()=>{if(failed++===0)throw new TypeError("Failed to fetch");return {designated:true,lease_ms:200};});
  f.auto.observe([call()]);
  await until(()=>f.last()?.state==="countdown");
  await until(()=>failed>=2);
  await until(()=>f.answers.length===1);
  expect(f.states.some(s=>s.state==="blocked")).toBe(false);
  expect(f.answers[0][0]).toBe("incoming");
  expect(f.releases).toBe(0);
  expect(f.preparations).toBe(1);
 }finally{f.auto.dispose()}
});

test("a retry follows quickly, not at the next regular check-in",async()=>{
 const f=fixture({heartbeatMs:300,retryMs:[10,20]});
 try{
  f.setSession(async()=>({designated:true,lease_ms:2000}));
  await f.auto.setAvailable(true);
  const times:number[]=[];let n=0;
  f.setSession(async()=>{times.push(Date.now());if(n++===0)throw httpError(503);return {designated:true,lease_ms:2000};});
  await until(()=>times.length>=2);
  expect(times[1]-times[0]).toBeLessThan(150);
  expect(f.last()?.state).toBe("available");
 }finally{f.auto.dispose()}
});

test("failures until the claim lapses block with heartbeat_failed, then recover in the background",async()=>{
 const f=fixture({recoveryMs:[40,40]});
 try{
  await f.auto.setAvailable(true);
  let attempts=0;
  f.setSession(async()=>{attempts++;throw httpError(503);});
  await until(()=>f.last()?.state==="blocked");
  expect(f.last()?.code).toBe("heartbeat_failed");
  expect(f.last()?.detail).toContain("Retrying automatically");
  expect(attempts).toBeGreaterThan(2);
  expect(f.releases).toBe(1);
  f.setSession(async()=>({designated:true,lease_ms:200}));
  await until(()=>f.last()?.state==="available");
  expect(f.last()?.code).toBeUndefined();
  expect(f.preparations).toBe(2);
  // It keeps checking in normally afterwards.
  const before=f.checkIns.length;await until(()=>f.checkIns.length>before+1);
  expect(f.last()?.state).toBe("available");
 }finally{f.auto.dispose()}
});

test("background recovery keeps trying while the server stays unreachable",async()=>{
 const f=fixture({recoveryMs:[20,20]});
 try{
  await f.auto.setAvailable(true);
  f.setSession(async()=>{throw new TypeError("Failed to fetch");});
  await until(()=>f.last()?.code==="heartbeat_failed");
  await until(()=>f.preparations>=3);
  expect(f.last()?.state==="blocked"||f.last()?.state==="preparing").toBe(true);
  f.setSession(async()=>({designated:true,lease_ms:200}));
  await until(()=>f.last()?.state==="available");
 }finally{f.auto.dispose()}
});

test("background recovery waits while a call is active",async()=>{
 const f=fixture({recoveryMs:[20,20]});
 try{
  await f.auto.setAvailable(true);
  f.setSession(async()=>{throw httpError(502);});
  await until(()=>f.last()?.code==="heartbeat_failed");
  f.setBusy(true);
  const prepared=f.preparations;
  f.setSession(async()=>({designated:true,lease_ms:200}));
  await Bun.sleep(80);
  expect(f.preparations).toBe(prepared);
  expect(f.last()?.state).toBe("blocked");
  f.setBusy(false);
  await until(()=>f.last()?.state==="available");
 }finally{f.auto.dispose()}
});

test("another browser taking over blocks with moved_to_another_browser and never retries",async()=>{
 const f=fixture({recoveryMs:[20,20]});
 try{
  await f.auto.setAvailable(true);
  f.setSession(async()=>({designated:false,lease_ms:200}));
  await until(()=>f.last()?.state==="blocked");
  expect(f.last()?.code).toBe("moved_to_another_browser");
  const count=f.checkIns.length;
  await Bun.sleep(100);
  expect(f.checkIns.length).toBe(count);
  expect(f.preparations).toBe(1);
 }finally{f.auto.dispose()}
});

test("a refused check-in blocks with session_rejected and is not retried",async()=>{
 const f=fixture({recoveryMs:[20,20]});
 try{
  await f.auto.setAvailable(true);
  f.setSession(async()=>{throw httpError(403);});
  await until(()=>f.last()?.state==="blocked");
  expect(f.last()?.code).toBe("session_rejected");
  const count=f.checkIns.length;
  await Bun.sleep(100);
  expect(f.checkIns.length).toBe(count);
 }finally{f.auto.dispose()}
});

test("registration failures carry a code",async()=>{
 const moved=fixture();
 try{
  moved.setSession(async()=>({designated:false}));
  const error=await moved.auto.setAvailable(true).catch(e=>e);
  expect(error.code).toBe("moved_to_another_browser");
  expect(moved.last()).toMatchObject({state:"blocked",code:"moved_to_another_browser"});
 }finally{moved.auto.dispose()}
 const unreachable=fixture({recoveryMs:[20,20]});
 try{
  unreachable.setSession(async()=>{throw httpError(503);});
  await expect(unreachable.auto.setAvailable(true)).rejects.toThrow("503");
  expect(unreachable.last()).toMatchObject({state:"blocked",code:"heartbeat_failed"});
  unreachable.setSession(async()=>({designated:true,lease_ms:200}));
  await until(()=>unreachable.last()?.state==="available");
 }finally{unreachable.auto.dispose()}
});

test("dispose and turning availability off stop background recovery",async()=>{
 const f=fixture({recoveryMs:[30,30]});
 await f.auto.setAvailable(true);
 f.setSession(async()=>{throw httpError(503);});
 await until(()=>f.last()?.code==="heartbeat_failed");
 await f.auto.setAvailable(false);
 const prepared=f.preparations;
 await Bun.sleep(80);
 expect(f.preparations).toBe(prepared);
 expect(f.last()?.state).toBe("off");
 f.setSession(async()=>{throw httpError(503);});
 await f.auto.setAvailable(true).catch(()=>{});
 expect(f.last()?.code).toBe("heartbeat_failed");
 f.auto.dispose();
 const disposed=f.preparations;
 await Bun.sleep(80);
 expect(f.preparations).toBe(disposed);
});

test("a claim that lapses during a countdown cancels that pick-up as a reported cancel",async()=>{
 const f=fixture({recoveryMs:[5000,5000]},5000);
 try{
  await f.auto.setAvailable(true);
  f.auto.observe([call()]);
  await until(()=>f.last()?.state==="countdown");
  f.setSession(async()=>{throw httpError(503);});
  await until(()=>f.last()?.state==="blocked");
  expect(f.states.some(s=>s.code==="pickup_canceled"&&s.eventCallId==="incoming")).toBe(true);
  expect(f.last()).toMatchObject({code:"heartbeat_failed",callId:undefined});
  expect(f.answers).toHaveLength(0);
 }finally{f.auto.dispose()}
});

test("background recovery goes from blocked to available without reporting off",async()=>{
 const f=fixture({recoveryMs:[20,20]});
 try{
  await f.auto.setAvailable(true);
  f.setSession(async()=>{throw httpError(503);});
  await until(()=>f.last()?.code==="heartbeat_failed");
  const from=f.states.length;
  f.setSession(async()=>({designated:true,lease_ms:200}));
  await until(()=>f.last()?.state==="available");
  expect(f.states.slice(from).map(s=>s.state)).toEqual(["preparing","available"]);
 }finally{f.auto.dispose()}
});
