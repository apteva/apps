// TablesPanel — dashboard surface for the tables app. Talks to the
// tables sidecar via /api/apps/tables/* (the platform proxy injects
// the per-install bearer token). Inherits the dashboard theme via
// Tailwind tokens.
//
// Layout: left rail = list of tables (with row counts), main area =
// selected table's row grid, bottom drawer = SELECT escape hatch.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// Inlined SDK app-event subscription. Panels are runtime-bundled
// standalone .mjs files and each app is independently installable
// from its own source — sharing across app directories would break
// the install when an app is cloned alone. Same hook storage uses;
// keep them in sync if you add reconnect/backoff knobs.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

type ColumnType = "text" | "number" | "bool" | "datetime" | "json" | "file_id";

interface ColumnDef {
  name: string;
  type: ColumnType;
  nullable: boolean;
  default?: unknown;
}

interface TableMeta {
  id: number;
  name: string;
  scope: "project" | "global";
  columns: ColumnDef[];
  row_count: number;
  created_at: string;
}

interface RowsResponse {
  rows: Record<string, unknown>[];
  total: number;
}

interface QueryResponse {
  columns: string[];
  rows: Record<string, unknown>[];
  truncated: boolean;
}

const API = "/api/apps/tables";
const PAGE_SIZE = 50;

export default function TablesPanel({ projectId, installId }: NativePanelProps) {
  const [tables, setTables] = useState<TableMeta[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [rows, setRows] = useState<Record<string, unknown>[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [showCreate, setShowCreate] = useState(false);
  const [showInsert, setShowInsert] = useState(false);
  const [showQuery, setShowQuery] = useState(false);
  const [showApi, setShowApi] = useState(false);
  const [showSchema, setShowSchema] = useState(false);
  const [editingRow, setEditingRow] = useState<Record<string, unknown> | null>(null);

  const withParams = useCallback(
    (extra: Record<string, string>) =>
      new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra }).toString(),
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(
      method: string,
      path: string,
      params: Record<string, string> = {},
      body?: unknown,
    ): Promise<T> => {
      const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
      if (body !== undefined) {
        (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
        opts.body = JSON.stringify(body);
      }
      const res = await fetch(`${API}${path}?${withParams(params)}`, opts);
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json();
    },
    [withParams],
  );

  const loadTables = useCallback(async () => {
    setBusy(true);
    try {
      const resp = await api<{ tables: TableMeta[] }>("GET", "/tables");
      const list = resp.tables || [];
      setTables(list);
      if (!selected && list.length > 0) setSelected(list[0].name);
      setStatus(`${list.length} table${list.length !== 1 ? "s" : ""}`);
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }, [api, selected]);

  const loadRows = useCallback(async () => {
    if (!selected) return;
    setBusy(true);
    try {
      const resp = await api<RowsResponse>("GET", `/tables/${selected}/rows`, {
        limit: String(PAGE_SIZE),
        offset: String(page * PAGE_SIZE),
      });
      setRows(resp.rows || []);
      setTotal(resp.total || 0);
    } catch (e) {
      setStatus(`Error: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }, [api, selected, page]);

  useEffect(() => {
    loadTables();
  }, [loadTables]);

  useEffect(() => {
    setPage(0);
    setEditingRow(null);
  }, [selected]);

  useEffect(() => {
    loadRows();
  }, [loadRows]);

  // Live refresh on mutations from agents or other tabs. We always
  // re-fetch the table list on schema-shaped events (the rail's row
  // counts may have shifted on any of them); we only re-fetch rows
  // when the active table is the one that changed.
  useAppEvents<{ table?: string; name?: string }>("tables", projectId, (ev) => {
    if (ev.topic.startsWith("table.")) {
      loadTables();
      // If the table the user is looking at was just dropped, clear
      // the selection so the main pane shows the empty state instead
      // of stale rows.
      if (ev.topic === "table.dropped" && ev.data.name === selected) {
        setSelected(null);
      }
      return;
    }
    if (ev.topic.startsWith("row.")) {
      // table-list row counts shift on every row change.
      loadTables();
      if (ev.data.table === selected) {
        loadRows();
      }
    }
  });

  const selectedTable = useMemo(
    () => tables.find((t) => t.name === selected) || null,
    [tables, selected],
  );

  const onCreate = async (name: string, columns: ColumnDef[]) => {
    try {
      await api("POST", "/tables", {}, { name, columns });
      setShowCreate(false);
      await loadTables();
      setSelected(name);
    } catch (e) {
      alert(`Create failed: ${(e as Error).message}`);
    }
  };

  const onInsert = async (row: Record<string, unknown>) => {
    if (!selectedTable) return;
    try {
      await api("POST", `/tables/${selectedTable.name}/rows`, {}, { row });
      setShowInsert(false);
      await Promise.all([loadTables(), loadRows()]);
    } catch (e) {
      alert(`Insert failed: ${(e as Error).message}`);
    }
  };

  const onUpdate = async (id: number, fields: Record<string, unknown>) => {
    if (!selectedTable) return;
    try {
      await api("PATCH", `/tables/${selectedTable.name}/rows/${id}`, {}, fields);
      setEditingRow(null);
      await loadRows();
    } catch (e) {
      alert(`Update failed: ${(e as Error).message}`);
    }
  };

  const onDeleteRow = async (id: number) => {
    if (!selectedTable) return;
    if (!confirm(`Delete row ${id} from ${selectedTable.name}?`)) return;
    try {
      await api("DELETE", `/tables/${selectedTable.name}/rows/${id}`);
      setEditingRow(null);
      await Promise.all([loadTables(), loadRows()]);
    } catch (e) {
      alert(`Delete failed: ${(e as Error).message}`);
    }
  };

  const onAlter = async (op: AlterOp) => {
    if (!selectedTable) return;
    await api("PATCH", `/tables/${selectedTable.name}`, {}, op);
    // The event subscription refreshes the table list automatically;
    // also refresh rows because new columns appear in the grid.
    await Promise.all([loadTables(), loadRows()]);
  };

  const onDropTable = async () => {
    if (!selectedTable) return;
    if (!confirm(`Drop table "${selectedTable.name}" and all its rows? This cannot be undone.`)) return;
    try {
      await api("DELETE", `/tables/${selectedTable.name}`, { confirm: "true" });
      setSelected(null);
      await loadTables();
    } catch (e) {
      alert(`Drop failed: ${(e as Error).message}`);
    }
  };

  const lastPage = Math.max(0, Math.ceil(total / PAGE_SIZE) - 1);

  return (
    <div className="h-full flex">
      {/* Left rail */}
      <aside className="w-64 border-r border-border bg-bg-card flex flex-col">
        <header className="p-3 border-b border-border flex items-center justify-between">
          <h2 className="text-sm font-medium text-text">Tables</h2>
          <button
            type="button"
            onClick={() => setShowCreate(true)}
            className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            + New
          </button>
        </header>
        <ul className="flex-1 overflow-auto">
          {tables.length === 0 && !busy && (
            <li className="p-4 text-xs text-text-muted">
              No tables yet. Click "+ New" to create one.
            </li>
          )}
          {tables.map((t) => (
            <li key={t.id}>
              <button
                type="button"
                onClick={() => setSelected(t.name)}
                className={`w-full text-left px-3 py-2 text-sm border-l-2 ${
                  selected === t.name
                    ? "bg-accent/10 border-accent text-text"
                    : "border-transparent text-text-muted hover:bg-bg-input/50 hover:text-text"
                }`}
              >
                <div className="flex items-center justify-between">
                  <span className="truncate font-mono text-xs">{t.name}</span>
                  <span className="text-[10px] text-text-dim ml-2">{t.row_count}</span>
                </div>
                {t.scope === "global" && (
                  <span className="text-[10px] text-text-dim">global</span>
                )}
              </button>
            </li>
          ))}
        </ul>
      </aside>

      {/* Main area */}
      <main className="flex-1 flex flex-col min-w-0">
        {selectedTable ? (
          <>
            <header className="border-b border-border p-3 flex items-center justify-between gap-3">
              <div className="flex items-center gap-2 min-w-0">
                <span className="font-mono text-sm text-text truncate">{selectedTable.name}</span>
                <span className="text-xs text-text-dim">
                  {selectedTable.columns.length} cols · {selectedTable.row_count} rows
                </span>
              </div>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setShowInsert(true)}
                  className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                >
                  + Insert
                </button>
                <button
                  type="button"
                  onClick={() => setShowQuery((v) => !v)}
                  className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                >
                  Query
                </button>
                <button
                  type="button"
                  onClick={() => setShowSchema(true)}
                  title="Add, rename, or drop columns"
                  className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                >
                  Schema
                </button>
                <button
                  type="button"
                  onClick={() => setShowApi(true)}
                  title="Show curl examples for calling this table from outside"
                  className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                >
                  API
                </button>
                <button
                  type="button"
                  onClick={onDropTable}
                  className="text-xs px-2 py-1 text-red border border-red/40 rounded hover:bg-red/10"
                >
                  Drop
                </button>
              </div>
            </header>
            <div className="flex-1 overflow-auto">
              {rows.length === 0 ? (
                <div className="p-12 text-center text-text-muted text-sm">
                  {busy ? "Loading…" : "No rows. Click + Insert to add the first one."}
                </div>
              ) : (
                <RowsTable
                  table={selectedTable}
                  rows={rows}
                  editingRow={editingRow}
                  onEditStart={(r) => setEditingRow(r)}
                  onEditCancel={() => setEditingRow(null)}
                  onEditSave={onUpdate}
                  onDelete={onDeleteRow}
                />
              )}
            </div>
            <footer className="border-t border-border p-2 flex items-center justify-between text-xs text-text-dim">
              <span>{status}</span>
              <Pager
                page={page}
                lastPage={lastPage}
                total={total}
                onChange={setPage}
              />
            </footer>
            {showQuery && (
              <QueryDrawer
                api={api}
                onClose={() => setShowQuery(false)}
              />
            )}
            {showInsert && (
              <InsertDialog
                table={selectedTable}
                onCancel={() => setShowInsert(false)}
                onSubmit={onInsert}
              />
            )}
            {showApi && (
              <ApiHelp
                table={selectedTable}
                projectId={projectId}
                onClose={() => setShowApi(false)}
              />
            )}
            {showSchema && (
              <SchemaEditor
                table={selectedTable}
                onAlter={onAlter}
                onClose={() => setShowSchema(false)}
              />
            )}
          </>
        ) : (
          <div className="flex-1 flex items-center justify-center text-text-muted text-sm">
            Select or create a table.
          </div>
        )}
        {showCreate && (
          <CreateDialog onCancel={() => setShowCreate(false)} onSubmit={onCreate} />
        )}
      </main>
    </div>
  );
}

// ─── rows table ─────────────────────────────────────────────────────

function RowsTable({
  table,
  rows,
  editingRow,
  onEditStart,
  onEditCancel,
  onEditSave,
  onDelete,
}: {
  table: TableMeta;
  rows: Record<string, unknown>[];
  editingRow: Record<string, unknown> | null;
  onEditStart: (r: Record<string, unknown>) => void;
  onEditCancel: () => void;
  onEditSave: (id: number, fields: Record<string, unknown>) => void;
  onDelete: (id: number) => void;
}) {
  return (
    <table className="w-full text-xs font-mono">
      <thead className="bg-bg-input/50 text-text-dim text-[10px] uppercase">
        <tr>
          <th className="text-left px-2 py-1.5 w-16">id</th>
          {table.columns.map((c) => (
            <th key={c.name} className="text-left px-2 py-1.5">
              <span className="text-text">{c.name}</span>{" "}
              <span className="text-text-dim normal-case">{c.type}</span>
            </th>
          ))}
          <th className="text-left px-2 py-1.5 w-32">updated_at</th>
          <th className="w-20" />
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const id = Number(r.id);
          const editing = editingRow && Number(editingRow.id) === id;
          return editing ? (
            <RowEditor
              key={id}
              table={table}
              row={editingRow!}
              onCancel={onEditCancel}
              onSave={(fields) => onEditSave(id, fields)}
              onDelete={() => onDelete(id)}
            />
          ) : (
            <tr
              key={id}
              onClick={() => onEditStart(r)}
              className="border-t border-border cursor-pointer hover:bg-bg-input/30"
            >
              <td className="px-2 py-1.5 text-text-dim">{id}</td>
              {table.columns.map((c) => (
                <td key={c.name} className="px-2 py-1.5 text-text truncate max-w-xs">
                  {renderCell(c, r[c.name])}
                </td>
              ))}
              <td className="px-2 py-1.5 text-text-dim">
                {String(r.updated_at || "").slice(0, 16)}
              </td>
              <td className="px-2 py-1.5 text-right text-text-dim">edit</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function renderCell(c: ColumnDef, v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (c.type === "bool") return v ? "true" : "false";
  if (c.type === "json") return JSON.stringify(v);
  return String(v);
}

function RowEditor({
  table,
  row,
  onCancel,
  onSave,
  onDelete,
}: {
  table: TableMeta;
  row: Record<string, unknown>;
  onCancel: () => void;
  onSave: (fields: Record<string, unknown>) => void;
  onDelete: () => void;
}) {
  const [fields, setFields] = useState<Record<string, string>>(() => {
    const out: Record<string, string> = {};
    for (const c of table.columns) {
      const v = row[c.name];
      if (v === null || v === undefined) out[c.name] = "";
      else if (c.type === "json") out[c.name] = JSON.stringify(v);
      else out[c.name] = String(v);
    }
    return out;
  });

  const submit = () => {
    const patch: Record<string, unknown> = {};
    for (const c of table.columns) {
      const raw = fields[c.name];
      if (raw === "" && c.nullable) {
        patch[c.name] = null;
        continue;
      }
      patch[c.name] = parseInputValue(c, raw);
    }
    onSave(patch);
  };

  return (
    <tr className="border-t border-border bg-accent/5">
      <td className="px-2 py-1.5 text-text-dim align-top">{Number(row.id)}</td>
      {table.columns.map((c) => (
        <td key={c.name} className="px-2 py-1 align-top">
          <input
            value={fields[c.name]}
            onChange={(e) => setFields({ ...fields, [c.name]: e.target.value })}
            className="bg-bg-input border border-border rounded px-1.5 py-0.5 text-xs w-full font-mono"
            placeholder={c.nullable ? "null" : ""}
          />
        </td>
      ))}
      <td className="px-2 py-1 align-top text-text-dim">
        {String(row.updated_at || "").slice(0, 16)}
      </td>
      <td className="px-2 py-1 align-top">
        <div className="flex flex-col gap-1">
          <button
            type="button"
            onClick={submit}
            className="text-[10px] px-1.5 py-0.5 border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            save
          </button>
          <button
            type="button"
            onClick={onCancel}
            className="text-[10px] px-1.5 py-0.5 border border-border rounded hover:bg-bg-input text-text-muted"
          >
            cancel
          </button>
          <button
            type="button"
            onClick={onDelete}
            className="text-[10px] px-1.5 py-0.5 border border-red/40 text-red rounded hover:bg-red/10"
          >
            delete
          </button>
        </div>
      </td>
    </tr>
  );
}

// parseInputValue translates an HTML input string back to the JSON
// shape the server expects for the column's type. It's deliberately
// permissive — invalid input lands as a server-side validation error
// the user sees in the alert.
function parseInputValue(c: ColumnDef, raw: string): unknown {
  if (c.type === "text" || c.type === "datetime") return raw;
  if (c.type === "number") {
    const n = Number(raw);
    return Number.isFinite(n) ? n : raw;
  }
  if (c.type === "bool") return raw === "true";
  if (c.type === "file_id") {
    const n = Number(raw);
    return Number.isFinite(n) ? n : raw;
  }
  if (c.type === "json") {
    try {
      return JSON.parse(raw);
    } catch {
      return raw;
    }
  }
  return raw;
}

// ─── pager ──────────────────────────────────────────────────────────

function Pager({
  page,
  lastPage,
  total,
  onChange,
}: {
  page: number;
  lastPage: number;
  total: number;
  onChange: (p: number) => void;
}) {
  if (total === 0) return null;
  return (
    <div className="flex items-center gap-2">
      <button
        type="button"
        onClick={() => onChange(Math.max(0, page - 1))}
        disabled={page === 0}
        className="text-xs px-1.5 py-0.5 border border-border rounded disabled:opacity-30"
      >
        ‹
      </button>
      <span>
        page {page + 1} / {lastPage + 1} ({total} rows)
      </span>
      <button
        type="button"
        onClick={() => onChange(Math.min(lastPage, page + 1))}
        disabled={page >= lastPage}
        className="text-xs px-1.5 py-0.5 border border-border rounded disabled:opacity-30"
      >
        ›
      </button>
    </div>
  );
}

// ─── create-table dialog ────────────────────────────────────────────

function CreateDialog({
  onCancel,
  onSubmit,
}: {
  onCancel: () => void;
  onSubmit: (name: string, cols: ColumnDef[]) => void;
}) {
  const [name, setName] = useState("");
  const [cols, setCols] = useState<ColumnDef[]>([
    { name: "title", type: "text", nullable: false },
  ]);

  const update = (i: number, patch: Partial<ColumnDef>) => {
    const next = [...cols];
    next[i] = { ...next[i], ...patch };
    setCols(next);
  };

  const submit = () => {
    if (!name) return;
    const cleaned = cols.filter((c) => c.name);
    if (cleaned.length === 0) return;
    onSubmit(name, cleaned);
  };

  return (
    <div className="absolute inset-0 bg-bg/80 flex items-center justify-center z-10">
      <div className="bg-bg-card border border-border rounded p-4 w-[28rem] max-w-[90vw] flex flex-col gap-3">
        <h3 className="text-sm font-medium text-text">New table</h3>
        <label className="flex flex-col gap-1 text-xs">
          <span className="text-text-dim">Table name</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="books"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
          />
        </label>
        <div className="flex flex-col gap-1">
          <span className="text-text-dim text-xs">Columns</span>
          {cols.map((c, i) => (
            <div key={i} className="flex items-center gap-1.5 text-xs">
              <input
                value={c.name}
                onChange={(e) => update(i, { name: e.target.value })}
                placeholder="column_name"
                className="bg-bg-input border border-border rounded px-1.5 py-0.5 text-xs font-mono flex-1 min-w-0"
              />
              <select
                value={c.type}
                onChange={(e) => update(i, { type: e.target.value as ColumnType })}
                className="bg-bg-input border border-border rounded px-1.5 py-0.5 text-xs"
              >
                <option value="text">text</option>
                <option value="number">number</option>
                <option value="bool">bool</option>
                <option value="datetime">datetime</option>
                <option value="json">json</option>
                <option value="file_id">file_id</option>
              </select>
              <label className="flex items-center gap-1 text-text-dim">
                <input
                  type="checkbox"
                  checked={c.nullable}
                  onChange={(e) => update(i, { nullable: e.target.checked })}
                />
                nullable
              </label>
              <button
                type="button"
                onClick={() => setCols(cols.filter((_, j) => j !== i))}
                disabled={cols.length === 1}
                className="text-text-dim hover:text-red disabled:opacity-30 px-1"
                aria-label="Remove column"
              >
                ×
              </button>
            </div>
          ))}
          <button
            type="button"
            onClick={() => setCols([...cols, { name: "", type: "text", nullable: true }])}
            className="text-xs text-accent hover:underline self-start"
          >
            + add column
          </button>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onCancel}
            className="text-xs px-3 py-1 border border-border rounded hover:bg-bg-input"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={!name}
            className="text-xs px-3 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            Create
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── insert-row dialog ──────────────────────────────────────────────

function InsertDialog({
  table,
  onCancel,
  onSubmit,
}: {
  table: TableMeta;
  onCancel: () => void;
  onSubmit: (row: Record<string, unknown>) => void;
}) {
  const [fields, setFields] = useState<Record<string, string>>({});

  const submit = () => {
    const row: Record<string, unknown> = {};
    for (const c of table.columns) {
      const raw = fields[c.name];
      if (raw === undefined || raw === "") {
        if (!c.nullable) {
          alert(`Column "${c.name}" is required.`);
          return;
        }
        continue;
      }
      row[c.name] = parseInputValue(c, raw);
    }
    onSubmit(row);
  };

  return (
    <div className="absolute inset-0 bg-bg/80 flex items-center justify-center z-10">
      <div className="bg-bg-card border border-border rounded p-4 w-[28rem] max-w-[90vw] flex flex-col gap-3">
        <h3 className="text-sm font-medium text-text">
          Insert into <span className="font-mono">{table.name}</span>
        </h3>
        <div className="flex flex-col gap-2">
          {table.columns.map((c) => (
            <label key={c.name} className="flex flex-col gap-1 text-xs">
              <span className="text-text-dim">
                <span className="font-mono text-text">{c.name}</span>{" "}
                <span>{c.type}</span>
                {!c.nullable && <span className="text-red"> *</span>}
              </span>
              <input
                value={fields[c.name] || ""}
                onChange={(e) => setFields({ ...fields, [c.name]: e.target.value })}
                placeholder={placeholderFor(c)}
                className="bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
              />
            </label>
          ))}
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onCancel}
            className="text-xs px-3 py-1 border border-border rounded hover:bg-bg-input"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            className="text-xs px-3 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            Insert
          </button>
        </div>
      </div>
    </div>
  );
}

function placeholderFor(c: ColumnDef): string {
  switch (c.type) {
    case "text":
      return "string";
    case "number":
      return "42";
    case "bool":
      return "true / false";
    case "datetime":
      return "2026-05-05T12:00:00Z";
    case "json":
      return '{"a": 1}';
    case "file_id":
      return "file id (integer)";
  }
}

// ─── query drawer ───────────────────────────────────────────────────

function QueryDrawer({
  api,
  onClose,
}: {
  api: <T>(method: string, path: string, params?: Record<string, string>, body?: unknown) => Promise<T>;
  onClose: () => void;
}) {
  const [sql, setSql] = useState(
    "SELECT 1 AS sample\n-- Reference user-tables with {table_name} placeholders.",
  );
  const [result, setResult] = useState<QueryResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    ref.current?.focus();
  }, []);

  // The drawer makes a query whose path embeds a table name, but for
  // this generic playground we route to the first matching table or
  // require the SQL itself to use placeholders. Simplest: hit any one
  // table; the server doesn't care which, since validateReadOnlySQL +
  // placeholder substitution operate on the full query body. We use
  // the first table from /tables.
  const run = async () => {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const tables = await api<{ tables: TableMeta[] }>("GET", "/tables");
      const first = tables.tables?.[0]?.name;
      if (!first) {
        setError("No tables yet — create one before running a query.");
        setBusy(false);
        return;
      }
      const resp = await api<QueryResponse>("POST", `/tables/${first}/query`, {}, { sql });
      setResult(resp);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="border-t border-border bg-bg-card flex flex-col" style={{ height: "20rem" }}>
      <header className="flex items-center justify-between px-3 py-1.5 border-b border-border">
        <span className="text-xs text-text-dim">tables_query — read-only SELECT</span>
        <button
          type="button"
          onClick={onClose}
          className="text-text-muted hover:text-text text-sm leading-none px-1"
          aria-label="Close"
        >
          ×
        </button>
      </header>
      <div className="flex flex-1 min-h-0">
        <textarea
          ref={ref}
          value={sql}
          onChange={(e) => setSql(e.target.value)}
          className="flex-1 bg-bg-input border-r border-border p-2 text-xs font-mono text-text resize-none focus:outline-none"
        />
        <div className="flex-1 overflow-auto p-2 text-xs font-mono">
          {error && <div className="text-red">{error}</div>}
          {result && (
            <div className="flex flex-col gap-2">
              {result.truncated && (
                <div className="text-text-dim text-[10px]">
                  truncated at row cap
                </div>
              )}
              <table className="w-full">
                <thead>
                  <tr className="text-text-dim text-[10px] uppercase">
                    {result.columns.map((c) => (
                      <th key={c} className="text-left pr-3 py-0.5">
                        {c}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {result.rows.map((r, i) => (
                    <tr key={i} className="border-t border-border">
                      {result.columns.map((c) => (
                        <td key={c} className="pr-3 py-0.5 text-text truncate max-w-xs">
                          {String(r[c] ?? "")}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
      <footer className="flex justify-end gap-2 px-3 py-1.5 border-t border-border">
        <button
          type="button"
          onClick={run}
          disabled={busy}
          className="text-xs px-3 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >
          {busy ? "Running…" : "Run"}
        </button>
      </footer>
    </div>
  );
}

// ─── API help modal ────────────────────────────────────────────────
//
// "How do I call this from outside?" docs scoped to the currently-
// selected table. Gives copy-paste curl examples for every endpoint
// and explains the three auth-key carriers apteva-server accepts.
//
// Note on the URL we surface: window.location.origin is the dashboard
// host, which IS the API host (apteva-server proxies /api/apps/*
// transparently). So a key issued from this dashboard works against
// these URLs without any extra wiring.

function ApiHelp({
  table,
  projectId,
  onClose,
}: {
  table: TableMeta;
  projectId: string;
  onClose: () => void;
}) {
  const origin = typeof window !== "undefined" ? window.location.origin : "https://your-host";
  const base = `${origin}/api/apps/tables`;
  const sample = sampleRowFor(table);
  const sampleJSON = JSON.stringify(sample, null, 2);
  const wherePred = whereExampleFor(table);
  const whereJSON = JSON.stringify({ where: [wherePred] }, null, 2);

  const examples: { title: string; verb: string; description: string; curl: string }[] = [
    {
      title: "List rows",
      verb: "GET",
      description: "First 50 rows ordered by id desc.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  "${base}/tables/${table.name}/rows?limit=50"`,
    },
    {
      title: "Filtered search",
      verb: "POST",
      description: "Typed predicates: eq, neq, lt, lte, gt, gte, contains, in, between, is_null, is_not_null.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X POST "${base}/tables/${table.name}/rows/search" \\\n  -d '${whereJSON}'`,
    },
    {
      title: "Get one row",
      verb: "GET",
      description: "Pass ?hydrate_files=true to resolve file_id columns to {id, url, expires_at}.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  "${base}/tables/${table.name}/rows/<id>"`,
    },
    {
      title: "Insert a row",
      verb: "POST",
      description: "Wrap a single object as { row: {...} } or pass { rows: [...] } for atomic batch.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X POST "${base}/tables/${table.name}/rows" \\\n  -d '{"row": ${sampleJSON.replace(/\n/g, "\n  ")}}'`,
    },
    {
      title: "Update a row",
      verb: "PATCH",
      description: "Body is a partial object — only listed fields are touched. updated_at moves automatically.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X PATCH "${base}/tables/${table.name}/rows/<id>" \\\n  -d '${JSON.stringify(sample).slice(0, 80)}...'`,
    },
    {
      title: "Delete a row",
      verb: "DELETE",
      description: "Filter-form (where + confirm=true) is supported on POST /rows/search semantics — see docs.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -X DELETE "${base}/tables/${table.name}/rows/<id>"`,
    },
    {
      title: "Run a SELECT (escape hatch)",
      verb: "POST",
      description: "Read-only. Reference user-tables with {name} placeholders; bind values via params.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X POST "${base}/tables/${table.name}/query" \\\n  -d '{"sql": "SELECT COUNT(*) AS n FROM {${table.name}}"}'`,
    },
  ];

  const projectHint =
    projectId && projectId !== ""
      ? `# Sidecar is bound to project_id="${projectId}". For globally-scoped\n# installs, append &project_id=<id> to the URL or pass _project_id in the body.`
      : "# Add ?project_id=<id> to the URL for globally-scoped installs.";

  return (
    <div className="absolute inset-0 bg-bg/80 flex items-center justify-center z-10 p-6">
      <div className="bg-bg-card border border-border rounded w-[44rem] max-w-full max-h-full flex flex-col overflow-hidden">
        <header className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div>
            <h3 className="text-sm font-medium text-text">
              Connect to <span className="font-mono">{table.name}</span> from outside
            </h3>
            <p className="text-xs text-text-dim mt-0.5">
              Same REST surface the dashboard uses, reachable from any host with a valid API key.
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-text-muted hover:text-text text-lg leading-none px-1"
            aria-label="Close"
          >
            ×
          </button>
        </header>
        <div className="overflow-auto flex-1 p-4 flex flex-col gap-4 text-xs">
          <section>
            <h4 className="text-text-dim uppercase text-[10px] tracking-wide mb-2">Auth carriers</h4>
            <p className="text-text-muted mb-2">
              Three ways to attach your API key — pick whichever fits the client. Keys are issued
              under your account settings.
            </p>
            <ul className="space-y-1.5 font-mono">
              <li>
                <code className="bg-bg-input px-1.5 py-0.5 rounded">
                  Authorization: Bearer $APTEVA_API_KEY
                </code>{" "}
                <span className="text-text-dim font-sans not-italic">— canonical</span>
              </li>
              <li>
                <code className="bg-bg-input px-1.5 py-0.5 rounded">
                  X-API-Key: $APTEVA_API_KEY
                </code>{" "}
                <span className="text-text-dim font-sans">— common alt header</span>
              </li>
              <li>
                <code className="bg-bg-input px-1.5 py-0.5 rounded">?api_key=$APTEVA_API_KEY</code>{" "}
                <span className="text-text-dim font-sans">— for SSE/EventSource</span>
              </li>
            </ul>
          </section>
          <section>
            <h4 className="text-text-dim uppercase text-[10px] tracking-wide mb-2">Base URL</h4>
            <CopyBlock text={base} />
            <p className="text-[10px] text-text-dim mt-2 whitespace-pre-line font-mono">
              {projectHint}
            </p>
          </section>
          <section>
            <h4 className="text-text-dim uppercase text-[10px] tracking-wide mb-2">Endpoints</h4>
            <div className="flex flex-col gap-3">
              {examples.map((ex) => (
                <div key={ex.title} className="border border-border rounded">
                  <div className="flex items-center gap-2 px-3 py-1.5 border-b border-border bg-bg-input/30">
                    <span className="text-[10px] font-mono px-1.5 py-0.5 bg-accent/15 text-accent rounded">
                      {ex.verb}
                    </span>
                    <span className="text-text font-medium">{ex.title}</span>
                  </div>
                  <div className="p-3 flex flex-col gap-2">
                    <p className="text-text-muted">{ex.description}</p>
                    <CopyBlock text={ex.curl} />
                  </div>
                </div>
              ))}
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}

// CopyBlock renders a code block with a copy-to-clipboard button.
function CopyBlock({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard API blocked — fall through silently
    }
  };
  return (
    <div className="relative group">
      <pre className="bg-bg-input border border-border rounded p-2 pr-14 text-[11px] font-mono text-text whitespace-pre-wrap break-all overflow-auto">
        {text}
      </pre>
      <button
        type="button"
        onClick={onCopy}
        className="absolute top-1.5 right-1.5 text-[10px] px-1.5 py-0.5 border border-border rounded bg-bg-card hover:bg-bg-input text-text-dim hover:text-text"
      >
        {copied ? "copied" : "copy"}
      </button>
    </div>
  );
}

// sampleRowFor synthesises a believable example payload from the
// table's schema. The values are deterministic placeholders, not
// random — so curl examples don't churn between renders.
function sampleRowFor(table: TableMeta): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const c of table.columns) {
    if (c.nullable && c.default === undefined) continue;
    switch (c.type) {
      case "text":
        out[c.name] = "example";
        break;
      case "number":
        out[c.name] = 42;
        break;
      case "bool":
        out[c.name] = true;
        break;
      case "datetime":
        out[c.name] = "2026-05-06T12:00:00Z";
        break;
      case "json":
        out[c.name] = { example: true };
        break;
      case "file_id":
        out[c.name] = 1;
        break;
    }
  }
  // If every column was nullable, still surface one column so the
  // example isn't an empty object.
  if (Object.keys(out).length === 0 && table.columns.length > 0) {
    const c = table.columns[0];
    out[c.name] = c.type === "number" ? 0 : "example";
  }
  return out;
}

// whereExampleFor picks the first column whose type makes for a clean
// predicate demo — string with contains, number with gte, bool with
// eq, etc. — and returns a {col, op, value} triple.
function whereExampleFor(table: TableMeta): { col: string; op: string; value: unknown } {
  for (const c of table.columns) {
    if (c.type === "text") return { col: c.name, op: "contains", value: "search" };
    if (c.type === "bool") return { col: c.name, op: "eq", value: true };
    if (c.type === "number") return { col: c.name, op: "gte", value: 0 };
  }
  return { col: "id", op: "gt", value: 0 };
}

// ─── schema editor ─────────────────────────────────────────────────
//
// Three operation shapes the panel POSTs to PATCH /tables/{name}:
//
//   {add:    {name, type, nullable?, default?}}
//   {rename: {from, to}}
//   {drop:   "<column name>"}
//
// All three forward to the same toolTablesAlter handler server-side.
// Reserved columns (id / created_at / updated_at) aren't editable —
// the server enforces that, and we hide them from the editor too.

type AlterOp =
  | { add: ColumnDef }
  | { rename: { from: string; to: string } }
  | { drop: string };

function SchemaEditor({
  table,
  onAlter,
  onClose,
}: {
  table: TableMeta;
  onAlter: (op: AlterOp) => Promise<void>;
  onClose: () => void;
}) {
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameTo, setRenameTo] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const editableColumns = table.columns; // reserved cols never appear here

  const safeAlter = async (op: AlterOp, after?: () => void) => {
    setBusy(true);
    setError(null);
    try {
      await onAlter(op);
      after?.();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const startRename = (name: string) => {
    setRenaming(name);
    setRenameTo(name);
    setError(null);
  };

  const cancelRename = () => {
    setRenaming(null);
    setRenameTo("");
  };

  const submitRename = async () => {
    if (!renaming || renameTo === renaming || !renameTo.trim()) {
      cancelRename();
      return;
    }
    await safeAlter({ rename: { from: renaming, to: renameTo.trim() } }, cancelRename);
  };

  const submitDrop = async (name: string) => {
    if (!confirm(`Drop column "${name}"? Existing values are lost.`)) return;
    await safeAlter({ drop: name });
  };

  return (
    <div className="absolute inset-0 bg-bg/80 flex items-center justify-center z-10 p-6">
      <div className="bg-bg-card border border-border rounded w-[36rem] max-w-full max-h-full flex flex-col overflow-hidden">
        <header className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div>
            <h3 className="text-sm font-medium text-text">
              Edit <span className="font-mono">{table.name}</span> schema
            </h3>
            <p className="text-xs text-text-dim mt-0.5">
              Add, rename, or drop columns. Reserved columns
              (<span className="font-mono">id</span>, <span className="font-mono">created_at</span>,{" "}
              <span className="font-mono">updated_at</span>) are managed automatically.
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-text-muted hover:text-text text-lg leading-none px-1"
            aria-label="Close"
          >
            ×
          </button>
        </header>
        <div className="overflow-auto flex-1 p-4 flex flex-col gap-4">
          {error && (
            <div className="text-xs text-red bg-red/10 border border-red/40 rounded p-2">
              {error}
            </div>
          )}
          <section className="flex flex-col gap-1">
            <h4 className="text-text-dim uppercase text-[10px] tracking-wide mb-1">
              Columns ({editableColumns.length})
            </h4>
            {editableColumns.length === 0 ? (
              <div className="text-xs text-text-muted py-2">
                No user columns yet. Add one below.
              </div>
            ) : (
              <ul className="flex flex-col gap-1">
                {editableColumns.map((c) => (
                  <li
                    key={c.name}
                    className="border border-border rounded px-2 py-1.5 text-xs flex items-center gap-2"
                  >
                    {renaming === c.name ? (
                      <>
                        <input
                          autoFocus
                          value={renameTo}
                          disabled={busy}
                          onChange={(e) => setRenameTo(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") submitRename();
                            if (e.key === "Escape") cancelRename();
                          }}
                          className="bg-bg-input border border-border rounded px-1.5 py-0.5 text-xs font-mono flex-1 min-w-0"
                        />
                        <button
                          type="button"
                          onClick={submitRename}
                          disabled={busy}
                          className="text-[10px] px-1.5 py-0.5 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
                        >
                          save
                        </button>
                        <button
                          type="button"
                          onClick={cancelRename}
                          disabled={busy}
                          className="text-[10px] px-1.5 py-0.5 border border-border rounded text-text-muted hover:bg-bg-input"
                        >
                          cancel
                        </button>
                      </>
                    ) : (
                      <>
                        <span className="font-mono text-text flex-1 truncate" title={c.name}>
                          {c.name}
                        </span>
                        <span className="text-text-dim text-[10px]">{c.type}</span>
                        {!c.nullable && (
                          <span className="text-[10px] text-red bg-red/10 border border-red/30 rounded px-1">
                            required
                          </span>
                        )}
                        <button
                          type="button"
                          onClick={() => startRename(c.name)}
                          disabled={busy}
                          className="text-[10px] px-1.5 py-0.5 border border-border rounded text-text-dim hover:text-text hover:bg-bg-input disabled:opacity-50"
                        >
                          rename
                        </button>
                        <button
                          type="button"
                          onClick={() => submitDrop(c.name)}
                          disabled={busy}
                          className="text-[10px] px-1.5 py-0.5 border border-red/40 text-red rounded hover:bg-red/10 disabled:opacity-50"
                        >
                          drop
                        </button>
                      </>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section className="border-t border-border pt-3 flex flex-col gap-2">
            <div className="flex items-center justify-between">
              <h4 className="text-text-dim uppercase text-[10px] tracking-wide">Add column</h4>
              {!adding && (
                <button
                  type="button"
                  onClick={() => setAdding(true)}
                  className="text-xs text-accent hover:underline"
                >
                  + new column
                </button>
              )}
            </div>
            {adding && (
              <AddColumnForm
                hasRows={table.row_count > 0}
                disabled={busy}
                onCancel={() => setAdding(false)}
                onSubmit={async (col) => {
                  await safeAlter({ add: col }, () => setAdding(false));
                }}
              />
            )}
          </section>
        </div>
      </div>
    </div>
  );
}

function AddColumnForm({
  hasRows,
  disabled,
  onCancel,
  onSubmit,
}: {
  hasRows: boolean;
  disabled: boolean;
  onCancel: () => void;
  onSubmit: (col: ColumnDef) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [type, setType] = useState<ColumnType>("text");
  const [nullable, setNullable] = useState(true);
  const [defaultStr, setDefaultStr] = useState("");

  const submit = async () => {
    if (!name.trim()) return;
    const col: ColumnDef = { name: name.trim(), type, nullable };
    if (defaultStr.trim() !== "") {
      col.default = parseInputValue({ name, type, nullable }, defaultStr.trim());
    }
    // Server requires a default when adding a non-nullable column to a
    // populated table — surface the rule in the UI before the round-trip.
    if (!nullable && hasRows && col.default === undefined) {
      alert("Non-nullable column on a populated table needs a default value.");
      return;
    }
    await onSubmit(col);
  };

  return (
    <div className="flex flex-col gap-2 border border-border rounded p-2 bg-bg-input/30">
      <div className="grid grid-cols-[1fr_auto_auto] gap-2 items-center">
        <input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="column_name"
          disabled={disabled}
          className="bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
        />
        <select
          value={type}
          onChange={(e) => setType(e.target.value as ColumnType)}
          disabled={disabled}
          className="bg-bg-input border border-border rounded px-1.5 py-1 text-xs"
        >
          <option value="text">text</option>
          <option value="number">number</option>
          <option value="bool">bool</option>
          <option value="datetime">datetime</option>
          <option value="json">json</option>
          <option value="file_id">file_id</option>
        </select>
        <label className="flex items-center gap-1 text-xs text-text-dim">
          <input
            type="checkbox"
            checked={nullable}
            onChange={(e) => setNullable(e.target.checked)}
            disabled={disabled}
          />
          nullable
        </label>
      </div>
      <input
        value={defaultStr}
        onChange={(e) => setDefaultStr(e.target.value)}
        placeholder={`default value (optional${!nullable && hasRows ? " — required when adding required col to populated table" : ""})`}
        disabled={disabled}
        className="bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
      />
      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={onCancel}
          disabled={disabled}
          className="text-xs px-3 py-1 border border-border rounded hover:bg-bg-input"
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={submit}
          disabled={disabled || !name.trim()}
          className="text-xs px-3 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >
          Add
        </button>
      </div>
    </div>
  );
}
