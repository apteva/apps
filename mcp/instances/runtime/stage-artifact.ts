// Publish an already-built package to the protected Instances backend.
// This is an artifact handoff helper; it never builds or deploys an app.
import {resolve,relative} from "node:path";
import {lstat} from "node:fs/promises";
const [directory,endpoint,appName,appVersion,platform,executable,capsFile]=Bun.argv.slice(2);
if(!directory||!endpoint||!appName||!appVersion||!platform||!executable)throw new Error("Usage: bun run runtime/stage-artifact.ts <package-dir> <Instances-sidecar-URL> <app> <version> <linux-amd64|linux-arm64> <executable-path> [capabilities.json]");
const token=process.env.APTEVA_APP_TOKEN;if(!token)throw new Error("APTEVA_APP_TOKEN must be the Instances installation's control credential");
const url=new URL(endpoint);if(url.username||url.password||!(url.protocol==="https:"||url.protocol==="http:"&&["127.0.0.1","localhost","[::1]"].includes(url.hostname)))throw new Error("Use a private loopback endpoint or trusted HTTPS endpoint");
const base=endpoint.replace(/\/$/,"")+"/_apteva/runtime/backends/native";
async function request(path:string,method="GET",body?:BodyInit):Promise<Response>{return fetch(base+path,{method,headers:{Authorization:"Bearer "+token,...(typeof body==="string"?{"Content-Type":"application/json"}:{})},body,redirect:"error"});}
async function check(r:Response):Promise<any>{if(!r.ok)throw new Error(`Artifact staging failed: HTTP ${r.status}`);return r.json();}
const descriptor=await check(await request("/describe"));if(descriptor.contract!=="apteva.runtime-backend/v1"||descriptor.id!=="native")throw new Error("Endpoint is not the Instances native backend");
const root=resolve(directory);const paths=await Array.fromAsync(new Bun.Glob("**/*").scan({cwd:root,onlyFiles:true,followSymlinks:false}));paths.sort();
const files=[];for(const path of paths){
 const absolute=resolve(root,path);if(relative(root,absolute).startsWith("..")||(await lstat(absolute)).isSymbolicLink())throw new Error("Package must contain regular files only");
 const file=Bun.file(absolute);const hash=new Bun.CryptoHasher("sha256");for await(const chunk of file.stream())hash.update(chunk);
 files.push({path,sha256:hash.digest("hex"),size:file.size});
}
const app_capabilities=capsFile?JSON.parse(await Bun.file(capsFile).text()):{passive_start:false,drain:false,replica_safe:false,worker_coordination:false,routing:""};
const metadata={id:"",app_name:appName,app_version:appVersion,platform,executable,files,app_capabilities};
metadata.id=appName.slice(0,40)+"-"+new Bun.CryptoHasher("sha256").update(JSON.stringify(metadata)).digest("hex").slice(0,24);
await check(await request("/artifacts","POST",JSON.stringify(metadata)));
const sealed=await request(`/artifacts/${metadata.id}/seal`,"POST");
if(!sealed.ok){if(sealed.status!==400)await check(sealed);for(const f of files)await check(await request(`/artifacts/${metadata.id}/files/${f.path}`,"PUT",Bun.file(resolve(root,f.path))));await check(await request(`/artifacts/${metadata.id}/seal`,"POST"));}
console.log(JSON.stringify({artifact_id:metadata.id,app_name:appName,app_version:appVersion,platform,release_digest:files.find(f=>f.path===executable)?.sha256},null,2));
