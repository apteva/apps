import { createConversationLocalization } from "../frontend/src/i18n";
import Widget, { type AgentConversationsWidgetProps } from "../frontend/src/AgentConversationsWidget";
import { DashboardConversations } from "./dashboard";
import { PageContextProvider } from "../frontend/src/context";

export default function AgentConversationsWidget(props: AgentConversationsWidgetProps) {
  if (!props.projectId) return <p>{createConversationLocalization(props).t("host.selectProject")}</p>;
  return <PageContextProvider.Provider value={props.pageContext}><div className="h-full min-w-0" style={props.slot === "dashboard.agent_detail" ? {height: "clamp(460px, 65vh, 640px)"} : undefined}><DashboardConversations projectId={props.projectId} installId={props.installId} locale={props.locale} timeZone={props.timeZone} messages={props.messages} composer={{...props.composer,layout:props.composer?.layout ?? props.widgetSettings?.composer_layout ?? "auto"}}><Widget {...props}/></DashboardConversations></div></PageContextProvider.Provider>;
}
