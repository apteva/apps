import { createConversationLocalization } from "../frontend/src/i18n";
import Widget, { type HostProps } from "../frontend/src/InboxWidget";
import { DashboardConversations } from "./dashboard";
export default function InboxWidget(props: HostProps) {
  if (!props.projectId) return <p>{createConversationLocalization(props).t("host.selectInboxProject")}</p>;
  return <DashboardConversations projectId={props.projectId} installId={props.installId} locale={props.locale} timeZone={props.timeZone} messages={props.messages}>
    <Widget {...props} conversationsHref="/apps/conversations/page"/>
  </DashboardConversations>;
}
