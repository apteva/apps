// Shared by the dashboard widgets. The host gives scope (project,
// install) and the tile's size and settings; data comes from the same
// /call/<tool> route the panel uses.
import { useCallback, useEffect, useRef, useState } from "react";

export type WidgetProps = {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
};

export interface Work {
  id: number; number: string; brand?: string; work_type: string; phase: string; status: string; priority: string;
  customer_name: string; summary: string; sla_due_at?: string; updated_at: string; created_at: string;
  estimate_cents: number; currency: string; settlement_mode?: string; partner?: string;
}
export interface SlotResource { id: number; name: string; kind: string; role?: string }
export interface Slot {
  id: number; work_id: number; starts_at: string; ends_at: string; status: string; eta?: string; resources: SlotResource[];
  work?: { number: string; work_type: string; status: string; summary: string; customer_name: string; party_size: number; priority: string };
}

export const PHASES = ["intake", "quote", "schedule", "execute", "bill"];
export const PHASE_LABEL: Record<string, string> = { intake: "Intake", quote: "Quote", schedule: "To schedule", execute: "Scheduled", bill: "To bill" };
export const PHASE_COLOR: Record<string, string> = {
  intake: "var(--color-blue)", quote: "var(--color-yellow)", schedule: "var(--color-accent)", execute: "var(--color-green)", bill: "var(--color-yellow)",
};
export const PRIORITY_TONE: Record<string, string> = { p1: "bg-red/15 text-red", p2: "bg-yellow/15 text-yellow", p3: "bg-bg-input text-text-muted", p4: "bg-bg-input text-text-dim" };

export const human = (s: string) => (s ? (s.charAt(0).toUpperCase() + s.slice(1)).replace(/_/g, " ") : "");

export function money(cents: number | undefined | null, currency = "EUR"): string {
  if (cents == null) return "—";
  try { return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "EUR", maximumFractionDigits: Math.abs(cents) >= 100000 ? 0 : 2 }).format(cents / 100); }
  catch { return `${(cents / 100).toFixed(2)} ${currency}`; }
}
export function clock(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}
export function day(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const today = new Date();
  const tomorrow = new Date(); tomorrow.setDate(today.getDate() + 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === tomorrow.toDateString()) return "Tomorrow";
  return d.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
}
export function ago(iso?: string): string {
  if (!iso) return "";
  const m = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (m < 1) return "just now";
  if (m < 60) return `${m} min ago`;
  if (m < 60 * 24) return `${Math.round(m / 60)} h ago`;
  return `${Math.round(m / 1440)} d ago`;
}
export function minutes(m?: number): string {
  if (m == null) return "—";
  if (m < 60) return `${Math.round(m)} min`;
  if (m < 60 * 48) return `${Math.round(m / 6) / 10} h`;
  return `${Math.round(m / 144) / 10} d`;
}
export const slaLate = (w: Work) => !!w.sla_due_at && new Date(w.sla_due_at).getTime() < Date.now() && (w.phase === "intake" || w.phase === "quote" || w.phase === "schedule");

export function setting<T extends string | number>(settings: Record<string, unknown> | undefined, key: string, allowed: readonly T[], def: T): T {
  const v = settings?.[key];
  const hit = allowed.find((a) => String(a) === String(v));
  return hit ?? def;
}
export function intSetting(settings: Record<string, unknown> | undefined, key: string, def: number, min: number, max: number): number {
  const n = Number(settings?.[key] ?? def);
  return Number.isFinite(n) ? Math.max(min, Math.min(max, Math.round(n))) : def;
}
export function brandSetting(settings?: Record<string, unknown>): string {
  const b = settings?.brand;
  return typeof b === "string" ? b.trim().toLowerCase() : "";
}

export async function callTool<T>(props: WidgetProps, tool: string, args: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
  const q = new URLSearchParams();
  if (props.projectId) q.set("project_id", props.projectId);
  if (props.installId) q.set("install_id", String(props.installId));
  const res = await fetch(`/api/apps/${encodeURIComponent(props.appName || "operations")}/call/${tool}?${q}`, {
    method: "POST", credentials: "same-origin", signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify(args),
  });
  if (!res.ok) {
    const body = (await res.text().catch(() => "")).trim();
    let detail = body;
    try { const p = JSON.parse(body) as { error?: string }; if (p.error) detail = p.error; } catch { /* plain text */ }
    throw new Error(detail || `${tool} failed (${res.status})`);
  }
  return res.json() as Promise<T>;
}

// The Operations page, optionally on a tab or with a job open.
export function pageLink(props: WidgetProps, params: Record<string, string> = {}): string {
  const q = new URLSearchParams(params);
  if (props.projectId) q.set("project_id", props.projectId);
  if (props.installId) q.set("install_id", String(props.installId));
  return `/apps/${encodeURIComponent(props.appName || "operations")}/page?${q}`;
}

// Loads data for a widget: re-runs when the key changes, on the host's
// event revision, and every minute. Stale responses are dropped.
export function useWidgetData<T>(props: WidgetProps, key: string, fetcher: (signal: AbortSignal) => Promise<T>) {
  const [state, setState] = useState<{ key: string; data: T | null; error: string }>({ key: "", data: null, error: "" });
  const active = useRef<AbortController | null>(null);
  const fetchRef = useRef(fetcher);
  fetchRef.current = fetcher;
  const load = useCallback(async () => {
    active.current?.abort();
    if (!props.projectId) return;
    const c = new AbortController();
    active.current = c;
    try {
      const data = await fetchRef.current(c.signal);
      if (active.current === c) setState({ key, data, error: "" });
    } catch (e) {
      if (!c.signal.aborted && active.current === c) setState((s) => ({ key, data: s.key === key ? s.data : null, error: (e as Error).message }));
    }
  }, [key, props.projectId]);
  useEffect(() => { void load(); return () => active.current?.abort(); }, [load, props.eventRevision]);
  useEffect(() => { const t = window.setInterval(() => void load(), 60000); return () => window.clearInterval(t); }, [load]);
  return state.key === key ? state : { key, data: null, error: "" };
}

export async function projectCurrency(props: WidgetProps, brand: string, signal?: AbortSignal): Promise<string> {
  try {
    const cfg = await callTool<{ policies?: Record<string, unknown>; brand_policies?: Record<string, Record<string, unknown>> }>(
      props, "config_get", { section: "policies", ...(brand ? { brand } : {}) }, signal);
    const cur = (brand && cfg.brand_policies?.[brand]?.currency) || cfg.policies?.currency;
    return typeof cur === "string" && cur ? cur : "EUR";
  } catch { return "EUR"; }
}
