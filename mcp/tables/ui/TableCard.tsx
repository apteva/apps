// TableCard — overview of a single tables-app table.
//
// The "what is this table" affordance: schema as typed pills, row
// count, scope, last-edited byline. Mirrors Notion's DatabaseCard
// shape so an operator who's seen one knows how to read the other.
//
// Composes ui-kit primitives only (Card / CardHeader / DataList /
// StatusPill / Avatar). Tables-specific helpers (vendor pill,
// column-type tones, scope variants) live in lib/tables.tsx.

import { Avatar, Card, CardHeader, DataList, StatusPill } from "@apteva/ui-kit";
import {
  columnTone, parseColumns, scopePillVariant, tablePanelUrl,
  tablesVendor, timeAgo,
} from "./lib/tables";

interface Props {
  table_id?: number;
  /** Required — used as the canonical link's query param. */
  name?: string;
  description?: string;
  /** "project" | "global" — drives the scope pill in the title row. */
  scope?: string;
  row_count?: number;
  /** Comma-separated "name:type" — e.g. "id:integer,customer:text". */
  columns?: string;
  created_at?: string;
  created_by?: string;
  created_by_avatar?: string;
  preview?: boolean;
  projectId?: string;
}

const previewSample: Required<Omit<Props, "preview" | "projectId">> = {
  table_id: 1,
  name: "invoices",
  description: "Generated invoices for Q4 customer renewals.",
  scope: "project",
  row_count: 142,
  columns:
    "id:integer, customer:text, amount:number, status:select, " +
    "due:date, contract:file_id, notes:text, paid:bool",
  created_at: new Date(Date.now() - 14 * 24 * 60 * 60 * 1000).toISOString(),
  created_by: "marc-olivier",
  created_by_avatar: "",
};

export default function TableCard(props: Props) {
  const p = props.preview
    ? previewSample
    : {
        table_id: props.table_id ?? 0,
        name: props.name || "untitled",
        description: props.description || "",
        scope: props.scope || "project",
        row_count: props.row_count ?? 0,
        columns: props.columns || "",
        created_at: props.created_at || new Date().toISOString(),
        created_by: props.created_by || "",
        created_by_avatar: props.created_by_avatar || "",
      };

  const cols = parseColumns(p.columns);

  return (
    <Card>
      <CardHeader
        vendor={tablesVendor}
        title={<span className="font-mono">{p.name}</span>}
        subtitle={p.description || `${cols.length} column${cols.length === 1 ? "" : "s"}`}
        status={{
          label: p.scope,
          variant: scopePillVariant(p.scope) === "warn" ? "warn" : "muted",
        }}
        action={{ label: "Open table", href: tablePanelUrl(p.name) }}
      />

      <div className="px-4 py-3 flex flex-col gap-3">
        <DataList
          items={[
            {
              label: "Rows",
              value: <span className="tabular-nums">{p.row_count.toLocaleString()}</span>,
            },
            ...(cols.length > 0
              ? [{
                  label: "Schema",
                  value: (
                    <span className="inline-flex flex-wrap gap-1">
                      {cols.map((c) => (
                        <span
                          key={c.name}
                          className={`text-[11px] font-mono px-1.5 py-0.5 rounded-md ${columnTone(c.type)}`}
                        >
                          {c.name}
                          <span className="opacity-60 ml-1">{c.type}</span>
                        </span>
                      ))}
                    </span>
                  ),
                }]
              : []),
            ...(p.scope === "global"
              ? [{
                  label: "Scope",
                  value: (
                    <span className="inline-flex items-center gap-2">
                      <StatusPill variant="warn">global</StatusPill>
                      <span className="text-text-dim">visible to every project in the workspace</span>
                    </span>
                  ),
                }]
              : []),
          ]}
        />

        {/* created byline */}
        <div className="flex items-center gap-2 text-xs pt-1 border-t border-border-subtle">
          {p.created_by && (
            <Avatar src={p.created_by_avatar} name={p.created_by} size={18} />
          )}
          {p.created_by && (
            <span className="text-text font-medium">{p.created_by}</span>
          )}
          <span className="text-text-dim">created {timeAgo(p.created_at)}</span>
        </div>
      </div>
    </Card>
  );
}
