import {useEffect, useState} from "react";
import type {Message} from "./types";

export interface ReplySuggestion {key:string; text:string; messageId:number}
export const isReplySuggestion = (component:{app:string;name:string}) => component.app==="conversations"&&component.name==="reply-suggestions";

/** Only the most recent conversational message can propose the next turn.
 * Later user messages (including queued/cancelled turns) invalidate older hints. */
export function latestReplySuggestion(messages:Message[],conversationId:string):ReplySuggestion|null {
 let latest:Message|undefined;
 for(const message of messages)if(message.conversation_id===conversationId&&(message.role==="user"||message.role==="agent")&&(!latest||message.id>latest.id))latest=message;
 if(!latest||latest.role!=="agent"||latest.phase!=="final"||latest.component_kind)return null;
 const components=(latest.components??[]).filter(isReplySuggestion);
 if(components.length!==1)return null;
 const component=components[0];
 if(component.version&&component.version!==1||component.interaction)return null;
 const items=component.props.items;
 if(!Array.isArray(items)||items.length!==1)return null;
 const item=items[0];
 if(!item||typeof item.text!=="string"||!item.text.trim()||Array.from(item.text.trim()).length>500)return null;
 return {key:`${latest.id}:${component.id??"next"}:${item.id??"next"}`,text:item.text.trim(),messageId:latest.id};
}

function readConsumed(key:string):string[] {
 try {const value=JSON.parse(sessionStorage.getItem(key)??"[]");return Array.isArray(value)?value.filter(v=>typeof v==="string"):[]}catch{return []}
}
export function useReplySuggestion(messages:Message[],conversationId:string,storageKey:string) {
 const key=`${storageKey}:reply-suggestions`;
 const [state,setState]=useState(()=>({key,consumed:readConsumed(key)}));
 useEffect(()=>{setState({key,consumed:readConsumed(key)})},[key]);
 const consumed=state.key===key?state.consumed:readConsumed(key);
 const candidate=latestReplySuggestion(messages,conversationId);
 const suggestion=candidate&&!consumed.includes(candidate.key)?candidate:null;
 const dismiss=()=>{
  if(!suggestion)return;
  const next=[...consumed.filter(k=>k!==suggestion.key),suggestion.key].slice(-100);
  try{sessionStorage.setItem(key,JSON.stringify(next))}catch{}
  setState({key,consumed:next});
 };
 return {suggestion,dismiss};
}
