import {createRoot} from "react-dom/client";
import DashboardChat from "../../ui/AgentConversationsWidget.mjs";
import DashboardProject from "../../ui/ProjectConversationsWidget.mjs";
import DashboardPanel from "../../ui/ConversationsPanel.mjs";
import DashboardInbox from "../../ui/InboxWidget.mjs";
const config=(window as any).APP_CARD_TEST;
const surface=new URLSearchParams(location.search).get("surface")??"widget";
const props={projectId:config.projectId,installId:config.installId,appName:"conversations",instanceId:config.agentId};
createRoot(document.getElementById("root")!).render(<main style={{height:"100vh",padding:16,boxSizing:"border-box"}}>
  {surface==="panel"?<DashboardPanel {...props}/>:surface==="project"?<DashboardProject {...props}/>:surface==="inbox"||surface==="global-inbox"?<DashboardInbox {...props} dashboardScope={surface==="global-inbox"?"global":"project"}/>:<DashboardChat {...props} widgetSettings={{display_mode:"single",show_new_conversation:false}}/>}
</main>);
