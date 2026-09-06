import React from "react";
import { createRoot } from "react-dom/client";
import Panel from "../ui/AnalyticsPanel";
import Widget from "../ui/AnalyticsDashboardWidget";
const callbacks = new Set<(ev: unknown) => void>();
(window as any).__aptevaAppEvents = {
  subscribe(_app: string, _project: string, fn: (ev: unknown) => void) {
    callbacks.add(fn);
    return () => callbacks.delete(fn);
  },
};
(window as any).emitAnalytics = (event?: any) =>
  callbacks.forEach((fn) =>
    fn(event || { seq: Date.now(), topic: "event.recorded", project_id: "p1" }),
  );
const root = createRoot(document.getElementById("root")!);
(window as any).renderAnalytics = (props: any = {}) =>
  root.render(
    props.widget ? (
      <Widget projectId={props.projectId || "p1"} {...props} />
    ) : (
      <Panel projectId={props.projectId || "p1"} />
    ),
  );
(window as any).renderAnalytics(
  Object.fromEntries(new URLSearchParams(location.search)),
);
