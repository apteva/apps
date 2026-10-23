import { expect, test } from "@playwright/test";

test("real browser audio: scoped interruption, partial receipts, silence, and subsequent speech", async ({page}) => {
  const receipts:any[]=[];let peer:any;
  await page.routeWebSocket("**/voice-test", socket=>{peer=socket;socket.onMessage(data=>{if(typeof data==="string")receipts.push(JSON.parse(data));});});
  await page.goto("/health");
  await page.evaluate(async()=>{
    const {VoiceAudioClient}=await import(/* @vite-ignore */ "/voiceAudio.js");
    const context=new AudioContext();await context.resume();const mic=context.createMediaStreamDestination();
    Object.defineProperty(navigator,"mediaDevices",{configurable:true,value:{getUserMedia:async()=>mic.stream}});
    const states:string[]=[];const client=new VoiceAudioClient((s:string)=>states.push(s),()=>{});
    Object.assign(window,{voiceTest:{client,states,context,mic}});
    await client.prepare("/realtime-capture-worklet.js");await client.connect(`ws://${location.host}/voice-test`);
  });
  await expect.poll(()=>receipts.some(x=>x.type==="playback.ready")).toBe(true);
  const frame=(id:string,ms:number,silence=false)=>{peer.send(JSON.stringify({type:"audio.frame",item_id:id,audio_end_ms:ms,silence}));const pcm=new Int16Array(ms*24);if(!silence)for(let i=0;i<pcm.length;i++)pcm[i]=Math.sin(i*0.06)*3000;peer.send(Buffer.from(pcm.buffer));};
  frame("A",1000);frame("B",200);
  await expect.poll(()=>receipts.some(x=>x.item_id==="A"&&x.audio_end_ms>50&&x.audio_end_ms<1000)).toBe(true);
  peer.send(JSON.stringify({type:"interrupt",item_id:"A"}));
  await expect.poll(()=>receipts.some(x=>x.type==="playback.cleared"&&x.item_id==="A")).toBe(true);
  frame("A",100); // Late canceled audio must never play or acknowledge completion.
  await expect.poll(()=>receipts.some(x=>x.item_id==="B"&&x.audio_end_ms===200)).toBe(true);
  expect(receipts.some(x=>x.item_id==="A"&&x.audio_end_ms===1000)).toBe(false);
  expect(receipts.filter(x=>x.type==="input.speech_started")).toHaveLength(0); // Playback is not microphone speech.
  frame("silent",200,true);
  await expect.poll(()=>receipts.some(x=>x.item_id==="silent"&&x.audio_end_ms===200)).toBe(true);
  expect(await page.evaluate(()=>(window as any).voiceTest.states.at(-1))).toBe("listening");
  await page.evaluate(()=>{
    const {context,mic}=(window as any).voiceTest;const oscillator=context.createOscillator();const gain=context.createGain();gain.gain.value=0.12;oscillator.connect(gain).connect(mic);oscillator.start();oscillator.stop(context.currentTime+0.35);
  });
  await expect.poll(()=>receipts.some(x=>x.type==="input.speech_started")).toBe(true);
  await expect.poll(()=>receipts.some(x=>x.type==="input.speech_stopped")).toBe(true);
  frame("next",200);await expect.poll(()=>receipts.some(x=>x.item_id==="next"&&x.audio_end_ms===200)).toBe(true);
  await page.evaluate(()=>{(window as any).voiceTest.client.close();void (window as any).voiceTest.context.close();});
});

test("real audio worklet preserves a tone across jittered 20ms packets without word-like cuts",async({page})=>{
 let peer:any;
 await page.routeWebSocket("**/voice-continuity",socket=>{peer=socket;socket.onMessage(()=>{});});
 await page.goto("/health");
 await page.evaluate(async()=>{
  const {VoiceAudioClient}=await import(/* @vite-ignore */ "/voiceAudio.js");const micContext=new AudioContext();await micContext.resume();const mic=micContext.createMediaStreamDestination();
  Object.defineProperty(navigator,"mediaDevices",{configurable:true,value:{getUserMedia:async()=>mic.stream}});
  const client=new VoiceAudioClient(()=>{},()=>{});await client.prepare("/realtime-capture-worklet.js");
  const context=(client as any).context;await context.audioWorklet.addModule("/voice-probe-worklet.js");const probe=new AudioWorkletNode(context,"continuity-probe");const samples:number[]=[];
  probe.port.onmessage=({data})=>samples.push(...data);(client as any).renderer.connect(probe).connect(context.destination);
  Object.assign(window,{continuity:{client,samples,probe,micContext,rate:context.sampleRate}});await client.connect(`ws://${location.host}/voice-continuity`);
 });
 await expect.poll(()=>!!peer).toBe(true);
 const started=Date.now();
 for(let i=0;i<50;i++){
  const at=started+i*20+[0,25,10,18][i%4];if(at>Date.now())await new Promise(r=>setTimeout(r,at-Date.now()));
  peer.send(JSON.stringify({type:"audio.frame",item_id:"tone",audio_end_ms:(i+1)*20,silence:false}));
  const pcm=Int16Array.from({length:480},(_,j)=>Math.round(Math.sin((i*480+j)*2*Math.PI*440/24000)*16000));peer.send(Buffer.from(pcm.buffer));
 }
 await expect.poll(()=>page.evaluate(()=>{const c=(window as any).continuity;const first=c.samples.findIndex((x:number)=>Math.abs(x)>0.01);return first>=0 && c.samples.length>first+c.rate*1.05;})).toBe(true);
 const stats=await page.evaluate(()=>{
  const {samples,rate}=(window as any).continuity;const start=samples.findIndex((x:number)=>Math.abs(x)>0.01),end=start+Math.floor(rate*0.985);let gap=0,maxGap=0,maxJump=0;
  for(let i=start+Math.ceil(rate*0.01);i<end;i++){gap=Math.abs(samples[i])<0.00001?gap+1:0;maxGap=Math.max(maxGap,gap);maxJump=Math.max(maxJump,Math.abs(samples[i]-samples[i-1]));}
  return {maxGap,maxJump,rate};
 });
 expect(stats.maxGap).toBeLessThan(8);expect(stats.maxJump).toBeLessThan(0.08);
 await page.evaluate(()=>{const c=(window as any).continuity;c.client.close();c.probe.disconnect();void c.micContext.close();});
});
