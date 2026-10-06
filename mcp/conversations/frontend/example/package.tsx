import * as React from "react";
import { createRoot } from "react-dom/client";
import { AptevaClient, type LoadedAppFrontend } from "@apteva/web-sdk";
import type { ConversationsClient } from "../dist/index.js";
import { ConversationChat, Inbox } from "../dist/react.js";
// Pin the UI; load only the scoped client from the app.
const config=(window as any).CONVERSATIONS_EXAMPLE as {baseURL:string;projectId:string;installId:number;agentId:number;accessToken?:string};
const params = new URLSearchParams(location.search);
function Example(){
 const [locale, setLocale] = React.useState(params.get("locale") || "en");
 const messages = params.has("empty") ? { "chat.empty": params.get("empty")! } : undefined;
 const [loaded,setLoaded]=React.useState<LoadedAppFrontend<ConversationsClient> | null>(null);
 const [error,setError]=React.useState("");
 React.useEffect(()=>{
  const controller=new AbortController();let frontend:LoadedAppFrontend<ConversationsClient>|undefined;
  const client=new AptevaClient({baseURL:config.baseURL,accessToken:config.accessToken});
  client.apps.load<ConversationsClient>("conversations",{projectId:config.projectId,installId:config.installId,signal:controller.signal})
   .then(value=>{if(controller.signal.aborted){value.dispose();return;}frontend=value;setLoaded(value);})
   .catch(error=>{if(!controller.signal.aborted)setError(String(error));});
  return ()=>{controller.abort();frontend?.dispose();};
 },[]);
 if(error)return <p role="alert">{error}</p>;
 if(!loaded)return <p>Loading Conversations…</p>;
 const name=new URLSearchParams(location.search).get("surface")==="inbox"?"inbox-overview":"conversation-chat";
 const Component=name==="inbox-overview"?Inbox:ConversationChat;
 return <main style={{height:"100vh",boxSizing:"border-box",padding:16,display:"flex",flexDirection:"column",gap:8}}>
  <label style={{color:"white"}}>Language <select style={{color:"#111",background:"#fff"}} aria-label="Example language" value={locale} onChange={event=>setLocale(event.target.value)}><option value="en">English</option><option value="fr-FR">Français</option><option value="es-ES">Español</option></select></label>
  <div style={{flex:1,minHeight:0}}><Component composer={(window as any).COMPOSER_OPTIONS} conversations={loaded.client} agentId={config.agentId} locale={locale} timeZone="Europe/Paris" messages={messages}/></div>
 </main>;
}
createRoot(document.getElementById("root")!).render(<Example/>);
