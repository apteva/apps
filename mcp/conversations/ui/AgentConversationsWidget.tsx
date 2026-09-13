import { createConversationLocalization } from "../frontend/src/i18n";
import Widget, { type AgentConversationsWidgetProps } from "../frontend/src/AgentConversationsWidget";
import { DashboardConversations } from "./dashboard";

export default function AgentConversationsWidget(props: AgentConversationsWidgetProps) {
  if (!props.projectId) return <p>{createConversationLocalization(props).t("host.selectProject")}</p>;
  return <DashboardConversations projectId={props.projectId} installId={props.installId} locale={props.locale} timeZone={props.timeZone} messages={props.messages} composer={{...props.composer,layout:props.composer?.layout ?? props.widgetSettings?.composer_layout ?? "auto"}}><Widget {...props}/></DashboardConversations>;
}
