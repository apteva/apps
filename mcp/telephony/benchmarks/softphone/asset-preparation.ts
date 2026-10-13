// Runs the released and working loaders against identical simulated HTTP.
// This measures preparation, not live-call CPU or browser native-module cache.
import { AptevaClient } from "@apteva/web-sdk";
import { loadAudioModules } from "../../frontend/src/audio-assets";
import worklet from "../../ui/softphone-worklet.js" with {type:"text"};
import worker from "../../ui/softphone-worker.js" with {type:"text"};
import { resolve } from "node:path";
import { unlink } from "node:fs/promises";

const root = resolve(import.meta.dir, "../..");
const baseline = Bun.argv[2] ?? "telephony/v0.12.17";
const source = Bun.spawnSync(["git", "show", `${baseline}:mcp/telephony/frontend/src/audio-assets.ts`], {cwd: root});
if (source.exitCode) throw new Error("Cannot read baseline loader");
const controlPath = resolve(root, `frontend/src/.asset-control-${crypto.randomUUID()}.ts`);
await Bun.write(controlPath, source.stdout);
const priorLocation = Object.getOwnPropertyDescriptor(globalThis, "location");
Object.defineProperty(globalThis, "location", {configurable:true,value:new URL("https://host.example/apps/telephony")});

async function run(load: typeof loadAudioModules, scenario: "cold_pcm" | "warm_pcm" | "concurrent_64_pcm" | "cold_rtc", optimized: boolean) {
  let requests=0, bytes=0, active=0, maximum=0;
  const waiting: Array<() => void> = [];
  const app = new AptevaClient({baseURL:"https://host.example",accessToken:"benchmark-only",fetch:(async (url) => {
    if (active === 4) await new Promise<void>(resolve => waiting.push(resolve));
    else active++;
    maximum=Math.max(maximum,active); requests++;
    try {
      await Bun.sleep(25);
      const body=String(url).includes("/worklet-")?worklet:worker;bytes+=new TextEncoder().encode(body).length;
      return new Response(body,{headers:{"Content-Type":"text/javascript"}});
    } finally { const next=waiting.shift();if(next)next();else active--; }
  }) as typeof fetch}).app("telephony",{projectId:"benchmark",installId:42});
  if(scenario==="warm_pcm"){(await load(app)).dispose(); requests=bytes=maximum=0;}
  const started=performance.now();
  const modules=await Promise.all(Array.from({length:scenario==="concurrent_64_pcm"?64:1},()=>load(app,scenario==="cold_rtc"&&optimized)));
  const elapsed=performance.now()-started; modules.forEach(module=>module.dispose());
  return {elapsed_ms:Math.round(elapsed*100)/100,verification_requests:requests,verification_source_bytes:bytes,max_http_concurrency:maximum};
}

try {
  const previous=await import(controlPath) as {loadAudioModules:typeof loadAudioModules};
  const results=[];
  for(const scenario of ["cold_pcm","warm_pcm","concurrent_64_pcm","cold_rtc"] as const){
    results.push({scenario,before:await run(previous.loadAudioModules,scenario,false),after:await run(loadAudioModules,scenario,true)});
  }
  console.log(JSON.stringify({baseline,transport:"simulated 25ms HTTP, four concurrent responses",scope:"integrity verification; native browser loading and call CPU excluded",results},null,2));
} finally {
  await unlink(controlPath);
  if(priorLocation)Object.defineProperty(globalThis,"location",priorLocation);else Reflect.deleteProperty(globalThis,"location");
}
