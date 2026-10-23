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
