import {isReplySuggestion} from "./replySuggestion";
import { createContext, useContext, useEffect, useState, type ComponentType, type ReactNode } from "react";
import { useConversationAPI } from "./context";
import { useConversationLocalization } from "./i18n";
import { ApprovalCard, ReportCard, AlertCard } from "./legacyCards";
import type { CardComponent, InteractionField, Message } from "./types";

export interface ConversationComponentProps {
 message:Message;
 component:CardComponent;
 projectId?:string;
 submit:(action:string,values?:Record<string,unknown>)=>Promise<Message>;
}
export type ConversationComponentRenderer = ComponentType<ConversationComponentProps>;
export type ConversationComponentRenderers = Record<string,ConversationComponentRenderer>;
const Registry=createContext<ConversationComponentRenderers>({});
const AppRenderer=createContext<ConversationComponentRenderer|undefined>(undefined);
/** Hosts can add installed-app renderers by "app/name". Lifecycle and actions
 * remain owned by Conversations regardless of the renderer. */
export function ConversationComponentProvider({renderers={},appRenderer,children}:{renderers?:ConversationComponentRenderers;appRenderer?:ConversationComponentRenderer;children:ReactNode}) {
 const inherited=useContext(Registry);const inheritedApp=useContext(AppRenderer);
 return <Registry.Provider value={{...inherited,...renderers}}><AppRenderer.Provider value={appRenderer??inheritedApp}>{children}</AppRenderer.Provider></Registry.Provider>;
}

function QuestionChoices({field, value, custom, onChange, onAnswer}: {
  field: InteractionField;
  value: string;
  custom: boolean;
  onChange: (value: string, custom: boolean) => void;
  onAnswer?: (value: string) => void;
}) {
  const {t} = useConversationLocalization();
  const customChoice = "custom:";
  const options = [...(field.options ?? [])];
  if (field.allow_custom) options.push({id: customChoice, label: t("component.custom")});
  if (!field.required && !onAnswer) options.push({id: "", label: t("component.noAnswer")});
  const selected = custom ? customChoice : value;
  const appearance = "min-h-11 min-w-0 cursor-pointer rounded-md border px-3 py-2 text-left text-text break-words";

  return <div className="flex min-w-0 flex-col gap-2">
    {options.map(option => {
      const isCustom = option.id === customChoice;
      if (onAnswer) {
        return <button key={option.id} type="button" className={`${appearance} border-border bg-bg-input hover:bg-bg-hover focus-visible:border-accent focus-visible:outline-none`}
          onClick={() => {
            if (!isCustom) onAnswer(option.id);
            else if (!custom) onChange("", true);
          }}>
          {option.label}
        </button>;
      }
      const checked = selected === option.id;
      return <label key={option.id} className={`flex items-center ${appearance} has-[:focus-visible]:border-accent ${checked ? "border-accent bg-accent/10" : "border-border bg-bg-input hover:bg-bg-hover"}`}>
        <input type="radio" name={`question-choice-${field.id}`} value={option.id}
          className="sr-only" checked={checked} required={field.required}
          onChange={() => onChange(isCustom ? "" : option.id, isCustom)} />
        <span className="min-w-0 break-words">{option.label}</span>
      </label>;
    })}
    {custom && <input aria-label={t("component.custom")} required pattern={".*\\S.*"}
      autoFocus={Boolean(onAnswer)} enterKeyHint={onAnswer ? "send" : undefined}
      placeholder={onAnswer ? t("component.customEnter") : undefined}
      className="min-w-0 rounded border border-border bg-bg-input px-2 py-2 text-text"
      maxLength={8000} value={value} onChange={event => onChange(event.target.value, true)} />}
  </div>;
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
 const isQuestion = component.app === "conversations" && component.name === "question-card";
 const answerActions = x.actions.filter(action => !action.cancel);
 const directChoice = isQuestion && x.fields?.length === 1 && x.fields[0].type === "choice" &&
   answerActions.length === 1 ? x.fields[0] : undefined;
 const visibleActions = directChoice ? x.actions.filter(action => action.cancel) : x.actions;
 const changeChoice = (id: string, value: string, isCustom: boolean) => {
   setCustom(previous => ({...previous, [id]: isCustom}));
   setValues(previous => {
     const next = {...previous};
     if (value || isCustom) next[id] = value;
     else delete next[id];
     return next;
   });
 };
 useEffect(()=>{try{if(pending)sessionStorage.setItem(draftKey,JSON.stringify({values,custom}));else sessionStorage.removeItem(draftKey)}catch{}},[draftKey,values,custom,pending]);
 const act=async(action:string, answers=values)=>{if(busy)return;setBusy(true);setError("");try{const m=await submit(action,answers);setSaved(m.components.find(c=>c.id===component.id)??null)}catch(e){setError(String(e))}finally{setBusy(false)}};
 return <section className="min-w-0 rounded-md border border-border bg-bg-card p-3" data-component-id={component.id}>
  <div className="text-xs font-semibold text-text-muted">{String(component.props.title||t("component.question"))}</div>
  {component.props.body!=null&&<p className="mt-2 text-sm text-text whitespace-pre-wrap break-words">{String(component.props.body)}</p>}
  {pending?<>
   <p role="status" className="mt-2 text-xs text-text-muted">{t(x.blocking?"component.waiting":"component.optional")}</p>
   <form className="mt-3 flex flex-col gap-3" onSubmit={event=>{
     event.preventDefault();
     if (directChoice && !custom[directChoice.id]) return;
     if (answerActions[0]) void act(answerActions[0].id);
   }}>
    <fieldset disabled={busy} className="flex min-w-0 flex-col gap-3">
    {(x.fields??[]).map(f=><fieldset className="flex min-w-0 flex-col gap-1 text-sm text-text" key={f.id}>
     <legend className={directChoice ? "sr-only" : "mb-1"}>{f.label}{!directChoice && f.required?" *":""}</legend>
     {f.type==="multichoice"?<div className="flex flex-wrap gap-2">{f.options?.map(o=><label key={o.id} className="inline-flex items-center gap-1"><input type="checkbox" checked={(values[f.id] as string[]??[]).includes(o.id)} onChange={e=>setValues(v=>({...v,[f.id]:e.target.checked?[...(v[f.id] as string[]??[]),o.id]:(v[f.id] as string[]??[]).filter(s=>s!==o.id)}))}/>{o.label}</label>)}</div>:
     f.type==="choice"&&isQuestion?<QuestionChoices field={f} value={String(values[f.id]??"")} custom={Boolean(custom[f.id])}
       onChange={(value, isCustom) => changeChoice(f.id, value, isCustom)}
       onAnswer={directChoice ? value => void act(answerActions[0].id, {[f.id]: value}) : undefined} />:
     f.type==="choice"&&!custom[f.id]?<select className="min-w-0 rounded border border-border bg-bg-input px-2 py-2 text-text" aria-label={f.label} value={values[f.id]?`option:${String(values[f.id])}`:""} required={f.required} onChange={e=>{if(e.target.value==="custom:"){setCustom(c=>({...c,[f.id]:true}));setValues(v=>({...v,[f.id]:""}))}else setValues(v=>({...v,[f.id]:e.target.value.slice(7)}))}}><option value="">{t("component.choose")}</option>{f.options?.map(o=><option key={o.id} value={`option:${o.id}`}>{o.label}</option>)}{f.allow_custom&&<option value="custom:">{t("component.custom")}</option>}</select>:
     f.type==="textarea"?<textarea aria-label={f.label} className="min-w-0 rounded border border-border bg-bg-input px-2 py-2 text-text" maxLength={8000} required={f.required} value={String(values[f.id]??"")} onChange={e=>setValues(v=>({...v,[f.id]:e.target.value}))}/>:
     <input aria-label={f.label} className="min-w-0 rounded border border-border bg-bg-input px-2 py-2 text-text" maxLength={8000} required={f.required} value={String(values[f.id]??"")} onChange={e=>setValues(v=>({...v,[f.id]:e.target.value}))}/>}
    </fieldset>)}
    {visibleActions.length>0&&<div className="flex flex-wrap gap-2">{visibleActions.map(a=><button key={a.id} type="button" className={`rounded border px-3 py-1.5 text-xs font-semibold ${a.style==="primary"?"border-accent bg-accent text-bg":a.style==="danger"?"border-error text-error":"border-border text-text"}`} onClick={e=>{if(a.cancel||e.currentTarget.form?.reportValidity())void act(a.id)}}>{a.label}</button>)}</div>}
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
 const renderers=useContext(Registry);const InstalledRenderer=useContext(AppRenderer);const {apiPost,conversationsClient}=useConversationAPI();const {t}=useConversationLocalization();
 const components=(message.components??[]).filter(c=>!isReplySuggestion(c));
 if(!components.length)return null;
 return <div className="flex min-w-0 flex-col gap-2">{components.map((component,index)=>{
  const single={...message,components:[component]};
  const submit=async(action:string,values:Record<string,unknown>={})=>{const result=await apiPost<{message:Message}>("/message-action",{message_id:message.id,component_id:component.id,action_id:action,values},projectId);onChanged?.(result.message);return result.message};
  const key=component.id??`${component.app}/${component.name}/${index}`;
  if(component.version&&component.version!==1)return <p key={key} role="status">{t("component.unavailable")}</p>;
  if(component.app==="conversations"&&component.name==="approval-card")return <ApprovalCard key={key} message={single} onAction={onApprovalAction??(async(id,action,note)=>{const result=await apiPost<{message:Message}>("/message-action",{message_id:id,action_id:action,note},projectId);onChanged?.(result.message)})}/>;
  if(component.app==="conversations"&&component.name==="report-card")return <ReportCard key={key} message={single}/>;
  if(component.app==="conversations"&&component.name==="alert-card")return <AlertCard key={key} message={single}/>;
  const Renderer=renderers[`${component.app}/${component.name}`]??(component.interaction?InteractionCard:InstalledRenderer);
  if(Renderer)return <Renderer key={key} message={message} component={component} submit={submit} projectId={projectId??conversationsClient.projectId}/>;
  return <p key={key} role="status" className="rounded border border-border p-3 text-sm text-text-muted">{t("component.previewUnavailable")}</p>;
 })}</div>;
}
