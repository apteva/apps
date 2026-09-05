// TablesPanel — dashboard surface for the tables app. Talks to the
// tables sidecar via /api/apps/tables/* (the platform proxy injects
// the per-install bearer token). Inherits the dashboard theme via
// Tailwind tokens.
//
// Layout: left rail = list of tables (with row counts), main area =
// selected table's row grid, bottom drawer = SELECT escape hatch.

import { useCallback, useEffect, useRef, useState } from "react";
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
    const bridge = (
      window as unknown as {
        __aptevaAppEvents?: {
          subscribe(
            app: string,
            projectId: string,
            fn: (ev: AppEventEnvelope<T>) => void,
          ): () => void;
        };
      }
    ).__aptevaAppEvents;
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
      es.onopen = () =>
        handlerRef.current({
          topic: "table.refresh",
          app,
          project_id: projectId,
          install_id: 0,
          seq: 0,
          time: "",
          data: {} as T,
        });
      es.onmessage = (e) => {
        try {
          const ev = parseJSON(e.data) as AppEventEnvelope<T>;
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

export default function TablesPanel({
  projectId,
  installId,
}: NativePanelProps) {
  const [selected, setSelected] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get("table"),
  );
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
        tables.push(...result.tables);
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
  const rowKey = selectedTable
    ? `${identity}:${selectedTable.id}:${page}:${cursors[page] ?? ""}:${visibleNames}`
    : "";
  const rowResource = useResource<RowsResponse>(rowKey, epoch, (signal) =>
    api<RowsResponse>(
      "GET",
      `/tables/${selected}/rows`,
      {
        limit: String(PAGE_SIZE),
        include_total: "false",
        select: [
          "id",
          "_revision",
          "updated_at",
          ...visibleColumns.map((c) => c.name),
        ].join(","),
        ...(cursors[page] ? { cursor: cursors[page]! } : {}),
      },
      undefined,
      signal,
    ),
  );
  const rows = rowResource.data?.rows ?? [];
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
    <div className="relative h-full flex min-h-0">
      <aside className="w-56 shrink-0 border-r border-border flex flex-col">
        <header className="p-3 flex justify-between">
          <strong>Tables</strong>
          <button onClick={() => setShowCreate(true)}>+ New</button>
        </header>
        <ul className="overflow-auto flex-1">
          {tables.map((t) => (
            <li key={t.id}>
              <button
                disabled={mutation}
                onClick={() => selectTable(t.name)}
                className={`w-full p-3 text-left ${selected === t.name ? "bg-accent/10" : ""}`}
              >
                <span className="font-mono">{t.name}</span>{" "}
                <small>{t.row_count}</small>
              </button>
            </li>
          ))}
        </ul>
      </aside>
      <main className="flex-1 flex flex-col min-w-0 min-h-0">
        {error && (
          <div role="alert" className="p-3 text-red">
            {error}
          </div>
        )}
        {selectedTable && gridTable ? (
          <>
            <header className="p-3 border-b border-border flex gap-3 flex-wrap items-center">
              <strong>{selectedTable.name}</strong>
              <small>{selectedTable.row_count} rows</small>
              <button disabled={mutation} onClick={() => setShowInsert(true)}>
                + Insert
              </button>
              <button onClick={() => setShowQuery((v) => !v)}>Query</button>
              <button disabled={mutation} onClick={() => setShowSchema(true)}>
                Schema
              </button>
              <button onClick={() => setShowApi(true)}>API</button>
              <button disabled={mutation} onClick={onDropTable}>
                Drop
              </button>
              <label>
                Columns{" "}
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
            </header>
            <div className="flex-1 overflow-auto">
              {rows.length ? (
                <RowsTable
                  table={gridTable}
                  rows={rows}
                  editingRow={activeEdit}
                  onEditStart={editRow}
                  onEditCancel={() => setEditing(null)}
                  onEditSave={onUpdate}
                  onDelete={onDeleteRow}
                />
              ) : (
                <p className="p-8">
                  {rowResource.busy ? "Loading…" : "No rows on this page."}
                </p>
              )}
            </div>
            {activeEdit &&
              !rows.some((r) => String(r.id) === String(activeEdit.id)) && (
                <table className="w-full">
                  <tbody>
                    <RowEditor
                      table={gridTable}
                      row={activeEdit}
                      onCancel={() => setEditing(null)}
                      onSave={(fields) =>
                        onUpdate(String(activeEdit.id), fields)
                      }
                      onDelete={() => onDeleteRow(String(activeEdit.id))}
                    />
                  </tbody>
                </table>
              )}
            <footer className="p-3 border-t border-border flex gap-3">
              <button
                disabled={!page || rowResource.busy}
                onClick={() => setPage((p) => p - 1)}
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
              >
                Next
              </button>
              {rowResource.busy && <span>Refreshing…</span>}
            </footer>
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
  onEditSave: (id: string, fields: Record<string, unknown>) => Promise<void>;
  onDelete: (id: string) => Promise<void>;
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
          const id = String(r.id);
          const editing = editingRow && String(editingRow.id) === id;
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
                <td
                  key={c.name}
                  className="px-2 py-1.5 text-text truncate max-w-xs"
                >
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
  if (c.type === "json") return stringifyJSON(v);
  return String(v);
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
