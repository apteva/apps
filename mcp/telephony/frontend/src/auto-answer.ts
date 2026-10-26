import {isIncomingBrowserCall,type Call,type TelephonyClient} from "./client";
export interface AutoAnswerPolicy {mode:"inherit"|"manual"|"automatic";delay_ms:number;allow_adviser_override?:boolean}
export interface AutoAnswerEffective {mode:"manual"|"automatic";delay_ms:number;source:string;allow_adviser_override:boolean}
export interface AutoAnswerPolicyRequest {scope?:"project"|"route"|"destination"|"adviser";scope_id?:string;identity?:Record<string,string>;policy?:AutoAnswerPolicy;inherit?:boolean}
export interface AutoAnswerPolicyView {scope:string;scope_id:string;policy:AutoAnswerPolicy;configured:boolean;effective:AutoAnswerEffective;admin:boolean;advisers?:Array<Record<string,string>>;routes?:Array<{id:string;name:string}>;destinations?:Array<{id:string;name:string}>}
export interface AutoAnswerSnapshot {available:boolean;designated:boolean;state:"off"|"preparing"|"available"|"countdown"|"answering"|"blocked";callId?:string;answerAt?:number;source?:string;detail?:string;offerReceivedAt?:number;answerStartedAt?:number}
interface Host {busy():boolean;prepare():Promise<void>;releasePreparation():void;answer(id:string,dest:string,session:string):Promise<void>;changed(value:AutoAnswerSnapshot):void}
/** The shared softphone owns pickup. The server only coordinates identity-bound offers. */
export class AutomaticAnswer {
 private readonly session=crypto.randomUUID();private generation=0;private disposed=false;
 private state:AutoAnswerSnapshot={available:false,designated:false,state:"off"};
 private heartbeat?:ReturnType<typeof setTimeout>;private countdown?:ReturnType<typeof setTimeout>;private watcher?:{close():void};
 private offered=new Map<string,Call>();private ignored=new Set<string>();private reserved=new Set<string>();private pending=false;private answerNowAction?:()=>void;
 constructor(private client:TelephonyClient,private host:Host,private watch:boolean){}
 private update(patch:Partial<AutoAnswerSnapshot>){this.state={...this.state,...patch};this.host.changed({...this.state});}
 async setAvailable(available:boolean,takeover=false):Promise<void>{
  if(this.disposed)throw Error("Softphone has been disposed");
  if(this.host.busy()&&available)throw Error("Finish the current call before enabling automatic answering");
  const gen=++this.generation;clearTimeout(this.heartbeat);this.watcher?.close();this.watcher=undefined;this.cancel();
  if(!available){this.host.releasePreparation();this.update({available:false,designated:false,state:"off",detail:undefined});await this.client.autoAnswerSession({session_id:this.session,available:false});return;}
  this.update({available:false,designated:false,state:"preparing",detail:undefined});
  try{
   await this.host.prepare();if(gen!==this.generation||this.disposed){if(!this.state.available)this.host.releasePreparation();return;}
   const result=await this.client.autoAnswerSession({session_id:this.session,available:true,takeover});if(gen!==this.generation||this.disposed){await this.client.autoAnswerSession({session_id:this.session,available:false});return;}
   if(!result.designated)throw Error("Another browser is available for this adviser. Choose Use this browser to move automatic answering here.");
   this.update({available:true,designated:true,state:"available"});this.scheduleHeartbeat(gen);
   if(this.watch)this.watcher=this.client.watchCalls(calls=>this.observe(calls),{onError:e=>this.update({detail:String(e)})});
   this.consider();
  }catch(error){if(gen===this.generation){this.host.releasePreparation();this.update({available:false,designated:false,state:"blocked",detail:error instanceof Error?error.message:String(error)});}throw error;}
 }
 private scheduleHeartbeat(gen:number){clearTimeout(this.heartbeat);this.heartbeat=setTimeout(()=>{void this.client.autoAnswerSession({session_id:this.session,available:true}).then(v=>{
  if(gen!==this.generation||this.disposed)return;if(!v.designated)throw Error("Automatic answering moved to another browser");this.scheduleHeartbeat(gen);
 }).catch(error=>{if(gen!==this.generation||this.disposed)return;this.generation++;this.cancel();this.watcher?.close();this.watcher=undefined;this.host.releasePreparation();this.update({available:false,designated:false,state:"blocked",detail:String(error)});});},10000);}
 observe(calls:Call[]){
  this.offered=new Map(calls.filter(isIncomingBrowserCall).map(c=>[c.id,c]));
  for(const id of this.ignored)if(!calls.some(c=>c.id===id))this.ignored.delete(id);
  if(this.state.callId&&!this.offered.has(this.state.callId)&&this.state.state!=="answering")this.cancel();
  this.consider();
 }
 /** Cancels this offer's automatic action; it remains manually answerable. */
 cancel(id=this.state.callId){
  clearTimeout(this.countdown);this.countdown=undefined;this.answerNowAction=undefined;
  if(id){this.ignored.add(id);if(this.reserved.delete(id))void this.client.autoAnswerOffer({session_id:this.session,call_id:id,cancel:true}).catch(()=>{});}
  this.update({callId:undefined,answerAt:undefined,source:undefined,state:this.state.available?"available":"off"});
 }
 answerNow(){if(this.state.state!=="countdown")return;clearTimeout(this.countdown);this.answerNowAction?.();this.answerNowAction=undefined;}
 private consider(){
  if(this.disposed||!this.state.available||!this.state.designated||this.pending||this.state.callId||this.host.busy())return;
  const call=[...this.offered.values()].find(c=>!this.ignored.has(c.id));if(!call)return;
  const gen=this.generation,received=Date.now();this.pending=true;
  void this.client.autoAnswerOffer({session_id:this.session,call_id:call.id,destination_id:call.ring_offers?.find(o=>o.kind==="browser")?.destination_id}).then(result=>{
   if(gen!==this.generation||this.disposed||!this.state.available)return;
   if(!result.eligible)return;
   this.reserved.add(call.id);if(this.ignored.has(call.id)){this.cancel(call.id);return;}
   if(!this.offered.has(call.id)||this.host.busy()){this.cancel(call.id);return;}
   this.ignored.add(call.id);
   this.update({callId:call.id,source:result.effective.source,offerReceivedAt:received,detail:undefined,state:result.effective.delay_ms?"countdown":"answering",answerAt:Date.now()+result.effective.delay_ms});
   const answer=()=>{
    this.answerNowAction=undefined;
    if(gen!==this.generation||this.disposed||!this.offered.has(call.id)||this.host.busy()||this.state.callId!==call.id){this.cancel(call.id);return;}
    this.update({state:"answering",answerAt:undefined,answerStartedAt:Date.now()});
    void this.host.answer(call.id,result.destination_id??"",this.session).then(()=>{if(gen===this.generation){this.reserved.delete(call.id);this.update({callId:undefined,state:"available"});}}).catch(error=>{
     this.cancel(call.id);if(gen===this.generation)this.update({detail:error instanceof Error?error.message:String(error)});
    });
   };
   // Zero means no intentional wait. A configured delay is the only timer.
   this.answerNowAction=answer;
   if(result.effective.delay_ms===0)answer();else this.countdown=setTimeout(answer,result.effective.delay_ms);
  }).catch(error=>{if(gen===this.generation){this.ignored.add(call.id);this.update({detail:error instanceof Error?error.message:String(error)});}}).finally(()=>{this.pending=false;});
 }
 dispose(){if(this.disposed)return;this.disposed=true;this.generation++;clearTimeout(this.heartbeat);this.watcher?.close();this.cancel();if(this.state.available)void this.client.autoAnswerSession({session_id:this.session,available:false}).catch(()=>{});this.host.releasePreparation();}
}
