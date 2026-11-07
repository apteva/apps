import {chromium} from '@playwright/test';
import {join} from 'node:path';
import {cpus,freemem,loadavg} from 'node:os';
const fixture=process.env.TELEPHONY_OUTBOUND_FIXTURE!;
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(fixture))throw new Error('Local fixture required');
const config=await(await fetch(fixture+'/fixture/config')).json();
const built=await Bun.build({entrypoints:[join(import.meta.dir,'browser-entry.ts')],target:'browser',format:'esm'});
if(!built.success)throw new AggregateError(built.logs);
const browserCode=await built.outputs[0].text();
const host=Bun.serve({hostname:'127.0.0.1',port:0,fetch(request){const path=new URL(request.url).pathname;
 if(path==='/browser.js')return new Response(browserCode,{headers:{'Content-Type':'text/javascript'}});
 if(path==='/source-worklet.js')return new Response(Bun.file(join(import.meta.dir,'source-worklet.js')),{headers:{'Content-Type':'text/javascript'}});
 if(path==='/capture-worklet.js')return new Response(Bun.file(join(import.meta.dir,'../../ui/softphone-worklet.js')),{headers:{'Content-Type':'text/javascript'}});
 return new Response('<!doctype html><script type="module" src="/browser.js"></script>',{headers:{'Content-Type':'text/html'}});
}});
const browser=await chromium.launch({headless:true,args:['--autoplay-policy=no-user-gesture-required','--use-fake-ui-for-media-stream','--use-fake-device-for-media-stream','--disable-background-timer-throttling','--disable-renderer-backgrounding','--disable-backgrounding-occluded-windows']});
let finished=false;let sampleNumber=0;let memory:any={};
const memoryEvidence=(ids:number[])=>{
 const rss=Bun.spawnSync(['ps','-o','rss=','-p',ids.join(',')]);
 const chromiumRssKB=rss.exitCode===0?rss.stdout.toString().trim().split(/\s+/).reduce((sum,n)=>sum+Number(n||0),0):null;
 if(process.platform==='darwin'){
  const vm=Bun.spawnSync(['vm_stat']).stdout.toString();const fields:Record<string,number>={};
  for(const line of vm.split('\n')){const match=line.match(/^([^:]+):\s*(\d+)/);if(match)fields[match[1].replaceAll('"','')]=Number(match[2]);}
  return{chromium_rss_kb:chromiumRssKB,vm_pages:fields,swap_usage:Bun.spawnSync(['sysctl','-n','vm.swapusage']).stdout.toString().trim()};
 }
 return{chromium_rss_kb:chromiumRssKB};
};
try{
 const context=await browser.newContext({permissions:['microphone']});const pages:any[]=[];
 for(const call of config.calls){const page=await context.newPage();pages.push(page);page.on('pageerror',e=>console.error('browser error',e.message));
  await page.goto(host.url.href);await page.waitForFunction(()=>typeof(window as any).startSoak==='function');
  await page.evaluate((c:any)=>(window as any).startSoak(c),{...call,fixture,speech:config.speech});
 }
 await Promise.all(pages.map(p=>p.evaluate(()=>(window as any).soak.beginSampling())));
 await fetch(fixture+'/fixture/ready',{method:'POST'});
 const cdp=await browser.newBrowserCDPSession();let previousCPU=0,previousAt=performance.now(),previousHost=cpus().map(c=>c.times);
 while(!finished){await Bun.sleep(1000);const now=performance.now();const processInfo=await cdp.send('SystemInfo.getProcessInfo');const cpu=processInfo.processInfo.reduce((sum,p)=>sum+p.cpuTime,0);if(sampleNumber++%10===0)memory=memoryEvidence(processInfo.processInfo.map(p=>p.id));
  const hostCPU=cpus().map(c=>c.times);let total=0,idle=0;hostCPU.forEach((c,i)=>{for(const key of ['user','nice','sys','idle','irq'] as const)total+=c[key]-previousHost[i][key];idle+=c.idle-previousHost[i].idle;});previousHost=hostCPU;
  const samples=await Promise.all(pages.map(p=>p.evaluate(()=>(window as any).soak.snapshot())));
  await fetch(fixture+'/fixture/browser',{method:'POST',body:JSON.stringify({at:Date.now(),memory,chromium_cpu_one_core_pct:previousCPU?100000*(cpu-previousCPU)/(now-previousAt):null,host_cpu_busy_pct:total?100*(total-idle)/total:0,free_memory_bytes:freemem(),load_average:loadavg(),calls:samples})});
  previousCPU=cpu;previousAt=now;finished=(await(await fetch(fixture+'/fixture/status')).json()).finished;
 }
 await Promise.all(pages.map(p=>p.evaluate(()=>(window as any).soak.stop())));
}finally{await browser.close();host.stop(true);}
