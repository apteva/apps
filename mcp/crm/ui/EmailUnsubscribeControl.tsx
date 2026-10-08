import { useEffect, useState } from "react";
import { emailUnsubscribeLabel, emailUnsubscribeConfirmation, emailUnsubscribeRequest, type EmailUnsubscribeState } from "./unsubscribe";

type API = <T,>(method:string,path:string,body?:any,params?:Record<string,string>,signal?:AbortSignal)=>Promise<T>;

export function EmailUnsubscribeControl({api,contactId,conversationId,refreshKey}: {api:API;contactId:number|string;conversationId:number|string;refreshKey?:string}) {
  const path=`/contacts/${contactId}/conversations/${conversationId}/unsubscribe`;
  const [preview,setPreview]=useState<EmailUnsubscribeState|null>(null);
  const [error,setError]=useState("");
  const [confirm,setConfirm]=useState(false);
  const [busy,setBusy]=useState(false);
  useEffect(()=>{
    const controller=new AbortController();
    setPreview(null);setError("");setConfirm(false);
    api<EmailUnsubscribeState>("GET",path,undefined,undefined,controller.signal)
      .then(state=>{if(!controller.signal.aborted)setPreview(state);})
      .catch(e=>{if(!controller.signal.aborted)setError((e as Error).message);});
    return ()=>controller.abort();
  },[api,path,refreshKey]);
  const save=async()=>{
    if(!preview||busy)return;
    setBusy(true);setError("");
    try {
      const result=await api<EmailUnsubscribeState>("POST",path,emailUnsubscribeRequest(preview));
      if(!result.confirmed||!result.outbound_blocked)throw new Error("Unsubscribe was not confirmed. Refresh before retrying.");
      setPreview(result);setConfirm(false);
    } catch(e) {setError((e as Error).message);}
    finally {setBusy(false);}
  };
  const cls="text-[10px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-50";
  return <span className="inline-flex items-center gap-1 flex-wrap" data-email-unsubscribe>
    <button type="button" className={cls} disabled={busy||!preview||preview.outbound_blocked} onClick={()=>setConfirm(true)}
      title={error|| (preview ? `${preview.address}: ${preview.inbound_blocked ? "existing inbound block remains" : "incoming replies remain allowed"}` : "Checking unsubscribe eligibility…")}>
      {preview ? emailUnsubscribeLabel(preview) : "Unsubscribe this email"}
    </button>
    {confirm&&preview&&<span role="alertdialog" aria-label="Confirm email unsubscribe" className="rounded border border-border bg-bg-input p-2 text-xs max-w-md">
      <span>{emailUnsubscribeConfirmation(preview)}</span>
      <button type="button" className={cls+" ml-2"} disabled={busy} onClick={()=>void save()}>{busy?"Saving…":"Confirm unsubscribe"}</button>
      <button type="button" className={cls+" ml-1"} disabled={busy} onClick={()=>setConfirm(false)}>Cancel</button>
    </span>}
    {error&&<span role="alert" className="text-xs text-red-500 max-w-sm">Unsubscribe unavailable: {error}</span>}
  </span>;
}
