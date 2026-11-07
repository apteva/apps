import type {SoftphoneAudioOptions} from "./softphone-audio";
export class AutoAnswerWarningError extends Error {
 constructor(readonly code:string,message:string){super(message);this.name="AutoAnswerWarningError";}
}
/** Output-only graph: never connected to capture, a MediaStream, or a carrier. */
export async function playPreparedWarning(context:AudioContext,options:Pick<SoftphoneAudioOptions,"outputDeviceId"|"outputVolume">,windowMs:number,signal:AbortSignal):Promise<void>{
 signal.throwIfAborted();
 if(context.state!=="running")throw new AutoAnswerWarningError("warning_playback_blocked","Enable browser audio, or answer manually.");
 if((options.outputVolume??1)<=0)throw new AutoAnswerWarningError("warning_output_muted","Turn up the selected audio output, or answer manually.");
 if(options.outputDeviceId&&options.outputDeviceId!=="default"){
  const output=context as AudioContext&{setSinkId?:(id:string)=>Promise<void>;sinkId?:string};
  if(!output.setSinkId)throw new AutoAnswerWarningError("warning_output_unsupported","This browser cannot use the selected output for the warning. Answer manually.");
  let timer:ReturnType<typeof setTimeout>|undefined,cancel:(()=>void)|undefined;
  try{await Promise.race([output.setSinkId(options.outputDeviceId),new Promise<never>((_,reject)=>{cancel=()=>reject(signal.reason??new DOMException("Cancelled","AbortError"));signal.addEventListener("abort",cancel,{once:true});if(signal.aborted)cancel();timer=setTimeout(()=>reject(Error("Output selection timed out")),1500)})])}
  catch{signal.throwIfAborted();throw new AutoAnswerWarningError("warning_output_failed","Check the selected headset, or answer manually.")}
  finally{clearTimeout(timer);if(cancel)signal.removeEventListener("abort",cancel)}
  signal.throwIfAborted();
  if(typeof output.sinkId==="string"&&output.sinkId!==options.outputDeviceId)throw new AutoAnswerWarningError("warning_output_failed","The selected audio output is unavailable. Answer manually.");
 }
 signal.throwIfAborted();
 return new Promise<void>((resolve,reject)=>{
  let oscillator:OscillatorNode|undefined,gain:GainNode|undefined,played=false,windowElapsed=false,settled=false;
  let windowTimer:ReturnType<typeof setTimeout>|undefined,deadline:ReturnType<typeof setTimeout>|undefined;
  const cleanup=()=>{clearTimeout(windowTimer);clearTimeout(deadline);signal.removeEventListener("abort",cancel);context.removeEventListener("statechange",stateChange);if(oscillator){oscillator.onended=null;try{oscillator.stop()}catch{}oscillator.disconnect()}gain?.disconnect()};
  const finish=(error?:unknown)=>{if(settled)return;settled=true;cleanup();error?reject(error):resolve()};
  const cancel=()=>finish(signal.reason??new DOMException("Warning cancelled","AbortError"));
  const stateChange=()=>{if(context.state!=="running")finish(new AutoAnswerWarningError("warning_playback_blocked","Browser audio stopped. Answer manually."))};
  const complete=()=>{if(played&&windowElapsed){stateChange();if(!settled)finish()}};
  signal.addEventListener("abort",cancel,{once:true});context.addEventListener("statechange",stateChange);
  if(signal.aborted){cancel();return}
  try{
   stateChange();if(settled)return;
   const start=context.currentTime+0.02,end=start+0.18;
   oscillator=context.createOscillator();gain=context.createGain();oscillator.type="sine";oscillator.frequency.value=880;
   gain.gain.setValueAtTime(0,start);gain.gain.linearRampToValueAtTime(0.2*(options.outputVolume??1),start+0.01);gain.gain.setValueAtTime(0.2*(options.outputVolume??1),end-0.02);gain.gain.linearRampToValueAtTime(0,end);
   oscillator.connect(gain);gain.connect(context.destination);
   oscillator.onended=()=>{if(context.state!=="running"||context.currentTime<end){finish(new AutoAnswerWarningError("warning_playback_unconfirmed","Warning playback could not be confirmed. Answer manually."));return}played=true;complete()};
   oscillator.start(start);oscillator.stop(end+0.01);
   windowTimer=setTimeout(()=>{windowElapsed=true;complete()},windowMs);
   deadline=setTimeout(()=>finish(new AutoAnswerWarningError("warning_playback_timeout","Warning playback timed out. Answer manually.")),windowMs+1500);
  }catch{finish(new AutoAnswerWarningError("warning_playback_failed","Unable to play the warning. Answer manually."))}
 });
}
