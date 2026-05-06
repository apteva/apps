// TableRowList — query results from a tables-app table.
//
// The most operationally useful Tables card. An agent fetches "all
// open invoices" or "rows where status=overdue" and hands the result
// here. Each row is a ui-kit Row primitive: title (a key field) on
// the left, status pill + summary fields on the right.
//
// Mirrors integrations/src/ui/notion/DatabaseRowList.tsx in shape so
// the chat surface reads as a single unified system regardless of
// whether the agent queried a Notion database or an Apteva table.

import { Card, CardHeader, Row, StatusPill } from "@apteva/ui-kit";
import { rowStatusVariant, tablePanelUrl, tablesVendor } from "./lib/tables";

interface RowItem {
  /** Numeric row id (what tables_query returns). */
  id: number;
  /** Human label for the row — usually the value of the table's
   *  primary text column (customer, name, etc.). */
  title: string;
  /** Status-like value, drives the trailing pill. Optional. */
  status?: string;
  /** Pre-formatted secondary trailing values, in order. Each
   *  appears right-aligned in the row. */
  summary?: string[];
}

interface Props {
  /** Parent table's name — for the header subtitle + canonical link. */
  table_name?: string;
  /** Optional view filter — "status = paid", "this sprint", etc. */
  view_label?: string;
  /** Either an array (preferred) or JSON-encoded string. */
  rows?: RowItem[] | string;
  /** Cap rendered rows; rest collapses into "+N more". Default 10. */
  max?: number;
  preview?: boolean;
  projectId?: string;
}

const previewRows: RowItem[] = [
  { id: 1042, title: "Acme Logistics",      status: "paid", summary: ["$48,000",  "May 11"] },
  { id: 1043, title: "Globex Innovations",  status: "paid", summary: ["$24,000",  "Jun 3"]  },
  { id: 1044, title: "Initech Corp",        status: "paid", summary: ["$120,000", "May 20"] },
  { id: 1045, title: "Soylent Foods",       status: "paid", summary: ["$62,000",  "Jun 17"] },
  { id: 1046, title: "Hooli",               status: "paid", summary: ["$315,000", "May 27"] },
  { id: 1047, title: "Stark Industries",    status: "overdue", summary: ["$92,000",  "Apr 28"] },
  { id: 1048, title: "Wayne Enterprises",   status: "pending", summary: ["$210,000", "Jun 30"] },
];

export default function TableRowList(props: Props) {
  const tableName = props.table_name || (props.preview ? "invoices" : "table");
  const rows = props.preview
    ? previewRows
    : (parseRows(props.rows) ?? []);

  const max = props.max ?? 10;
  const visible = rows.slice(0, max);
  const overflow = rows.length - visible.length;

  const subtitle =
    rows.length === 0
      ? props.view_label || "No rows"
      : `${rows.length} row${rows.length === 1 ? "" : "s"}` +
        (props.view_label ? ` in view: ${props.view_label}` : "");

  return (
    <Card fullWidth>
      <CardHeader
        vendor={tablesVendor}
        title={<span className="font-mono">{tableName}</span>}
        subtitle={subtitle}
        action={{ label: "Open query", href: tablePanelUrl(tableName) }}
      />

      <div className="flex flex-col">
        {visible.map((r, i) => (
          <Row
            key={r.id}
            flush={i === 0}
            href={`${tablePanelUrl(tableName)}&row=${r.id}`}
            leading={
              <span className="font-mono text-[11px] text-text-dim tabular-nums w-12 text-right">
                #{r.id}
              </span>
            }
            title={r.title}
            trailing={
              <span className="inline-flex items-center gap-3">
                {r.summary?.map((s, idx) => (
                  <span key={idx} className="text-text-dim tabular-nums whitespace-nowrap">
                    {s}
                  </span>
                ))}
                {r.status && (
                  <StatusPill variant={mapStatusVariant(r.status)}>{r.status}</StatusPill>
                )}
              </span>
            }
          />
        ))}
        {overflow > 0 && (
          <div className="px-4 py-2 text-xs text-text-dim border-t border-border">
            +{overflow} more
          </div>
        )}
      </div>
    </Card>
  );
}

function mapStatusVariant(status: string): "success" | "error" | "info" | "warn" | "neutral" {
  const v = rowStatusVariant(status);
  return v;
}

function parseRows(raw: RowItem[] | string | undefined): RowItem[] | null {
  if (!raw) return null;
  if (Array.isArray(raw)) return raw;
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}
