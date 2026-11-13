import { TestsDashboardWidget } from "./TestsWidgets";
import type { WidgetHostProps } from "./dashboard";
export default function TestsFailuresWidget(props: WidgetHostProps) { return <TestsDashboardWidget {...props} kind="failures" />; }
