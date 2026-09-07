import Widget, { type HostProps } from "../frontend/src/InboxWidget";
import { DashboardConversations } from "./dashboard";
export default function InboxWidget(props: HostProps) {
  if (!props.projectId) return <p>Select a project to open the inbox.</p>;
  return <DashboardConversations projectId={props.projectId} installId={props.installId}>
    <Widget {...props} conversationsHref="/apps/conversations/page"/>
  </DashboardConversations>;
}
