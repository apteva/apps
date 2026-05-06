// Shared helpers for every Tables UI component.
//
//   * tablesVendor — CardHeader brand pill (cyan, table-grid logo)
//   * column-type pills — typed schema visualization
//   * parsers for the agent's compact "name:type" / "k=v" prop strings
//
// Same pattern as integrations/src/ui/<vendor>/lib/<vendor>.tsx —
// every vendor lib has the same shape so adding the next first-party
// app is mechanical.

import type { ReactNode } from "react";
import type { CardVendor, StatusPillVariant } from "@apteva/ui-kit";

// ─── Brand mark ───────────────────────────────────────────────────
//
// Inline SVG grid — a 2×3 cell table glyph that reads at 14×14.
// currentColor so the vendor pill recolors via inline `style.color`.

export const tablesLogo: ReactNode = (
  <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round" aria-hidden>
    <rect x="3" y="4" width="18" height="16" rx="2" />
    <line x1="3" y1="10" x2="21" y2="10" />
    <line x1="3" y1="15" x2="21" y2="15" />
    <line x1="9"  y1="4"  x2="9"  y2="20" />
    <line x1="15" y1="4"  x2="15" y2="20" />
  </svg>
);

// Cyan — distinct from storage's slate, signals "data / database"
// without borrowing a third-party brand color. Dark mode uses
// cyan-400 so the pill still pops on near-black surfaces; light
// mode uses cyan-700 for crisp contrast on warm-beige paper.
export const TABLES_BRAND_COLOR = { light: "#0e7490", dark: "#22d3ee" };

export const tablesVendor: CardVendor = {
  name: "Tables",
  logo: tablesLogo,
  color: TABLES_BRAND_COLOR,
};

// ─── Column type vocabulary ───────────────────────────────────────
//
// Tables app's strict-schema column types. Mirrors the JSON the
// tables_create / tables_describe MCP tools accept and emit.

export type TablesColumnType =
  | "text" | "integer" | "number" | "float" | "bool" | "boolean"
  | "date" | "datetime" | "timestamp"
  | "select" | "enum"
  | "json" | "array"
  | "file_id";

export interface TableColumnDef {
  name: string;
  type: TablesColumnType;
  nullable?: boolean;
}

/** Parse "name:type, name:type" string into a typed array. Tolerates
 *  whitespace and missing types (defaults to "text"). */
export function parseColumns(raw?: string): TableColumnDef[] {
  if (!raw) return [];
  return raw
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean)
    .map((entry) => {
      const [name, type] = entry.split(":").map((x) => x.trim());
      return { name: name || "—", type: ((type || "text") as TablesColumnType) };
    });
}

/** Column type → tailwind classes for the schema-pill background +
 *  text color. Matches the same vocabulary Notion's DatabaseCard
 *  pills use, so a workspace mixing Notion DBs and Apteva tables
 *  reads as a coherent system. */
export function columnTone(type: TablesColumnType): string {
  switch (type) {
    case "select":
    case "enum":      return "bg-info/10 text-info";
    case "integer":
    case "number":
    case "float":     return "bg-cyan-500/10 text-cyan-700 dark:bg-cyan-500/15 dark:text-cyan-400";
    case "date":
    case "datetime":
    case "timestamp": return "bg-warn/10 text-warn";
    case "bool":
    case "boolean":   return "bg-bg-hover text-text";
    case "file_id":   return "bg-emerald-500/10 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-400";
    case "json":
    case "array":     return "bg-purple-500/10 text-purple-700 dark:bg-purple-500/15 dark:text-purple-400";
    case "text":
    default:          return "bg-bg-hover text-text-muted";
  }
}

// ─── Row-field parsing ────────────────────────────────────────────
//
// Agent-friendly compact format: "key=value, key=value". Used by
// RowCard + as the trailing summary in TableRowList.

export interface RowField { key: string; value: string }

export function parseFields(raw?: string): RowField[] {
  if (!raw) return [];
  return raw
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean)
    .map((entry) => {
      const eq = entry.indexOf("=");
      if (eq === -1) return { key: entry, value: "" };
      return { key: entry.slice(0, eq).trim(), value: entry.slice(eq + 1).trim() };
    });
}

// ─── Status / scope mappers ───────────────────────────────────────

/** Map a row's `status` cell value to a StatusPill variant. Tables
 *  doesn't have a fixed status vocabulary (it's per-table) so we use
 *  conservative heuristics: "paid"/"done"/"closed" → success;
 *  "overdue"/"failed" → error; everything else → neutral. Caller
 *  can override by passing an explicit `status_variant` prop. */
export function rowStatusVariant(status?: string): StatusPillVariant {
  if (!status) return "neutral";
  const s = status.toLowerCase();
  if (/(paid|done|closed|completed|success|active|live)/.test(s)) return "success";
  if (/(overdue|failed|error|cancelled|blocked|expired)/.test(s)) return "error";
  if (/(pending|waiting|in[ -]?progress|queued|scheduled)/.test(s)) return "info";
  if (/(warn|review|attention|stale)/.test(s)) return "warn";
  return "neutral";
}

/** Scope pill: project (neutral) vs global (warn — global tables are
 *  shared across the org, an asymmetric signal worth flagging). */
export function scopePillVariant(scope?: string): StatusPillVariant {
  return scope === "global" ? "warn" : "neutral";
}

// ─── URL builders ─────────────────────────────────────────────────

/** Where the dashboard mounts the tables app's panel. The dashboard
 *  resolves the actual route at runtime; we just point at the app
 *  slug + the table name as a query param. */
export function tablePanelUrl(tableName?: string): string {
  const q = tableName ? `?table=${encodeURIComponent(tableName)}` : "";
  return `/apps/tables${q}`;
}

export function rowPanelUrl(tableName: string, rowId: number | string): string {
  return `/apps/tables?table=${encodeURIComponent(tableName)}&row=${encodeURIComponent(String(rowId))}`;
}

// ─── Formatters ───────────────────────────────────────────────────

export function timeAgo(iso?: string): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "";
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return "just now";
  const m = s / 60; if (m < 60) return `${Math.round(m)}m ago`;
  const h = m / 60; if (h < 24) return `${Math.round(h)}h ago`;
  const d = h / 24; if (d < 30) return `${Math.round(d)}d ago`;
  const mo = d / 30; if (mo < 12) return `${Math.round(mo)}mo ago`;
  return `${Math.round(mo / 12)}y ago`;
}

export function minusHoursISO(h: number): string {
  return new Date(Date.now() - h * 60 * 60 * 1000).toISOString();
}
