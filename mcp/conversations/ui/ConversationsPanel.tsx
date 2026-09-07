import Panel, { type NativePanelProps } from "../frontend/src/ConversationsPanel";
import { DashboardConversations } from "./dashboard";

export default function ConversationsPanel(props: NativePanelProps) {
  if (!props.projectId) return <p>Select a project to open conversations.</p>;
  return <DashboardConversations projectId={props.projectId} installId={props.installId}><Panel {...props}/></DashboardConversations>;
}
