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
export function AttachmentContent({attachments=[],chatID,messageID}: {attachments?:Attachment[];chatID?:string;messageID?:number}) {
 const {t}=useConversationLocalization();const {conversationsClient}=useConversationAPI();
 const [zoom,setZoom]=useState<Attachment|null>(null);const [error,setError]=useState("");const [busy,setBusy]=useState("");const [resolved,setResolved]=useState<Record<string,string>>({});
 const load=async(item:Attachment)=>{if(!chatID)return null;const result=item.ref&&messageID?await conversationsClient.attachmentReference(chatID,messageID,item.ref):item.id?await conversationsClient.attachment(chatID,item.id):null;if(!result)return null;const bytes=Uint8Array.from(atob(result.content_base64),c=>c.charCodeAt(0));return {item:result.attachment,url:URL.createObjectURL(new Blob([bytes],{type:result.attachment.mime_type||item.mime_type||"application/octet-stream"}))};};
 useEffect(()=>{let active=true;const urls:string[]=[];const refs=attachments.filter(item=>item.ref&&messageID);void Promise.all(refs.map(async item=>{try{const result=await load(item);if(active&&result){urls.push(result.url);setResolved(prev=>({...prev,[item.ref!]:result.url}));}}catch(e){if(active)setError(String(e));}}));return()=>{active=false;urls.forEach(url=>URL.revokeObjectURL(url));};},[attachments,messageID,chatID]);
 const download=async(item:Attachment)=>{if((!item.id&&!item.ref)||!chatID)return;setBusy(item.ref||item.id||"");setError("");try{const result=await load(item);if(!result)throw new Error("attachment unavailable");const link=document.createElement("a");link.href=result.url;link.download=item.name||result.item.name||"attachment";link.click();setTimeout(()=>URL.revokeObjectURL(result.url),1000);}catch(e){setError(String(e));}finally{setBusy("");}};
 return <>{attachments.length>0&&<div className="chat-message-attachments">{attachments.map((item,index)=>{
 const source=item.data_url||resolved[item.ref||""];const visual=item.type==="image"&&Boolean(source);
 return <div className={`chat-message-attachment ${visual?"chat-message-photo":""}`} key={item.id||item.ref||index}>
 {visual
 ? <button type="button" className="chat-image-preview" aria-label={t("composer.enlarge",{name:item.name||t("attachment.image")})} title={item.name} onClick={()=>setZoom(item)}><img src={source} alt={item.name||t("attachment.image")} loading="lazy"/></button>
 : (
   <>
    <AttachmentIcon item={item}/>
    <div className="chat-file-info">
     <span>{item.name||t("attachment.image")}</span>
     {item.size!=null ? <small>{item.mime_type} · {(item.size/1024).toFixed(1)} KB</small> : null}
    </div>
    {(item.id||item.ref)&&chatID ? <button type="button" disabled={busy===(item.ref||item.id)} onClick={()=>download(item)}>{t("composer.download")}</button> : null}
   </>
  )}
 </div>;
 })}</div>}{error&&<p role="alert">{error}</p>}
 {zoom&&<ImageDialog item={zoom} source={zoom.data_url||resolved[zoom.ref||""]} close={()=>setZoom(null)} download={()=>download(zoom)} label={t("composer.close")} downloadLabel={t("composer.download")}/>}
 </>;
}

function attachmentKind(item: Attachment): "archive"|"code"|"document"|"generic" {
 const mime=(item.mime_type??"").toLowerCase();
 const name=(item.name??"").toLowerCase();
 const extension=name.includes(".")?name.slice(name.lastIndexOf(".")+1):"";
 if (mime.includes("zip")||mime.includes("compressed")||["7z","bz2","gz","rar","tar","tgz","xz"].includes(extension)) return "archive";
 if (["c","cc","cpp","css","go","h","hpp","html","java","js","json","jsx","md","py","rb","rs","sh","sql","ts","tsx","xml","yaml","yml"].includes(extension)) return "code";
 if (mime.includes("pdf")||mime.includes("document")||mime.includes("spreadsheet")||mime.includes("presentation")||["doc","docx","ods","odt","pdf","ppt","pptx","rtf","xls","xlsx"].includes(extension)) return "document";
 return "generic";
}
function AttachmentIcon({item}:{item:Attachment}) {
 const kind=attachmentKind(item);
 return <span className={`chat-file-icon chat-file-icon-${kind}`} aria-hidden="true">
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
   {kind==="archive" ? <><path d="m6 3-2 4v13h16V7l-2-4z"/><path d="M4 7h16M8 3v4m4-4v4m4-4v4M10 11h4m-4 3h4m-4 3h4"/></>
    : kind==="code" ? <><path d="M5 3h10l4 4v14H5z"/><path d="M15 3v5h5"/><path d="m10 13-2 2 2 2m4-4 2 2-2 2"/></>
    : kind==="document" ? <><path d="M5 3h10l4 4v14H5z"/><path d="M15 3v5h5M9 13h6M9 17h6"/></>
    : <><path d="M5 3h10l4 4v14H5z"/><path d="M15 3v5h5M9 13h6M9 17h4"/></>}
  </svg>
 </span>;
}
function ImageDialog({item,source,close,download,label,downloadLabel}:{item:Attachment;source?:string;close:()=>void;download:()=>void;label:string;downloadLabel:string}){
 const ref=useRef<HTMLDialogElement>(null);useEffect(()=>{ref.current?.showModal();return()=>ref.current?.close()},[]);
 return <dialog ref={ref} className="chat-image-dialog" onCancel={close} onClick={e=>{if(e.target===e.currentTarget)close()}}><button type="button" aria-label={label} onClick={close}>×</button><img src={source} alt={item.name||""}/><p className="chat-image-caption">{item.name}{item.size!=null&&<small>{item.mime_type} · {(item.size/1024).toFixed(1)} KB</small>}</p>{(item.id||item.ref)&&<button type="button" onClick={download}>{downloadLabel}</button>}</dialog>;
}
export function GenericComponents({components=[]}: {components?:Array<{app:string;name:string;props:Record<string,unknown>}>}) {
 return <>{components.filter(c => !["approval-card","report-card","alert-card"].includes(c.name)).map((c,i) => <details key={i} className="rounded border border-border p-2"><summary>{c.app}: {c.name}</summary><pre className="whitespace-pre-wrap break-words text-xs">{JSON.stringify(c.props,null,2)}</pre></details>)}</>;
}
