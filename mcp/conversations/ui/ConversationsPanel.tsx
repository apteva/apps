import { createConversationLocalization } from "../frontend/src/i18n";
import Panel, { type NativePanelProps } from "../frontend/src/ConversationsPanel";
import { DashboardConversations } from "./dashboard";

export default function ConversationsPanel(props: NativePanelProps) {
  if (!props.projectId) return <p>{createConversationLocalization(props).t("host.selectProject")}</p>;
  return <DashboardConversations projectId={props.projectId} installId={props.installId} locale={props.locale} timeZone={props.timeZone} messages={props.messages} composer={props.composer}><Panel {...props}/></DashboardConversations>;
}
