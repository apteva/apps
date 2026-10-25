import { useRef, useState } from "react";
import { useConversationLocalization } from "./i18n";
import type { Message } from "./types";

interface QueueActions {
  canSteer: boolean;
  onEdit: (message: Message, content: string) => Promise<void>;
  onRemove: (message: Message) => Promise<void>;
  onSteer: (message: Message) => Promise<void>;
}

function Icon({path}: {path:string}) {
  return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={path}/></svg>;
}
const queueIcon="M5 4v9a4 4 0 0 0 4 4h10 M15 13l4 4-4 4 M10 5h9 M10 9h6";
const steerIcon="M4 5v6a3 3 0 0 0 3 3h13 M16 10l4 4-4 4";
const editIcon="m16 3 5 5-12 12-6 1 1-6L16 3Z M13 6l5 5";
const removeIcon="M3 6h18 M9 6V3h6v3 M6 6l1 15h10l1-15 M10 10v7 M14 10v7";

function QueuedMessage({message,canSteer,onEdit,onRemove,onSteer}:QueueActions & {message:Message}) {
  const { t }=useConversationLocalization();
  const [editing,setEditing]=useState(false);
  const [content,setContent]=useState(message.content);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const locked=useRef(false);
  const preview=useRef<HTMLButtonElement>(null);
  const run=async(action:()=>Promise<void>)=>{
    if(locked.current)return;
    locked.current=true;setBusy(true);setError("");
    try {await action();setEditing(false);return true;}
    catch(err) {setError(err instanceof Error?err.message:String(err));return false;}
    finally {locked.current=false;setBusy(false);}
  };
  const edit=()=>{setContent(message.content);setError("");setEditing(true);};
  const cancel=()=>{setEditing(false);setError("");requestAnimationFrame(()=>preview.current?.focus());};
  const save=()=>{if(content.trim())void run(()=>onEdit(message,content));};
  const filenames=message.attachments?.map(a=>a.name||a.mime_type||t("queue.attachment")).join(", ");
  return <li className="chat-queue-item" aria-busy={busy||undefined}>
    <div className="chat-queue-row">
      <span className="chat-queue-icon"><Icon path={queueIcon}/></span>
      {editing ? <div className="chat-queue-editor">
        <textarea autoFocus rows={2} value={content} aria-label={t("queue.edit")} disabled={busy}
          onChange={event=>setContent(event.target.value)}
          onKeyDown={event=>{
            if(event.nativeEvent.isComposing)return;
            if(event.key==="Escape"&&!busy){event.preventDefault();event.stopPropagation();cancel();}
            if(event.key==="Enter"&&!event.shiftKey){event.preventDefault();event.stopPropagation();save();}
          }}/>
        {filenames&&<span className="chat-queue-files" title={filenames}>{filenames}</span>}
        <div className="chat-queue-editor-actions">
          <button type="button" disabled={busy} onClick={cancel}>{t("common.cancel")}</button>
          <button type="button" disabled={busy||!content.trim()} onClick={save}>{t("common.save")}</button>
        </div>
      </div> : <>
        <button ref={preview} type="button" className="chat-queue-preview" onClick={edit} disabled={busy} title={message.content||filenames} aria-label={t("queue.editText",{text:message.content||filenames||t("queue.attachment")})}>
          <span className="chat-queue-text">{message.content||filenames||t("queue.attachment")}</span>
          {message.content&&filenames&&<span className="chat-queue-files">{filenames}</span>}
        </button>
        <div className="chat-queue-actions">
          <button type="button" className="chat-queue-control chat-queue-steer" disabled={busy||!canSteer} onClick={()=>void run(()=>onSteer(message))} aria-label={t("queue.steer")} title={t("queue.steerHint")}>
            <Icon path={steerIcon}/><span>{t("queue.steerLabel")}</span>
          </button>
          <button type="button" className="chat-queue-control chat-queue-remove" disabled={busy} onClick={()=>void run(()=>onRemove(message))} aria-label={t("queue.remove")} title={t("queue.remove")}><Icon path={removeIcon}/></button>
          <button type="button" className="chat-queue-control" disabled={busy} onClick={edit} aria-label={t("queue.edit")} title={t("queue.edit")}><Icon path={editIcon}/></button>
        </div>
      </>}
    </div>
    {error&&<p className="chat-queue-error" role="alert">{error}</p>}
  </li>;
}

/** Server-owned queued turns live beside the composer until released. Their
 * inline editor is separate from the new-message draft and its attachments. */
export function QueuedMessages({messages,...actions}:QueueActions & {messages:Message[]}) {
  const { t }=useConversationLocalization();
  if(!messages.length)return null;
  return <section className="chat-queue" aria-label={t("queue.title")}>
    <ol className="chat-queue-list">{messages.map(message=><QueuedMessage key={message.id} message={message} {...actions}/>)}</ol>
  </section>;
}
