// RowCard — single row from a tables-app table.
//
// The "show me this specific row" detail card. Title is the row's
// natural identifier (a `#<id>` short label); subtitle locates it
// inside the parent table. Body is a DataList of every field, with
// status / file / date cells getting type-aware rendering.

import { Card, CardHeader, DataList, StatusPill } from "@apteva/ui-kit";
import {
  parseFields, rowPanelUrl, rowStatusVariant, tablesVendor,
} from "./lib/tables";

interface Props {
  /** Numeric row id — what the agent gets back from `tables_insert`. */
  row_id: number;
  /** The parent table's name, e.g. "invoices". */
  table_name: string;
  /** Optional title for the card — defaults to "#<row_id>". */
  title?: string;
  /** "key=value, key=value" of the row's fields. */
  fields?: string;
  /** Which field's value drives the status pill in the header. */
  status_field?: string;
  preview?: boolean;
  projectId?: string;
}

const previewSample = {
  row_id: 1042,
  table_name: "invoices",
  title: "invoice #1042",
  fields:
    "customer=Acme Logistics, " +
    "amount=$48,000.00, " +
    "status=paid, " +
    "due=May 11, " +
    "contract=contract-acme-q4.pdf, " +
    "notes=Extended via signed renewal addendum, expedited approval.",
  status_field: "status",
};

export default function RowCard(props: Props) {
  const p = props.preview
    ? previewSample
    : {
        row_id: props.row_id,
        table_name: props.table_name,
        title: props.title || `#${props.row_id}`,
        fields: props.fields || "",
        status_field: props.status_field || "status",
      };

  const fields = parseFields(p.fields);
  const statusField = fields.find((f) => f.key.toLowerCase() === p.status_field.toLowerCase());
  const status = statusField?.value;

  // Filter the status field out of the body — it's surfaced in the
  // header's status slot already, no need to repeat it inline.
  const bodyFields = fields.filter((f) => f.key.toLowerCase() !== p.status_field.toLowerCase());

  return (
    <Card>
      <CardHeader
        vendor={tablesVendor}
        title={p.title}
        subtitle={
          <span className="inline-flex items-center gap-1">
            in <span className="font-mono">{p.table_name}</span>
            <span>·</span>
            <span>row {p.row_id}</span>
          </span>
        }
        status={
          status
            ? { label: status, variant: rowStatusVariant(status) === "success" ? "live" : rowStatusVariant(status) === "error" ? "error" : rowStatusVariant(status) === "info" ? "active" : rowStatusVariant(status) === "warn" ? "warn" : "muted" }
            : undefined
        }
        action={{ label: "View row", href: rowPanelUrl(p.table_name, p.row_id) }}
      />

      <div className="px-4 py-3">
        {bodyFields.length === 0 ? (
          <div className="text-sm text-text-dim italic">No fields.</div>
        ) : (
          <DataList
            items={bodyFields.map((f) => ({
              label: f.key,
              value: renderFieldValue(f.key, f.value),
            }))}
          />
        )}
      </div>
    </Card>
  );
}

// renderFieldValue — light type heuristics: a value that looks like a
// filename gets a tiny attachment chip; everything else is plain text.
// Real type info lives in the parent table's schema; the agent could
// pass it explicitly later, but this covers the 80% case.
function renderFieldValue(key: string, value: string): React.ReactNode {
  if (!value) return <span className="text-text-dim">—</span>;
  // File chip — match a value that looks like a filename with a real extension
  if (/\.[a-z0-9]{2,5}$/i.test(value) && key.toLowerCase().match(/file|attach|contract|invoice|doc/)) {
    return (
      <span className="inline-flex items-center gap-1.5 text-sm">
        <FileChip />
        <span className="font-mono text-xs text-text">{value}</span>
      </span>
    );
  }
  // Currency / number — let the agent format it; just monospace it
  if (/^\$|^[+-]?[\d,.]+$/.test(value)) {
    return <span className="font-mono tabular-nums text-sm">{value}</span>;
  }
  // Status-like value — render as a pill
  if (/^[a-z][a-z _-]{0,20}$/i.test(value) && key.toLowerCase().includes("status")) {
    return <StatusPill variant={mapToPill(rowStatusVariant(value))}>{value}</StatusPill>;
  }
  return <span className="text-sm">{value}</span>;
}

function mapToPill(v: string): "success" | "error" | "info" | "warn" | "neutral" {
  if (v === "success" || v === "error" || v === "info" || v === "warn") return v;
  return "neutral";
}

function FileChip() {
  return (
    <span className="inline-flex items-center justify-center w-4 h-4 rounded-sm bg-emerald-500/15 text-emerald-700 dark:text-emerald-400">
      <svg viewBox="0 0 24 24" width="10" height="10" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden>
        <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5z" />
        <path d="M14 3v5h5" />
      </svg>
    </span>
  );
}
