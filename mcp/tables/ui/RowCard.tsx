// RowCard — single row from a tables-app table.
//
// The "show me this specific row" detail card. Title is the row's
// natural identifier (a `#<id>` short label); subtitle locates it
// inside the parent table. Body is a DataList of every field, with
// status / file / date cells getting type-aware rendering.

import { Card, CardHeader, DataList, StatusPill } from "@apteva/ui-kit";
import {
  CellTone, cellToneClass, parseFields, rowPanelUrl,
  rowStatusVariant, tablesVendor,
} from "./lib/tables";

/** Structured field — same as the legacy "key=value" form, plus an
 *  optional tone that highlights the cell with a subtle tint. The
 *  agent picks the tone (e.g. "warn" for a value the user should
 *  notice). */
export interface FieldEntry {
  key: string;
  value: string;
  tone?: CellTone;
}

interface Props {
  /** Numeric row id — what the agent gets back from `tables_insert`. */
  row_id: number;
  /** The parent table's name, e.g. "invoices". */
  table_name: string;
  /** Optional title for the card — defaults to "#<row_id>". */
  title?: string;
  /** Either a "key=value, key=value" string (legacy, no tones) or
   *  an array of {key, value, tone?} entries (new — supports per-
   *  field highlighting). */
  fields?: string | FieldEntry[];
  /** Which field's value drives the status pill in the header. */
  status_field?: string;
  preview?: boolean;
  projectId?: string;
}

const previewSample: {
  row_id: number; table_name: string; title: string;
  fields: FieldEntry[]; status_field: string;
} = {
  row_id: 1042,
  table_name: "invoices",
  title: "invoice #1042",
  fields: [
    { key: "customer",  value: "Acme Logistics" },
    { key: "amount",    value: "$48,000.00", tone: "warn" }, // big invoice — highlight
    { key: "status",    value: "paid" },                     // moves to header
    { key: "due",       value: "May 11" },
    { key: "contract",  value: "contract-acme-q4.pdf" },
    { key: "notes",     value: "Extended via signed renewal addendum, expedited approval." },
  ],
  status_field: "status",
};

export default function RowCard(props: Props) {
  const p = props.preview
    ? previewSample
    : {
        row_id: props.row_id,
        table_name: props.table_name,
        title: props.title || `#${props.row_id}`,
        fields: props.fields ?? "",
        status_field: props.status_field || "status",
      };

  const fields = normaliseFields(p.fields);
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
              value: renderFieldValue(f.key, f.value, f.tone),
            }))}
          />
        )}
      </div>
    </Card>
  );
}

/** normaliseFields accepts either the legacy "k=v, k=v" string OR an
 *  array of {key, value, tone?}. Returns a uniform FieldEntry[] so
 *  the rest of the component doesn't branch. */
function normaliseFields(raw: string | FieldEntry[] | undefined): FieldEntry[] {
  if (!raw) return [];
  if (typeof raw === "string") return parseFields(raw); // tone undefined for legacy
  // The agent might send a JSON-string form when calling via MCP —
  // try once to parse as JSON, fall back to comma-string parsing.
  if (Array.isArray(raw)) return raw;
  return [];
}

// renderFieldValue — light type heuristics: a value that looks like a
// filename gets a tiny attachment chip; everything else is plain text.
// Real type info lives in the parent table's schema; the agent could
// pass it explicitly later, but this covers the 80% case.
//
// When `tone` is supplied, the cell wraps in a tinted pill — that
// trumps the file/number heuristics so the agent's intent wins. The
// status-pill branch still applies (a status field with a status-y
// value remains a StatusPill regardless of tone) because that's the
// dedicated channel for that signal.
function renderFieldValue(key: string, value: string, tone?: CellTone): React.ReactNode {
  if (!value) return <span className="text-text-dim">—</span>;
  // Status-like value — render as a pill (header status takes precedence
  // over agent-supplied tone for the canonical "status" field).
  if (/^[a-z][a-z _-]{0,20}$/i.test(value) && key.toLowerCase().includes("status")) {
    return <StatusPill variant={mapToPill(rowStatusVariant(value))}>{value}</StatusPill>;
  }
  // Agent-supplied tone takes precedence over filename/number formatting:
  // if the agent flagged a cell, that's a deliberate signal we shouldn't
  // bury under generic styling.
  if (tone && tone !== "neutral") {
    return (
      <span className={`inline-block px-1.5 py-0.5 rounded text-sm tabular-nums ${cellToneClass(tone)}`}>
        {value}
      </span>
    );
  }
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
