// TablesPanel — dashboard surface for the tables app. Talks to the
// tables sidecar via /api/apps/tables/* (the platform proxy injects
// the per-install bearer token). Inherits the dashboard theme via
// Tailwind tokens.
//
// Layout: left rail = list of tables (with row counts), main area =
// selected table's row grid, bottom drawer = SELECT escape hatch.

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  parseJSON,
  stringifyJSON,
  parseInputValue,
  initialField,
  fieldValue,
  type FieldValue,
  type ColumnDef,
  type ColumnType,
} from "./lib/values";
import { useResource } from "./lib/useResource";
import { Dialog } from "./Dialog";
import TablesDiagnosticsWidget from "./TablesDiagnosticsWidget";

import { useAppEvents } from "./lib/useAppEvents";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
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
  total?: number;
  has_more: boolean;
  next_cursor?: string;
  next_offset: number;
}

interface QueryResponse {
  columns: string[];
  rows: Record<string, unknown>[];
  truncated: boolean;
}

const API = "/api/apps/tables";
const PAGE_SIZE = 50;
type PanelApi = <T>(method: string, path: string, params?: Record<string, string>, body?: unknown, signal?: AbortSignal) => Promise<T>;

type RowFilter = { col: string; op: string; value: unknown };

function formatRowCount(value: number): string {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function formatDate(value: unknown): string {
  if (typeof value !== "string" || !value) return "—";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

export default function TablesPanel({
  projectId,
  installId,
}: NativePanelProps) {
  const [selected, setSelected] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get("table"),
  );
  const [surface, setSurface] = useState<"tables" | "projections" | "diagnostics">("tables");
  const [tableSearch, setTableSearch] = useState("");
  const [rowSearch, setRowSearch] = useState("");
  const [filterColumn, setFilterColumn] = useState("");
  const [filterValue, setFilterValue] = useState("");
  const [filterApplied, setFilterApplied] = useState<RowFilter | null>(null);
  const [orderBy, setOrderBy] = useState("");
  const [epoch, setEpoch] = useState(0);
  const [page, setPage] = useState(0);
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const [columnLimit, setColumnLimit] = useState(8);
  const [showCreate, setShowCreate] = useState(false),
    [showInsert, setShowInsert] = useState(false);
  const [showQuery, setShowQuery] = useState(false),
    [showApi, setShowApi] = useState(false),
    [showSchema, setShowSchema] = useState(false);
  const [editing, setEditing] = useState<{
    key: string;
    tableID: string;
    row: Record<string, unknown>;
  } | null>(null);
  const editRequest = useRef(0);
  const [status, setStatus] = useState("");
  const [mutation, setMutation] = useState(false);
  const mutationRef = useRef(false);
  const scope = `${projectId}:${installId}`,
    identity = `${scope}:${selected ?? ""}`;
  const currentIdentity = useRef(identity);
  currentIdentity.current = identity;
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const refresh = useCallback(() => {
    if (!refreshTimer.current)
      refreshTimer.current = setTimeout(() => {
        refreshTimer.current = null;
        setEpoch((x) => x + 1);
      }, 120);
  }, []);
  useEffect(
    () => () => {
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
    },
    [],
  );
  const api = useCallback(
    async <T,>(
      method: string,
      path: string,
      params: Record<string, string> = {},
      body?: unknown,
      signal?: AbortSignal,
    ): Promise<T> => {
      const query = new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...params,
      });
      const res = await fetch(`${API}${path}?${query}`, {
        method,
        credentials: "same-origin",
        signal,
        headers:
          body === undefined ? {} : { "Content-Type": "application/json" },
        body: body === undefined ? undefined : stringifyJSON(body),
      });
      const text = await res.text();
      if (!res.ok) {
        let message = text;
        try {
          message = (parseJSON(text) as { error?: string }).error || text;
        } catch {}
        throw new Error(`${res.status}: ${message}`);
      }
      return parseJSON(text) as T;
    },
    [projectId, installId],
  );
  const list = useResource<TableMeta[]>(
    projectId ? scope : "",
    epoch,
    async (signal) => {
      let offset = 0;
      const tables: TableMeta[] = [];
      do {
        const result = await api<{
          tables: TableMeta[];
          has_more: boolean;
          next_offset: number;
        }>(
          "GET",
          "/tables",
          { summary: "true", limit: "200", offset: String(offset) },
          undefined,
          signal,
        );
        // Summary/list responses from older sidecars may omit the schema
        // payload. Keep the workspace renderable while the selected table's
        // full description is loaded below.
        tables.push(
          ...(result.tables ?? []).map((table) => ({
            ...table,
            columns: Array.isArray(table.columns) ? table.columns : [],
          })),
        );
        if (!result.has_more) break;
        offset = result.next_offset;
      } while (!signal.aborted);
      return tables;
    },
  );
  const tables = list.data ?? [];
  useEffect(() => {
    if (!list.data) return;
    setSelected((current) => current ?? list.data![0]?.name ?? null);
  }, [list.data]);
  const description = useResource<TableMeta>(
    selected ? identity : "",
    epoch,
    (signal) =>
      api<TableMeta>("GET", `/tables/${selected}`, {}, undefined, signal),
  );
  const selectedTable = description.data
    ? { ...description.data, columns: description.data.columns ?? [] }
    : null;
  const visibleColumns = (selectedTable?.columns ?? []).slice(0, columnLimit);
  const visibleNames = visibleColumns.map((c) => c.name).join(",");
  const rowSearchColumn = selectedTable?.columns.find((c) => c.type === "text");
  const rowKey = selectedTable
    ? `${identity}:${selectedTable.id}:${page}:${cursors[page] ?? ""}:${visibleNames}:${JSON.stringify(filterApplied)}:${orderBy}`
    : "";
  const rowResource = useResource<RowsResponse>(rowKey, epoch, (signal) => {
    const where = filterApplied ? [filterApplied] : [];
    const params = {
      limit: String(PAGE_SIZE),
      include_total: "false",
      select: [
        "id",
        "_revision",
        "updated_at",
        ...visibleColumns.map((c) => c.name),
      ].join(","),
      ...(orderBy ? { order_by: orderBy } : {}),
      ...(cursors[page] ? { cursor: cursors[page]! } : {}),
    };
    return api<RowsResponse>(
      where.length ? "POST" : "GET",
      `/tables/${selected}/rows${where.length ? "/search" : ""}`,
      params,
      where.length ? { where } : undefined,
      signal,
    );
  });
  const rows = rowResource.data?.rows ?? [];
  const filteredTables = useMemo(() => {
    const query = tableSearch.trim().toLowerCase();
    if (!query) return tables;
    return tables.filter((table) => table.name.toLowerCase().includes(query));
  }, [tableSearch, tables]);
  const resetRows = () => {
    setPage(0);
    setCursors([undefined]);
    setEditing(null);
  };
  const applyQuickSearch = () => {
    const value = rowSearch.trim();
    if (!value || !rowSearchColumn) {
      setFilterApplied(null);
      resetRows();
      return;
    }
    setFilterApplied({ col: rowSearchColumn.name, op: "contains", value });
    resetRows();
  };
  const applyTypedFilter = () => {
    const column = selectedTable?.columns.find((c) => c.name === filterColumn);
    if (!column || !filterValue.trim()) return;
    try {
      const value = parseInputValue(column, filterValue.trim());
      setFilterApplied({
        col: column.name,
        op: column.type === "text" ? "contains" : "eq",
        value,
      });
      setRowSearch("");
      resetRows();
    } catch (error) {
      setStatus((error as Error).message);
    }
  };
  const clearFilter = () => {
    setFilterApplied(null);
    setRowSearch("");
    setFilterValue("");
    resetRows();
  };
  const selectTable = (name: string) => {
    editRequest.current++;
    deepRow.current = null;
    const url = new URL(window.location.href);
    url.searchParams.delete("row");
    window.history.replaceState(null, "", url);
    setSelected(name);
    setPage(0);
    setCursors([undefined]);
    setEditing(null);
    setRowSearch("");
    setFilterColumn("");
    setFilterValue("");
    setFilterApplied(null);
    setOrderBy("");
  };
  useEffect(() => {
    setPage(0);
    setCursors([undefined]);
    setEditing(null);
    setColumnLimit(8);
    setShowCreate(false);
    setShowInsert(false);
    setShowQuery(false);
    setShowSchema(false);
    setShowApi(false);
    setStatus("");
    setRowSearch("");
    setFilterColumn("");
    setFilterValue("");
    setFilterApplied(null);
    setOrderBy("");
  }, [identity]);
  useEffect(() => {
    if (!selected) return;
    const url = new URL(window.location.href);
    url.searchParams.set("table", selected);
    window.history.replaceState(null, "", url);
  }, [selected]);
  useAppEvents<{ table?: string; name?: string }>("tables", projectId, (ev) => {
    if (ev.install_id && ev.install_id !== installId) return;
    if (ev.topic === "table.dropped" && ev.data.name === selected) {
      setSelected(null);
      setEditing(null);
    }
    if (ev.topic.startsWith("table.") || ev.topic.startsWith("row.")) {
      refresh();
      if (ev.topic === "table.altered" && ev.data.name === selected) {
        setCursors([undefined]);
        setPage(0);
        setEditing(null);
      }
    }
  });
  useEffect(() => {
    const connected = (e: Event) => {
      if ((e as CustomEvent).detail?.projectId === projectId) refresh();
    };
    window.addEventListener("apteva:app-events-connected", connected);
    const handler = () => refresh();
    window.addEventListener("online", handler);
    window.addEventListener("focus", handler);
    return () => {
      window.removeEventListener("apteva:app-events-connected", connected);
      window.removeEventListener("online", handler);
      window.removeEventListener("focus", handler);
    };
  }, [refresh, projectId]);
  useEffect(() => {
    if (page > 0 && rowResource.data && !rows.length && !rowResource.busy) {
      setPage((p) => Math.max(0, p - 1));
    }
  }, [rowResource.data, rowResource.busy, page, rows.length]);
  const mutate = async (work: () => Promise<void>) => {
    if (mutationRef.current) throw new Error("A change is already being saved");
    mutationRef.current = true;
    setMutation(true);
    try {
      await work();
      refresh();
    } finally {
      mutationRef.current = false;
      setMutation(false);
    }
  };
  const editRow = async (row: Record<string, unknown>) => {
    const request = ++editRequest.current;
    const key = identity;
    try {
      const result = await api<{
        row: Record<string, unknown>;
        found: boolean;
      }>("GET", `/tables/${selected}/rows/${String(row.id)}`, {
        select: ["id", "_revision", ...visibleColumns.map((c) => c.name)].join(
          ",",
        ),
      });
      if (currentIdentity.current === key && request === editRequest.current) {
        if (result.found)
          setEditing({
            key,
            tableID: String(selectedTable!.id),
            row: result.row,
          });
        else {
          setStatus("Row was deleted; refreshing.");
          refresh();
        }
      }
    } catch (e) {
      if (currentIdentity.current === key) setStatus((e as Error).message);
    }
  };
  const deepRow = useRef<string | null>(
    new URLSearchParams(window.location.search).get("row"),
  );
  useEffect(() => {
    if (selectedTable && deepRow.current) {
      const id = deepRow.current;
      deepRow.current = null;
      void editRow({ id });
    }
  }, [selectedTable?.id]);
  const onCreate = async (name: string, columns: ColumnDef[]) =>
    mutate(async () => {
      await api("POST", "/tables", {}, { name, columns });
      setShowCreate(false);
      selectTable(name);
    });
  const onInsert = async (row: Record<string, unknown>) =>
    mutate(async () => {
      const key = identity;
      await api("POST", `/tables/${selected}/rows`, {}, { row });
      if (currentIdentity.current === key) {
        setShowInsert(false);
        setCursors([undefined]);
        setPage(0);
      }
    });
  const onUpdate = async (id: string, fields: Record<string, unknown>) =>
    mutate(async () => {
      const key = identity;
      await api(
        "PATCH",
        `/tables/${selected}/rows/${id}`,
        {
          expected_revision: String(editing?.row._revision),
          expected_table_id: editing?.tableID ?? "",
          select: "id,_revision",
        },
        fields,
      );
      if (currentIdentity.current === key) setEditing(null);
    });
  const onDeleteRow = async (id: string) => {
    if (!confirm(`Delete row ${id} from ${selected}?`)) return;
    await mutate(async () => {
      const key = identity;
      await api("DELETE", `/tables/${selected}/rows/${id}`, {
        expected_revision: String(editing?.row._revision),
        expected_table_id: editing?.tableID ?? "",
      });
      if (currentIdentity.current === key) setEditing(null);
    });
  };
  const onAlter = async (op: AlterOp) =>
    mutate(async () => {
      await api("PATCH", `/tables/${selected}`, {}, op);
      setEditing(null);
      setCursors([undefined]);
      setPage(0);
    });
  const onDropTable = async () => {
    if (!confirm(`Drop table "${selected}" and all its rows?`)) return;
    try {
      await mutate(async () => {
        await api("DELETE", `/tables/${selected}`, { confirm: "true" });
        setSelected(null);
      });
    } catch (e) {
      setStatus((e as Error).message);
    }
  };
  const activeEdit =
    editing?.key === identity && editing.tableID === String(selectedTable?.id)
      ? editing.row
      : null;
  const gridTable = selectedTable
    ? { ...selectedTable, columns: visibleColumns }
    : null;
  const error = status || rowResource.error || description.error || list.error;
  return (
    <div className="relative h-full flex min-h-0 bg-bg">
      <aside className="w-64 shrink-0 border-r border-border bg-bg-card flex flex-col">
        <header className="p-4 border-b border-border">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-[11px] uppercase tracking-[0.16em] text-text-dim">Workspace</p>
              <strong className="text-base text-text">Tables</strong>
            </div>
            <button
              type="button"
              className="rounded-md bg-accent px-2.5 py-1.5 text-xs font-medium text-bg hover:opacity-90"
              onClick={() => setShowCreate(true)}
            >
              + New table
            </button>
          </div>
          <label className="relative mt-3 block">
            <span className="sr-only">Search tables</span>
            <input
              value={tableSearch}
              onChange={(event) => setTableSearch(event.target.value)}
              placeholder="Search tables…"
              className="w-full rounded-md border border-border bg-bg-input px-3 py-2 text-sm text-text outline-none placeholder:text-text-dim focus:border-accent"
            />
          </label>
          <div className="mt-3 flex items-center justify-between text-[11px] text-text-dim">
            <span>{tables.length} tables</span>
            <span>{tables.reduce((sum, table) => sum + table.row_count, 0).toLocaleString()} rows</span>
          </div>
          <nav className="mt-4 grid grid-cols-3 gap-1 rounded-md bg-bg-input/60 p-1" aria-label="Tables workspace">
            <button type="button" onClick={() => setSurface("tables")} className={`rounded px-2 py-1.5 text-xs ${surface === "tables" ? "bg-bg-card font-medium text-text shadow-sm" : "text-text-dim hover:text-text"}`}>Data</button>
            <button type="button" onClick={() => setSurface("projections")} className={`rounded px-2 py-1.5 text-xs ${surface === "projections" ? "bg-bg-card font-medium text-text shadow-sm" : "text-text-dim hover:text-text"}`}>Projections</button>
            <button type="button" onClick={() => setSurface("diagnostics")} className={`rounded px-2 py-1.5 text-xs ${surface === "diagnostics" ? "bg-bg-card font-medium text-text shadow-sm" : "text-text-dim hover:text-text"}`}>Diagnostics</button>
          </nav>
        </header>
        <ul className="overflow-auto flex-1 p-2">
          {filteredTables.map((t) => (
            <li key={t.id}>
              <button
                disabled={mutation}
                onClick={() => selectTable(t.name)}
                className={`w-full rounded-md px-3 py-2.5 text-left transition-colors ${selected === t.name ? "bg-accent/10 text-text" : "text-text-muted hover:bg-bg-input/60 hover:text-text"}`}
              >
                <span className="block truncate font-mono text-xs">{t.name}</span>
                <span className="mt-1 block text-[11px] text-text-dim">{formatRowCount(t.row_count)} rows · {(t.columns ?? []).length} columns</span>
              </button>
            </li>
          ))}
          {!filteredTables.length && <li className="p-4 text-center text-xs text-text-dim">No matching tables</li>}
        </ul>
      </aside>
      <main className="flex-1 flex flex-col min-w-0 min-h-0">
        {error && (
          <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-2 text-xs text-red">
            {error}
          </div>
        )}
        {surface === "projections" ? (
          <ProjectionWorkspace api={api} />
        ) : surface === "diagnostics" ? (
          <TablesDiagnosticsWidget projectId={projectId} installId={installId} compact={false} />
        ) : selectedTable && gridTable ? (
          <>
            <header className="border-b border-border bg-bg-card px-5 py-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h1 className="truncate text-lg font-semibold text-text">{selectedTable.name}</h1>
                    <span className="rounded-full bg-bg-input px-2 py-0.5 text-[11px] text-text-dim">{selectedTable.scope}</span>
                  </div>
                  <p className="mt-1 text-xs text-text-dim">{formatRowCount(selectedTable.row_count)} rows · {selectedTable.columns.length} columns</p>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <button disabled={mutation} onClick={() => setShowInsert(true)} className="rounded-md bg-accent px-3 py-2 text-xs font-medium text-bg hover:opacity-90">+ Insert row</button>
                  <button onClick={() => setShowQuery((v) => !v)} className="rounded-md border border-border px-3 py-2 text-xs text-text hover:bg-bg-input">SQL editor</button>
                  <button disabled={mutation} onClick={() => setShowSchema(true)} className="rounded-md border border-border px-3 py-2 text-xs text-text hover:bg-bg-input">Schema</button>
                  <button onClick={() => setShowApi(true)} className="rounded-md border border-border px-3 py-2 text-xs text-text hover:bg-bg-input">API</button>
                  <button disabled={mutation} onClick={onDropTable} className="rounded-md px-2 py-2 text-xs text-red hover:bg-red/10" aria-label="More table actions">•••</button>
                </div>
              </div>
              <div className="mt-4 flex flex-wrap items-center gap-2">
                <form className="flex min-w-[15rem] flex-1 gap-2" onSubmit={(event) => { event.preventDefault(); applyQuickSearch(); }}>
                  <input
                    value={rowSearch}
                    onChange={(event) => setRowSearch(event.target.value)}
                    placeholder={rowSearchColumn ? `Search ${rowSearchColumn.name}…` : "Search rows…"}
                    className="min-w-0 flex-1 rounded-md border border-border bg-bg-input px-3 py-2 text-xs text-text outline-none placeholder:text-text-dim focus:border-accent"
                  />
                  <button type="submit" className="rounded-md border border-border px-3 py-2 text-xs text-text hover:bg-bg-input">Search</button>
                </form>
                <select
                  aria-label="Filter column"
                  value={filterColumn}
                  onChange={(event) => setFilterColumn(event.target.value)}
                  className="rounded-md border border-border bg-bg-input px-2.5 py-2 text-xs text-text outline-none"
                >
                  <option value="">Filter by column…</option>
                  {selectedTable.columns.map((column) => <option key={column.name} value={column.name}>{column.name}</option>)}
                </select>
                <input
                  aria-label="Filter value"
                  value={filterValue}
                  onChange={(event) => setFilterValue(event.target.value)}
                  onKeyDown={(event) => { if (event.key === "Enter") applyTypedFilter(); }}
                  placeholder="Value"
                  className="w-32 rounded-md border border-border bg-bg-input px-2.5 py-2 text-xs text-text outline-none placeholder:text-text-dim focus:border-accent"
                />
                <button type="button" onClick={applyTypedFilter} disabled={!filterColumn || !filterValue.trim()} className="rounded-md border border-border px-3 py-2 text-xs text-text hover:bg-bg-input disabled:opacity-40">Add filter</button>
                {filterApplied && <button type="button" onClick={clearFilter} className="rounded-full bg-accent/10 px-2.5 py-1.5 text-xs text-accent hover:bg-accent/20">{filterApplied.col} {filterApplied.op} · clear</button>}
                <label className="ml-auto flex items-center gap-2 text-xs text-text-dim">
                  Sort
                  <select aria-label="Sort rows" value={orderBy} onChange={(event) => { setOrderBy(event.target.value); resetRows(); }} className="rounded-md border border-border bg-bg-input px-2.5 py-2 text-xs text-text outline-none">
                    <option value="">Default</option>
                    {selectedTable.columns.flatMap((column) => [<option key={`${column.name}-asc`} value={`${column.name} asc`}>{column.name} ↑</option>, <option key={`${column.name}-desc`} value={`${column.name} desc`}>{column.name} ↓</option>])}
                  </select>
                </label>
                <label className="flex items-center gap-2 text-xs text-text-dim">
                  Columns
                <select
                  aria-label="Visible columns"
                  value={columnLimit}
                  onChange={(e) => {
                    setColumnLimit(Number(e.target.value));
                    setEditing(null);
                  }}
                >
                  {[4, 8, 16, 256].map((n) => (
                    <option key={n} value={n}>
                      {n === 256 ? "All" : n}
                    </option>
                  ))}
                </select>
                </label>
              </div>
            </header>
            <div className="flex-1 overflow-auto">
              {rows.length ? (
                <RowsTable
                  table={gridTable}
                  rows={rows}
                  onEditStart={editRow}
                  orderBy={orderBy}
                  onSort={(value) => { setOrderBy(value); resetRows(); }}
                />
              ) : (
                <p className="p-12 text-center text-sm text-text-dim">
                  {rowResource.busy ? "Loading rows…" : filterApplied || rowSearch ? "No rows match this filter." : "No rows yet. Insert the first one to get started."}
                </p>
              )}
            </div>
            <footer className="flex items-center justify-between border-t border-border bg-bg-card px-5 py-3 text-xs text-text-dim">
              <span>{rowResource.busy ? "Refreshing…" : filterApplied ? "Filtered view" : "All rows"}</span>
              <div className="flex items-center gap-2">
              <button
                disabled={!page || rowResource.busy}
                onClick={() => setPage((p) => p - 1)}
                className="rounded-md border border-border px-2.5 py-1.5 hover:bg-bg-input disabled:opacity-40"
              >
                Previous
              </button>
              <span>Page {page + 1}</span>
              <button
                disabled={
                  !rowResource.data?.has_more ||
                  !rowResource.data.next_cursor ||
                  rowResource.busy
                }
                onClick={() => {
                  setCursors((old) => [
                    ...old.slice(0, page + 1),
                    rowResource.data!.next_cursor,
                  ]);
                  setPage((p) => p + 1);
                }}
                className="rounded-md border border-border px-2.5 py-1.5 hover:bg-bg-input disabled:opacity-40"
              >
                Next
              </button>
              </div>
            </footer>
            {activeEdit && <RowDetailDrawer table={selectedTable} row={activeEdit} onClose={() => setEditing(null)} onSave={(fields) => onUpdate(String(activeEdit.id), fields)} onDelete={() => onDeleteRow(String(activeEdit.id))} />}
            {showQuery && (
              <QueryDrawer
                key={identity}
                tableName={selectedTable.name}
                api={api}
                onClose={() => setShowQuery(false)}
              />
            )}
            {showInsert && (
              <InsertDialog
                key={identity}
                table={selectedTable}
                onCancel={() => setShowInsert(false)}
                onSubmit={onInsert}
              />
            )}
            {showApi && (
              <ApiHelp
                table={selectedTable}
                projectId={projectId}
                installId={installId}
                onClose={() => setShowApi(false)}
              />
            )}
            {showSchema && (
              <SchemaEditor
                key={identity}
                table={selectedTable}
                onAlter={onAlter}
                onClose={() => setShowSchema(false)}
              />
            )}
          </>
        ) : (
          <p className="p-8">
            {description.busy ? "Loading…" : "Select or create a table."}
          </p>
        )}
        {showCreate && (
          <CreateDialog
            onCancel={() => setShowCreate(false)}
            onSubmit={onCreate}
          />
        )}
      </main>
    </div>
  );
}

interface ProjectionStatus {
  name: string;
  version: number;
  status: string;
  is_current?: boolean;
  built?: boolean;
  ready?: boolean;
  stale?: boolean;
  latest_relevant_change?: number;
  published_change_id?: number;
  pending_scopes?: number;
  refresh_running?: boolean;
  min_refresh_interval_seconds?: number;
  last_successful_publication_at?: string;
  next_scheduled_refresh?: string;
  last_failure?: string | null;
  coverage_from?: string | null;
  coverage_to?: string | null;
  scope_columns?: string[];
  source_tables?: string[];
  sql?: string;
}

function projectionTone(projection: ProjectionStatus): string {
  if (projection.last_failure) return "bg-red/10 text-red";
  if (projection.ready) return "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400";
  if (projection.status === "building" || projection.refresh_running) return "bg-sky-500/15 text-sky-600 dark:text-sky-400";
  return "bg-amber-500/15 text-amber-600 dark:text-amber-400";
}

function ProjectionWorkspace({ api }: { api: PanelApi }) {
  const [epoch, setEpoch] = useState(0);
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState("");
  const list = useResource<{ projections: ProjectionStatus[] }>("projections", epoch, (signal) => api("GET", "/projections", {}, undefined, signal));
  const projections = list.data?.projections ?? [];
  const active = projections.find((projection) => `${projection.name}:${projection.version}` === selected) ?? projections[0];
  useEffect(() => {
    if (!selected && active) setSelected(`${active.name}:${active.version}`);
  }, [active, selected]);
  const refresh = async (projection: ProjectionStatus) => {
    setBusy(`${projection.name}:refresh`);
    setMessage("");
    try {
      await api("POST", `/projections/${encodeURIComponent(projection.name)}/refresh`, {}, { rebuild: true, force: true });
      setMessage("Refresh queued. The status will update automatically.");
      setEpoch((value) => value + 1);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  };
  const togglePause = async (projection: ProjectionStatus) => {
    const paused = projection.status !== "paused";
    setBusy(`${projection.name}:pause`);
    setMessage("");
    try {
      await api("POST", `/projections/${encodeURIComponent(projection.name)}/pause`, {}, { paused });
      setEpoch((value) => value + 1);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-bg">
      <header className="border-b border-border bg-bg-card px-6 py-5">
        <p className="text-[11px] uppercase tracking-[0.16em] text-text-dim">Analytics</p>
        <div className="mt-1 flex flex-wrap items-center justify-between gap-3"><div><h1 className="text-xl font-semibold text-text">Projections</h1><p className="mt-1 text-sm text-text-dim">Published, refreshable views over your Tables data.</p></div><span className="rounded-full bg-bg-input px-3 py-1.5 text-xs text-text-dim">{projections.length} versions</span></div>
      </header>
      {message && <div role="status" className="border-b border-accent/20 bg-accent/5 px-6 py-3 text-xs text-accent">{message}</div>}
      <div className="flex min-h-0 flex-1">
        <div className="w-72 shrink-0 overflow-auto border-r border-border p-3">
          {list.error && <p className="p-3 text-xs text-red">{list.error}</p>}
          {!list.busy && !projections.length && <div className="rounded-lg border border-dashed border-border p-5 text-center"><p className="text-sm font-medium text-text">No projections yet</p><p className="mt-1 text-xs text-text-dim">Create one with the projections_create tool, then manage it here.</p></div>}
          <div className="flex flex-col gap-1">{projections.map((projection) => { const key = `${projection.name}:${projection.version}`; return <button type="button" key={key} onClick={() => setSelected(key)} className={`rounded-lg p-3 text-left ${selected === key ? "bg-accent/10" : "hover:bg-bg-input/60"}`}><div className="flex items-center justify-between gap-2"><span className="truncate font-mono text-xs text-text">{projection.name}</span><span className="text-[10px] text-text-dim">v{projection.version}</span></div><div className="mt-2 flex items-center gap-2"><span className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${projectionTone(projection)}`}>{projection.last_failure ? "Failed" : projection.ready ? "Ready" : projection.status === "paused" ? "Paused" : projection.status === "building" ? "Building" : "Stale"}</span>{projection.is_current && <span className="text-[10px] text-text-dim">Current</span>}</div></button>; })}</div>
        </div>
        <div className="min-w-0 flex-1 overflow-auto p-6">
          {active ? <div className="mx-auto max-w-4xl"><div className="flex flex-wrap items-start justify-between gap-4"><div><div className="flex items-center gap-2"><h2 className="font-mono text-lg text-text">{active.name}</h2><span className="text-xs text-text-dim">version {active.version}</span></div><div className="mt-2 flex flex-wrap gap-2"><span className={`rounded-full px-2.5 py-1 text-xs font-medium ${projectionTone(active)}`}>{active.ready ? "Ready" : active.status}</span>{active.is_current && <span className="rounded-full bg-bg-input px-2.5 py-1 text-xs text-text-dim">Current version</span>}</div></div><div className="flex gap-2"><button type="button" disabled={busy !== ""} onClick={() => refresh(active)} className="rounded-md bg-accent px-3 py-2 text-xs font-medium text-bg hover:opacity-90 disabled:opacity-40">{busy === `${active.name}:refresh` ? "Queueing…" : "Refresh now"}</button><button type="button" disabled={busy !== ""} onClick={() => togglePause(active)} className="rounded-md border border-border px-3 py-2 text-xs text-text hover:bg-bg-input disabled:opacity-40">{active.status === "paused" ? "Resume" : "Pause"}</button></div></div>
            {active.last_failure && <div className="mt-6 rounded-lg border border-red/30 bg-red/10 p-4 text-sm text-red"><strong>Last refresh failed</strong><p className="mt-1 text-xs">{active.last_failure}</p></div>}
            <div className="mt-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">{[["Published", active.last_successful_publication_at ? formatDate(active.last_successful_publication_at) : "Not yet"], ["Pending scopes", String(active.pending_scopes ?? 0)], ["Refresh interval", `${active.min_refresh_interval_seconds ?? 0}s`], ["Coverage", active.coverage_from && active.coverage_to ? `${formatDate(active.coverage_from)} – ${formatDate(active.coverage_to)}` : "Declared by definition"]].map(([label, value]) => <div key={label} className="rounded-lg border border-border bg-bg-card p-4"><p className="text-[11px] uppercase tracking-wide text-text-dim">{label}</p><p className="mt-2 text-sm font-medium text-text">{value}</p></div>)}</div>
            <div className="mt-6 grid gap-6 lg:grid-cols-2"><section className="rounded-lg border border-border bg-bg-card p-5"><h3 className="text-sm font-semibold text-text">Sources and scopes</h3><dl className="mt-4 space-y-3 text-xs"><div><dt className="text-text-dim">Source tables</dt><dd className="mt-1 flex flex-wrap gap-1">{(active.source_tables ?? []).map((table) => <span key={table} className="rounded bg-bg-input px-2 py-1 font-mono text-text">{table}</span>)}</dd></div><div><dt className="text-text-dim">Scope columns</dt><dd className="mt-1 font-mono text-text">{active.scope_columns?.join(", ") || "Whole projection"}</dd></div></dl></section><section className="rounded-lg border border-border bg-bg-card p-5"><h3 className="text-sm font-semibold text-text">Freshness</h3><dl className="mt-4 space-y-3 text-xs"><div className="flex justify-between gap-3"><dt className="text-text-dim">Latest relevant change</dt><dd className="font-mono text-text">{active.latest_relevant_change ?? 0}</dd></div><div className="flex justify-between gap-3"><dt className="text-text-dim">Published change</dt><dd className="font-mono text-text">{active.published_change_id ?? 0}</dd></div><div className="flex justify-between gap-3"><dt className="text-text-dim">Next refresh</dt><dd className="text-text">{active.next_scheduled_refresh ? formatDate(active.next_scheduled_refresh) : "—"}</dd></div></dl></section></div>
          </div> : <p className="text-sm text-text-dim">{list.busy ? "Loading projections…" : "Select a projection to inspect it."}</p>}
        </div>
      </div>
    </div>
  );
}

// ─── rows table ─────────────────────────────────────────────────────

function RowsTable({
  table,
  rows,
  onEditStart,
  orderBy,
  onSort,
}: {
  table: TableMeta;
  rows: Record<string, unknown>[];
  onEditStart: (r: Record<string, unknown>) => void;
  orderBy: string;
  onSort: (value: string) => void;
}) {
  const toggleSort = (name: string) => {
    const [column, direction] = orderBy.split(" ");
    onSort(column === name && direction === "asc" ? `${name} desc` : `${name} asc`);
  };
  return (
    <table className="min-w-full text-sm">
      <thead className="sticky top-0 z-[1] bg-bg-card text-[11px] uppercase tracking-wide text-text-dim shadow-sm">
        <tr>
          <th className="border-b border-border px-4 py-3 text-left font-medium">ID</th>
          {table.columns.map((c) => (
            <th key={c.name} className="border-b border-border px-4 py-3 text-left font-medium">
              <button type="button" onClick={() => toggleSort(c.name)} className="group flex items-center gap-2 whitespace-nowrap text-left hover:text-text">
                <span className="font-mono normal-case text-text">{c.name}</span>
                <span className="rounded bg-bg-input px-1.5 py-0.5 text-[10px] normal-case text-text-dim">{c.type}</span>
                {orderBy.startsWith(`${c.name} `) && <span className="text-accent">{orderBy.endsWith("asc") ? "↑" : "↓"}</span>}
              </button>
            </th>
          ))}
          <th className="border-b border-border px-4 py-3 text-left font-medium">Updated</th>
          <th className="border-b border-border px-4 py-3" />
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const id = String(r.id);
          return (
            <tr key={id} onClick={() => onEditStart(r)} className="cursor-pointer border-b border-border/70 transition-colors hover:bg-accent/5">
              <td className="px-4 py-3 align-middle font-mono text-xs text-text-dim">{id}</td>
              {table.columns.map((c) => (
                <td key={c.name} className="max-w-[22rem] truncate px-4 py-3 align-middle text-text">
                  {renderCell(c, r[c.name])}
                </td>
              ))}
              <td className="whitespace-nowrap px-4 py-3 align-middle text-xs text-text-dim">{formatDate(r.updated_at)}</td>
              <td className="px-4 py-3 text-right text-xs text-accent">View →</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function renderCell(c: ColumnDef, v: unknown): ReactNode {
  if (v === null || v === undefined || v === "") return <span className="text-text-dim">—</span>;
  if (c.type === "bool") return <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${v ? "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400" : "bg-bg-input text-text-dim"}`}>{v ? "True" : "False"}</span>;
  if (c.type === "datetime") return <span className="whitespace-nowrap">{formatDate(v)}</span>;
  if (c.type === "json") return <span className="font-mono text-xs text-text-muted" title={stringifyJSON(v)}>{stringifyJSON(v)}</span>;
  return <span className={c.type === "number" ? "tabular-nums" : ""}>{String(v)}</span>;
}

function RowDetailDrawer({
  table,
  row,
  onClose,
  onSave,
  onDelete,
}: {
  table: TableMeta;
  row: Record<string, unknown>;
  onClose: () => void;
  onSave: (fields: Record<string, unknown>) => Promise<void>;
  onDelete: () => Promise<void>;
}) {
  const [fields, setFields] = useState<Record<string, FieldValue>>(() =>
    Object.fromEntries(table.columns.map((column) => [column.name, initialField(column, row[column.name])])),
  );
  const dirty = useRef(new Set<string>());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const update = (name: string, value: FieldValue) => {
    dirty.current.add(name);
    setFields((current) => ({ ...current, [name]: value }));
  };
  const save = async () => {
    if (!dirty.current.size) return;
    setBusy(true);
    setError("");
    try {
      const patch: Record<string, unknown> = {};
      for (const column of table.columns) {
        if (dirty.current.has(column.name)) patch[column.name] = fieldValue(column, fields[column.name]!);
      }
      await onSave(patch);
    } catch (error) {
      setError((error as Error).message);
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    setError("");
    try {
      await onDelete();
    } catch (error) {
      setError((error as Error).message);
      setBusy(false);
    }
  };
  return (
    <div className="absolute inset-0 z-10 bg-black/20" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <aside role="dialog" aria-label={`Row ${String(row.id)}`} className="absolute inset-y-0 right-0 flex w-[min(28rem,100%)] flex-col border-l border-border bg-bg-card shadow-2xl">
        <header className="flex items-center justify-between border-b border-border px-5 py-4">
          <div><p className="text-[11px] uppercase tracking-[0.14em] text-text-dim">Row detail</p><h2 className="mt-1 font-mono text-sm text-text">{table.name} · {String(row.id)}</h2></div>
          <button type="button" onClick={onClose} className="rounded-md px-2 py-1 text-xl leading-none text-text-dim hover:bg-bg-input hover:text-text" aria-label="Close row detail">×</button>
        </header>
        <div className="flex-1 overflow-auto px-5 py-4">
          {error && <div role="alert" className="mb-4 rounded-md border border-red/30 bg-red/10 p-3 text-xs text-red">{error}</div>}
          <div className="mb-5 grid grid-cols-2 gap-3 rounded-md bg-bg-input/40 p-3 text-xs">
            <div><span className="block text-text-dim">Row ID</span><span className="mt-1 block font-mono text-text">{String(row.id)}</span></div>
            <div><span className="block text-text-dim">Revision</span><span className="mt-1 block font-mono text-text">{String(row._revision ?? "—")}</span></div>
            <div className="col-span-2"><span className="block text-text-dim">Updated</span><span className="mt-1 block text-text">{formatDate(row.updated_at)}</span></div>
          </div>
          <div className="flex flex-col gap-4">
            {table.columns.map((column) => <label key={column.name} className="flex flex-col gap-1.5"><span className="flex items-center justify-between text-xs font-medium text-text"><span className="font-mono">{column.name}</span><span className="text-[10px] font-normal text-text-dim">{column.type}{column.nullable ? " · nullable" : ""}</span></span><FieldInput column={column} value={fields[column.name]!} onChange={(value) => update(column.name, value)} disabled={busy} /></label>)}
          </div>
        </div>
        <footer className="flex items-center justify-between border-t border-border px-5 py-4">
          <button type="button" disabled={busy} onClick={remove} className="rounded-md px-2 py-2 text-xs text-red hover:bg-red/10 disabled:opacity-40">Delete row</button>
          <div className="flex gap-2"><button type="button" disabled={busy} onClick={onClose} className="rounded-md border border-border px-3 py-2 text-xs text-text hover:bg-bg-input">Cancel</button><button type="button" disabled={busy || !dirty.current.size} onClick={save} className="rounded-md bg-accent px-3 py-2 text-xs font-medium text-bg hover:opacity-90 disabled:opacity-40">{busy ? "Saving…" : "Save changes"}</button></div>
        </footer>
      </aside>
    </div>
  );
}

export function FieldInput({
  column,
  value,
  onChange,
  insert = false,
  disabled = false,
}: {
  column: ColumnDef;
  value: FieldValue;
  onChange: (value: FieldValue) => void;
  insert?: boolean;
  disabled?: boolean;
}) {
  const cls = "bg-bg-input border border-border rounded p-1 text-xs w-full";
  return (
    <div className="flex flex-col gap-1">
      <select
        aria-label={`${column.name} value mode`}
        className={cls}
        disabled={disabled}
        value={value.mode}
        onChange={(e) =>
          onChange({ ...value, mode: e.target.value as FieldValue["mode"] })
        }
      >
        {insert && (
          <option value="default">
            {column.default !== undefined
              ? "Use default"
              : column.nullable
                ? "Omit"
                : "Enter value"}
          </option>
        )}
        <option value="value">Value</option>
        {column.nullable && <option value="null">Null</option>}
      </select>
      {value.mode === "value" &&
        (column.type === "bool" ? (
          <select
            aria-label={column.name}
            disabled={disabled}
            className={cls}
            value={value.text}
            onChange={(e) => onChange({ ...value, text: e.target.value })}
          >
            <option value="">Choose…</option>
            <option value="true">true</option>
            <option value="false">false</option>
          </select>
        ) : (
          <input
            aria-label={column.name}
            disabled={disabled}
            className={cls}
            value={value.text}
            onChange={(e) => onChange({ ...value, text: e.target.value })}
          />
        ))}
    </div>
  );
}
export function RowEditor({
  table,
  row,
  onCancel,
  onSave,
  onDelete,
}: {
  table: TableMeta;
  row: Record<string, unknown>;
  onCancel: () => void;
  onSave: (fields: Record<string, unknown>) => Promise<void> | void;
  onDelete: () => Promise<void> | void;
}) {
  const [fields, setFields] = useState<Record<string, FieldValue>>(() =>
    Object.fromEntries(
      table.columns.map((c) => [c.name, initialField(c, row[c.name])]),
    ),
  );
  const dirty = useRef(new Set<string>()),
    saving = useRef(false);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const run = async (action: () => Promise<void> | void) => {
    if (saving.current) return;
    saving.current = true;
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };
  const submit = () =>
    run(() => {
      const patch: Record<string, unknown> = {};
      for (const c of table.columns) {
        if (dirty.current.has(c.name))
          patch[c.name] = fieldValue(c, fields[c.name]);
      }
      if (!Object.keys(patch).length) {
        onCancel();
        return;
      }
      return onSave(patch);
    });
  return (
    <tr className="border-t border-border bg-accent/5">
      <td>{String(row.id)}</td>
      {table.columns.map((c) => (
        <td key={c.name} className="p-1 align-top">
          <FieldInput
            column={c}
            value={fields[c.name] ?? initialField(c, row[c.name])}
            disabled={busy}
            onChange={(v) => {
              dirty.current.add(c.name);
              setFields((old) => ({ ...old, [c.name]: v }));
            }}
          />
        </td>
      ))}
      <td>{String(row.updated_at ?? "")}</td>
      <td>
        <button disabled={busy} onClick={submit}>
          save
        </button>{" "}
        <button disabled={busy} onClick={onCancel}>
          cancel
        </button>{" "}
        <button disabled={busy} onClick={() => run(onDelete)}>
          delete
        </button>
        {error && <p role="alert">{error}</p>}
      </td>
    </tr>
  );
}

// ─── create-table dialog ────────────────────────────────────────────

function CreateDialog({
  onCancel,
  onSubmit,
}: {
  onCancel: () => void;
  onSubmit: (name: string, cols: ColumnDef[]) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const saving = useRef(false);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [cols, setCols] = useState<ColumnDef[]>([
    { name: "title", type: "text", nullable: false },
  ]);

  const update = (i: number, patch: Partial<ColumnDef>) => {
    const next = [...cols];
    next[i] = { ...next[i], ...patch };
    setCols(next);
  };

  const submit = async () => {
    if (!name) return;
    const cleaned = cols.filter((c) => c.name);
    if (cleaned.length === 0) return;
    if (saving.current) return;
    saving.current = true;
    setBusy(true);
    setError("");
    try {
      await onSubmit(name, cleaned);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };

  return (
    <Dialog
      title="New table"
      onClose={() => {
        if (!busy) onCancel();
      }}
    >
      <div className="bg-bg-card border border-border rounded p-4 w-[28rem] max-w-[90vw] flex flex-col gap-3">
        <h3 className="text-sm font-medium text-text">New table</h3>
        {error && <p role="alert">{error}</p>}
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
                onChange={(e) =>
                  update(i, { type: e.target.value as ColumnType })
                }
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
            onClick={() =>
              setCols([...cols, { name: "", type: "text", nullable: true }])
            }
            className="text-xs text-accent hover:underline self-start"
          >
            + add column
          </button>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <button
            type="button"
            disabled={busy}
            onClick={onCancel}
            className="text-xs px-3 py-1 border border-border rounded hover:bg-bg-input"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={!name || busy}
            className="text-xs px-3 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            Create
          </button>
        </div>
      </div>
    </Dialog>
  );
}

// ─── insert-row dialog ──────────────────────────────────────────────

export function InsertDialog({
  table,
  onCancel,
  onSubmit,
}: {
  table: TableMeta;
  onCancel: () => void;
  onSubmit: (row: Record<string, unknown>) => Promise<void> | void;
}) {
  const [fields, setFields] = useState<Record<string, FieldValue>>(() =>
    Object.fromEntries(
      table.columns.map((c) => [c.name, initialField(c, undefined, true)]),
    ),
  );
  const saving = useRef(false);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const submit = async () => {
    if (saving.current) return;
    saving.current = true;
    setBusy(true);
    setError("");
    try {
      const row: Record<string, unknown> = {};
      for (const c of table.columns) {
        const v = fieldValue(c, fields[c.name]);
        if (v !== undefined) row[c.name] = v;
      }
      await onSubmit(row);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };
  return (
    <Dialog
      title={`Insert into ${table.name}`}
      onClose={() => {
        if (!busy) onCancel();
      }}
    >
      <div className="bg-bg-card border border-border p-4 w-[32rem] max-w-full flex flex-col gap-3">
        <h3>Insert into {table.name}</h3>
        {error && <p role="alert">{error}</p>}
        {table.columns.map((c) => (
          <label key={c.name}>
            {c.name} <small>{c.type}</small>
            <FieldInput
              column={c}
              value={fields[c.name]}
              insert
              disabled={busy}
              onChange={(v) => setFields((old) => ({ ...old, [c.name]: v }))}
            />
          </label>
        ))}
        <button disabled={busy} onClick={onCancel}>
          Cancel
        </button>
        <button disabled={busy} onClick={submit}>
          {busy ? "Saving…" : "Insert"}
        </button>
      </div>
    </Dialog>
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
  tableName,
  api,
  onClose,
}: {
  tableName: string;
  api: <T>(
    method: string,
    path: string,
    params?: Record<string, string>,
    body?: unknown,
  ) => Promise<T>;
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

  const run = async () => {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const resp = await api<QueryResponse>(
        "POST",
        `/tables/${tableName}/query`,
        {},
        { sql },
      );
      setResult(resp);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="border-t border-border bg-bg-card flex flex-col"
      style={{ height: "20rem" }}
    >
      <header className="flex items-center justify-between px-3 py-1.5 border-b border-border">
        <span className="text-xs text-text-dim">
          tables_query — read-only SELECT
        </span>
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
                  truncated at row or byte limit
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
                        <td
                          key={c}
                          className="pr-3 py-0.5 text-text truncate max-w-xs"
                        >
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
  installId,
  onClose,
}: {
  table: TableMeta;
  projectId: string;
  installId: number;
  onClose: () => void;
}) {
  const origin =
    typeof window !== "undefined"
      ? window.location.origin
      : "https://your-host";
  const base = `${origin}/api/apps/tables/_install/${installId}`;
  const sample = sampleRowFor(table);
  const sampleJSON = stringifyJSON(sample, 2);
  const wherePred = whereExampleFor(table);
  const whereJSON = stringifyJSON({ where: [wherePred] }, 2);

  const examples: {
    title: string;
    verb: string;
    description: string;
    curl: string;
  }[] = [
    {
      title: "List rows",
      verb: "GET",
      description: "First 50 rows ordered by id desc.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  "${base}/tables/${table.name}/rows?project_id=${encodeURIComponent(projectId)}&limit=50"`,
    },
    {
      title: "Filtered search",
      verb: "POST",
      description:
        "Typed predicates: eq, neq, lt, lte, gt, gte, contains, in, between, is_null, is_not_null.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X POST "${base}/tables/${table.name}/rows/search?project_id=${encodeURIComponent(projectId)}" \\\n  -d '${whereJSON}'`,
    },
    {
      title: "Get one row",
      verb: "GET",
      description:
        "Pass ?hydrate_files=true to resolve file_id columns to {id, url, expires_at}.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  "${base}/tables/${table.name}/rows/<id>?project_id=${encodeURIComponent(projectId)}"`,
    },
    {
      title: "Insert a row",
      verb: "POST",
      description:
        "Wrap a single object as { row: {...} } or pass { rows: [...] } for atomic batch.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X POST "${base}/tables/${table.name}/rows?project_id=${encodeURIComponent(projectId)}" \\\n  -d '{"row": ${sampleJSON.replace(/\n/g, "\n  ")}}'`,
    },
    {
      title: "Update a row",
      verb: "PATCH",
      description:
        "Body is a partial object — only listed fields are touched. updated_at moves automatically.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X PATCH "${base}/tables/${table.name}/rows/<id>?project_id=${encodeURIComponent(projectId)}" \\\n  -d '${stringifyJSON(sample)}'`,
    },
    {
      title: "Delete a row",
      verb: "DELETE",
      description:
        "Deletes one row. Use the rows_delete MCP tool for a confirmed filtered deletion.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -X DELETE "${base}/tables/${table.name}/rows/<id>?project_id=${encodeURIComponent(projectId)}"`,
    },
    {
      title: "Run a SELECT (escape hatch)",
      verb: "POST",
      description:
        "Read-only. Reference user-tables with {name} placeholders; bind values via params.",
      curl: `curl -H "Authorization: Bearer $APTEVA_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -X POST "${base}/tables/${table.name}/query?project_id=${encodeURIComponent(projectId)}" \\\n  -d '{"sql": "SELECT COUNT(*) AS n FROM {${table.name}}"}'`,
    },
  ];

  const projectHint =
    projectId && projectId !== ""
      ? `# These examples select install ${installId} and project ${projectId}.`
      : "# Add ?project_id=<id> to the URL for globally-scoped installs.";

  return (
    <Dialog title="Table API" onClose={onClose}>
      <div className="bg-bg-card border border-border rounded w-[44rem] max-w-full max-h-full flex flex-col overflow-hidden">
        <header className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div>
            <h3 className="text-sm font-medium text-text">
              Connect to <span className="font-mono">{table.name}</span> from
              outside
            </h3>
            <p className="text-xs text-text-dim mt-0.5">
              Same REST surface the dashboard uses, reachable from any host with
              a valid API key.
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
            <h4 className="text-text-dim uppercase text-[10px] tracking-wide mb-2">
              Auth carriers
            </h4>
            <p className="text-text-muted mb-2">
              Three ways to attach your API key — pick whichever fits the
              client. Keys are issued under your account settings.
            </p>
            <ul className="space-y-1.5 font-mono">
              <li>
                <code className="bg-bg-input px-1.5 py-0.5 rounded">
                  Authorization: Bearer $APTEVA_API_KEY
                </code>{" "}
                <span className="text-text-dim font-sans not-italic">
                  — canonical
                </span>
              </li>
              <li>
                <code className="bg-bg-input px-1.5 py-0.5 rounded">
                  X-API-Key: $APTEVA_API_KEY
                </code>{" "}
                <span className="text-text-dim font-sans">
                  — common alt header
                </span>
              </li>
              <li>
                <code className="bg-bg-input px-1.5 py-0.5 rounded">
                  ?api_key=$APTEVA_API_KEY
                </code>{" "}
                <span className="text-text-dim font-sans">
                  — for SSE/EventSource
                </span>
              </li>
            </ul>
          </section>
          <section>
            <h4 className="text-text-dim uppercase text-[10px] tracking-wide mb-2">
              Base URL
            </h4>
            <CopyBlock text={base} />
            <p className="text-[10px] text-text-dim mt-2 whitespace-pre-line font-mono">
              {projectHint}
            </p>
          </section>
          <section>
            <h4 className="text-text-dim uppercase text-[10px] tracking-wide mb-2">
              Endpoints
            </h4>
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
    </Dialog>
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
    out[c.name] = {
      text: "example",
      number: 42,
      bool: true,
      datetime: "2026-01-01T00:00:00Z",
      json: { example: true },
      file_id: "1",
    }[c.type];
  }
  return out;
}

// whereExampleFor picks the first column whose type makes for a clean
// predicate demo — string with contains, number with gte, bool with
// eq, etc. — and returns a {col, op, value} triple.
function whereExampleFor(table: TableMeta): {
  col: string;
  op: string;
  value: unknown;
} {
  for (const c of table.columns) {
    if (c.type === "text")
      return { col: c.name, op: "contains", value: "search" };
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
    await safeAlter(
      { rename: { from: renaming, to: renameTo.trim() } },
      cancelRename,
    );
  };

  const submitDrop = async (name: string) => {
    if (!confirm(`Drop column "${name}"? Existing values are lost.`)) return;
    await safeAlter({ drop: name });
  };

  return (
    <Dialog
      title="Edit schema"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <div className="bg-bg-card border border-border rounded w-[36rem] max-w-full max-h-full flex flex-col overflow-hidden">
        <header className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div>
            <h3 className="text-sm font-medium text-text">
              Edit <span className="font-mono">{table.name}</span> schema
            </h3>
            <p className="text-xs text-text-dim mt-0.5">
              Add, rename, or drop columns. Reserved columns (
              <span className="font-mono">id</span>,{" "}
              <span className="font-mono">created_at</span>,{" "}
              <span className="font-mono">updated_at</span>) are managed
              automatically.
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
                        <span
                          className="font-mono text-text flex-1 truncate"
                          title={c.name}
                        >
                          {c.name}
                        </span>
                        <span className="text-text-dim text-[10px]">
                          {c.type}
                        </span>
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
              <h4 className="text-text-dim uppercase text-[10px] tracking-wide">
                Add column
              </h4>
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
    </Dialog>
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
  const [error, setError] = useState("");

  const submit = async () => {
    if (!name.trim()) return;
    const col: ColumnDef = { name: name.trim(), type, nullable };
    if (defaultStr.trim() !== "") {
      try {
        col.default = parseInputValue({ name, type, nullable }, defaultStr);
      } catch (e) {
        setError((e as Error).message);
        return;
      }
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
      {error && <p role="alert">{error}</p>}
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
