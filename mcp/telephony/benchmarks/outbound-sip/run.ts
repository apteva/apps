import {resolve,join} from 'node:path';
import {mkdir} from 'node:fs/promises';
import {tmpdir} from 'node:os';
const args=process.argv.slice(2),options=new Map<string,string>();
for(let i=0;i<args.length;i+=2){if(!['--minutes','--seconds','--calls','--output','--recording','--pause-ms'].includes(args[i])||!args[i+1])throw new Error('Usage: bun run benchmark:outbound-sip --minutes 30 --calls 16 [--output /new/path] [--recording true] [--pause-ms 570]');options.set(args[i],args[i+1]);}
if(options.has('--minutes')&&options.has('--seconds'))throw new Error('Choose minutes or seconds');
if(options.has('--recording')&&!['true','false'].includes(options.get('--recording')!))throw new Error('Recording must be true or false');
const seconds=options.has('--seconds')?Number(options.get('--seconds')):60*Number(options.get('--minutes')??30),calls=Number(options.get('--calls')??16);
const pauseMS=Number(options.get('--pause-ms')??420);
if(!Number.isInteger(pauseMS)||pauseMS<20||pauseMS>1000)throw new Error('Pause must be 20..1000 milliseconds');
if(!Number.isInteger(seconds)||seconds<8||seconds>14400||!Number.isInteger(calls)||calls<2||calls>64)throw new Error('Duration 8..14400 seconds; calls 2..64');
const root=resolve(import.meta.dir,'../..'),output=resolve(options.get('--output')??join(tmpdir(),`telephony-outbound-soak-${Date.now()}`));
await mkdir(output,{recursive:false});
const revision=Bun.spawnSync(['git','rev-parse','HEAD'],{cwd:root}).stdout.toString().trim();
const sourceFiles=Bun.spawnSync(['rg','--files','-g','*.go','-g','*.ts','-g','*.js'],{cwd:root}).stdout.toString().trim().split('\n').sort();
const hashes:Record<string,string>={};for(const file of sourceFiles)hashes[file]=new Bun.CryptoHasher('sha256').update(await Bun.file(join(root,file)).arrayBuffer()).digest('hex');
const dirty=Bun.spawnSync(['git','status','--porcelain','--','.'],{cwd:root}).stdout.toString().trim().length>0;

await Bun.write(join(output,'source.json'),JSON.stringify({revision,dirty,source_sha256:hashes,appVersion:(await Bun.file(join(root,'apteva.yaml')).text()).match(/^version:\s*(.*)$/m)?.[1],seconds,calls,pause_ms:pauseMS,recording:options.get('--recording')==='true',platform:process.platform,arch:process.arch,scope:'Local Chromium, production Telephony outbound SIP engine and local TLS/SRTP peer. No production, staging or live carrier.'},null,2)+'\n');
const child=Bun.spawn(['go','test','-tags','integration','-run','^TestOutboundSIPBrowserSoak$','-count=1','-timeout',`${seconds+240}s`,'-v','.'],{cwd:root,env:{...process.env,GOWORK:'off',TELEPHONY_OUTBOUND_SOAK_OUTPUT:output,TELEPHONY_OUTBOUND_SOAK_SECONDS:String(seconds),TELEPHONY_OUTBOUND_SOAK_CALLS:String(calls),TELEPHONY_OUTBOUND_SOAK_PAUSE_MS:String(pauseMS),TELEPHONY_OUTBOUND_SOAK_RECORDING:options.get('--recording')==='true'?'1':'0'},stdout:'pipe',stderr:'pipe'});
const read=async(stream:ReadableStream<Uint8Array>,name:string)=>{const file=Bun.file(join(output,name)).writer();for await(const chunk of stream){file.write(chunk);process.stdout.write(chunk);}await file.end();};
await Promise.all([read(child.stdout,'test.log'),read(child.stderr,'test-errors.log')]);const testExitCode=await child.exited;process.exitCode=testExitCode;
const changed:string[]=[];
for(const [file,hash]of Object.entries(hashes)){try{if(new Bun.CryptoHasher('sha256').update(await Bun.file(join(root,file)).arrayBuffer()).digest('hex')!==hash)changed.push(file);}catch{changed.push(file);}}
await Bun.write(join(output,'source-verification.json'),JSON.stringify({unchanged:changed.length===0,changed_files:changed,test_exit_code:testExitCode},null,2)+'\n');
for(const file of changed){process.exitCode=1;console.error(`Source changed during soak: ${file}`);}
console.log(`Results: ${output}`);
