import {test,expect} from "bun:test";
import {AutomaticAnswer,type AutoAnswerSnapshot} from "../src/auto-answer";
import type {Call,TelephonyClient} from "../src/client";
const call=(id="incoming"):Call=>({id,direction:"inbound",status:"pending",peer_kind:"human",answerable:true,from_number:"+1",to_number:"+2",ring_offers:[{id:"offer-"+id,destination_id:"sales",kind:"browser"}]});
async function until(fn:()=>boolean){for(let i=0;i<650&&!fn();i++)await Bun.sleep(2);expect(fn()).toBe(true)}
function fixture(delay=0){
 const requests:any[]=[],answers:any[]=[],states:AutoAnswerSnapshot[]=[];let busy=false,designated=true,eligible=true,preparations=0,release=0;
 let prepare=async()=>{};let reserve=async()=>({eligible,destination_id:"sales",effective:{mode:"automatic",delay_ms:delay,source:"adviser",allow_adviser_override:true}});
 const client={autoAnswerSession:async(v:any)=>{requests.push({kind:"session",...v});return {designated:v.available&&designated}},autoAnswerOffer:async(v:any)=>{requests.push({kind:"offer",...v});return v.cancel?{canceled:true}:reserve()}} as unknown as TelephonyClient;
 const auto=new AutomaticAnswer(client,{busy:()=>busy,prepare:async()=>{preparations++;await prepare()},releasePreparation:()=>{release++},answer:async(...v)=>{answers.push(v);busy=true},changed:v=>states.push(v)},false);
 return {auto,requests,answers,states,setBusy(v:boolean){busy=v},setDesignated(v:boolean){designated=v},setEligible(v:boolean){eligible=v},setPrepare(v:()=>Promise<void>){prepare=v},setReserve(v:typeof reserve){reserve=v},get preparations(){return preparations},get releases(){return release}};
}
test("manual default never starts devices, sessions or answers",async()=>{const f=fixture();try{f.auto.observe([call()]);await Bun.sleep(10);expect(f.requests).toHaveLength(0);expect(f.answers).toHaveLength(0);expect(f.preparations).toBe(0)}finally{f.auto.dispose()}});
test("zero delay answers as soon as the reservation arrives, exactly once",async()=>{const f=fixture();try{await f.auto.setAvailable(true);f.auto.observe([call()]);f.auto.observe([call()]);await until(()=>f.answers.length===1);expect(f.answers[0][0]).toBe("incoming");expect(f.answers[0][1]).toBe("sales");expect(f.preparations).toBe(1);expect(f.states.some(v=>v.state==="countdown")).toBe(false);f.auto.observe([call()]);await Bun.sleep(10);expect(f.answers).toHaveLength(1)}finally{f.auto.dispose()}});
test("unready microphone cannot register availability or answer",async()=>{const f=fixture();f.setPrepare(async()=>{throw Error("microphone denied")});try{await expect(f.auto.setAvailable(true)).rejects.toThrow("microphone denied");f.auto.observe([call()]);expect(f.requests).toHaveLength(0);expect(f.answers).toHaveLength(0);expect(f.states.at(-1)?.state).toBe("blocked")}finally{f.auto.dispose()}});
test("another browser remains designated until explicit takeover",async()=>{const f=fixture();f.setDesignated(false);try{await expect(f.auto.setAvailable(true)).rejects.toThrow("Another browser");f.auto.observe([call()]);expect(f.answers).toHaveLength(0);f.setDesignated(true);await f.auto.setAvailable(true,true);expect(f.requests.find(v=>v.takeover===true)).toBeDefined()}finally{f.auto.dispose()}});
test("configured delay can be canceled without declining the offer",async()=>{const f=fixture(80);try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>f.states.at(-1)?.state==="countdown");f.auto.cancel();await Bun.sleep(100);f.auto.observe([call()]);expect(f.answers).toHaveLength(0);expect(f.requests.some(v=>v.cancel)).toBe(true)}finally{f.auto.dispose()}});
test("Answer now bypasses only the configured countdown",async()=>{const f=fixture(200);try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>f.states.at(-1)?.state==="countdown");f.auto.answerNow();f.auto.answerNow();await until(()=>f.answers.length===1);await Bun.sleep(220);expect(f.answers).toHaveLength(1)}finally{f.auto.dispose()}});
test("hangup or withdrawn offer during countdown never answers",async()=>{const f=fixture(60);try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>f.states.at(-1)?.state==="countdown");f.auto.observe([{...call(),status:"completed"}]);await Bun.sleep(80);expect(f.answers).toHaveLength(0)}finally{f.auto.dispose()}});
test("manual answer or removed offer while reservation is in flight suppresses late auto-answer",async()=>{
 for(const mode of ["manual","removed"]){const f=fixture();let resolve!:(v:any)=>void;f.setReserve(()=>new Promise(r=>{resolve=r}));try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>Boolean(resolve));if(mode==="manual")f.auto.cancel("incoming");else f.auto.observe([]);resolve({eligible:true,destination_id:"sales",effective:{mode:"automatic",delay_ms:0,source:"project"}});await Bun.sleep(10);expect(f.answers).toHaveLength(0);expect(f.requests.some(v=>v.cancel)).toBe(true)}finally{f.auto.dispose()}}
});
test("occupied softphone and ineligible offers preserve manual behavior",async()=>{const f=fixture();try{await f.auto.setAvailable(true);f.setBusy(true);f.auto.observe([call()]);await Bun.sleep(5);expect(f.requests.filter(v=>v.kind==="offer")).toHaveLength(0);f.setBusy(false);f.setEligible(false);f.auto.observe([call()]);await Bun.sleep(10);expect(f.answers).toHaveLength(0)}finally{f.auto.dispose()}});
test("simultaneous calls, repeated notifications, and dispose never issue two answers",async()=>{const f=fixture();try{await f.auto.setAvailable(true);for(let i=0;i<20;i++)f.auto.observe([call("a"),call("b")]);await until(()=>f.answers.length===1);expect(f.answers).toHaveLength(1)}finally{f.auto.dispose()}});
test("becoming unavailable cancels a pending countdown",async()=>{const f=fixture(60);try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>f.states.at(-1)?.state==="countdown");await f.auto.setAvailable(false);await Bun.sleep(80);expect(f.answers).toHaveLength(0);expect(f.states.at(-1)?.available).toBe(false)}finally{f.auto.dispose()}});

function warningFixture({window=1000,ttl=5000,delay=0}={}){
 const states:AutoAnswerSnapshot[]=[],sequence:string[]=[],answers:any[]=[],requests:any[]=[];let busy=false,warningSignal:AbortSignal|undefined;
 let finishWarning!:()=>void,failWarning!:(e:unknown)=>void;
 const warning=new Promise<void>((resolve,reject)=>{finishWarning=resolve;failWarning=reject});
 const client={autoAnswerSession:async(v:any)=>{requests.push(v);return {designated:v.available}},autoAnswerOffer:async(v:any)=>{requests.push(v);return v.cancel?{canceled:true}:{eligible:true,destination_id:'sales',offer_id:'offer-incoming',offer_ttl_ms:ttl,effective:{mode:'automatic',delay_ms:delay,source:'adviser',warning_tone:{enabled:true,window_ms:window}}}}} as unknown as TelephonyClient;
 const auto=new AutomaticAnswer(client,{busy:()=>busy,prepare:async()=>{},releasePreparation(){},warning:async(_ms,signal)=>{sequence.push('warning');warningSignal=signal;await warning;sequence.push('played')},answer:async(...args)=>{sequence.push('answer');answers.push(args);busy=true},changed:s=>states.push(s)},false);
 return {auto,states,sequence,answers,requests,finishWarning,failWarning,get signal(){return warningSignal},setBusy(){busy=true}};
}
test('warning completion gates zero-delay answer and repeated offers cause one cue/answer',async()=>{
 const f=warningFixture();try{await f.auto.setAvailable(true);for(let i=0;i<20;i++)f.auto.observe([call()]);await until(()=>Boolean(f.signal));f.auto.answerNow();expect(f.answers).toHaveLength(0);expect(f.sequence).toEqual(['warning']);f.finishWarning();await until(()=>f.answers.length===1);expect(f.sequence).toEqual(['warning','played','answer']);expect(f.answers[0][3]).toBe(true);expect(f.states.some(s=>s.code==='warning_completed')).toBe(true);f.auto.observe([call()]);await Bun.sleep(5);expect(f.answers).toHaveLength(1)}finally{f.auto.dispose()}
});
for(const mode of ['cancel','absent','withdraw','same-call-offer-withdrawn','other-call','expire','dispose'] as const)test(`${mode} during warning never answers, including late playback completion`,async()=>{
 const f=warningFixture({ttl:mode==='expire'?35:5000});try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>Boolean(f.signal));
 if(mode==='cancel')f.auto.cancel();if(mode==='absent')await f.auto.setAvailable(false);if(mode==='withdraw')f.auto.observe([]);if(mode==='same-call-offer-withdrawn')f.auto.observe([{...call(),ring_offers:[]}]);if(mode==='other-call'){f.setBusy();f.auto.observe([call()])}if(mode==='expire')await Bun.sleep(50);if(mode==='dispose')f.auto.dispose();
 expect(f.signal!.aborted).toBe(true);f.finishWarning();await Bun.sleep(5);expect(f.answers).toHaveLength(0);expect(f.requests.some(r=>r.cancel===true)).toBe(true);
 }finally{f.auto.dispose()}
});
test('blocked warning preserves manual answering and exposes a structured failure',async()=>{
 const f=warningFixture();try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>Boolean(f.signal));f.failWarning(Object.assign(Error('blocked'),{code:'warning_playback_blocked'}));await until(()=>f.states.at(-1)?.code==='warning_playback_blocked');expect(f.states.at(-1)?.manualAnswerAvailable).toBe(true);f.auto.observe([call()]);await Bun.sleep(5);expect(f.answers).toHaveLength(0);expect(f.sequence).toEqual(['warning']);}finally{f.auto.dispose()}
});
test('a withdrawn handled offer replay cannot play another cue',async()=>{
 const f=warningFixture();try{await f.auto.setAvailable(true);f.auto.observe([call()]);await until(()=>Boolean(f.signal));f.auto.observe([]);f.finishWarning();await Bun.sleep(5);f.auto.observe([call()]);await Bun.sleep(5);expect(f.sequence.filter(v=>v==='warning')).toHaveLength(1);expect(f.answers).toHaveLength(0)}finally{f.auto.dispose()}
});
