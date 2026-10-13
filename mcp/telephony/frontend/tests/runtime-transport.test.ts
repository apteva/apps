import {expect,test} from 'bun:test';
import type {AppHandle} from '@apteva/web-sdk';
import {RuntimeTelephonyTransport} from '../src/runtime-transport';

function fixture(endpoint='https://voice.example/runtime-browser'){
 const calls:Array<{path:string;body?:any}>=[];
 const app={name:'telephony',projectId:'project-a',installId:42,mcpURL:()=> 'https://management.example/api/apps/telephony/_install/42/mcp',
  get:async(path:string)=>{calls.push({path});return {managed:true,browser_api_base:endpoint,generation:1};},
  post:async(path:string,body:any)=>{calls.push({path,body});return {token:'scoped-browser-credential',expires_at:Math.floor(Date.now()/1000)+300,endpoint};},
  subscribe:()=>({close(){}}),
 } as unknown as AppHandle;
 return {app,calls};
}
test('direct requests use discovered endpoint and scoped credentials, with refresh sharing',async()=>{
 const f=fixture(),sent:Array<{url:URL;init:RequestInit}>=[];
 const transport=new RuntimeTelephonyTransport(f.app,'direct',(async(input: RequestInfo | URL, init?: RequestInit)=>{sent.push({url:new URL(String(input)),init:init!});return Response.json({ok:true});}) as unknown as typeof fetch);
 await Promise.all([transport.request('GET','/calls'),transport.request('POST','/softphone/place',{to:'+12025550100'})]);
 expect(f.calls.filter(x=>x.path==='/runtime-endpoints')).toHaveLength(1);
 expect(f.calls.filter(x=>x.path==='/runtime-browser-bootstrap')).toHaveLength(1);
 expect(f.calls.at(-1)?.body.origin).toBe('https://management.example');
 for(const request of sent){expect(request.url.origin).toBe('https://voice.example');expect(request.url.searchParams.get('project_id')).toBe('project-a');expect(new Headers(request.init.headers).get('Authorization')).toBe('Bearer scoped-browser-credential');expect(request.init.credentials).toBe('omit');expect(request.init.redirect).toBe('error');}
 const main=new URL('https://management.example/api/apps/telephony/_install/42/softphone/media/call-a/media-secret?session_generation=session-a');
 expect(transport.mediaURL(main).href).toBe('https://voice.example/runtime-browser/softphone/media/call-a/media-secret?session_generation=session-a');
});
test('legacy proxy request initiates synchronously and does not discover or delegate',async()=>{
 const f=fixture();let started=false;f.app.get=async()=>{started=true;return {ok:true} as any;};
 const transport=new RuntimeTelephonyTransport(f.app,'proxy');const result=transport.request('GET','/calls');expect(started).toBe(true);await result;expect(f.calls).toHaveLength(0);
});
test('discovery rejects credential-bearing, insecure and unrelated ingress paths',async()=>{
 for(const endpoint of ['http://voice.example/runtime-browser','https://user:secret@voice.example/runtime-browser','https://voice.example/runtime-browser?token=x','https://voice.example/arbitrary']){
  const f=fixture(endpoint),transport=new RuntimeTelephonyTransport(f.app,'direct');await expect(transport.prepare()).rejects.toThrow();expect(f.calls.some(c=>c.path==='/runtime-browser-bootstrap')).toBe(false);
 }
});
test('authorization failure never retries call creation through proxy',async()=>{
 const f=fixture(),transport=new RuntimeTelephonyTransport(f.app,'direct',(async()=>new Response('',{status:403})) as unknown as typeof fetch);
 await expect(transport.request('POST','/softphone/place',{idempotency_key:'same-call'})).rejects.toMatchObject({status:403});
 expect(f.calls.map(x=>x.path)).toEqual(['/runtime-endpoints','/runtime-browser-bootstrap']);
});
test('direct SSE uses bearer headers, bounded events and normal cleanup',async()=>{
 const f=fixture(),seen:any[]=[];
 const transport=new RuntimeTelephonyTransport(f.app,'direct',(async(_input: RequestInfo | URL, init?: RequestInit)=>{expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer scoped-browser-credential');return new Response('data: {"type":"calls.changed"}\n\n',{headers:{'Content-Type':'text/event-stream'}});}) as unknown as typeof fetch);
 const watch=transport.subscribe('/calls/events',e=>seen.push(e));
 for(let i=0;i<30&&!seen.length;i++)await Bun.sleep(5);
 watch.close();expect(seen).toEqual([{type:'calls.changed'}]);
});

test('backend can separate browser PCM media and WebRTC signaling ingress',async()=>{
 const f=fixture();
 f.app.get=async()=>({managed:true,browser_api_base:'https://voice.example/runtime-browser',browser_media_base:'https://pcm.example/runtime-browser',browser_signaling_base:'https://rtc.example/runtime-browser'}) as any;
 const transport=new RuntimeTelephonyTransport(f.app,'direct');await transport.prepare();
 const original=new URL('https://management.example/api/apps/telephony/_install/42/softphone/media/call-a/media-secret');
 expect(transport.mediaURL(original,'websocket').origin).toBe('https://pcm.example');
 expect(transport.mediaURL(original,'webrtc').origin).toBe('https://rtc.example');
 expect(transport.mediaURL(original,'auto').origin).toBe('https://rtc.example');
});
test('direct SSE retries transient reads and stops retrying when closed',async()=>{
 const f=fixture();let reads=0;const seen:any[]=[];
 const transport=new RuntimeTelephonyTransport(f.app,'direct',(async()=>{reads++;return reads===1?new Response('',{status:503}):new Response('data: {"type":"calls.changed"}\n\n');}) as unknown as typeof fetch);
 let watch:{close():void};watch=transport.subscribe('/calls/events',event=>{seen.push(event);watch.close();});
 for(let i=0;i<250&&!seen.length;i++)await Bun.sleep(5);
 expect(seen).toEqual([{type:'calls.changed'}]);expect(reads).toBe(2);
 await Bun.sleep(550);expect(reads).toBe(2);
});
test('direct call conflicts preserve the structured body used by offer handling',async()=>{
 const f=fixture(),transport=new RuntimeTelephonyTransport(f.app,'direct',(async()=>new Response('{"code":"offer_expired"}',{status:409})) as unknown as typeof fetch);
 await expect(transport.request('POST','/softphone/answer/call-a',{})).rejects.toMatchObject({status:409,body:'{"code":"offer_expired"}'});
});

test('configured external connection bypasses management discovery and only sends scoped grants',async()=>{
 const f=fixture();let issues=0;const sent:any[]=[];
 const transport=new RuntimeTelephonyTransport(f.app,'direct',(async(url:any,init:any)=>{sent.push({url:String(url),init});return Response.json({ok:true})}) as typeof fetch,{baseURL:'https://voice.example/runtime-browser',credentialProvider:async()=>{issues++;return {token:'external-scoped-grant',expires_at:Math.floor(Date.now()/1000)+300,endpoint:'https://voice.example/runtime-browser'}}});
 await Promise.all([transport.request('GET','/calls'),transport.request('GET','/softphone/numbers')]);
 expect(f.calls).toHaveLength(0);expect(issues).toBe(1);
 expect(sent.every(r=>r.url.startsWith('https://voice.example/runtime-browser/'))).toBe(true);
 expect(new Headers(sent[0].init.headers).get('Authorization')).toBe('Bearer external-scoped-grant');
});

test('native fetch is invoked with its global receiver',async()=>{
 const f=fixture();const branded=function(this:unknown){expect(this).toBe(globalThis);return Promise.resolve(Response.json({ok:true}))} as unknown as typeof fetch;
 const transport=new RuntimeTelephonyTransport(f.app,'direct',branded,{baseURL:'https://voice.example/runtime-browser',credentialProvider:async()=>({token:'scoped',expires_at:Math.floor(Date.now()/1000)+300,endpoint:'https://voice.example/runtime-browser'})});
 await transport.request('GET','/calls');
});
