import {resolve,join} from "node:path";
import {mkdtemp,mkdir,rm} from "node:fs/promises";
import {tmpdir} from "node:os";
const root=resolve(import.meta.dir,"../..");
const opts=new Map<string,string>();
for(let i=2;i<process.argv.length;i+=2){const k=process.argv[i],v=process.argv[i+1];if(!["--core","--server","--output","--live"].includes(k)||!v||opts.has(k))throw Error("Invalid option");opts.set(k,v)}
if(!opts.has("--core")||!opts.has("--server"))throw Error("--core /path/core --server /path/server required");
if(opts.has("--live")&&opts.get("--live")!=="true")throw Error("--live true required");
const live=opts.get("--live")==="true";
if(live&&!process.env.OPENAI_API_KEY)throw Error("OPENAI_API_KEY must be supplied securely in the environment");
const output=resolve(opts.get("--output")??join(tmpdir(),`telephony-agent-audio-${Date.now()}`));
await mkdir(output,{recursive:false});
const temp=await mkdtemp(join(tmpdir(),"telephony-agent-build-"));
const env={...process.env,GOWORK:"off",TELEMETRY_URL:"",TELEMETRY_LIVE_URL:"",SERVER_URL:"",TELEPHONY_AGENT_AUDIO_LIVE:live?"1":"0"} as NodeJS.ProcessEnv;
// Never log credentials or include them in generated source/command arguments.
function redact(s:string){const key=process.env.OPENAI_API_KEY;return (key?s.split(key).join("[REDACTED]"):s).replace(/sk-[A-Za-z0-9_-]{16,}/g,"[REDACTED]")}
async function sourceHashes(cwd:string){
 const child=Bun.spawn(["rg","--files","-g","*.go","-g","go.mod","-g","go.sum"],{cwd,stdout:"pipe",stderr:"pipe"});
 const files=(await new Response(child.stdout).text()).trim().split("\n").sort();
 if(await child.exited)throw Error("Cannot fingerprint sources");
 const result:Record<string,string>={};for(const file of files){result[file]=new Bun.CryptoHasher("sha256").update(await Bun.file(join(cwd,file)).arrayBuffer()).digest("hex")};return result
}
try{
 const metadata:Record<string,unknown>={live,created_at:new Date().toISOString(),telephony_sources:await sourceHashes(root)};
 for(const component of ["core","server"]){
  const cwd=resolve(opts.get(`--${component}`)!);
  const source=component==="core"?join(import.meta.dir,"core_fixture.go.txt"):join(root,"benchmarks/internal-audio/server_fixture.go.txt");
  const virtual=join(cwd,"zz_telephony_agent_audio_fixture_test.go");if(await Bun.file(virtual).exists())throw Error("Fixture path occupied");
  const fixture=join(temp,`${component}.go`);await Bun.write(fixture,await Bun.file(source).arrayBuffer());
  const overlay=join(temp,`${component}.json`);await Bun.write(overlay,JSON.stringify({Replace:{[virtual]:fixture}}));
  const binary=join(temp,component);
  console.log(`Building actual ${component} fixture.`);
  const child=Bun.spawn(["go","test","-c","-overlay",overlay,"-o",binary,"."],{cwd,env,stdout:"pipe",stderr:"pipe"});
  const [stdout,stderr,code]=await Promise.all([new Response(child.stdout).text(),new Response(child.stderr).text(),child.exited]);
  await Bun.write(join(output,`${component}-build.log`),redact(stdout+stderr));if(code)throw Error(`${component} build failed; see output directory`);
  env[`TELEPHONY_INTERNAL_${component.toUpperCase()}_HELPER`]=binary;
  const revision=Bun.spawn(["git","rev-parse","HEAD"],{cwd,stdout:"pipe",stderr:"pipe"});
  const sources=Bun.spawn(["git","diff","--","*.go"],{cwd,stdout:"pipe",stderr:"pipe"});
  metadata[component]={revision:(await new Response(revision.stdout).text()).trim(),source_sha256:await sourceHashes(cwd),local_diff_sha256:new Bun.CryptoHasher("sha256").update(await new Response(sources.stdout).arrayBuffer()).digest("hex"),fixture_sha256:new Bun.CryptoHasher("sha256").update(await Bun.file(source).arrayBuffer()).digest("hex")};
 }
 await Bun.write(join(output,"source.json"),JSON.stringify(metadata,null,2)+"\n");
 const pattern=live?"^TestAgentAudioActualLiveCarrierPath$":"^TestAgentAudioActualCoreServerCarrierPath$";
 const child=Bun.spawn(["go","test","-tags","integration","-run",pattern,"-count=1","-timeout","4m","-v","."],{cwd:root,env:{...env,TELEPHONY_INTERNAL_OUTPUT:output},stdout:"pipe",stderr:"pipe"});
 const [stdout,stderr,code]=await Promise.all([new Response(child.stdout).text(),new Response(child.stderr).text(),child.exited]);
 const log=redact(stdout+stderr);await Bun.write(join(output,"verification.log"),log);console.log(log);process.exitCode=code;
 console.log(`Results: ${output}`);
}finally{await rm(temp,{recursive:true,force:true})}
