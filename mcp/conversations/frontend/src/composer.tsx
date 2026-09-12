import { useEffect, useRef, useState } from "react";
import type { Attachment } from "./types";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";

export interface ComposerOptions {
  files?: boolean;
  screenshot?: boolean;
  accept?: string;
  maxFiles?: number;
  maxFileBytes?: number;
  captureScreenshot?: () => Promise<File | null>;
  actions?: Array<{id:string;label:string;run:()=>Promise<File[] | File | null>}>;
}
export interface DraftAttachment {key:string;name:string;file?:File;attachment?:Attachment;error?:string;busy?:boolean}
const randomID=()=>Array.from(crypto.getRandomValues(new Uint8Array(16)),n=>n.toString(16).padStart(2,"0")).join("");
export async function fileBase64(file:Blob):Promise<string>{
 return new Promise((resolve,reject)=>{const reader=new FileReader();reader.onerror=()=>reject(new Error("Cannot read file"));reader.onload=()=>resolve(String(reader.result).split(",")[1]);reader.readAsDataURL(file);});
}
export async function captureScreenshot():Promise<File|null>{
 const stream=await navigator.mediaDevices.getDisplayMedia({video:true,audio:false});
 try{
  const video=document.createElement("video");video.srcObject=stream;video.muted=true;await video.play();
  const canvas=document.createElement("canvas");canvas.width=video.videoWidth;canvas.height=video.videoHeight;
  canvas.getContext("2d")!.drawImage(video,0,0);
  const blob=await new Promise<Blob|null>(resolve=>canvas.toBlob(resolve,"image/png"));
  return blob?new File([blob],`Screenshot-${new Date().toISOString().replace(/[:.]/g,"-")}.png`,{type:"image/png"}):null;
 }finally{stream.getTracks().forEach(track=>track.stop());}
}
export function useComposerAttachments(chat:string,storageKey:string){
 const {conversationsClient,composer={}}=useConversationAPI();const {t}=useConversationLocalization();
 const [items,setItems]=useState<DraftAttachment[]>(()=>{try{return JSON.parse(sessionStorage.getItem(storageKey+":attachments")||"[]");}catch{return [];}});
 const [error,setError]=useState("");const mounted=useRef(true);const current=useRef(items);current.current=items;
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;};},[]);
 useEffect(()=>{try{sessionStorage.setItem(storageKey+":attachments",JSON.stringify(items.filter(i=>i.attachment).map(({key,name,attachment})=>({key,name,attachment}))));}catch{}},[items,storageKey]);
 const update=(key:string,patch:Partial<DraftAttachment>)=>{if(mounted.current)setItems(all=>all.map(i=>i.key===key?{...i,...patch}:i));};
 const upload=async(item:DraftAttachment)=>{
  if(!item.file)return;update(item.key,{busy:true,error:undefined});
  try{const attachment=await conversationsClient.upload(chat,item.key,item.file.name,await fileBase64(item.file));update(item.key,{attachment,busy:false});}
  catch(e){update(item.key,{busy:false,error:String(e)});}
 };
 const add=async(files:File[])=>{
  setError("");const max=Math.min(composer.maxFiles??10,10);
  if(current.current.length+files.length>max){setError(t("composer.tooMany",{count:max}));return;}
  if(files.some(f=>f.size===0||f.size>Math.min(composer.maxFileBytes??10*1024*1024,10*1024*1024))){setError(t("composer.tooLarge"));return;}
  if(composer.accept){const patterns=composer.accept.toLowerCase().split(",").map(s=>s.trim());if(files.some(f=>!patterns.some(p=>p.startsWith(".")?f.name.toLowerCase().endsWith(p):p.endsWith("/*")?f.type.startsWith(p.slice(0,-1)):f.type===p))){setError(t("composer.unsupported"));return;}}
  const added=files.map(file=>({key:randomID(),file,name:file.name,busy:true}));current.current=[...current.current,...added];setItems(current.current);
  // Sequential uploads keep memory bounded when selecting several large files.
  for(const item of added)await upload(item);
 };
 const clearSent=(sent:Attachment[])=>{const ids=new Set(sent.map(i=>i.id));
  try{const saved:DraftAttachment[]=JSON.parse(sessionStorage.getItem(storageKey+":attachments")||"[]");sessionStorage.setItem(storageKey+":attachments",JSON.stringify(saved.filter(i=>!ids.has(i.attachment?.id))));}catch{}
  if(mounted.current)setItems(all=>all.filter(i=>!i.attachment||!ids.has(i.attachment.id)));
 };
 return {items,add,retry:upload,remove:(key:string)=>setItems(all=>all.filter(i=>i.key!==key)),clearSent,error,setError,options:composer};
}
export type ComposerController=ReturnType<typeof useComposerAttachments>;
export function ComposerAttachments({controller}:{controller:ComposerController}){
 const {t}=useConversationLocalization();
 return <>{controller.items.length>0&&<div className="chat-attachment-tray">{controller.items.map(item=><div className="chat-attachment-chip" key={item.key}>
 {item.attachment?.type==="image"&&<img src={item.attachment.data_url} alt={item.name}/>}
 <span title={item.name}>{item.name}</span>
 {item.busy&&<span role="status">{t("composer.uploading")}</span>}
 {item.error&&<button type="button" title={item.error} onClick={()=>controller.retry(item)}>{t("composer.retry")}</button>}
 <button type="button" aria-label={t("composer.remove",{name:item.name})} onClick={()=>controller.remove(item.key)}>×</button>
 </div>)}</div>}{controller.error&&<p className="chat-attachment-error" role="alert">{controller.error}</p>}</>;
}
export function ComposerMenu({controller}:{controller:ComposerController}){
 const {t}=useConversationLocalization();const input=useRef<HTMLInputElement>(null);const [open,setOpen]=useState(false);const [capturing,setCapturing]=useState(false);const root=useRef<HTMLDivElement>(null);
 const opts=controller.options;const canCapture=Boolean(opts.captureScreenshot||globalThis.navigator?.mediaDevices?.getDisplayMedia);
 useEffect(()=>{if(!open)return;const outside=(e:PointerEvent)=>{if(!root.current?.contains(e.target as Node))setOpen(false)};document.addEventListener("pointerdown",outside);return()=>document.removeEventListener("pointerdown",outside)},[open]);
 const run=async(action:()=>Promise<File[]|File|null>)=>{setOpen(false);setCapturing(true);try{const files=await action();if(files)await controller.add(Array.isArray(files)?files:[files]);}catch(e){if(!(e instanceof DOMException&&e.name==="NotAllowedError"))controller.setError(t("composer.captureFailed"));}finally{setCapturing(false)}};
 if(opts.files===false&&opts.screenshot===false&&!opts.actions?.length)return null;
 return <div ref={root} className="chat-composer-menu" onKeyDown={e=>{if(e.key==="Escape"){setOpen(false);root.current?.querySelector<HTMLButtonElement>("button")?.focus()}}}>
 <input ref={input} type="file" multiple accept={opts.accept} hidden onChange={e=>{void controller.add(Array.from(e.target.files??[]));e.target.value="";}}/>
 <button type="button" className="chat-composer-add" aria-label={t("composer.add")} aria-expanded={open} disabled={capturing} onClick={()=>setOpen(!open)}><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true"><path d="M12 5v14 M5 12h14"/></svg></button>
 {open&&<div className="chat-composer-popover">
 {opts.files!==false&&<button type="button" onClick={()=>{setOpen(false);input.current?.click()}}><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><path d="m8 13 7-7a3 3 0 0 1 4 4L9 20a5 5 0 0 1-7-7L13 2"/></svg><span>{t("composer.files")}</span></button>}
 {opts.screenshot!==false&&<button type="button" disabled={!canCapture} title={!canCapture?t("composer.captureUnavailable"):undefined} onClick={()=>run(opts.captureScreenshot??captureScreenshot)}><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><path d="M3 7h4l2-3h6l2 3h4v13H3z"/><circle cx="12" cy="13" r="4"/></svg><span>{t("composer.screenshot")}</span></button>}
 {opts.actions?.map(action=><button type="button" key={action.id} onClick={()=>run(action.run)}>{action.label}</button>)}
 </div>}
 </div>;
}
