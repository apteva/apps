export type Settings = Record<string, unknown>;
export interface WidgetConfig extends Record<string, unknown> {
  app: string;
  topic: string;
  aggregation: string;
  value?: string;
  by?: string;
  window: string;
  interval?: string;
  limit?: number;
  order?: string;
  where: Record<string, unknown>;
}
export function settingString(settings: Settings, key: string, fallback = ""): string {
  return typeof settings[key] === "string" ? settings[key].trim() : fallback;
}
export function allowedValues(raw: unknown): string[] {
  return typeof raw === "string" ? [...new Set(raw.split(/[,\n]/).map(x => x.trim()).filter(Boolean))] : [];
}
export function widgetConfig(settings: Settings, kind: "trend" | "ranking", selections: { topic: string; filter: string; window: string }): WidgetConfig {
  let where: Record<string, unknown> = {};
  const raw = settingString(settings, "where", "{}");
  try {
    const parsed = JSON.parse(raw || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error();
    where = { ...parsed };
  } catch { throw new Error("Property filters must be a JSON object."); }
  const field = settingString(settings, "filter_field");
  if (field) {
    if (selections.filter !== "") {
      where[field] = selections.filter;
    } else {
      // An explicitly configured option list is also the scope for “All”.
      // This keeps a widget's aggregate inside the values its author chose
      // instead of silently including unrelated hosts or test data.
      const allowed = allowedValues(settings.filter_options);
      if (allowed.length) where[field] = allowed;
    }
  }
  const config: WidgetConfig = {
    app: settingString(settings, "app"), topic: selections.topic,
    aggregation: settingString(settings, "aggregation", "count"),
    window: selections.window, where,
  };
  const value = settingString(settings, "value");
  if (value) config.value = value;
  if (kind === "trend") {
    config.interval = settingString(settings, "interval", "day");
    // The settings form stores the distinct field as group_by; older widgets
    // put it in value, so that still works as a fallback.
    if (config.aggregation === "distinct") config.by = settingString(settings, "group_by") || value;
  } else {
    config.by = settingString(settings, "group_by");
    const limit = Number(settings.limit ?? 10);
    config.limit = Number.isFinite(limit) ? Math.max(1, Math.min(100, Math.floor(limit))) : 10;
    config.order = settingString(settings, "order", "desc");
  }
  return config;
}
export function rankingRows(data: Record<string, any> | null): Array<{ label: string; value: number; count: number }> {
  if (Array.isArray(data?.top)) return data.top.map((row: any) => ({ label: String(row.value ?? "(none)"), value: Number(row.count ?? 0), count: Number(row.count ?? 0) }));
  return (data?.rows ?? []).map((row: any) => ({ label: String(row.group ?? "(none)"), value: Number(row.value), count: Number(row.count) }));
}
export interface WidgetScope { global: boolean; projectId: string; selectedProject: string }
export type GlobalProject = { id: string; name: string };
// widgetQueryRequest builds the Trend/Ranking query. Inside a project it is
// unchanged; on All projects it asks for scope=global and names a project
// only when the viewer picked one from the backend's project list.
export function widgetQueryRequest(api: string, kind: "trend" | "ranking", config: WidgetConfig, scope: WidgetScope): { url: string; body: Record<string, unknown> } | null {
  const type = kind === "trend" ? "timeseries" : config.aggregation === "count" ? "top" : "table";
  const widget = { type, config };
  if (scope.global) return { url: `${api}/query-widget?scope=global`, body: scope.selectedProject ? { project_id: scope.selectedProject, widget } : { widget } };
  if (!scope.projectId) return null;
  return { url: `${api}/query-widget?project_id=${encodeURIComponent(scope.projectId)}`, body: { widget } };
}
export function globalProjects(data: Record<string, any> | null): GlobalProject[] {
  return Array.isArray(data?.projects) ? data.projects.filter((p: any) => typeof p?.id === "string").map((p: any) => ({ id: p.id, name: String(p.name || p.id) })) : [];
}
