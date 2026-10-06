import { createConversationLocalization } from "../frontend/src/i18n";
import Widget, { type AgentConversationsWidgetProps } from "../frontend/src/AgentConversationsWidget";
import { DashboardConversations } from "./dashboard";
import { PageContextProvider } from "../frontend/src/context";

export default function AgentConversationsWidget(props: AgentConversationsWidgetProps) {
  if (!props.projectId) return <p>{createConversationLocalization(props).t("host.selectProject")}</p>;
  const agentDetail = props.slot === "dashboard.agent_detail";
  const frameClassName = agentDetail
    ? "agent-conversations-widget-frame h-full min-h-0 min-w-0 overflow-hidden rounded-lg border border-border bg-bg"
    : "h-full min-h-0 min-w-0";

  return (
    <PageContextProvider.Provider value={props.pageContext}>
      <div className="h-full min-w-0" style={agentDetail ? { height: "clamp(460px, 65vh, 640px)" } : undefined}>
        <DashboardConversations
          projectId={props.projectId}
          installId={props.installId}
          locale={props.locale}
          timeZone={props.timeZone}
          messages={props.messages}
          composer={{ ...props.composer, layout: props.composer?.layout ?? props.widgetSettings?.composer_layout ?? "auto" }}
        >
          <div className={frameClassName}><Widget {...props} /></div>
        </DashboardConversations>
      </div>
    </PageContextProvider.Provider>
  );
}
