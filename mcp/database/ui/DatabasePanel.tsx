import { useCallback, useEffect, useRef, useState } from "react";

type Database = { id: string; name: string; adapter: string };
type Collection = { name: string; fields: { name: string; type: string; nullable?: boolean }[]; primaryKey: string[]; indexes: unknown[] };
type Props = { projectId: string; installId: number };
type Row = Record<string, unknown>;
const initialSchema = JSON.stringify([{ name: "name", type: "text" }, { name: "amount", type: "number", nullable: true }], null, 2);
const inputClass = "rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground";
const buttonClass = "rounded-md border border-border px-3 py-2 text-sm hover:bg-muted disabled:opacity-40";

export default function DatabasePanel({ projectId, installId }: Props) {
  const [databases, setDatabases] = useState<Database[]>([]);
  const [database, setDatabase] = useState("");
  const [collections, setCollections] = useState<Collection[]>([]);
  const [collection, setCollection] = useState("");
  const [view, setView] = useState("records");
  const [rows, setRows] = useState<Row[]>([]);
  const [cursor, setCursor] = useState("");
  const [result, setResult] = useState<unknown>(null);
  const [editor, setEditor] = useState("{}");
  const [newDB, setNewDB] = useState("");
  const [adapter, setAdapter] = useState("sqlite");
  const [newCollection, setNewCollection] = useState("");
  const [schema, setSchema] = useState(initialSchema);
  const [createOpen, setCreateOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [revision, setRevision] = useState(0);
  const selected = collections.find(c => c.name === collection);
  const generation = useRef(0);

  const api = useCallback(async <T,>(op: string, args: Row = {}): Promise<T> => {
    const query = new URLSearchParams({ project_id: projectId, install_id: String(installId) });
    const response = await fetch(`/api/apps/database/operations/${op}?${query}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(args),
    });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error?.message || `Request failed (${response.status})`);
    return body as T;
  }, [projectId, installId]);

  useEffect(() => {
    let cancelled = false;
    setDatabase(""); setCollection(""); setCollections([]); setRows([]); setError("");
    api<Database[]>("databases_list").then(list => {
      if (!cancelled) { setDatabases(list); setDatabase(list[0]?.name || ""); }
    }).catch(e => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; generation.current++; };
  }, [api]);

  useEffect(() => {
    let cancelled = false;
    setCollections([]); setCollection(""); setRows([]); setCursor(""); setResult(null); generation.current++;
    if (database) api<Collection[]>("collections_list", { database }).then(list => {
      if (!cancelled) { setCollections(list); setCollection(list[0]?.name || ""); }
    }).catch(e => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [api, database]);

  useEffect(() => {
    let cancelled = false;
    generation.current++; setRows([]); setCursor(""); setResult(null);
    if (database && collection) api<{ records: Row[]; nextCursor: string }>("find", { database, collection, limit: 50 }).then(v => {
      if (!cancelled) { setRows(v.records); setCursor(v.nextCursor); }
    }).catch(e => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [api, database, collection, revision]);

  async function action(fn: () => Promise<void>) {
    setBusy(true); setError(""); setNotice("");
    try { await fn(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  function changeView(next: string) {
    setView(next); setResult(null);
    setEditor(next === "aggregate" ? JSON.stringify({ groupBy: [], metrics: [{ name: "records", op: "count" }] }, null, 2)
      : next === "indexes" ? JSON.stringify({ name: "by_name", fields: [{ field: "name", direction: "asc" }], unique: false }, null, 2)
      : next === "insert" ? JSON.stringify([{}], null, 2) : "{}");
  }
  const columns = selected ? [
    ...selected.fields.filter(f => !selected.primaryKey.includes(f.name)).map(f => f.name),
    ...selected.primaryKey,
  ] : [];

  return <div className="flex h-full min-h-[500px] flex-col bg-background text-foreground">
    <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-6 py-4">
      <div><h2 className="text-xl font-semibold">Database</h2><p className="text-sm text-muted-foreground">Local collections, records, indexes and aggregates</p></div>
      <div className="flex items-center gap-2">
        <select aria-label="Database" className={inputClass} value={database} disabled={busy} onChange={e => setDatabase(e.target.value)}>
          {!databases.length && <option value="">No databases yet</option>}
          {databases.map(d => <option key={d.id} value={d.name}>{d.name} · {d.adapter}</option>)}
        </select>
        <button className={buttonClass} disabled={busy} onClick={() => setCreateOpen(!createOpen)}>Create</button>
      </div>
    </header>
    {error && <div role="alert" className="m-4 rounded-md border border-red-500/30 bg-red-500/10 p-3 text-sm">{error}</div>}
    {notice && <div role="status" className="mx-4 mt-3 text-sm text-muted-foreground">{notice}</div>}
    {createOpen && <div className="grid gap-5 border-b border-border bg-muted/20 p-5 md:grid-cols-2">
      <form className="space-y-3" onSubmit={e => { e.preventDefault(); void action(async () => {
        const d = await api<Database>("database_create", { database: newDB, adapter });
        setDatabases(await api<Database[]>("databases_list")); setDatabase(d.name); setNewDB(""); setNotice(`Database ${d.name} is ready.`);
      }); }}>
        <h3 className="font-medium">New database</h3>
        <input required pattern="[a-z][a-z0-9_]{0,63}" aria-label="New database name" placeholder="Database name" className={inputClass} value={newDB} onChange={e => setNewDB(e.target.value)} />
        <select aria-label="Adapter" className={inputClass} value={adapter} onChange={e => setAdapter(e.target.value)}><option value="sqlite">SQLite</option><option value="pebble">Pebble</option></select>
        <button className={buttonClass} disabled={busy}>Create database</button>
      </form>
      <form className="space-y-3" onSubmit={e => { e.preventDefault(); void action(async () => {
        await api("collection_create", { database, collection: newCollection, fields: JSON.parse(schema) });
        setCollections(await api<Collection[]>("collections_list", { database })); setCollection(newCollection); setNewCollection(""); setCreateOpen(false);
      }); }}>
        <h3 className="font-medium">New collection {database && `in ${database}`}</h3>
        <input required pattern="[a-z][a-z0-9_]{0,63}" aria-label="Collection name" placeholder="Collection name" className={inputClass} value={newCollection} onChange={e => setNewCollection(e.target.value)} />
        <label className="block text-sm text-muted-foreground">Fields<textarea aria-label="Collection fields" className={`${inputClass} mt-1 block h-36 w-full font-mono`} value={schema} onChange={e => setSchema(e.target.value)} /></label>
        <button className={buttonClass} disabled={busy || !database}>Create collection</button>
      </form>
    </div>}
    <div className="flex min-h-0 flex-1">
      <aside className="w-48 shrink-0 border-r border-border p-3">
        <p className="px-2 py-2 text-xs uppercase tracking-wide text-muted-foreground">Collections · {collections.length}</p>
        {collections.map(c => <button key={c.name} disabled={busy} className={`mb-1 block w-full rounded-md px-3 py-2 text-left text-sm ${collection === c.name ? "bg-muted font-medium" : "hover:bg-muted/50"}`} onClick={() => setCollection(c.name)}>{c.name}</button>)}
        {!collections.length && <p className="p-2 text-sm text-muted-foreground">Create a collection to store records.</p>}
      </aside>
      <main className="min-w-0 flex-1 overflow-auto p-5">
        {!collection ? <div className="py-16 text-center text-muted-foreground">{database ? "Your database is ready. Add its first collection." : "Create a local database to get started."}</div> : <>
          <div className="mb-4 flex flex-wrap items-center gap-2">
            <h3 className="mr-auto text-lg font-semibold">{collection}</h3>
            {["records", "query", "aggregate", "indexes", "schema", "insert"].map(v => <button key={v} disabled={busy} className={`${buttonClass} ${view === v ? "bg-muted" : ""}`} onClick={() => changeView(v)}>{v[0].toUpperCase() + v.slice(1)}</button>)}
          </div>
          {view === "records" && <>
            <div className="overflow-x-auto rounded-lg border border-border"><table className="w-full text-left text-sm"><thead className="bg-muted/50"><tr>{columns.map(c => <th key={c} className="whitespace-nowrap p-3 font-medium">{c}</th>)}</tr></thead><tbody>{rows.map((r, i) => <tr key={i} className="border-t border-border">{columns.map(c => <td key={c} className="max-w-xs truncate p-3" title={JSON.stringify(r[c])}>{r[c] === null ? <span className="text-muted-foreground">null</span> : typeof r[c] === "object" ? JSON.stringify(r[c]) : String(r[c] ?? "")}</td>)}</tr>)}</tbody></table>{!rows.length && <p className="p-8 text-center text-muted-foreground">No records yet.</p>}</div>
            <div className="mt-3 flex gap-2"><button className={buttonClass} disabled={busy} onClick={() => setRevision(v => v + 1)}>Refresh</button><button className={buttonClass} disabled={busy || !cursor} onClick={() => void action(async () => {
              const version = generation.current;
              const v = await api<{ records: Row[]; nextCursor: string }>("find", { database, collection, limit: 50, cursor });
              if (version === generation.current) { setRows(v.records); setCursor(v.nextCursor); }
            })}>Next page</button></div>
          </>}
          {view === "schema" && <pre className="overflow-auto rounded-lg bg-muted/30 p-4 text-xs">{JSON.stringify(selected, null, 2)}</pre>}
          {["query", "aggregate", "indexes", "insert"].includes(view) && <>
            {view === "indexes" && <pre className="mb-4 rounded-lg bg-muted/30 p-3 text-xs">{JSON.stringify(selected?.indexes, null, 2)}</pre>}
            <label className="block text-sm text-muted-foreground">{view === "insert" ? "Records (JSON array)" : view === "indexes" ? "New index definition" : "Query (JSON)"}
              <textarea aria-label={`${view} JSON`} className={`${inputClass} mt-2 h-56 w-full font-mono`} value={editor} onChange={e => setEditor(e.target.value)} spellCheck={false} />
            </label>
            <div className="mt-3 flex gap-2"><button className={buttonClass} disabled={busy} onClick={() => void action(async () => {
              const body = JSON.parse(editor);
              const op = view === "query" ? "find" : view === "indexes" ? "index_create" : view;
              const args = view === "insert" ? { records: body } : view === "indexes" ? { index: body } : body;
              setResult(await api(op, { ...args, database, collection }));
              if (view === "indexes") setCollections(await api<Collection[]>("collections_list", { database }));
              if (view === "insert") setNotice("Records inserted. Open Records and refresh to view them.");
            })}>{busy ? "Working…" : view === "indexes" ? "Create index" : view === "insert" ? "Insert records" : "Run"}</button>
              {view === "query" && <button className={buttonClass} disabled={busy} onClick={() => void action(async () => { const { where, orderBy } = JSON.parse(editor); setResult(await api("explain", { database, collection, where, orderBy })); })}>Explain</button>}
            </div>
            {result !== null && <pre className="mt-4 max-h-96 overflow-auto rounded-lg bg-muted/30 p-4 text-xs">{JSON.stringify(result, null, 2)}</pre>}
          </>}
        </>}
      </main>
    </div>
  </div>;
}
