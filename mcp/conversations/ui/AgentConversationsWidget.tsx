import Widget, { type AgentConversationsWidgetProps } from "../frontend/src/AgentConversationsWidget";
import { DashboardConversations } from "./dashboard";

export default function AgentConversationsWidget(props: AgentConversationsWidgetProps) {
  if (!props.projectId) return <p>Select a project to open conversations.</p>;
  return <DashboardConversations projectId={props.projectId} installId={props.installId}><Widget {...props}/></DashboardConversations>;
}
