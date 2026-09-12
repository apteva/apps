import { createRoot } from "react-dom/client";
import { useMemo, useState } from "react";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../dist/index.js";
import { ConversationChat, Inbox } from "../dist/react.js";
import DashboardInbox from "../../ui/InboxWidget";
import DashboardChat from "../../ui/AgentConversationsWidget";
const params=new URLSearchParams(location.search);
// Credentials enter through a host login flow, never a URL or source file.
const config=(window as any).CONVERSATIONS_EXAMPLE as {baseURL:string;projectId:string;installId:number;agentId:number;accessToken?:string};
function Example(){
 const [locale,setLocale]=useState(params.get("locale") || "en");
 const localization={composer:(window as any).COMPOSER_OPTIONS,locale,timeZone:"Europe/Paris",messages:params.has("empty")?{"chat.empty":params.get("empty")!}:undefined};
 const conversations=useMemo(()=>new AptevaClient({baseURL:config.baseURL,accessToken:config.accessToken}).use(conversationsExtension({audience:"public"}),{projectId:config.projectId,installId:config.installId}),[]);
 return <main style={{height:"100vh",boxSizing:"border-box",padding:16,display:"flex",flexDirection:"column",gap:8}}><label style={{color:"white"}}>Language <select style={{color:"#111",background:"#fff"}} aria-label="Example language" value={locale} onChange={event=>setLocale(event.target.value)}><option value="en">English</option><option value="fr-FR">Français</option><option value="es-ES">Español</option></select></label><div style={{flex:1,minHeight:0}}>{params.get("surface")==="inbox" ? (params.get("host")==="dashboard" ? <DashboardInbox {...localization} projectId={config.projectId} installId={config.installId}/> : <Inbox {...localization} conversations={conversations}/>) : params.get("host")==="dashboard"
  ? <DashboardChat {...localization} appName="conversations" projectId={config.projectId} installId={config.installId} instanceId={config.agentId} widgetSettings={{display_mode:"single"}}/>
  : <ConversationChat {...localization} conversations={conversations} agentId={config.agentId}/>}</div></main>;
}
createRoot(document.getElementById("root")!).render(<Example/>);
