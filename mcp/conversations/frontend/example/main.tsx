import * as React from "react";
import { createRoot } from "react-dom/client";
import { AptevaClient, type LoadedAppFrontend } from "@apteva/web-sdk";
// The host supplies these values after login. No app package imports or tokens in URLs.
const config=(window as any).CONVERSATIONS_EXAMPLE as {baseURL:string;projectId:string;installId:number;agentId:number;accessToken?:string};
const params = new URLSearchParams(location.search);
function Example(){
 const [locale, setLocale] = React.useState(params.get("locale") || "en");
 const messages = params.has("empty") ? { "chat.empty": params.get("empty")! } : undefined;
 const [loaded,setLoaded]=React.useState<LoadedAppFrontend<any,React.ComponentType<any>> | null>(null);
 const [error,setError]=React.useState("");
 React.useEffect(()=>{
  const controller=new AbortController();let frontend:LoadedAppFrontend<any,React.ComponentType<any>>|undefined;
  const client=new AptevaClient({baseURL:config.baseURL,accessToken:config.accessToken});
  client.apps.load<any,React.ComponentType<any>>("conversations",{projectId:config.projectId,installId:config.installId,react:React,signal:controller.signal})
   .then(value=>{if(controller.signal.aborted){value.dispose();return;}frontend=value;setLoaded(value);})
   .catch(error=>{if(!controller.signal.aborted)setError(String(error));});
  return ()=>{controller.abort();frontend?.dispose();};
 },[]);
 if(error)return <p role="alert">{error}</p>;
 if(!loaded)return <p>Loading Conversations…</p>;
 const name=new URLSearchParams(location.search).get("surface")==="inbox"?"inbox-overview":"conversation-chat";
 const Component=loaded.components[name];
 return <main style={{height:"100vh",boxSizing:"border-box",padding:16,display:"flex",flexDirection:"column",gap:8}}>
  <label style={{color:"white"}}>Language <select style={{color:"#111",background:"#fff"}} aria-label="Example language" value={locale} onChange={event=>setLocale(event.target.value)}><option value="en">English</option><option value="fr-FR">Français</option><option value="es-ES">Español</option></select></label>
  <div style={{flex:1,minHeight:0}}><Component conversations={loaded.client} agentId={config.agentId} locale={locale} timeZone="Europe/Paris" messages={messages}/></div>
 </main>;
}
createRoot(document.getElementById("root")!).render(<Example/>);
