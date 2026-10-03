import StandaloneWidget, { WidgetHostProps } from "./StandaloneWidget";
export default function AnalyticsTrendWidget(props: WidgetHostProps) {
  return <StandaloneWidget {...props} kind="trend"/>;
}
