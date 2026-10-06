import {outputWallTimeMS} from './audio-clock';
import {SoftphoneSession, DEFAULT_SOFTPHONE_AUDIO_OPTIONS} from '../../ui/softphone-audio';

function tone(frame:Float32Array,rate:number):[number,number]{
 const energy=frame.reduce((n,x)=>n+x*x,0);if(energy/frame.length<(100/32768)**2)return[-1,-120];
 let best=0,symbol=-1;
 for(let n=0;n<17;n++){const frequency=n===16?3000:600+n*100,coef=2*Math.cos(2*Math.PI*frequency/rate);let a=0,b=0;
  for(const x of frame){const value=x+coef*a-b;b=a;a=value;}const power=a*a+b*b-coef*a*b;if(power>best){best=power;symbol=n;}}
 return best<energy*frame.length*.3?[-1,-120]:[symbol,20*Math.log10(Math.sqrt(energy/frame.length))];
}
(window as any).runBenchmark=async(config:any)=>{
 const errors:string[]=[],states:any[]=[],diagnostics:any[]=[],notices:any[]=[];
 const sourceContext=new AudioContext({sampleRate:24000});await sourceContext.audioWorklet.addModule('/probe.js');await sourceContext.resume();
 const source=new AudioWorkletNode(sourceContext,'benchmark-source'),destination=sourceContext.createMediaStreamDestination();source.connect(destination);
 const original=navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
 // Controlled signal replaces the hardware microphone only. Capture DSP,
 // framing, worker, WebSockets, Go bridge and carrier codec still execute.
 navigator.mediaDevices.getUserMedia=async()=>destination.stream;
 const session:any=new SoftphoneSession({refreshMediaURL:async()=>{
  const response=await fetch('/refresh-media',{method:'POST'});
  if(!response.ok)throw Object.assign(new Error('Local media attach failed'),{status:response.status});
  return (await response.json()).media_url;
 },onNotice:detail=>notices.push({detail,at:Date.now()}),onState:(state,detail)=>states.push({state,detail,at:Date.now()}),onDiagnostics:d=>diagnostics.push({at:Date.now(),...d})});
 const wireDiagnostics:unknown[]=[];
 const sendText=session.sendText.bind(session);
 session.sendText=(data:string)=>{
  const message=JSON.parse(data);
  if(message.type==='diagnostics'){wireDiagnostics.push(message);if(wireDiagnostics.length>4)wireDiagnostics.shift();}
  sendText(data);
 };
 let probe:AudioWorkletNode|undefined;
 try{
  await session.start(config.media_url,'/worklet.js','/worker.js',DEFAULT_SOFTPHONE_AUDIO_OPTIONS);
  const deadline=Date.now()+10000;
  while(!states.some(x=>x.state==='live')&&Date.now()<deadline)await new Promise(r=>setTimeout(r,20));
  if(!states.some(x=>x.state==='live'))throw new Error('carrier media did not connect');
  const ctx:AudioContext=session.ctx;
  await ctx.audioWorklet.addModule('/probe.js');probe=new AudioWorkletNode(ctx,'benchmark-output');
  const silent=ctx.createGain();silent.gain.value=0;session.playback.connect(probe);probe.connect(silent);silent.connect(ctx.destination);
  const markers:any[]=[];let last=-2,stable=0,segment=false,symbols:number[]=[],start=-1000,level=-120;
  const arm=await(await fetch('/arm',{method:'POST'})).json();
  const sourceStart=sourceContext.currentTime+(arm.start_at-Date.now())/1000;
  source.port.postMessage({start:sourceStart});
  probe.port.onmessage=event=>{
   const [symbol,db]=tone(event.data.frame,ctx.sampleRate),end=outputWallTimeMS(ctx,event.data.end,performance.timeOrigin,performance.now());
   if(symbol<0){segment=false;stable=0;last=-2;return;}
   stable=symbol===last?stable+1:1;last=symbol;
   if(stable<2||segment)return;segment=true;
   if(symbol===16){symbols=[];start=end-20;level=db;}else if(end-start<260){symbols.push(symbol);if(symbols.length===3){
    const [hi,lo,check]=symbols;if((hi^lo^10)===check)markers.push({id:hi*16+lo,at_ms:start,level_dbfs:level});symbols=[];start=-1000;
   }}
  };
  // Report render-clock stalls separately from nominal marker latency. A
  // synthetic source can emit its tone late before the softphone sees it.
  const clockBase={wall:performance.now(),source:sourceContext.currentTime,playback:ctx.currentTime};
  const clockProgress={wall_elapsed_ms:0,source_elapsed_ms:0,playback_elapsed_ms:0,max_source_lag_ms:0,max_playback_lag_ms:0};
  const finish=arm.start_at+config.duration_ms+config.drain_ms;
  let muted=false,unmuted=false,reconnected=false;
  while(Date.now()<finish) {
    const elapsed=Date.now()-arm.start_at;
    clockProgress.wall_elapsed_ms=performance.now()-clockBase.wall;
    clockProgress.source_elapsed_ms=(sourceContext.currentTime-clockBase.source)*1000;
    clockProgress.playback_elapsed_ms=(ctx.currentTime-clockBase.playback)*1000;
    clockProgress.max_source_lag_ms=Math.max(clockProgress.max_source_lag_ms,clockProgress.wall_elapsed_ms-clockProgress.source_elapsed_ms);
    clockProgress.max_playback_lag_ms=Math.max(clockProgress.max_playback_lag_ms,clockProgress.wall_elapsed_ms-clockProgress.playback_elapsed_ms);
    if(config.mute_microphone && elapsed>=4000 && !muted) {session.setMuted(true);muted=true;}
    if(config.mute_microphone && elapsed>=6000 && !unmuted) {session.setMuted(false);unmuted=true;}
    if(config.reconnect_browser && elapsed>=4000 && !reconnected) {reconnected=true;await fetch('/disconnect-browser',{method:'POST'});}
    await new Promise(r=>setTimeout(r,100));
  }
  const result={clock_progress:clockProgress,wire_diagnostics:wireDiagnostics,markers,states,notices,diagnostics:diagnostics.slice(-8),audio_context_rate:ctx.sampleRate,source_context_rate:sourceContext.sampleRate,start_at:arm.start_at,output_clock_mapping_uncertainty_ms:20,errors};
  session.sendDiagnostics();return result;
 }finally{navigator.mediaDevices.getUserMedia=original;probe?.disconnect();session.stop();source.disconnect();await sourceContext.close();}
};
