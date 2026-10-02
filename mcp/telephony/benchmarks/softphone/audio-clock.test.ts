import {expect,test} from 'bun:test';
import {outputWallTimeMS} from './audio-clock';
test('output timestamps correct a render clock ahead of the audible hardware clock',()=>{
 const ctx={currentTime:10.2,getOutputTimestamp:()=>({contextTime:10,performanceTime:12000})};
 expect(outputWallTimeMS(ctx,10.1,100000,12000)).toBe(112100);
 // The prior mapping produced 111900, before the carrier source could exist.
 expect(112000+(10.1-ctx.currentTime)*1000).toBeCloseTo(111900);
});
test('unavailable hardware timestamps fall back to a fresh monotonic sample',()=>{
 const ctx={currentTime:10,getOutputTimestamp:()=>({contextTime:0,performanceTime:0})};
 expect(outputWallTimeMS(ctx,10.02,100000,12000)).toBeCloseTo(112020);
});
