// Paid Tier 3 driver. Run only via apteva test scenarios/22-voice-duplex-browser.yaml.
import { chromium } from "@playwright/test";
import { join } from "node:path";
import { voiceTranscriptMessages } from "../src/voiceTranscript";
const env=process.env;
for(const name of ["APTEVA_RUN_CONVERSATIONS_VOICE_TIER3","OPENAI_API_KEY","APTEVA_TEST_SERVER_URL","APTEVA_TEST_SERVER_API_KEY","APTEVA_TEST_PROJECT_ID","APTEVA_TEST_INSTALLS","APTEVA_TEST_AGENT_IDS"]) if(!env[name])throw Error(`Missing runner environment ${name}`);
const base=env.APTEVA_TEST_SERVER_URL!,agent=JSON.parse(env.APTEVA_TEST_AGENT_IDS!)[0],install=JSON.parse(env.APTEVA_TEST_INSTALLS!).conversations;
const scope=`project_id=${encodeURIComponent(env.APTEVA_TEST_PROJECT_ID!)}&install_id=${install}`;
async function api(path:string,method="GET",body?:unknown){
 const url=base+path+(path.startsWith("/api/apps/")?(path.includes("?")?"&":"?")+scope:"");
 const r=await fetch(url,{method,headers:{Authorization:`Bearer ${env.APTEVA_TEST_SERVER_API_KEY}`,"Content-Type":"application/json"},...(body?{body:JSON.stringify(body)}:{})});
 if(!r.ok)throw Error(`Runner API ${method} ${path.split("?")[0]}: HTTP ${r.status}`);
 return r.status===204?null:r.json();
}
async function speech(text:string){
 const r=await fetch("https://api.openai.com/v1/audio/speech",{method:"POST",headers:{Authorization:`Bearer ${env.OPENAI_API_KEY}`,"Content-Type":"application/json"},body:JSON.stringify({model:"gpt-4o-mini-tts",voice:"alloy",input:text,instructions:"Parlez naturellement en français.",response_format:"pcm"})});
 if(!r.ok)throw Error(`Caller speech synthesis HTTP ${r.status}`);
 return Buffer.from(await r.arrayBuffer()).toString("base64");
}
const [opening,interruption]=await Promise.all([
 speech("Bonjour. Pouvez-vous expliquer en plusieurs phrases les informations à préparer pour prendre un rendez-vous ?"),
 speech("Non, attendez. Je veux simplement un rendez-vous mardi. Quel jour est-ce que je viens de demander ?")
]);
console.log("Caller audio synthesized; waiting for runner-owned telemetry subscription readiness.");
await Bun.sleep(65_000);
const root=join(import.meta.dir,"..");
const bundle=await Bun.build({entrypoints:[join(root,"src/voiceAudio.ts")],target:"browser",format:"esm"});
if(!bundle.success)throw Error("Voice client build failed");
const source=await bundle.outputs[0].text();
const host=Bun.serve({hostname:"127.0.0.1",port:0,fetch(req){const p=new URL(req.url).pathname;return p==="/voiceAudio.js"?new Response(source,{headers:{"Content-Type":"text/javascript"}}):p==="/worklet.js"?new Response(Bun.file(join(root,"../ui/realtime-capture-worklet.js")),{headers:{"Content-Type":"text/javascript"}}):new Response("<!doctype html><body>Conversations live voice browser test</body>",{headers:{"Content-Type":"text/html"}});}});
const browser=await chromium.launch({headless:true,args:["--autoplay-policy=no-user-gesture-required"]});
let chat="",voice:any;
const sent:any[]=[],received:any[]=[];let micFrames=0;
try{
 const conv=await api("/api/apps/conversations/chats","POST",{agent_id:agent,title:"Tier 3 duplex browser",directive:"Parlez français. Répondez naturellement. Pour expliquer la préparation d'un rendez-vous, commencez avec plusieurs phrases. Si la personne vous interrompt, écoutez et répondez à sa nouvelle demande. Ne prétendez jamais avoir réservé quoi que ce soit. Ne terminez pas la conversation."});chat=conv.id;
 voice=await api(`/api/apps/conversations/voice?chat_id=${chat}`,"POST");
 const page=await browser.newPage();
 page.on("websocket",socket=>{
  socket.on("framesent",({payload})=>{if(typeof payload==="string"){try{sent.push({...JSON.parse(payload),at:Date.now()});}catch{}}else micFrames++;});
  socket.on("framereceived",({payload})=>{if(typeof payload==="string"){try{received.push({...JSON.parse(payload),at:Date.now()});}catch{}}});
 });
 await page.goto(`http://127.0.0.1:${host.port}`);
 await page.evaluate(async(url)=>{
  const {VoiceAudioClient}=await import(/* @vite-ignore */ "/voiceAudio.js");
  const micContext=new AudioContext();await micContext.resume();const mic=micContext.createMediaStreamDestination();
  Object.defineProperty(navigator,"mediaDevices",{configurable:true,value:{getUserMedia:async()=>mic.stream}});
  const states:any[]=[];let disconnected=0;
  const client=new VoiceAudioClient((state:string)=>states.push({state,at:Date.now()}),()=>disconnected++);
  const play=async(base64:string)=>{const bytes=Uint8Array.from(atob(base64),x=>x.charCodeAt(0));const pcm=new Int16Array(bytes.buffer);const audio=micContext.createBuffer(1,pcm.length,24000);const samples=audio.getChannelData(0);for(let i=0;i<pcm.length;i++)samples[i]=pcm[i]/32768;const s=micContext.createBufferSource();s.buffer=audio;s.connect(mic);s.start();};
  Object.assign(window,{liveVoice:{client,play,states,micContext,get disconnected(){return disconnected;}}});
  await client.prepare("/worklet.js");await client.connect(url);
 },voice.audio_bridge_url);
 const wait=async(predicate:()=>boolean|Promise<boolean>,label:string,ms=45_000)=>{const until=Date.now()+ms;while(Date.now()<until){if(await predicate())return;await Bun.sleep(100);}throw Error(`Timed out: ${label}`);};
 await wait(()=>sent.some(x=>x.type==="playback.ready"),"browser playback.ready");
 await page.evaluate(pcm=>(window as any).liveVoice.play(pcm),opening);
 await wait(()=>received.some(x=>x.type==="audio.frame"&&!x.silence)&&sent.some(x=>x.type==="playback.progress"&&x.audio_end_ms>=200),"initial real voiced playback");
 // Interrupt only after the caller ends AND actual assistant speech is consumed
 // while more voiced PCM is still arriving. Silent receipts cannot satisfy this.
 await wait(()=>sent.some(x=>x.type==="input.speech_stopped"),"first caller speech end");
 const callerEnded=sent.find(x=>x.type==="input.speech_stopped").at;
 await wait(()=>received.some(f=>f.type==="audio.frame"&&!f.silence&&f.at>callerEnded&&Date.now()-f.at<400&&sent.some(p=>p.type==="playback.progress"&&p.item_id===f.item_id&&p.audio_end_ms>=f.audio_end_ms)),"ongoing consumed assistant speech after caller finished");
 const interruptionAt=Date.now(),micBefore=micFrames;
 const onsetsBefore=sent.filter(x=>x.type==="input.speech_started").length;
 await page.evaluate(pcm=>(window as any).liveVoice.play(pcm),interruption);
 await wait(()=>sent.filter(x=>x.type==="input.speech_started").length>onsetsBefore,"automatic browser interruption onset");
 await wait(()=>received.some(x=>x.type==="interrupt"&&x.reason==="renderer_speech_started"&&x.at>=interruptionAt),"Core scoped clear");
 const clears=received.filter(x=>x.type==="interrupt"&&x.at>=interruptionAt),cancelled=new Set(clears.map(x=>x.item_id));
 if(cancelled.has(undefined))throw Error("Core sent unscoped duplex interruption");
 await wait(()=>[...cancelled].every(id=>sent.some(x=>x.type==="playback.cleared"&&x.item_id===id&&x.at>=interruptionAt)),"browser clear receipts");
 await wait(()=>received.some(f=>f.type==="audio.frame"&&!f.silence&&!cancelled.has(f.item_id)&&f.at>interruptionAt+500&&sent.some(p=>p.type==="playback.progress"&&p.item_id===f.item_id&&p.audio_end_ms>=f.audio_end_ms)),"resumed consumed voiced browser playback");
 await wait(async()=>{const messages=await api(`/api/apps/conversations/messages?chat_id=${chat}`);return messages.some((m:any)=>m.role==="agent"&&/mardi/i.test(m.content));},"correct French response persisted in Conversations");
 // Wait for the caller's delayed caption, then exercise exactly the same
 // chronological presentation used by panels and embedded clients.
 await wait(async()=>{const messages=await api(`/api/apps/conversations/messages?chat_id=${chat}`);return messages.some((m:any)=>m.role==="user"&&/mardi/i.test(m.content));},"interruption transcript persisted");
 const saved=await api(`/api/apps/conversations/messages?chat_id=${chat}`);
 const displayed=voiceTranscriptMessages(saved);
 const callerIndex=displayed.findIndex((m:any)=>m.role==="user"&&/mardi/i.test(m.content));
 const replyIndex=displayed.findIndex((m:any)=>m.role==="agent"&&/mardi/i.test(m.content));
 if(callerIndex<0 || replyIndex<=callerIndex)throw Error("Assistant's acknowledgment appeared before the caller interruption");
 if(!saved.some((m:any)=>m.role==="agent"&&m.metadata?.transcript?.fragments?.length))throw Error("Core did not persist timed caption fragments");
 if(micFrames-micBefore<30)throw Error("Microphone stream stopped during interruption");
 for(const id of cancelled){const cleared=sent.find(x=>x.type==="playback.cleared"&&x.item_id===id);if(sent.some(x=>x.type==="playback.progress"&&x.item_id===id&&x.at>cleared.at))throw Error("Canceled audio acknowledged after clearing");}
 const events=await api(`/api/telemetry?agent_id=${agent}&thread_id=${voice.thread_id}&limit=1000`);
 const data=(e:any)=>typeof e.data==="string"?JSON.parse(e.data):e.data;
 if(!events.some((e:any)=>e.type==="realtime.session_opened"&&data(e).applied?.model==="gpt-live-1"))throw Error("Actual GPT Live model not confirmed");
 if(events.some((e:any)=>["realtime.error","realtime.execution_error","realtime.audio_overflow"].includes(e.type)))throw Error("Realtime error/overflow during browser workflow");
 if(events.some((e:any)=>e.type==="realtime.output_blocked"&&(data(e).reason??data(e).pattern)==="missing_output_transcript"))throw Error("Missing caption blocked live audio");
 const summary=await page.evaluate(()=>({disconnected:(window as any).liveVoice.disconnected,states:(window as any).liveVoice.states}));
 if(summary.disconnected)throw Error("Browser voice disconnected");
 const clearMS=Math.max(...clears.map(c=>sent.find(x=>x.type==="playback.cleared"&&x.item_id===c.item_id).at-c.at));
 console.log(JSON.stringify({model:"gpt-live-1",player:"Conversations VoiceAudioClient + AudioWorklet + Chromium Web Audio",transcriptOrder:"caller interruption before next assistant reply",scopedClears:cancelled.size,maxBrowserClearAckMS:clearMS,microphoneFramesDuringRecovery:micFrames-micBefore,realProgressReceipts:sent.filter(x=>x.type==="playback.progress").length,result:"French caller interrupted real assistant speech; correct next reply consumed by browser renderer and persisted; no missing-caption stops or disconnects"}));
 await page.evaluate(()=>{(window as any).liveVoice.client.close();void (window as any).liveVoice.micContext.close();});
}finally{
 if(chat){await api(`/api/apps/conversations/voice?chat_id=${chat}`,"DELETE").catch(()=>{});await api(`/api/apps/conversations/chats?id=${chat}`,"DELETE").catch(()=>{});}
 await browser.close();host.stop(true);
}
