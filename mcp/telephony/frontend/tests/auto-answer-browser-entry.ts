import {TelephonyClient} from '../src/client';
import type {AppHandle} from '@apteva/web-sdk';
const request=async(path:string,method:string,body?:unknown)=>{((window as any).requests??=[]).push({path,method,at:performance.now()});const r=await fetch('/api'+path,{method,headers:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});if(!r.ok)throw Error(`${r.status}: ${await r.text()}`);return path.startsWith("/ui/frontend/")?r.text():r.json()};
const app={name:'telephony',projectId:'project-a',installId:42,mcpURL:()=>location.origin+'/api/mcp',get:(p:string)=>request(p,'GET'),post:(p:string,b:unknown)=>request(p,'POST',b)} as unknown as AppHandle;
const client=new TelephonyClient(app,{runtime:'proxy'});
const w=window as any;w.states=[];w.cue=[];
const warningMode=new URLSearchParams(location.search).get('warning');
const originalOscillator=AudioContext.prototype.createOscillator;
AudioContext.prototype.createOscillator=function(){
 if(warningMode==='blocked')throw new DOMException('Blocked warning','NotAllowedError');
 const node=originalOscillator.call(this),start=node.start.bind(node);
 node.start=(when=0)=>{w.cue.push({stage:'scheduled',at:performance.now(),frequency:node.frequency.value});start(when)};
 node.addEventListener('ended',()=>w.cue.push({stage:'rendered',at:performance.now()}));return node;
};w.phone=client.createSoftphone({pollIntervalMs:0,onAutoAnswer:s=>{w.states.push({...s,at:performance.now()});if(s.state==='warning'&&warningMode==='cancel')setTimeout(()=>w.phone.cancelAutoAnswer(),70)},audio:{mediaTransport:'websocket'},onSessionEvent:e=>{(w.events??=[]).push(e)}});
document.querySelector('button')!.addEventListener('click',()=>{w.ready=w.phone.setAutoAnswerAvailable(true).then(()=>{w.phone.observeCalls([{id:'auto-browser',status:'pending',direction:'inbound',peer_kind:'human',from_number:'+1',to_number:'+2',ring_offers:[{id:'sales-auto-browser',destination_id:'sales',kind:'browser'}]}]);}).catch((e:Error)=>{w.error=e.message;});});
