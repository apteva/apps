import { createRoot } from "react-dom/client";
import { useMemo } from "react";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../dist/index.js";
import { ConversationChat, Inbox } from "../dist/react.js";
import DashboardInbox from "../../ui/InboxWidget";
import DashboardChat from "../../ui/AgentConversationsWidget";
const params=new URLSearchParams(location.search);
// Credentials enter through a host login flow, never a URL or source file.
const config=(window as any).CONVERSATIONS_EXAMPLE as {baseURL:string;projectId:string;installId:number;agentId:number;accessToken?:string};
function Example(){
 const conversations=useMemo(()=>new AptevaClient({baseURL:config.baseURL,accessToken:config.accessToken}).use(conversationsExtension({audience:"public"}),{projectId:config.projectId,installId:config.installId}),[]);
 return <main style={{height:"100vh",padding:16}}>{params.get("surface")==="inbox" ? (params.get("host")==="dashboard" ? <DashboardInbox projectId={config.projectId} installId={config.installId}/> : <Inbox conversations={conversations}/>) : params.get("host")==="dashboard"
  ? <DashboardChat appName="conversations" projectId={config.projectId} installId={config.installId} instanceId={config.agentId} widgetSettings={{display_mode:"single"}}/>
  : <ConversationChat conversations={conversations} agentId={config.agentId}/>}</main>;
}
createRoot(document.getElementById("root")!).render(<Example/>);
