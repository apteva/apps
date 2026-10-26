import { TelephonyClient } from '../src/client';
import type { AppHandle } from '@apteva/web-sdk';
(window as any).runManagedMedia = async () => {
  const gatewayRequests: string[] = [];
  const request = async (path: string, method: string, body?: unknown) => {
    gatewayRequests.push(path);
    const response = await fetch('/api' + path, { method, headers: {'Content-Type':'application/json'}, body: body === undefined ? undefined : JSON.stringify(body) });
    if (!response.ok) throw Error(`Gateway ${response.status}`);
    return response.json();
  };
  const app = {name:'telephony',projectId:'project-a',installId:42,mcpURL:()=>location.origin+'/api/apps/telephony/_install/42/mcp',get:(p:string)=>request(p,'GET'),post:(p:string,b:unknown)=>request(p,'POST',b)} as unknown as AppHandle;
  const client = new TelephonyClient(app);
  await client.listCalls();
  const session = await client.answer('call-soft-1', {rejoin:true});
  const socket = new WebSocket(client.mediaURL(session, 'websocket'));
  socket.binaryType='arraybuffer';
  const pcm = new Int16Array([1000,-4000,3000,0]);
  let ready=false, echoes=0;
  socket.onmessage=e=>{
    if(typeof e.data==='string') { if(JSON.parse(e.data).type==='ready')ready=true; }
    else { const received=new Int16Array(e.data);if(received.length!==pcm.length||received.some((v,i)=>v!==pcm[i]))throw Error('PCM changed');echoes++; }
  };
  const until = async (condition:()=>boolean) => {const deadline=performance.now()+5000;while(!condition()){if(socket.readyState===WebSocket.CLOSED||performance.now()>deadline)throw Error('Media did not become ready or echo');await new Promise(r=>setTimeout(r,10));}};
  try {
    await until(()=>ready);
    socket.send(pcm);await until(()=>echoes===1);
    const now=Date.now;
    try {Date.now=()=>now()+280000;await client.renew(session);} finally {Date.now=now;}
    // Give the server's ownership monitor time to validate the refreshed grant.
    await new Promise(r=>setTimeout(r,1200));
    socket.send(pcm);await until(()=>echoes===2);
    await client.listCalls();
    if(gatewayRequests.some(p=>/^\/softphone\/(answer|renew)\//.test(p)))throw Error('Media authorization used gateway');
    return {echoes,gatewayRequests,remoteMedia:new URL(socket.url).origin!==location.origin};
  } finally {socket.close();}
};
