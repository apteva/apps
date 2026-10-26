import {TelephonyClient} from '../src/client';
import type {AppHandle} from '@apteva/web-sdk';
const request=async(path:string,method:string,body?:unknown)=>{((window as any).requests??=[]).push({path,method});const r=await fetch('/api'+path,{method,headers:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});if(!r.ok)throw Error(`${r.status}: ${await r.text()}`);return path.startsWith("/ui/frontend/")?r.text():r.json()};
const app={name:'telephony',projectId:'project-a',installId:42,mcpURL:()=>location.origin+'/api/mcp',get:(p:string)=>request(p,'GET'),post:(p:string,b:unknown)=>request(p,'POST',b)} as unknown as AppHandle;
const client=new TelephonyClient(app,{runtime:'proxy'});
const w=window as any;w.states=[];w.phone=client.createSoftphone({pollIntervalMs:0,onAutoAnswer:s=>w.states.push(s),audio:{mediaTransport:'websocket'},onSessionEvent:e=>{(w.events??=[]).push(e)}});
document.querySelector('button')!.addEventListener('click',()=>{w.ready=w.phone.setAutoAnswerAvailable(true).then(()=>{w.phone.observeCalls([{id:'auto-browser',status:'pending',direction:'inbound',peer_kind:'human',from_number:'+1',to_number:'+2',ring_offers:[{id:'offer',destination_id:'sales',kind:'browser'}]}]);}).catch((e:Error)=>{w.error=e.message;});});
