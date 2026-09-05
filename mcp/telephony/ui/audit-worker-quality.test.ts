// Regression for fallback sample-rate conversion.
import {expect,test} from "bun:test";
import {readFileSync} from "node:fs";
import vm from "node:vm";

test("audit: 48kHz fallback resampling must reject content above 12kHz",()=>{
  const context=vm.createContext({self:{},postMessage(){},performance:{now:()=>0},setTimeout,clearTimeout});
  vm.runInContext(readFileSync(new URL("./softphone-worker.js",import.meta.url),"utf8"),context);
  const result=vm.runInContext(`(()=>{
    const frame=new Float32Array(960);
    for(let i=0;i<frame.length;i++)frame[i]=Math.sin(2*Math.PI*18000*i/48000);
    const output=resample(frame,48000,24000);
    return Math.sqrt(output.reduce((sum,x)=>sum+x*x,0)/output.length);
  })()`,context);
  // 18kHz should be removed before downsampling; otherwise it aliases to 6kHz.
  expect(result).toBeLessThan(0.02);
});
