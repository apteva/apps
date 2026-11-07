// No call/carrier commands. The token stays in an HTTP header, never in a URL
// or the result file. The optional control endpoint must echo text unchanged.
import {resolve} from 'node:path';

const args = process.argv.slice(2);
const options = new Map<string,string>();
for (let i=0;i<args.length;i+=2) {
  if (!['--url','--control-url','--seconds','--output','--codec'].includes(args[i]) || !args[i+1]) throw new Error('Usage: bun benchmarks/media-diagnostic/probe.ts --url wss://.../media/diagnostic/echo [--control-url wss://.../echo] [--seconds 0] [--output /path/result.json]');
  if(options.has(args[i]))throw new Error('Duplicate option');options.set(args[i],args[i+1]);
}
const seconds=Number(options.get('--seconds')??'0');
if(!Number.isSafeInteger(seconds)||seconds<0)throw new Error('--seconds must be a nonnegative safe integer; 0 runs until stopped');
const codecs={ 'l16-16':{encoding:'L16',sample_rate:16000,payload:Buffer.alloc(640).toString('base64')},'l16-24':{encoding:'L16',sample_rate:24000,payload:Buffer.alloc(960).toString('base64')},'pcmu-8':{encoding:'PCMU',sample_rate:8000,payload:Buffer.alloc(160,0xff).toString('base64')} };
const codecName=options.get('--codec')??'l16-16';
if(!(codecName in codecs))throw new Error('--codec must be l16-16, l16-24 or pcmu-8');
const codec=codecs[codecName as keyof typeof codecs];
const token=process.env.TELEPHONY_DIAGNOSTIC_TOKEN;
if(!token||!/^[a-f0-9]{64}$/.test(token))throw new Error('Set TELEPHONY_DIAGNOSTIC_TOKEN to the temporary token');
function endpoint(raw:string|undefined):string {
  if(!raw)throw new Error('--url required');const u=new URL(raw);
  if(!['ws:','wss:'].includes(u.protocol)||u.username||u.password||u.search||u.hash)throw new Error('Use a credential-free WebSocket URL with no query/fragment');
  return u.toString();
}
const target=endpoint(options.get('--url'));
const control=options.has('--control-url')?endpoint(options.get('--control-url')):undefined;
const probeSettings={duration_seconds:seconds,frame_duration_ms:20,max_pending_frames:1000,max_buffered_bytes:512*1024,checkpoint_interval_seconds:10,automatic_reconnect:false};
const output=resolve(options.get('--output')??'/private/tmp/telephony-media-diagnostic.json');
const snapshots=new Map<string,()=>unknown>();
const stops=new Set<(reason:string)=>void>();
let stopRequested='';
const stop=(reason='operator_stop')=>{stopRequested=reason;for(const fn of stops)fn(reason);};
process.on('SIGINT',()=>stop());process.on('SIGTERM',()=>stop());
let writing=false;
const checkpoint=async()=>{
  if(writing)return;writing=true;
  try{await Bun.write(output,JSON.stringify({state:'running',checkpoint_at:new Date().toISOString(),codec:codecName,settings:probeSettings,results:[...snapshots.values()].map(fn=>fn())},null,2)+'\n');}
  finally{writing=false;}
};
const checkpoints=setInterval(()=>{void checkpoint();},10000);
const startAt=performance.now();
const at=()=>new Date().toISOString();
const ms=(n:number)=>Math.round(n*1000)/1000;
const safeCloseReason=(s:string)=> /:\/\/|token|secret|bearer|authorization/i.test(s)?'peer reason redacted':s.slice(0,128);
type Observation={at:string;event:string;sequence?:number;value_ms?:number;buffered_bytes?:number};
async function run(label:string,url:string,headers?:Record<string,string>) {
  const events:Observation[]=[];let evicted=0;
  const record=(event:Observation)=>{if(events.length===256){events.shift();evicted++;}events.push(event);};
  let sent=0,received=0,lastReceipt=0,maxReceiptGap=0,maxRTT=0,maxSendLateness=0,maxBuffered=0;
  let nextSendAt=startAt;
  let closeCode=0,closeReason='',failure='',normal=false,duplicates=0,endedAt='';
  const pending=new Map<number,number>();
  let timer:ReturnType<typeof setTimeout>|undefined;
  let watchdog:ReturnType<typeof setInterval>|undefined;
  const ws=new WebSocket(url,headers?{headers}:undefined);
  const snapshot=()=>({label,started_at:new Date(performance.timeOrigin+startAt).toISOString(),ended_at:endedAt,state:endedAt?'finished':'running',sent_frames:sent,echoed_frames:received,missing_frames:pending.size,duplicate_frames:duplicates,max_receipt_gap_ms:ms(maxReceiptGap),max_echo_rtt_ms:ms(maxRTT),max_sender_timer_lateness_ms:ms(maxSendLateness),max_buffered_bytes:maxBuffered,close_code:closeCode,close_reason:closeReason,expected_finish:normal,failure,events,events_evicted:evicted});
  snapshots.set(label,snapshot);
  await new Promise<void>(resolveDone=>{
    let opened=false,done=false,closingAt=0;
    const finish=()=>{if(done)return;done=true;endedAt=at();if(timer)clearTimeout(timer);if(watchdog)clearInterval(watchdog);stops.delete(stopRun);resolveDone();};
    const stopRun=(reason:string)=>{
      if(done)return;
      normal=true;record({at:at(),event:reason});
      if(timer)clearTimeout(timer);
      if(ws.readyState===WebSocket.OPEN){ws.send(JSON.stringify({event:'stop',stream_id:'diagnostic-stream'}));timer=setTimeout(()=>ws.close(1000,'operator stop'),1000);}
      else {ws.close(1000,'operator stop');}
    };
    stops.add(stopRun);
    // Poll instead of a giant setTimeout: multi-day tests cannot overflow a timer.
    watchdog=setInterval(()=>{
      const elapsed=performance.now()-startAt;
      if(ws.readyState===WebSocket.CLOSING&&!closingAt)closingAt=performance.now();
      if((!opened&&elapsed>10000)||(seconds>0&&elapsed>(seconds+10)*1000)||(closingAt>0&&performance.now()-closingAt>10000)){
        failure='probe_connect_or_finish_timeout';record({at:at(),event:failure});ws.close(1000,'probe timeout');finish();
      }
    },1000);
    const sendFrame=()=>{
      if(ws.readyState!==WebSocket.OPEN)return;
      const now=performance.now();
      // No catch-up flood from our own delayed scheduler. Report missed slots.
      const slot=Math.floor((now-startAt)/20);
      if(now<nextSendAt){timer=setTimeout(sendFrame,Math.max(1,nextSendAt-now));return;}
      const lateness=Math.max(0,now-nextSendAt);
      maxSendLateness=Math.max(maxSendLateness,lateness);
      if(seconds>0&&now-startAt>=seconds*1000){normal=true;ws.send(JSON.stringify({event:'stop',stream_id:'diagnostic-stream'}));timer=setTimeout(()=>ws.close(1000,'diagnostic finished'),1000);return;}
      const sequence=sent+1;
      pending.set(sequence,now);
      if(pending.size>probeSettings.max_pending_frames){failure='too_many_unreturned_frames';ws.close(1000,'probe bounded pending limit');return;}
      ws.send(JSON.stringify({event:'media',stream_id:'diagnostic-stream',sequence_number:sequence,media:{track:'inbound',chunk:sequence,timestamp:slot*20,payload:codec.payload}}));sent++;
      maxBuffered=Math.max(maxBuffered,ws.bufferedAmount);
      if(ws.bufferedAmount>probeSettings.max_buffered_bytes){failure='probe_send_backpressure_limit';ws.close(1000,'probe backpressure limit');return;}
      // Save periodic and delayed send observations, never payloads or URLs.
      if(sequence%50===1||lateness>5)record({at:at(),event:'send',sequence,value_ms:ms(lateness),buffered_bytes:ws.bufferedAmount});
      nextSendAt=startAt+(slot+1)*20;
      timer=setTimeout(sendFrame,Math.max(1,nextSendAt-performance.now()));
    };
    ws.addEventListener('open',()=>{opened=true;record({at:at(),event:'connected'});
      ws.send(JSON.stringify({event:'connected',version:'1.0.0'}));
      ws.send(JSON.stringify({event:'start',stream_id:'diagnostic-stream',start:{media_format:{encoding:codec.encoding,sample_rate:codec.sample_rate,channels:1}}}));if(stopRequested)stopRun(stopRequested);else sendFrame();
    });
    ws.addEventListener('message',event=>{
      const now=performance.now();
      try {
        if(typeof event.data!=='string')throw new Error('unexpected frame type');
        const frame=JSON.parse(event.data);
        if(frame.event==='stop'){normal=true;ws.close(1000,'diagnostic finished');return;}
        if(frame.event!=='media')return;
        const sequence=Number(frame.sequence_number);const before=pending.get(sequence);
        if(before===undefined){duplicates++;return;}
        if(frame.media?.payload!==codec.payload){failure='echo_payload_changed';ws.close(1000,'probe payload mismatch');return;}
        pending.delete(sequence);received++;
        const gap=lastReceipt?now-lastReceipt:0;lastReceipt=now;
        const rtt=now-before;maxReceiptGap=Math.max(maxReceiptGap,gap);maxRTT=Math.max(maxRTT,rtt);
        if(gap>40||rtt>40||sequence%50===1)record({at:at(),event:'echo',sequence,value_ms:ms(rtt)});
      }catch{failure='invalid_echo_frame';ws.close(1000,'probe invalid echo');}
    });
    ws.addEventListener('error',()=>{failure='websocket_error';record({at:at(),event:failure});});
    ws.addEventListener('close',event=>{closeCode=event.code;closeReason=safeCloseReason(event.reason.replaceAll(token!,"REDACTED"));record({at:at(),event:'closed'});if(!opened&&!failure)failure='closed_before_open';finish();if(!normal||failure)stop('paired_probe_failed');});
  });
  return snapshot();
}
const results=await Promise.all([run('telephony',target,{'X-Telephony-Diagnostic-Token':token}),...(control?[run('control',control)]:[])]);
clearInterval(checkpoints);
while(writing)await Bun.sleep(1);
await Bun.write(output,JSON.stringify({codec:codecName,settings:probeSettings,clock:'client monotonic RTT; UTC timestamps for correlation; not synchronised one-way latency',results},null,2)+'\n');
for(const r of results)console.log(`${r.label}: ${r.echoed_frames}/${r.sent_frames} frames; max RTT ${r.max_echo_rtt_ms} ms; receive gap ${r.max_receipt_gap_ms} ms; close ${r.close_code}; ${r.failure||'finished'}`);
console.log(`Report: ${output}`);
process.exit(results.some(r=>r.failure||!r.expected_finish||r.missing_frames||r.duplicate_frames||r.close_code!==1000)?1:0);
