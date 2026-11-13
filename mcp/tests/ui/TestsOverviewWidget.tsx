import { TestsDashboardWidget } from "./TestsWidgets";
import type { WidgetHostProps } from "./dashboard";
export default function TestsOverviewWidget(props: WidgetHostProps) { return <TestsDashboardWidget {...props} kind="overview" />; }
