import {test,expect} from 'bun:test';
import {playPreparedWarning} from '../../ui/auto-answer-warning';
function fixture(){
 const links:any[]=[],sinks:string[]=[],listeners=new Map<string,()=>void>();let stopped=0,disconnected=0,ended:(()=>void)|null=null;
 const context={state:'running',currentTime:0,sinkId:'default',destination:{output:true},async setSinkId(id:string){sinks.push(id);this.sinkId=id},addEventListener:(s:string,f:()=>void)=>listeners.set(s,f),removeEventListener:(s:string)=>listeners.delete(s),createGain:()=>({gain:{setValueAtTime(){},linearRampToValueAtTime(){}},connect:(to:any)=>links.push(to),disconnect(){disconnected++}}),createOscillator:()=>({type:'',frequency:{value:0},set onended(fn:(()=>void)|null){ended=fn},start(){},stop(){stopped++},connect:(to:any)=>links.push(to),disconnect(){disconnected++}})};
 return {context:context as unknown as AudioContext,links,sinks,get stopped(){return stopped},get disconnected(){return disconnected},end(){context.currentTime=0.3;ended?.()},suspend(){context.state='suspended';listeners.get('statechange')?.()},setSinkFailure(){context.setSinkId=async()=>{throw Error('device unplugged')}}};
}
test('selected headset and output-only graph, with confirmed render and a full warning window',async()=>{
 const f=fixture(),signal=new AbortController();let finished=false;const start=performance.now();const task=playPreparedWarning(f.context,{outputDeviceId:'headset-selected',outputVolume:0.6},750,signal.signal).then(()=>{finished=true});
 await Bun.sleep(2);expect(f.sinks).toEqual(['headset-selected']);expect(f.links).toHaveLength(2);expect(f.links[1]).toBe(f.context.destination);f.end();await Bun.sleep(5);expect(finished).toBe(false);await task;expect(performance.now()-start).toBeGreaterThanOrEqual(740);expect(f.disconnected).toBe(2);expect(f.stopped).toBeGreaterThan(0);
});
test('elapsed time alone never confirms playback',async()=>{const f=fixture();await expect(playPreparedWarning(f.context,{},0,new AbortController().signal)).rejects.toMatchObject({code:'warning_playback_timeout'})});
for(const mode of ['cancel','suspended','muted','sink-failed','sink-unsupported'] as const)test(`warning ${mode} fails closed and preserves prepared context`,async()=>{
 const f=fixture(),controller=new AbortController();if(mode==='sink-failed')f.setSinkFailure();if(mode==='sink-unsupported')(f.context as any).setSinkId=undefined;
 const task=playPreparedWarning(f.context,{outputVolume:mode==='muted'?0:1,outputDeviceId:mode.startsWith('sink')?'headset':undefined},750,controller.signal);const failure=task.catch(e=>e);
 await Bun.sleep(2);if(mode==='cancel')controller.abort();if(mode==='suspended')f.suspend();const error=await failure;
 if(mode==='cancel')expect(error.name).toBe('AbortError');else expect(error.code).toBe(mode==='suspended'?'warning_playback_blocked':mode==='muted'?'warning_output_muted':mode==='sink-unsupported'?'warning_output_unsupported':'warning_output_failed');expect(f.context.state).not.toBe('closed');
});
