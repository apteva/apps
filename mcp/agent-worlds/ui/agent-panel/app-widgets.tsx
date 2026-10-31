import { Component, lazy, Suspense, type ComponentType, type LazyExoticComponent, type ReactNode } from "react";

// Other apps' agent widgets: anything an installed app declares for the
// dashboard's dashboard.agent_detail slot (Conversations, Tasks, …).
export const AGENT_SLOT = "dashboard.agent_detail";

export type AppComponentSpec = { name: string; entry: string; label?: string; description?: string; slots?: string[]; visibility?: string; settings_schema?: SettingsSchema };
export type InstalledApp = { name: string; install_id: number; project_id?: string; version?: string; display_name?: string; status?: string; ui_components?: AppComponentSpec[] };
export type SettingsProperty = { type?: string; title?: string; description?: string; default?: unknown; enum?: unknown[]; enum_names?: string[]; minimum?: number; maximum?: number };
export type SettingsSchema = { type?: string; properties?: Record<string, SettingsProperty> };
export type AppWidget = { component: string; label: string; appName: string; appLabel: string; installId: number; version: string; entry: string; attachedOnly: boolean; schema?: SettingsSchema };

// A project install shadows a global install of the same app, as on the dashboard.
export function agentWidgets(apps: InstalledApp[], projectId: string): AppWidget[] {
  const visible = new Map<string, InstalledApp>();
  for (const app of apps) {
    if (app.project_id && app.project_id !== projectId) continue;
    const current = visible.get(app.name);
    if (!current || (!current.project_id && app.project_id === projectId)) visible.set(app.name, app);
  }
  return [...visible.values()].flatMap(app => (app.ui_components || [])
    .filter(spec => spec.slots?.includes(AGENT_SLOT) && spec.entry)
    .map(spec => ({
      component: `${app.name}:${spec.name}`, label: spec.label || spec.name, appName: app.name, appLabel: app.display_name || app.name,
      installId: app.install_id, version: app.version || "", entry: spec.entry, attachedOnly: spec.visibility === "attached", schema: spec.settings_schema,
    })))
    .sort((a, b) => a.label.localeCompare(b.label));
}

export function defaultSettings(schema?: SettingsSchema) {
  const settings: Record<string, unknown> = {};
  for (const [key, property] of Object.entries(schema?.properties || {})) if (property.default !== undefined) settings[key] = property.default;
  return settings;
}

// Same module URL the dashboard builds, so the browser reuses its cache.
export function widgetModuleURL(widget: AppWidget, projectId: string) {
  const query = new URLSearchParams();
  if (widget.version) query.set("v", widget.version);
  query.set("install_id", String(widget.installId));
  if (projectId) query.set("project_id", projectId);
  return `/api/apps/${encodeURIComponent(widget.appName)}${widget.entry}?${query}`;
}

const modules = new Map<string, LazyExoticComponent<ComponentType<any>>>();
function loadWidget(url: string) {
  let loaded = modules.get(url);
  if (!loaded) {
    loaded = lazy(async () => {
      const module = await import(/* @vite-ignore */ url);
      const Widget = module.default || module.Component;
      if (!Widget) throw new Error("The widget module has no component");
      return { default: Widget };
    });
    modules.set(url, loaded);
  }
  return loaded;
}

class Boundary extends Component<{ label: string; children: ReactNode }, { error: string }> {
  state = { error: "" };
  static getDerivedStateFromError(error: Error) { return { error: error.message || "failed to load" }; }
  render() { return this.state.error ? <div className="aw-block-note" role="alert">{this.props.label} could not be shown: {this.state.error}</div> : this.props.children; }
}

// Mounts an app widget with the props the dashboard passes on its agent page.
export function AppWidgetMount({ widget, projectId, agentId, settings }: { widget: AppWidget; projectId: string; agentId: string; settings: Record<string, unknown> }) {
  const numericId = Number(agentId);
  const Widget = loadWidget(widgetModuleURL(widget, projectId));
  return <Boundary key={`${widget.component}:${agentId}`} label={widget.label}>
    <Suspense fallback={<div className="aw-block-note">Loading {widget.label}…</div>}>
      <Widget appName={widget.appName} installId={widget.installId} projectId={projectId} agentId={Number.isInteger(numericId) ? numericId : agentId} instanceId={Number.isInteger(numericId) ? numericId : 0}
        slot={AGENT_SLOT} widgetSize="full" widgetSettings={{ ...defaultSettings(widget.schema), ...settings }} />
    </Suspense>
  </Boundary>;
}
