export type RequestFailureCategory="http"|"network"|"timeout"|"canceled"|"invalid_response"|"callback"|"stream_closed";
export interface RequestDiagnostic {category:RequestFailureCategory;status?:number;code?:string;requestId?:string;clientRequestId?:string}
export const diagnosticID=()=>globalThis.crypto?.randomUUID?.()??`${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;
const safe=(value:unknown,max=128)=>typeof value==="string"&&value.length<=max&&/^[a-zA-Z0-9_.:-]+$/.test(value)?value:undefined;
export function requestDiagnostic(error:unknown,signal?:AbortSignal):RequestDiagnostic{
 const e=error as {status?:number;category?:RequestFailureCategory;code?:unknown;body?:string;requestId?:string;clientRequestId?:string;name?:string;message?:string};let body:any;
 try{if(typeof e?.body==="string"&&e.body.length<=65536)body=JSON.parse(e.body)}catch{/* never retain arbitrary response text */}
 const status=typeof e?.status==="number"&&e.status>=0&&e.status<=599?e.status:undefined;
 const known:RequestFailureCategory[]=["http","network","timeout","canceled","invalid_response","callback","stream_closed"];
 const category=(known.includes(e?.category!)?e.category:undefined)??(signal?.aborted?(signal.reason?.name==="TimeoutError"?"timeout":"canceled"):e?.name==="TimeoutError"?"timeout":e?.name==="AbortError"?"canceled":e?.body==="SSE connection closed"?"stream_closed":status&&status>0?"http":e?.name==="SyntaxError"||/JSON (?:Parse|parse)|(?:Unexpected.*JSON)|(?:JSON.*unexpected)|(?:[Pp]arse JSON)/.test(e?.body??"")?"invalid_response":"network");
 return {category,status,code:(()=>{const v=body?.code??body?.error?.code??e?.code;return typeof v==="string"&&/^[a-z][a-z0-9_]{0,79}$/.test(v)?v:undefined})(),requestId:safe(body?.request_id??e?.requestId),clientRequestId:safe(e?.clientRequestId)};
}
export function correlatedInit(init?:RequestInit):RequestInit{
 const headers=new Headers(init?.headers);if(!safe(headers.get("X-Request-ID")))headers.set("X-Request-ID",`tel-${diagnosticID()}`);return {...init,headers};
}
export function correlatedFailure(error:unknown,init?:RequestInit,serverRequestId?:string):unknown{
 const diagnostic=requestDiagnostic(error,init?.signal??undefined);const id=safe(new Headers(init?.headers).get("X-Request-ID"));
 // Preserve existing Error identity/status/body for callers relying on them.
 try{return Object.assign(error instanceof Error?error:new Error("Telephony request failed"),diagnostic,{requestId:safe(serverRequestId)??diagnostic.requestId??id,clientRequestId:id});}catch{return Object.assign(new Error("Telephony request failed"),diagnostic,{requestId:id,clientRequestId:id})}
}
export interface CallWatchDiagnostics {
 browser_tab_id:string;browser_session_id:string;watch_id:string;
 state:"disabled"|"connecting"|"connected"|"reconnecting"|"closed";
 connections:number;disconnects:number;reconnects:number;
 last_connect_at?:string;last_disconnect_at?:string;last_reconnect_attempt_at?:string;last_heartbeat_at?:string;last_stream_event_at?:string;last_successful_call_list_at?:string;
 last_request_id?:string;last_failure?:RequestDiagnostic;consecutive_list_failures:number;
}
export type OfferBrowserStage="warning_attempted"|"warning_completed"|"warning_failed"|"auto_answer_canceled"|"ui_displayed"|"ui_hidden"|"ui_failed"|"ringtone_attempted"|"ringtone_started"|"ringtone_failed"|"ringtone_stopped"|"ringtone_suppressed"|"answer_attempted"|"answer_failed";
export interface OfferBrowserEvent {call_id:string;offer_id:string;stage:OfferBrowserStage;event_id?:string;occurred_at?:string;visibility?:"visible"|"hidden"|"prerender";focused?:boolean;code?:string;request_id?:string}
const tabKey=Symbol.for("apteva.telephony.browser-tab-id");
export function browserTabID():string{const root=globalThis as any;return root[tabKey]??=diagnosticID()}
