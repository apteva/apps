import {test,expect} from "bun:test";
import {mediaRecoveryContext,safeMediaSessionEvent} from "../src/media-lease";

test("structured events retain correlation but exclude arbitrary errors, tokens and URLs",()=>{
 const context=mediaRecoveryContext("automatic_retry"),second=mediaRecoveryContext("manual_reconnect");
 expect(context.recovery_id).not.toBe(second.recovery_id);expect(context.attempt_id).not.toBe(second.attempt_id);
 const event=safeMediaSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"failed",...context,session_id:"session-a",detail:"https://user:DO_NOT_STORE@local/softphone/media/call/DO_NOT_STORE",token:"DO_NOT_STORE",media_url:"DO_NOT_STORE"} as any);
 expect(event).toMatchObject(context);expect(event.session_id).toBe("session-a");expect(event.detail).toBe("detail_redacted");
 expect(JSON.stringify(event)).not.toContain("DO_NOT_STORE");
 const malicious=safeMediaSessionEvent({timestamp:"DO_NOT_STORE",action:"https://DO_NOT_STORE",outcome:"failed",code:"Bearer DO_NOT_STORE",recovery_id:"DO_NOT_STORE",initiating_action:"DO_NOT_STORE"} as any);
 expect(JSON.stringify(malicious)).not.toContain("DO_NOT_STORE");
});

test("PCM session collection is paced and bounded; blocked telemetry never sends synchronously",async()=>{
 const {SoftphoneSession}=await import("../../ui/softphone-audio");
 const session:any=new SoftphoneSession({});const sent:any[]=[];
 session.worker={postMessage:(v:any)=>sent.push(v)};session.mediaSocketConnected=true;
 session.diagnostics.websocketBufferedBytes=3000;
 try{
  for(let i=0;i<100;i++)session.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:"attempt_started",attempt_id:`attempt-${i.toString(16)}`});
  expect(sent).toHaveLength(0);expect(session.diagnostics.sessionEvents).toHaveLength(50);
  expect(session.transportSender.skippedAuxiliary).toBe(68);
  session.transportSender.tick();expect(sent).toHaveLength(0);
  session.diagnostics.websocketBufferedBytes=0;session.transportSender.tick();
  expect(sent).toHaveLength(1);expect(sent[0].type).toBe("send.telemetry");
  expect(new TextEncoder().encode(sent[0].data).byteLength).toBeLessThanOrEqual(1100);
  expect(JSON.parse(sent[0].data).diagnostics.session_events).toHaveLength(1);
 }finally{session.transportSender.stop();}
});
