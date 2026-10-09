import { createContext, useContext, useEffect, useState, type ComponentType, type ReactNode } from "react";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";
import { ApprovalCard, ReportCard, AlertCard } from "./legacyCards";
import type { CardComponent, Message } from "./types";

export interface ConversationComponentProps {
 message:Message;
 component:CardComponent;
 submit:(action:string,values?:Record<string,unknown>)=>Promise<Message>;
}
export type ConversationComponentRenderer = ComponentType<ConversationComponentProps>;
export type ConversationComponentRenderers = Record<string,ConversationComponentRenderer>;
const Registry=createContext<ConversationComponentRenderers>({});
/** Hosts can add installed-app renderers by "app/name". Lifecycle and actions
 * remain owned by Conversations regardless of the renderer. */
export function ConversationComponentProvider({renderers,children}:{renderers:ConversationComponentRenderers;children:ReactNode}) {
 const inherited=useContext(Registry);return <Registry.Provider value={{...inherited,...renderers}}>{children}</Registry.Provider>;
}
function InteractionCard({message,component,submit}:ConversationComponentProps) {
 const {t}=useConversationLocalization();
 const {conversationsClient}=useConversationAPI();
 const draftKey=`conversations:component-draft:${conversationsClient.storageKey}:${message.conversation_id}:${message.id}:${component.id}`;
 const [draft]=useState(()=>{try{return JSON.parse(sessionStorage.getItem(draftKey)??"{}")}catch{return {}}});
 const [values,setValues]=useState<Record<string,unknown>>(draft.values??{});const [custom,setCustom]=useState<Record<string,boolean>>(draft.custom??{});const [saved,setSaved]=useState<CardComponent|null>(null);const [busy,setBusy]=useState(false);const [error,setError]=useState("");
 const current=component.interaction?.status!=="pending"?component:saved??component;
 const x=current.interaction;if(!x)return null;
 const pending=x.status==="pending" && (!x.expires_at||Date.parse(x.expires_at)>Date.now());
 useEffect(()=>{try{if(pending)sessionStorage.setItem(draftKey,JSON.stringify({values,custom}));else sessionStorage.removeItem(draftKey)}catch{}},[draftKey,values,custom,pending]);
 const act=async(action:string)=>{setBusy(true);setError("");try{const m=await submit(action,values);setSaved(m.components.find(c=>c.id===component.id)??null)}catch(e){setError(String(e))}finally{setBusy(false)}};
 return <section className="min-w-0 rounded-md border border-border bg-bg-card p-3" data-component-id={component.id}>
  <div className="text-xs font-semibold text-text-muted">{String(component.props.title||t("component.question"))}</div>
  {component.props.body!=null&&<p className="mt-2 text-sm text-text whitespace-pre-wrap break-words">{String(component.props.body)}</p>}
  {pending?<>
   <p role="status" className="mt-2 text-xs text-text-muted">{t(x.blocking?"component.waiting":"component.optional")}</p>
   <form className="mt-3 flex flex-col gap-3" onSubmit={e=>{e.preventDefault();const action=x.actions.find(a=>!a.cancel);if(action)void act(action.id)}}>
    <fieldset disabled={busy} className="flex min-w-0 flex-col gap-3">
    {(x.fields??[]).map(f=><fieldset className="flex min-w-0 flex-col gap-1 text-sm text-text" key={f.id}>
     <legend className="mb-1">{f.label}{f.required?" *":""}</legend>
     {f.type==="multichoice"?<div className="flex flex-wrap gap-2">{f.options?.map(o=><label key={o.id} className="inline-flex items-center gap-1"><input type="checkbox" checked={(values[f.id] as string[]??[]).includes(o.id)} onChange={e=>setValues(v=>({...v,[f.id]:e.target.checked?[...(v[f.id] as string[]??[]),o.id]:(v[f.id] as string[]??[]).filter(s=>s!==o.id)}))}/>{o.label}</label>)}</div>:
     f.type==="choice"&&!custom[f.id]?<select className="min-w-0 rounded border border-border bg-bg-input px-2 py-2 text-text" aria-label={f.label} value={values[f.id]?`option:${String(values[f.id])}`:""} required={f.required} onChange={e=>{if(e.target.value==="custom:"){setCustom(c=>({...c,[f.id]:true}));setValues(v=>({...v,[f.id]:""}))}else setValues(v=>({...v,[f.id]:e.target.value.slice(7)}))}}><option value="">{t("component.choose")}</option>{f.options?.map(o=><option key={o.id} value={`option:${o.id}`}>{o.label}</option>)}{f.allow_custom&&<option value="custom:">{t("component.custom")}</option>}</select>:
     f.type==="textarea"?<textarea aria-label={f.label} className="min-w-0 rounded border border-border bg-bg-input px-2 py-2 text-text" maxLength={8000} required={f.required} value={String(values[f.id]??"")} onChange={e=>setValues(v=>({...v,[f.id]:e.target.value}))}/>:
     <input aria-label={f.label} className="min-w-0 rounded border border-border bg-bg-input px-2 py-2 text-text" maxLength={8000} required={f.required} value={String(values[f.id]??"")} onChange={e=>setValues(v=>({...v,[f.id]:e.target.value}))}/>}
    </fieldset>)}
    <div className="flex flex-wrap gap-2">{x.actions.map(a=><button key={a.id} type="button" className={`rounded border px-3 py-1.5 text-xs font-semibold ${a.style==="primary"?"border-accent bg-accent text-bg":a.style==="danger"?"border-error text-error":"border-border text-text"}`} onClick={e=>{if(a.cancel||e.currentTarget.form?.reportValidity())void act(a.id)}}>{a.label}</button>)}</div>
    </fieldset>
   </form>
  </>:<>
   <p role="status" className="mt-2 text-xs text-text-muted">{t(x.status==="answered"?"component.answered":x.status==="cancelled"?"component.cancelled":"component.expired")}</p>
   {Object.entries(x.values??{}).map(([id,value])=>{const f=x.fields?.find(f=>f.id===id);const label=(s:unknown)=>f?.options?.find(o=>o.id===s)?.label??String(s);return <p key={id} className="mt-2 text-sm text-text break-words"><span className="text-text-muted">{f?.label??id}: </span>{Array.isArray(value)?value.map(label).join(", "):label(value)}</p>})}
  </>}
  {error&&<p role="alert" className="mt-2 text-xs text-error">{error}</p>}
 </section>;
}
export function MessageComponents({message,onApprovalAction,onChanged,projectId}:{message:Message;onApprovalAction?:(id:number,action:string,note:string)=>Promise<void>;onChanged?:(message:Message)=>void;projectId?:string}) {
 const renderers=useContext(Registry);const {apiPost}=useConversationAPI();const {t}=useConversationLocalization();
 if(!message.components?.length)return null;
 return <div className="flex min-w-0 flex-col gap-2">{(message.components??[]).map((component,index)=>{
  const single={...message,components:[component]};
  const submit=async(action:string,values:Record<string,unknown>={})=>{const result=await apiPost<{message:Message}>("/message-action",{message_id:message.id,component_id:component.id,action_id:action,values},projectId);onChanged?.(result.message);return result.message};
  const key=component.id??`${component.app}/${component.name}/${index}`;
  if(component.version&&component.version!==1)return <p key={key} role="status">{t("component.unavailable")}</p>;
  if(component.app==="conversations"&&component.name==="approval-card")return <ApprovalCard key={key} message={single} onAction={onApprovalAction??(async(id,action,note)=>{const result=await apiPost<{message:Message}>("/message-action",{message_id:id,action_id:action,note},projectId);onChanged?.(result.message)})}/>;
  if(component.app==="conversations"&&component.name==="report-card")return <ReportCard key={key} message={single}/>;
  if(component.app==="conversations"&&component.name==="alert-card")return <AlertCard key={key} message={single}/>;
  const Renderer=renderers[`${component.app}/${component.name}`]??(component.interaction?InteractionCard:undefined);
  if(Renderer)return <Renderer key={key} message={message} component={component} submit={submit}/>;
  return <details key={key} className="rounded border border-border p-2"><summary>{component.app}: {component.name}</summary><pre className="whitespace-pre-wrap break-words text-xs">{JSON.stringify(component.props,null,2)}</pre></details>;
 })}</div>;
}
