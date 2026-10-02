/** Map the render clock to actual output time, rather than taking one possibly
 * delayed main-thread sample of Date.now() - AudioContext.currentTime. */
export function outputWallTimeMS(context: Pick<AudioContext,'currentTime'|'getOutputTimestamp'>,audioSeconds:number,origin:number,now:number):number {
 const stamp=context.getOutputTimestamp?.();
 if(stamp && Number.isFinite(stamp.contextTime) && stamp.contextTime!>0 && Number.isFinite(stamp.performanceTime) && stamp.performanceTime!>0) {
  return origin+stamp.performanceTime!+(audioSeconds-stamp.contextTime!)*1000;
 }
 return origin+now+(audioSeconds-context.currentTime)*1000;
}
