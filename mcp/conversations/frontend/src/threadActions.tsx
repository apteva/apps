import {useEffect, useLayoutEffect, useRef, useState, type ReactNode} from "react";
import {useConversationAPI} from "./context";
import {useConversationLocalization} from "./i18n";
import type {Conversation} from "./types";
import type {ConversationsClient} from "./client";

export interface ThreadAction {
  id: string;
  label: string;
  onSelect: (conversation: Conversation, client: ConversationsClient) => void | Promise<void>;
}
export interface ThreadActionOptions { enabled?: boolean; actions?: readonly ThreadAction[] }
interface MenuItem {id:string; label:string; icon:string; danger?:boolean; separated?:boolean; run:()=>void|Promise<void>}

/** A menu stays in the host's theme scope. Native popovers put it in the top
 * layer, avoiding clipping and transformed embedded-panel ancestors. */
function ContextMenu({point,items,onClose}:{point:{x:number;y:number};items:MenuItem[];onClose:(restore?:boolean)=>void}) {
  const ref=useRef<HTMLDivElement>(null);
  const [error,setError]=useState("");
  const [busy,setBusy]=useState(false);
  useLayoutEffect(()=>{
    const menu=ref.current!;
    menu.showPopover?.();
    const rect=menu.getBoundingClientRect();
    menu.style.left=`${Math.max(8,Math.min(point.x,innerWidth-rect.width-8))}px`;
    menu.style.top=`${Math.max(8,Math.min(point.y,innerHeight-rect.height-8))}px`;
    menu.querySelector<HTMLButtonElement>("button")?.focus();
  },[point]);
  useEffect(()=>{
    const outside=(e:Event)=>{if(!ref.current?.contains(e.target as Node))onClose(false)};
    const resize=()=>onClose();
    document.addEventListener("pointerdown",outside,true);
    document.addEventListener("scroll",outside,true);
    window.addEventListener("resize",resize);
    return ()=>{document.removeEventListener("pointerdown",outside,true);document.removeEventListener("scroll",outside,true);window.removeEventListener("resize",resize)};
  },[onClose]);
  const choose=async(item:MenuItem)=>{
    if(busy)return;
    setBusy(true);setError("");
    try{await item.run();onClose(false)}catch(e){setError(e instanceof Error?e.message:String(e));setBusy(false)}
  };
  return <div ref={ref} popover="manual" role="menu" className="thread-context-menu fixed m-0 max-h-[calc(100dvh-16px)] w-60 max-w-[calc(100vw-16px)] overflow-auto rounded-lg border border-border bg-bg-card p-1 shadow-xl" style={{inset:"auto",left:point.x,top:point.y}} onContextMenu={e=>e.preventDefault()} onKeyDown={e=>{
    const buttons=[...ref.current!.querySelectorAll<HTMLButtonElement>('button[role="menuitem"]')];
    const index=buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next=e.key==="ArrowDown"?(index+1)%buttons.length:e.key==="ArrowUp"?(index-1+buttons.length)%buttons.length:e.key==="Home"?0:e.key==="End"?buttons.length-1:-1;
    if(next>=0){e.preventDefault();buttons[next]?.focus()}
    if(e.key==="Escape"||e.key==="Tab"){e.preventDefault();e.stopPropagation();onClose()}
  }}>
    {items.map(item=><div key={item.id}>{item.separated&&<div role="separator" className="my-1 border-t border-border"/>}<button role="menuitem" type="button" disabled={busy} className={`flex min-h-9 w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-bg-input focus-visible:bg-bg-input focus-visible:outline-none disabled:opacity-50 ${item.danger?"text-error":"text-text"}`} onClick={()=>void choose(item)}><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={item.icon}/></svg>{item.label}</button></div>)}
    {error&&<p role="alert" className="p-2 text-xs text-error">{error}</p>}
  </div>;
}

function ActionDialog({title,onClose,children}:{title:string;onClose:()=>void;children:ReactNode}) {
  const ref=useRef<HTMLDialogElement>(null);
  useLayoutEffect(()=>{const dialog=ref.current!;if(dialog.showModal)dialog.showModal();else dialog.setAttribute("open","")},[]);
  return <dialog ref={ref} aria-label={title} className="thread-action-dialog m-auto w-[min(420px,calc(100vw-32px))] rounded-lg border border-border bg-bg-card p-4 text-text shadow-xl" onCancel={e=>{e.preventDefault();onClose()}}>
    <h2 className="mb-3 text-sm font-semibold">{title}</h2>{children}
  </dialog>;
}

export function ThreadActionRow({conversation,unread=false,latestId=0,onChanged,renderDetails,enabled=true,children}:{conversation:Conversation;unread?:boolean;latestId?:number;onChanged:(updated?:Conversation,removed?:boolean,read?:boolean)=>void;renderDetails:(close:()=>void)=>ReactNode;enabled?:boolean;children:ReactNode}) {
  const {t}=useConversationLocalization();
  const {conversationsClient:client,apiGet,apiPatch,apiDelete,apiPost,threadActions}=useConversationAPI();
  const row=useRef<HTMLDivElement>(null);
  const anchor=useRef<HTMLElement|null>(null);
  const [point,setPoint]=useState<{x:number;y:number}|null>(null);
  const [dialog,setDialog]=useState<"rename"|"delete"|"archive"|"details"|null>(null);
  const [title,setTitle]=useState(conversation.title);
  const [error,setError]=useState("");
  const [busy,setBusy]=useState(false);
  const active=enabled&&threadActions!==false&&threadActions?.enabled!==false;
  const close=(restore=true)=>{setPoint(null);if(restore)anchor.current?.focus()};
  const restoreFocus=()=>requestAnimationFrame(()=>anchor.current?.focus());
  const closeDialog=()=>{if(busy)return;setDialog(null);setError("");restoreFocus()};
  useEffect(()=>{if(!active)setPoint(null)},[active]);
  const openDialog=(kind:typeof dialog)=>{setError("");setTitle(conversation.title);setDialog(kind)};
  const run=async(operation:()=>Promise<void>)=>{if(busy)return;setBusy(true);setError("");try{await operation();setDialog(null);restoreFocus()}catch(e){setError(e instanceof Error?e.message:String(e))}finally{setBusy(false)}};
  const patch=async(input:{title?:string;archived?:boolean})=>{const updated=await apiPatch<Conversation>(`/chats?id=${encodeURIComponent(conversation.id)}`,input,conversation.project_id);onChanged(updated)};
  const archived=Boolean(conversation.archived_at);
  const items:MenuItem[]=[{id:"rename",label:t("thread.rename"),icon:"M12 20h9 M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z",run:()=>openDialog("rename")}];
  if(unread)items.push({id:"read",label:t("thread.markRead"),icon:"M20 6 9 17l-5-5",run:async()=>{
    let last=latestId;
    if(!last){const rows=await apiGet<{messages:Array<{id:number}>}>(`/messages?chat_id=${encodeURIComponent(conversation.id)}&page=1&limit=1`,conversation.project_id);last=Math.max(0,...rows.messages.map(m=>m.id))}
    await apiPost("/seen",{chat_id:conversation.id,last_seen_id:last},conversation.project_id);onChanged(undefined,false,true);anchor.current?.focus();
  }});
  items.push({id:"details",label:t("thread.details"),icon:"M12 11v6 M12 7h.01 M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0",run:()=>openDialog("details")},
    {id:"archive",label:t(archived?"thread.restore":"thread.archive"),icon:"M3 3h18v4H3Z M5 7v14h14V7 M10 11h4",run:()=>openDialog("archive")});
  if(threadActions&&threadActions.actions)for(const action of threadActions.actions)items.push({id:`host:${action.id}`,label:action.label,icon:"M8 12h8 M12 8l4 4-4 4",run:()=>action.onSelect(conversation,client)});
  items.push({id:"delete",label:t("thread.delete"),icon:"M3 6h18 M19 6v14H5V6 M9 6V3h6v3",danger:true,separated:true,run:()=>openDialog("delete")});
  if(!active)return <>{children}</>;
  return <div ref={row} className="thread-action-row relative" data-thread-actions={conversation.id} onContextMenu={e=>{
    if((e.target as HTMLElement).closest('input,textarea,a,dialog,[role="dialog"],[role="menu"]'))return;
    e.preventDefault();e.stopPropagation();anchor.current=row.current?.querySelector("button")??null;setPoint({x:e.clientX,y:e.clientY});
  }} onKeyDown={e=>{
    if(e.key!=="ContextMenu"&&!(e.shiftKey&&e.key==="F10"))return;
    if((e.target as HTMLElement).closest('[role="menu"],dialog,[role="dialog"]'))return;
    e.preventDefault();e.stopPropagation();anchor.current=e.target as HTMLElement;const r=anchor.current.getBoundingClientRect();setPoint({x:r.left,y:r.bottom});
  }}>
    {children}
    <button type="button" className="thread-actions-trigger absolute right-1 top-1 flex h-9 w-9 items-center justify-center rounded bg-bg-card text-text-muted hover:bg-bg-input focus-visible:opacity-100" aria-label={t("thread.options",{title:conversation.title})} aria-haspopup="menu" aria-expanded={Boolean(point)} onClick={e=>{anchor.current=e.currentTarget;const r=e.currentTarget.getBoundingClientRect();setPoint({x:r.left,y:r.bottom})}}><svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
    {point&&<ContextMenu point={point} items={items} onClose={close}/>}
    {dialog==="details"&&renderDetails(closeDialog)}
    {dialog&&dialog!=="details"&&<ActionDialog title={t(dialog==="rename"?"thread.rename":dialog==="delete"?"thread.delete":archived?"thread.restore":"thread.archive")} onClose={closeDialog}>
      <form onSubmit={e=>{e.preventDefault();void run(async()=>{
        if(dialog==="rename")await patch({title:title.trim()});
        else if(dialog==="archive")await patch({archived:!archived});
        else{await apiDelete(`/chats?id=${encodeURIComponent(conversation.id)}`,conversation.project_id);onChanged(undefined,true)}
      })}}>
        {dialog==="rename"?<input aria-label={t("common.title")} autoFocus required maxLength={500} value={title} onChange={e=>setTitle(e.target.value)} className="w-full rounded border border-border bg-bg-input p-2 text-sm"/>:<p className="text-sm text-text-muted">{t(dialog==="delete"?"thread.deleteConfirm":archived?"thread.restoreConfirm":"thread.archiveConfirm",{title:conversation.title})}</p>}
        {error&&<p role="alert" className="mt-2 text-xs text-error">{error}</p>}
        <div className="mt-4 flex justify-end gap-2"><button type="button" disabled={busy} onClick={closeDialog} className="rounded border border-border px-3 py-1.5 text-sm">{t("common.cancel")}</button><button type="submit" disabled={busy||(dialog==="rename"&&!title.trim())} className={`rounded border px-3 py-1.5 text-sm ${dialog==="delete"?"border-error text-error":"border-accent text-accent"}`}>{t(dialog==="rename"?"common.save":dialog==="delete"?"thread.confirmDelete":archived?"thread.restore":"thread.archive")}</button></div>
      </form>
    </ActionDialog>}
  </div>;
}
