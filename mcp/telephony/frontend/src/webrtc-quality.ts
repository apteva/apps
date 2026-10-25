import {RTCSendPolicy} from "./rtc-send-policy";
import {DEFAULT_NETWORK_POLICY,validateNetworkPolicy} from "./network-policy";
import type { SoftphoneAudioOptions } from "../../ui/softphone-audio";

/** Sampled control only: never runs in a capture/render callback. Native
 * WebRTC still owns congestion control and its actual playout buffer. */
export class RTCQualityPolicy {
  target:number; bitrate=32000;
  get uploadControl(){return "standard";}
  get uploadReason(){return "standard";}
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

/** Preserve the released controller for unconfigured hosts. A saved network
 * profile changes only its existing options, initial rate and rate bounds. */
class ProfiledRTCQualityPolicy extends RTCQualityPolicy {
 get uploadControl(){return this.guarded?"guarded":"standard";}
 get uploadReason(){return this.guarded?.reason??"standard";}
 private readonly minimumRate:number;private readonly maximumRate:number;private readonly guarded?:RTCSendPolicy;
 constructor(options:Partial<SoftphoneAudioOptions>){
  const p=validateNetworkPolicy(options.networkProfile);
  super({...options,playbackTargetMs:p.playback_target_ms,playbackMaxMs:p.playback_max_ms});
  if(p.upload_bitrate_control==="guarded")this.guarded=new RTCSendPolicy(p);
  this.bitrate=p.initial_bitrate_bps;this.minimumRate=p.min_bitrate_bps;this.maximumRate=p.max_bitrate_bps;
 }
 observe(metrics:Record<string,number>,now=performance.now()){
  super.observe(metrics,now);if(this.guarded){this.guarded.observe(metrics,now);this.bitrate=this.guarded.bitrate;}this.bitrate=Math.max(this.minimumRate,Math.min(this.maximumRate,this.bitrate));
 }
}
export function rtcQualityPolicy(options:Partial<SoftphoneAudioOptions>={}):RTCQualityPolicy {
 return options.networkProfile?new ProfiledRTCQualityPolicy(options):new RTCQualityPolicy(options);
}
/** Slow, bounded quality notices. Absence of stats, silence/DTX,
 * holds and browser scheduling gaps never constitute evidence of a bad path. */
export class RTCPathHealth {
 private uploadBadAt?:number;private path?:number;
 private badSince?:number;private last?:number;private lastAttempt=-Infinity;private baselineRTT?:number;private attempts=0;
 constructor(private readonly profile=DEFAULT_NETWORK_POLICY){}
 observe(metrics:Record<string,number>,now:number,eligible:boolean):"degraded"|undefined {
  if(!eligible||!this.profile.quality_warnings||this.last!==undefined&&(now<=this.last||now-this.last>3000)){this.badSince=undefined;this.uploadBadAt=undefined;this.baselineRTT=undefined;this.last=now;return;}
  this.last=now;
  if(this.path!==metrics.path_revision){this.path=metrics.path_revision;this.baselineRTT=undefined;this.badSince=undefined;this.uploadBadAt=undefined;}
  const rtt=metrics.pair_currentRoundTripTime;
  if(rtt!==undefined&&rtt>0&&rtt<5)this.baselineRTT=Math.min(this.baselineRTT??rtt,rtt);
  const delayGrowth=rtt!==undefined&&this.baselineRTT!==undefined&&rtt-this.baselineRTT>=.08;
  const losses=metrics.receiver_loss_delta??0,window=metrics.receiver_loss_window_ms;
  const lossEvidence=window!==undefined&&window>=500&&window<=2500&&losses>=3;
  const concealed=metrics.receiver_concealed_delta_ms??0,jitter=metrics.receiver_jitter??0;
  if(metrics.remote_receiver_report_updated===1){
   this.uploadBadAt=metrics.remote_receiver_report_age_ms<=5000&&metrics.sender_packets_delta>0&&metrics.remote_receiver_fractionLost>=.03?now:undefined;
  }
  const uploadBad=this.uploadBadAt!==undefined&&now-this.uploadBadAt<=5000;
  const bad=uploadBad||(lossEvidence&&(delayGrowth||concealed>=80))||(concealed>=80&&jitter>=.03);
  if(!bad){this.badSince=undefined;return;}
  this.badSince??=now;
  if(this.attempts>=2||now-this.badSince<this.profile.degraded_seconds*1000||now-this.lastAttempt<this.profile.warning_cooldown_seconds*1000)return;
  this.attempts++;this.lastAttempt=now;this.badSince=undefined;return "degraded";
 }
}
