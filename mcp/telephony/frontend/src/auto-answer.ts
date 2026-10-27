import {isIncomingBrowserCall,type Call,type TelephonyClient} from "./client";
export interface AutoAnswerWarningPolicy {enabled:boolean;window_ms?:number}
export interface AutoAnswerPolicy {warning_tone?:AutoAnswerWarningPolicy;mode:"inherit"|"manual"|"automatic";delay_ms:number;allow_adviser_override?:boolean}
export interface AutoAnswerEffective {warning_tone?:AutoAnswerWarningPolicy;mode:"manual"|"automatic";delay_ms:number;source:string;allow_adviser_override:boolean}
export interface AutoAnswerPolicyRequest {scope?:"project"|"route"|"destination"|"adviser";scope_id?:string;identity?:Record<string,string>;policy?:AutoAnswerPolicy;inherit?:boolean}
export interface AutoAnswerPolicyView {scope:string;scope_id:string;policy:AutoAnswerPolicy;configured:boolean;effective:AutoAnswerEffective;admin:boolean;advisers?:Array<Record<string,string>>;routes?:Array<{id:string;name:string}>;destinations?:Array<{id:string;name:string}>}
export interface AutoAnswerSnapshot {available:boolean;designated:boolean;state:"off"|"preparing"|"available"|"warning"|"countdown"|"answering"|"blocked";callId?:string;answerAt?:number;source?:string;detail?:string;offerReceivedAt?:number;answerStartedAt?:number;warningStartedAt?:number;warningCompletedAt?:number;code?:string;eventCallId?:string;offerId?:string;manualAnswerAvailable?:boolean}
interface Host {busy():boolean;prepare():Promise<void>;releasePreparation():void;warning?(windowMs:number,signal:AbortSignal):Promise<void>;answer(id:string,dest:string,session:string,warningCompleted:boolean):Promise<void>;changed(value:AutoAnswerSnapshot):void}
/** The shared softphone owns pickup. The server only coordinates identity-bound offers. */
export class AutomaticAnswer {
 private readonly session=crypto.randomUUID();private generation=0;private disposed=false;
 private state:AutoAnswerSnapshot={available:false,designated:false,state:"off"};
 private attempt?:AbortController;private expiry?:ReturnType<typeof setTimeout>;private pendingCallId?:string;
 private heartbeat?:ReturnType<typeof setTimeout>;private countdown?:ReturnType<typeof setTimeout>;private watcher?:{close():void};
 private handledOffers=new Set<string>();
 private offered=new Map<string,Call>();private ignored=new Set<string>();private reserved=new Set<string>();private pending=false;private answerNowAction?:()=>void;
 constructor(private client:TelephonyClient,private host:Host,private watch:boolean){}
 private update(patch:Partial<AutoAnswerSnapshot>){this.state={...this.state,...patch};this.host.changed({...this.state});}
 async setAvailable(available:boolean,takeover=false):Promise<void>{
  if(this.disposed)throw Error("Softphone has been disposed");
  if(this.host.busy()&&available)throw Error("Finish the current call before enabling automatic answering");
  const gen=++this.generation;clearTimeout(this.heartbeat);this.watcher?.close();this.watcher=undefined;this.cancel(undefined,"adviser_unavailable");
  if(!available){this.host.releasePreparation();this.update({available:false,designated:false,state:"off",detail:undefined});await this.client.autoAnswerSession({session_id:this.session,available:false});return;}
  this.update({available:false,designated:false,state:"preparing",detail:undefined});
  try{
   await this.host.prepare();if(gen!==this.generation||this.disposed){if(!this.state.available)this.host.releasePreparation();return;}
   const result=await this.client.autoAnswerSession({session_id:this.session,available:true,takeover,warning_tone_supported:Boolean(this.host.warning)});if(gen!==this.generation||this.disposed){await this.client.autoAnswerSession({session_id:this.session,available:false});return;}
   if(!result.designated)throw Error("Another browser is available for this adviser. Choose Use this browser to move automatic answering here.");
   this.update({available:true,designated:true,state:"available"});this.scheduleHeartbeat(gen);
   if(this.watch)this.watcher=this.client.watchCalls(calls=>this.observe(calls),{onError:e=>this.update({detail:String(e)})});
   this.consider();
  }catch(error){if(gen===this.generation){this.host.releasePreparation();this.update({available:false,designated:false,state:"blocked",detail:error instanceof Error?error.message:String(error)});}throw error;}
 }
 private scheduleHeartbeat(gen:number){clearTimeout(this.heartbeat);this.heartbeat=setTimeout(()=>{void this.client.autoAnswerSession({session_id:this.session,available:true,warning_tone_supported:Boolean(this.host.warning)}).then(v=>{
  if(gen!==this.generation||this.disposed)return;if(!v.designated)throw Error("Automatic answering moved to another browser");this.scheduleHeartbeat(gen);
 }).catch(error=>{if(gen!==this.generation||this.disposed)return;this.generation++;this.cancel();this.watcher?.close();this.watcher=undefined;this.host.releasePreparation();this.update({available:false,designated:false,state:"blocked",detail:String(error)});});},10000);}
 observe(calls:Call[]){
  this.offered=new Map(calls.filter(isIncomingBrowserCall).map(c=>[c.id,c]));
  for(const id of this.ignored)if(!calls.some(c=>c.id===id))this.ignored.delete(id);
  if(this.state.callId&&(!this.offered.has(this.state.callId)||(this.state.offerId&&!this.offered.get(this.state.callId)?.ring_offers?.some(o=>o.id===this.state.offerId)))&&this.state.state!=="answering")this.cancel(undefined,"offer_withdrawn");
  if(this.state.callId&&this.host.busy()&&this.state.state!=="answering")this.cancel(undefined,"another_call_started");
  this.consider();
 }
 /** Cancels this offer's automatic action; it remains manually answerable. */
 cancel(id=this.state.callId??this.pendingCallId,code="pickup_canceled"){
  this.attempt?.abort();this.attempt=undefined;
  clearTimeout(this.countdown);clearTimeout(this.expiry);this.countdown=undefined;this.expiry=undefined;this.answerNowAction=undefined;
  for(const callId of new Set([id,this.state.callId].filter((v):v is string=>Boolean(v)))){
   this.ignored.add(callId);if(this.reserved.delete(callId))void this.client.autoAnswerOffer({session_id:this.session,call_id:callId,cancel:true}).catch(()=>{});
  }
  this.update({callId:undefined,answerAt:undefined,source:undefined,state:this.state.available?"available":"off",code,eventCallId:id,manualAnswerAvailable:true});
 }
 answerNow(){if(this.state.state!=="countdown")return;clearTimeout(this.countdown);this.answerNowAction?.();this.answerNowAction=undefined;}
 private consider(){
  if(this.disposed||!this.state.available||!this.state.designated||this.pending||this.state.callId||this.host.busy())return;
  const call=[...this.offered.values()].find(c=>!this.ignored.has(c.id)&&!c.ring_offers?.some(o=>this.handledOffers.has(o.id)));if(!call)return;
  const gen=this.generation,received=Date.now();this.pending=true;this.pendingCallId=call.id;
  void this.client.autoAnswerOffer({session_id:this.session,call_id:call.id,destination_id:call.ring_offers?.find(o=>o.kind==="browser")?.destination_id}).then(async result=>{
   if(gen!==this.generation||this.disposed||!this.state.available)return;
   if(!result.eligible)return;
   this.reserved.add(call.id);if(this.ignored.has(call.id)||!this.offered.has(call.id)||this.host.busy()){this.cancel(call.id);return;}
   const offerId=result.offer_id??call.ring_offers?.find(o=>o.kind==="browser"&&o.destination_id===result.destination_id)?.id;
   const stillOffered=()=>{const current=this.offered.get(call.id);return Boolean(current&&(!offerId||current.ring_offers?.some(o=>o.id===offerId&&o.kind==="browser")))};
   if(!stillOffered()){this.cancel(call.id,"offer_withdrawn");return;}
   if(offerId){this.handledOffers.add(offerId);if(this.handledOffers.size>256)this.handledOffers.delete(this.handledOffers.values().next().value!);}
   this.ignored.add(call.id);const attempt=new AbortController();this.attempt=attempt;
   const admitted=performance.now(),ttl=result.offer_ttl_ms??Infinity;
   const current=()=>!attempt.signal.aborted&&gen===this.generation&&!this.disposed&&this.state.available&&this.state.designated&&stillOffered()&&!this.host.busy()&&this.state.callId===call.id&&performance.now()-admitted<ttl;
   const warning=result.effective.warning_tone,windowMs=warning?.enabled?(warning.window_ms||1000):0;
   this.update({callId:call.id,offerId,eventCallId:call.id,source:result.effective.source,offerReceivedAt:received,warningStartedAt:undefined,warningCompletedAt:undefined,detail:undefined,code:undefined,manualAnswerAvailable:true,state:windowMs?"warning":result.effective.delay_ms?"countdown":"answering",answerAt:Date.now()+Math.max(result.effective.delay_ms,windowMs)});
   if(Number.isFinite(ttl))this.expiry=setTimeout(()=>{if(this.state.callId===call.id)this.cancel(call.id,"offer_expired")},Math.max(0,ttl));
   let warningCompleted=false;
   if(windowMs){
    if(!Number.isFinite(windowMs)||windowMs<750||windowMs>3000){this.cancel(call.id,"warning_policy_invalid");return;}
    this.update({warningStartedAt:Date.now(),code:"warning_started"});
    try{
     if(!current()) {this.cancel(call.id,"offer_withdrawn");return;}
     if(!this.host.warning)throw Object.assign(Error("Update the softphone to play the pickup warning."),{code:"warning_runtime_unsupported"});
     await this.host.warning(windowMs,attempt.signal);
     // A custom audio runtime must not shorten the policy's minimum window.
     const warningRemaining=Math.max(0,windowMs-(performance.now()-admitted));
     if(warningRemaining>0)await new Promise<void>((resolve,reject)=>{
       const abort=()=>{clearTimeout(timer);attempt.signal.removeEventListener("abort",abort);reject(attempt.signal.reason)};
       const timer=setTimeout(()=>{attempt.signal.removeEventListener("abort",abort);resolve()},warningRemaining);
       attempt.signal.addEventListener("abort",abort,{once:true});if(attempt.signal.aborted)abort();
     });
     if(!current()){if(!attempt.signal.aborted)this.cancel(call.id,"offer_withdrawn");return;}
     warningCompleted=true;this.update({warningCompletedAt:Date.now(),code:"warning_completed"});
    }catch(error){if(attempt.signal.aborted)return;const value=error as {code?:string};const code=value?.code?.startsWith("warning_")?value.code:"warning_playback_failed";this.cancel(call.id,code);this.update({detail:"Automatic pickup paused. Check browser audio and the selected headset, or answer manually.",manualAnswerAvailable:true});return;}
   }
   const answer=()=>{
    this.answerNowAction=undefined;
    if(!current()){this.cancel(call.id,this.host.busy()?"another_call_started":performance.now()-admitted>=ttl?"offer_expired":"offer_withdrawn");return;}
    clearTimeout(this.expiry);this.expiry=undefined;
    this.update({state:"answering",answerAt:undefined,answerStartedAt:Date.now(),code:"answer_started"});
    void this.host.answer(call.id,result.destination_id??"",this.session,warningCompleted).then(()=>{if(gen===this.generation){this.reserved.delete(call.id);this.attempt=undefined;this.update({callId:undefined,state:"available",code:"answer_connected"});}}).catch(error=>{
     this.cancel(call.id,"answer_failed");if(gen===this.generation)this.update({detail:error instanceof Error?error.message:String(error)});
    });
   };
   if(attempt.signal.aborted)return;
   const remaining=Math.max(0,result.effective.delay_ms-(performance.now()-admitted));
   this.answerNowAction=answer;
   if(remaining===0)answer();else {this.update({state:"countdown"});this.countdown=setTimeout(answer,remaining);}
  }).catch(error=>{if(gen===this.generation){this.ignored.add(call.id);const code=(error as {code?:string})?.code;this.update({code:code==="auto_answer_warning_unsupported"?code:"reservation_failed",eventCallId:call.id,manualAnswerAvailable:true,detail:error instanceof Error?error.message:String(error)});}}).finally(()=>{this.pending=false;this.pendingCallId=undefined;});
 }
 dispose(){if(this.disposed)return;this.disposed=true;this.generation++;clearTimeout(this.heartbeat);this.watcher?.close();this.cancel();if(this.state.available)void this.client.autoAnswerSession({session_id:this.session,available:false}).catch(()=>{});this.host.releasePreparation();}
}
