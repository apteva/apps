import vm from 'node:vm';
import {readFileSync} from 'node:fs';

function replay(source:string, rate:number, delayMS:number) {
 const ctors:Record<string,any>={};
 const clock=vm.createContext({Float32Array,sampleRate:rate,currentTime:0,AudioWorkletProcessor:class{port={postMessage(){}}},registerProcessor(n:string,c:any){ctors[n]=c;}});
 vm.runInContext(source,clock);
 const p=new ctors['softphone-playback']({processorOptions:{adaptiveReserve:false}});
 const frameLength=Math.round(rate*.02),out=new Float32Array(128);
 let generated=0,sequence=0,first:number|null=null,startingQueue:number|null=null;
 for(let n=0;n<rate*2;n+=128) {
  const now=n*1000/rate;clock.currentTime=n/rate;
  while(sequence===0 || now>=delayMS+(sequence-1)*20) {
   p.handleMessage({frame:new Float32Array(frameLength).fill(.25),sequence});generated+=frameLength;sequence++;
  }
  const queue=p.queued*1000/rate;p.process([],[[out]]);
  if(first===null&&p.playedSamples>0){first=now;startingQueue=queue;}
 }
 return {rate,second_packet_at_ms:delayMS,start_ms:first,starting_queue_ms:startingQueue,underruns:p.underruns,missing_ms:p.underrunSamples*1000/rate,dropped_ms:p.droppedSamples*1000/rate,accounted_samples:p.playedSamples+p.queued+p.droppedSamples,generated_samples:generated};
}
if(import.meta.main) {
 const previous=Bun.spawnSync(['git','show','telephony/v0.11.1:mcp/telephony/ui/softphone-worklet.js']);
 if(previous.exitCode!==0)throw Error('Cannot load released comparison worklet');
 const source=readFileSync(new URL('../../ui/softphone-worklet.js',import.meta.url),'utf8'),results=[];
 for(const rate of [24000,44100,48000])for(const delay of [20,50,80,90,120]) {
  const old=replay(previous.stdout.toString(),rate,delay),current=replay(source,rate,delay);
  if(current.accounted_samples!==current.generated_samples||current.dropped_ms!==0)throw Error('Startup replay lost supplied samples');
  if(delay>=80 && current.missing_ms>=old.missing_ms)throw Error('Delayed startup did not reduce underruns');
  results.push({previous:old,current});
 }
 const report={schema:'telephony-startup-replay/v1',created_at:new Date().toISOString(),scope:'Local actual worklets; released 0.11.1 versus current; first packet then delayed continuous delivery; not production packet reconstruction',worklet_sha256:new Bun.CryptoHasher('sha256').update(source).digest('hex'),results};
 const path=process.argv[2]??'/tmp/telephony-startup-replay.json';await Bun.write(path,JSON.stringify(report,null,2));
 for(const r of results)console.log(`${r.current.rate}Hz / second at ${r.current.second_packet_at_ms}ms: missing ${r.previous.missing_ms.toFixed(1)} -> ${r.current.missing_ms.toFixed(1)}ms; start ${r.previous.start_ms?.toFixed(1)} -> ${r.current.start_ms?.toFixed(1)}ms`);
}
