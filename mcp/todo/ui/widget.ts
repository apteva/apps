import { tzOffsetMinutes } from "./api";

export type WidgetProps = {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
};

export function widgetPreferences(settings?: Record<string, unknown>) {
  const target = typeof settings?.target === "string" && /^(list|group):[1-9]\d*$/.test(settings.target)
    ? settings.target : "all";
  const limit = Number(settings?.max_todos ?? 8);
  return {
    target,
    view: todoWidgetView(settings?.view),
    sort: settings?.sort === "priority" ? "priority" : "due",
    limit: Number.isFinite(limit) ? Math.max(1, Math.min(20, Math.round(limit))) : 8,
  };
}

export function widgetAPIURL(props: WidgetProps, path: string, params: Record<string, string> = {}) {
  const query = new URLSearchParams(params);
  // Scope comes from the host, never from widget settings.
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  query.set("tz_offset", String(tzOffsetMinutes()));
  return `/api/apps/${encodeURIComponent(props.appName || "todo")}${path}?${query}`;
}

export function widgetPageLink(props: WidgetProps, target: string, view?: string) {
  const query = new URLSearchParams();
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  const selection = todoPanelSelection(new URLSearchParams({
    [target.startsWith("group:") ? "group_id" : "list_id"]: target.split(":")[1] || "",
  }).toString());
  if (selection.listId) query.set("list_id", String(selection.listId));
  if (selection.groupId) query.set("group_id", String(selection.groupId));
  if (view) query.set("view", todoWidgetView(view));
  return `/apps/${encodeURIComponent(props.appName || "todo")}/page?${query}`;
}

export function todoPanelSelection(search: string) {
  const params = new URLSearchParams(search);
  const positiveID = (name: string) => {
    const value = Number(params.get(name));
    return Number.isSafeInteger(value) && value > 0 ? value : null;
  };
  const listId = positiveID("list_id");
  return { listId, groupId: listId ? null : positiveID("group_id") };
}

export async function widgetRequest<T>(url: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", ...init });
  if (!response.ok) {
    const body = (await response.text()).trim();
    throw new Error(body || `Request failed (${response.status})`);
  }
  return response.json() as Promise<T>;
}

export function todoWidgetView(value: unknown): "today" | "overdue" | "upcoming" | "all" {
  return value === "overdue" || value === "upcoming" || value === "all" ? value : "today";
}
export function todoPanelView(search: string): "today" | "overdue" | "upcoming" | "all" {
  const params = new URLSearchParams(search);
  if (params.has("view")) return todoWidgetView(params.get("view"));
  const selection = todoPanelSelection(search);
  return selection.listId || selection.groupId ? "all" : "today";
}
