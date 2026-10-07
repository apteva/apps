// Build reproducible release collectors and pin their SHA-256 in the app source.
// Usage: bun run collector/build.ts /absolute/release-output-directory
import {mkdir} from "node:fs/promises";
import {resolve,join} from "node:path";
const out=resolve(Bun.argv[2]||"/tmp/instances-collectors");await mkdir(out,{recursive:true});
const targets=[['linux','amd64'],['linux','arm64'],['darwin','amd64'],['darwin','arm64']];
const sums:Record<string,string>={};
await Promise.all(targets.map(async([platform,arch])=>{
 const asset=`instances-collector-${platform}-${arch}`,path=join(out,asset);
 const proc=Bun.spawn(['go','build','-trimpath','-ldflags=-s -w','-o',path,'./cmd/instances-collector'],{cwd:new URL('..',import.meta.url).pathname,env:{...process.env,GOWORK:'off',CGO_ENABLED:'0',GOOS:platform,GOARCH:arch},stdout:'inherit',stderr:'inherit'});
 if(await proc.exited!==0)throw new Error(`Collector build failed: ${asset}`);
 const data=await Bun.file(path).arrayBuffer();if(data.byteLength>16*1024*1024)throw new Error(`Collector exceeds SSH upload limit: ${asset}`);
 sums[asset]=new Bun.CryptoHasher('sha256').update(data).digest('hex');console.log(`${asset}: ${(data.byteLength/1024/1024).toFixed(2)} MiB`);
}));
const sorted=Object.fromEntries(Object.entries(sums).sort(([a],[b])=>a.localeCompare(b)));
await Bun.write(new URL('./checksums.json',import.meta.url),JSON.stringify(sorted,null,2)+'\n');
await Bun.write(join(out,'SHA256SUMS'),Object.entries(sorted).map(([name,sum])=>`${sum}  ${name}`).join('\n')+'\n');
console.log(`Collectors and SHA256SUMS ready in ${out}`);
