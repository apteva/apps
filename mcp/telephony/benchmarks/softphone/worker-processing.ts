// Measure the production Worker in Chromium's Worker runtime. Fixtures bypass
// WebSockets and Worklet IPC; use the full browser benchmark for those costs.
import {chromium} from '@playwright/test';

const source=await Bun.file(new URL('../../ui/softphone-worker.js',import.meta.url)).text();
const built=await Bun.build({entrypoints:[`${import.meta.dir}/worker-processing-entry.ts`],target:'browser',format:'iife'});
if(!built.success)throw new AggregateError(built.logs,'Worker processing harness build failed');
const harness=await built.outputs[0].text();
const server=Bun.serve({hostname:'127.0.0.1',port:0,fetch(request){
 const path=new URL(request.url).pathname;
 if(path==='/')return new Response('<!doctype html><title>Local Worker processing benchmark</title>',{headers:{'Content-Type':'text/html'}});
 if(path==='/worker.js')return new Response(harness,{headers:{'Content-Type':'text/javascript'}});
 if(path==='/production-worker.js')return new Response(source,{headers:{'Content-Type':'text/javascript'}});
 return new Response('Not found',{status:404});
}});
let browser;
try {
 browser=await chromium.launch({headless:true});
 const page=await browser.newPage();await page.goto(`http://127.0.0.1:${server.port}`);
 const results=await page.evaluate(()=>new Promise((resolve,reject)=>{
  const worker=new Worker('/worker.js');
  worker.onerror=e=>{worker.terminate();reject(new Error(e.message));};
  worker.onmessage=e=>{if(e.data.type==='benchmark.complete'){worker.terminate();resolve(e.data.results);}};
 }));
 const report={scope:'Chromium Worker executing exact production source; full-duplex 20ms PCM with clock probes/telemetry active; bypasses network/Worklet IPC/audio hardware, excludes server work',worker_sha256:new Bun.CryptoHasher('sha256').update(source).digest('hex'),browser_version:browser.version(),created_at:new Date().toISOString(),results};
 console.log(JSON.stringify(report,null,2));
 if(process.argv[2])await Bun.write(process.argv[2],JSON.stringify(report,null,2)+'\n');
} finally {await browser?.close();server.stop(true);}
