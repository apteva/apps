import { createConversationLocalization } from "../frontend/src/i18n";
import { ProjectConversationsWidget as Widget, type ProjectConversationsWidgetProps } from "../frontend/src/AgentConversationsWidget";
import { DashboardConversations } from "./dashboard";
import { PageContextProvider } from "../frontend/src/context";

/** Home tiles stretch to their row, so give the chat the agent page's fixed height. */
export default function ProjectConversationsWidget(props: ProjectConversationsWidgetProps) {
  if (!props.projectId) return <p>{createConversationLocalization(props).t("host.selectProject")}</p>;
  const settings = props.widgetSettings;
  const widgetSettings = {
    ...settings,
    agent_avatars: settings?.agent_avatars ?? "threads",
    show_page_context: settings?.show_page_context ?? false,
  };

  return (
    <PageContextProvider.Provider value={props.pageContext}>
      <div className="h-full min-w-0" style={{ height: "clamp(460px, 65vh, 640px)" }}>
        <DashboardConversations
          projectId={props.projectId}
          installId={props.installId}
          locale={props.locale}
          timeZone={props.timeZone}
          messages={props.messages}
          composer={{ ...props.composer, layout: props.composer?.layout ?? settings?.composer_layout ?? "compact" }}
        >
          <div className="h-full min-h-0 min-w-0 overflow-hidden rounded-lg border border-border bg-bg">
            <Widget {...props} widgetSettings={widgetSettings} conversationsHref={props.conversationsHref ?? "/apps/conversations/page"} />
          </div>
        </DashboardConversations>
      </div>
    </PageContextProvider.Provider>
  );
}
