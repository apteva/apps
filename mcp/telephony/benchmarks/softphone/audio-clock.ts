/** Map the render clock to actual output time, rather than taking one possibly
 * delayed main-thread sample of Date.now() - AudioContext.currentTime. */
export function outputWallTimeMS(context: Pick<AudioContext,'currentTime'|'getOutputTimestamp'>,audioSeconds:number,origin:number,now:number):number {
 const stamp=context.getOutputTimestamp?.();
 if(stamp && Number.isFinite(stamp.contextTime) && stamp.contextTime!>0 && Number.isFinite(stamp.performanceTime) && stamp.performanceTime!>0) {
  return origin+stamp.performanceTime!+(audioSeconds-stamp.contextTime!)*1000;
 }
 return origin+now+(audioSeconds-context.currentTime)*1000;
}

/** Arm modeled traffic only once synthetic render clocks are advancing.
 * The setup wait is recorded separately, never subtracted from call scores. */
export async function waitForAudioClockProgress(contexts:Pick<AudioContext,'currentTime'>[],timeoutMS=3000):Promise<number>{
 const started=performance.now();let at=started,previous=contexts.map(c=>c.currentTime),stable=0;
 while(performance.now()-started<timeoutMS){
  await new Promise(r=>setTimeout(r,100));const now=performance.now(),current=contexts.map(c=>c.currentTime),elapsed=now-at;
  stable=current.every((value,i)=>Number.isFinite(value)&&(value-previous[i])*1000>=elapsed*.8)?stable+1:0;
  if(stable>=2)return now-started;previous=current;at=now;
 }
 throw new Error('Benchmark render clocks did not start advancing');
}
