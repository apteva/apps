export interface RecoveryTimers {set:(fn:()=>void,ms:number)=>ReturnType<typeof setTimeout>;clear:(id?:ReturnType<typeof setTimeout>)=>void}
const realTimers:RecoveryTimers={set:(fn,ms)=>setTimeout(fn,ms),clear:id=>clearTimeout(id)};
/** Control-plane recovery only. Each request is bounded, attempts never overlap,
 * and stop removes timers/listeners and aborts the active request. Healthy calls
 * do not create a loop. HTTP reachability is verified by the authorized attempt;
 * navigator.onLine is not treated as proof of a working network. */
export class AudioRecoveryLoop {
  private stopped=false;
  private active?:AbortController;
  private timer?:ReturnType<typeof setTimeout>;
  private attempt=0;
  private readonly online=()=>this.wake();
  constructor(private run:(signal:AbortSignal)=>Promise<void>,private permanent:(error:unknown)=>boolean,
    private ended:(error:unknown)=>void,private event?:(outcome:string)=>void,private clock:RecoveryTimers=realTimers) {
    globalThis.addEventListener?.("online",this.online);
    this.schedule(1000);
  }
  private emit(outcome:string){try{this.event?.(outcome);}catch{/* observations cannot interrupt recovery */}}
  private schedule(ms:number){if(!this.stopped){this.clock.clear(this.timer);this.timer=this.clock.set(()=>void this.tick(),ms);}}
  wake(){if(!this.stopped&&!this.active)this.schedule(0);}
  private async tick(){
    if(this.stopped||this.active)return;
    const controller=new AbortController();this.active=controller;this.attempt++;
    this.emit("attempt_started");
    let deadline:ReturnType<typeof setTimeout>|undefined;
    try{
      await Promise.race([this.run(controller.signal),new Promise<never>((_,reject)=>{
        deadline=this.clock.set(()=>{controller.abort();reject(new Error("Audio recovery request timed out"));},30000);
      })]);
      if(this.stopped)return;
      this.emit("connected");this.stop();
    }catch(error){
      if(this.stopped)return;
      if(this.permanent(error)){this.emit("revoked");this.stop();this.ended(error);return;}
      this.emit("waiting_for_network");
      this.schedule(Math.min(15000,1000*2**Math.min(4,this.attempt)));
    }finally{this.clock.clear(deadline);if(this.active===controller)this.active=undefined;}
  }
  stop(){if(this.stopped)return;this.stopped=true;this.clock.clear(this.timer);this.active?.abort();globalThis.removeEventListener?.("online",this.online);}
}
