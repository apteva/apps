import type { SoftphoneAudioOptions } from "../../ui/softphone-audio";

/** Sampled control only: never runs in a capture/render callback. Native
 * WebRTC still owns congestion control and its actual playout buffer. */
export class RTCQualityPolicy {
  target:number; bitrate=32000;
  private stable?:number; private last=-Infinity;
  private readonly initial:number; private readonly maximum:number;
  constructor(private options:Partial<SoftphoneAudioOptions>={}) {
    this.initial=options.playbackTargetMs??60;this.target=this.initial;
    this.maximum=Math.max(this.initial,options.playbackMaxMs??160);
  }
  observe(metrics:Record<string,number>,now=performance.now()) {
    if(now<this.last){this.last=-Infinity;this.stable=undefined;}
    if(now-this.last<2000)return;
    this.last=now;
    const jitter=metrics.receiver_jitter===undefined?undefined:metrics.receiver_jitter*1000;
    const concealment=metrics.receiver_concealed_delta_ms??0;
    if(this.options.playbackAdaptive!==false){
      const wanted=Math.min(this.maximum,Math.max(this.initial,this.initial+Math.ceil(Math.max(0,(jitter??0)-10)*2/20)*20,concealment>0?this.target+20:this.initial));
      if(wanted>this.target){this.target=Math.min(wanted,this.target+20);this.stable=undefined;}
      else if(jitter!==undefined&&concealment===0&&jitter<10){
        this.stable??=now;
        if(now-this.stable>=10000){this.target=Math.max(this.initial,this.target-10);this.stable=now;}
      }else this.stable=undefined;
    }
    // Reserve wire overhead. This is an available-bandwidth estimate, not a
    // measurement of the office's bandwidth or a substitute for native BWE.
    const available=metrics.pair_availableOutgoingBitrate;
    const loss=metrics.remote_receiver_fractionLost;
    if(available!==undefined&&available>0&&available<64000)this.bitrate=Math.max(16000,Math.min(32000,Math.floor((available-32000)*.8/1000)*1000));
    else if(loss!==undefined&&loss>=.03)this.bitrate=Math.max(16000,this.bitrate-4000);
    else if(loss!==undefined&&loss<.01)this.bitrate=Math.min(32000,this.bitrate+2000);
  }
}

/** Keep monitoring bursts below a small network packet. Signaling actions
 * (answer, DTMF, cancellation) never use this observational byte budget. */
export class RTCControlBudget {
  private tokens=1100;private at?:number;rate=640;
  observeAvailableBitrate(available?:number){this.rate=available!==undefined&&available>=96000?2000:640;}
  consume(bytes:number,now=performance.now()):boolean {
    if(!Number.isFinite(bytes)||!Number.isFinite(now)||bytes<0||bytes>1100)return false;
    if(this.at!==undefined){this.tokens=Math.min(1100,this.tokens+Math.max(0,now-this.at)*this.rate/1000);}
    this.at=now;
    if(this.tokens<bytes)return false;
    this.tokens-=bytes;return true;
  }
}
