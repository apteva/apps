import { useEffect, useRef, useState } from "react";
import { useConversationAPI } from "./context";
import type { Attachment } from "./types";
import { useConversationLocalization } from "./i18n";
export function reportSectionsText(value: unknown): string {
  if (!Array.isArray(value)) return "";
  return value.map(section => {
    if (!section || typeof section !== "object") return String(section ?? "");
    const {title, heading, body, text, content, ...rest} = section as Record<string,unknown>;
    return [title || heading ? `## ${String(title || heading)}` : "", String(body ?? text ?? content ?? ""), Object.keys(rest).length ? JSON.stringify(rest,null,2) : ""].filter(Boolean).join("\n\n");
  }).filter(Boolean).join("\n\n");
}
export function AttachmentContent({attachments=[],chatID}: {attachments?:Attachment[];chatID?:string}) {
 const {t}=useConversationLocalization();const {conversationsClient}=useConversationAPI();
 const [zoom,setZoom]=useState<Attachment|null>(null);const [error,setError]=useState("");const [busy,setBusy]=useState("");
 const download=async(item:Attachment)=>{if(!item.id||!chatID)return;setBusy(item.id);setError("");try{
  const result=await conversationsClient.attachment(chatID,item.id);const bytes=Uint8Array.from(atob(result.content_base64),c=>c.charCodeAt(0));
  const url=URL.createObjectURL(new Blob([bytes],{type:"application/octet-stream"}));const link=document.createElement("a");link.href=url;link.download=item.name||"attachment";link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
 }catch(e){setError(String(e));}finally{setBusy("");}};
 return <>{attachments.length>0&&<div className="chat-message-attachments">{attachments.map((item,index)=>{
 const visual=item.type==="image"&&/^data:image\/(png|jpeg|gif|webp);base64,/i.test(item.data_url??"");
 return <div className={`chat-message-attachment ${visual?"chat-message-photo":""}`} key={item.id||index}>
 {visual
 ? <button type="button" className="chat-image-preview" aria-label={t("composer.enlarge",{name:item.name||t("attachment.image")})} title={item.name} onClick={()=>setZoom(item)}><img src={item.data_url} alt={item.name||t("attachment.image")} loading="lazy"/></button>
 : <><span className="chat-file-icon" aria-hidden="true">▤</span><div className="chat-file-info"><span>{item.name||t("attachment.image")}</span>{item.size!=null&&<small>{item.mime_type} · {(item.size/1024).toFixed(1)} KB</small>}</div>
 {item.id&&chatID&&<button type="button" disabled={busy===item.id} onClick={()=>download(item)}>{t("composer.download")}</button>}</>}
 </div>;
 })}</div>}{error&&<p role="alert">{error}</p>}
 {zoom&&<ImageDialog item={zoom} close={()=>setZoom(null)} download={()=>download(zoom)} label={t("composer.close")} downloadLabel={t("composer.download")}/>}
 </>;
}
function ImageDialog({item,close,download,label,downloadLabel}:{item:Attachment;close:()=>void;download:()=>void;label:string;downloadLabel:string}){
 const ref=useRef<HTMLDialogElement>(null);useEffect(()=>{ref.current?.showModal();return()=>ref.current?.close()},[]);
 return <dialog ref={ref} className="chat-image-dialog" onCancel={close} onClick={e=>{if(e.target===e.currentTarget)close()}}><button type="button" aria-label={label} onClick={close}>×</button><img src={item.data_url} alt={item.name||""}/><p className="chat-image-caption">{item.name}{item.size!=null&&<small>{item.mime_type} · {(item.size/1024).toFixed(1)} KB</small>}</p>{item.id&&<button type="button" onClick={download}>{downloadLabel}</button>}</dialog>;
}
export function GenericComponents({components=[]}: {components?:Array<{app:string;name:string;props:Record<string,unknown>}>}) {
 return <>{components.filter(c => !["approval-card","report-card","alert-card"].includes(c.name)).map((c,i) => <details key={i} className="rounded border border-border p-2"><summary>{c.app}: {c.name}</summary><pre className="whitespace-pre-wrap break-words text-xs">{JSON.stringify(c.props,null,2)}</pre></details>)}</>;
}
