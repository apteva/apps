import { useCallback, useEffect, useRef, useState } from "react";

export type ActivityFilter = "all" | "errors" | "running" | "ok";
export interface ActivityHostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}
export interface FunctionInvocationSummary {
  id: number;
  function_id: number;
  function_name?: string;
  started_at: string;
  finished_at?: string;
  duration_ms: number;
  status: string;
  trigger_kind: string;
  error?: string;
}
export interface InvocationPage {
  invocations: FunctionInvocationSummary[];
  next_cursor?: string;
}
export type ActivityAPI = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>, signal?: AbortSignal) => Promise<T>;

export function activityPreferences(raw?: Record<string, unknown>) {
  const status: ActivityFilter = raw?.status === "all" || raw?.status === "running" ? raw.status : "errors";
  const requested = Number(raw?.max_items ?? 8);
  return { status, maxItems: Number.isFinite(requested) ? Math.max(4, Math.min(20, Math.round(requested))) : 8 };
}
export function activityRoute(search: string) {
  const query = new URLSearchParams(search);
  const requested = query.get("status");
  const status: ActivityFilter = requested === "errors" || requested === "running" || requested === "ok" ? requested : "all";
  const id = Number(query.get("invocation_id"));
  return { status, invocationId: Number.isSafeInteger(id) && id > 0 ? id : undefined };
}
export function activityPageLink(props: ActivityHostProps, status: ActivityFilter, id?: number): string {
  const query = new URLSearchParams({ tab: "logs", status });
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  if (id) query.set("invocation_id", String(id));
  return `/apps/${encodeURIComponent(props.appName || "functions")}/page?${query}`;
}
export function activityURL(props: ActivityHostProps, path: string, extra: Record<string, string> = {}): string {
  const query = new URLSearchParams(extra);
  if (props.projectId) query.set("project_id", props.projectId);
  if (props.installId) query.set("install_id", String(props.installId));
  return `/api/apps/${encodeURIComponent(props.appName || "functions")}${path}?${query}`;
}
export function activityStatusLabel(status: string): string {
  if (status === "upstream_timeout") return "Upstream timeout";
  if (status === "timeout") return "Function timeout";
  if (status === "canceled") return "Canceled";
  return status === "ok" ? "Successful" : status === "running" ? "Running" : status === "error" ? "Error" : status;
}
export function activityStatusTone(status: string): string {
  if (status === "ok") return "bg-green/15 text-green";
  if (["error", "timeout", "upstream_timeout"].includes(status)) return "bg-red/15 text-red";
  return status === "running" ? "bg-blue/15 text-blue" : "bg-border text-text-muted";
}
export function activityTime(value: string): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return value;
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return new Date(timestamp).toLocaleString();
}

// Coalesce event bursts, cancel old scopes, and refresh all loaded pages so
// inspecting history does not lose older calls or retain stale running rows.
export function useInvocationFeed(api: ActivityAPI, status: ActivityFilter, functionId: string, limit: number, live: boolean, revision: number, enabled = true) {
  const [page, setPage] = useState<InvocationPage | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [paging, setPaging] = useState(false);
  const controller = useRef<AbortController | null>(null);
  const pages = useRef(1);
  const busy = useRef(false);
  const nextCursor = useRef("");
  const load = useCallback(async (more = false) => {
    if (!enabled || busy.current) return;
    busy.current = true;
    const request = new AbortController();
    controller.current = request;
    more ? setPaging(true) : setLoading(true);
    try {
      const filters: Record<string, string> = { limit: String(limit) };
      if (status !== "all") filters.status = status;
      if (functionId) filters.function_id = functionId;
      const loaded: FunctionInvocationSummary[] = [];
      let cursor = more ? nextCursor.current : "";
      let result: InvocationPage;
      for (let index = 0; index < (more ? 1 : pages.current); index++) {
        result = await api<InvocationPage>("GET", "/invocations", undefined, { ...filters, cursor }, request.signal);
        if (request.signal.aborted || controller.current !== request) return;
        loaded.push(...(result.invocations || []));
        cursor = result.next_cursor || "";
        if (!cursor) break;
      }
      nextCursor.current = cursor;
      if (more) pages.current++;
      setPage(previous => ({
        invocations: [...new Map([...(more ? previous?.invocations || [] : []), ...loaded].map(row => [row.id, row])).values()],
        next_cursor: cursor,
      }));
      setError("");
    } catch (cause) {
      if (!request.signal.aborted && controller.current === request) setError((cause as Error).message);
    } finally {
      if (controller.current === request) { busy.current = false; setLoading(false); setPaging(false); }
    }
  }, [api, status, functionId, limit, enabled]);

  useEffect(() => {
    controller.current?.abort();
    busy.current = false;
    pages.current = 1;
    nextCursor.current = "";
    setPage(null);
    setError("");
    setLoading(false);
    setPaging(false);
    void load();
    return () => { controller.current?.abort(); controller.current = null; busy.current = false; };
  }, [load]);
  useEffect(() => {
    if (!live || !enabled) return;
    const timer = window.setInterval(() => { if (document.visibilityState !== "hidden") void load(); }, 10000);
    const onVisible = () => { if (document.visibilityState !== "hidden") void load(); };
    document.addEventListener("visibilitychange", onVisible);
    return () => { window.clearInterval(timer); document.removeEventListener("visibilitychange", onVisible); };
  }, [load, live, enabled]);
  const eventRefresh = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    if (live && enabled && eventRefresh.current === null) {
      eventRefresh.current = setTimeout(() => { eventRefresh.current = null; if (document.visibilityState !== "hidden") void load(); }, 500);
    }
  }, [revision, load, live, enabled]);
  useEffect(() => () => { if (eventRefresh.current !== null) clearTimeout(eventRefresh.current); eventRefresh.current = null; }, [load, live]);
  return { page, loading, paging, error, refresh: () => load(), loadMore: () => load(true) };
}
