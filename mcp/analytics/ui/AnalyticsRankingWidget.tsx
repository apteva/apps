import StandaloneWidget, { WidgetHostProps } from "./StandaloneWidget";
export default function AnalyticsRankingWidget(props: WidgetHostProps) {
  return <StandaloneWidget {...props} kind="ranking"/>;
}
