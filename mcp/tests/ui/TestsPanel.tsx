import { useCallback, useEffect, useRef, useState } from "react";

type Props = { projectId: string; installId: number; appName?: string };
type Suite = { id: number; name: string; description: string; environment: string };
type Assertion = { path: string; op: string; value?: unknown };
type Definition = { function?: string; event?: unknown; url?: string; method?: string; body?: string; headers?: Record<string, string>; timeout_ms: number; assertions: Assertion[] };
type Check = { id?: number; suite_id: number; name: string; kind: "function" | "http"; enabled: boolean; definition: Definition };
type Result = { check_id: number; name: string; kind: string; status: string; duration_ms: number; error: string; output: unknown; assertions: (Assertion & { passed: boolean; actual: unknown; exists: boolean })[] };
type Run = { id: number; suite_id: number; suite_name: string; environment: string; status: string; passed: number; failed: number; error: string; created_at: string; checks?: Check[]; results?: Result[] };
const input = "w-full rounded-lg border border-border bg-surface-2 px-3 py-2 text-text focus:outline-none focus:ring-1 focus:ring-accent";
const button = "rounded-lg border border-border px-3 py-2 text-sm hover:bg-surface-2 disabled:opacity-40 disabled:cursor-not-allowed";
const primary = `${button} bg-accent text-white`;
const pretty = (v: unknown) => JSON.stringify(v, null, 2) ?? "null";
const sample = (kind: "function" | "http"): Definition => kind === "function"
  ? { function: "calculate-price", event: { quantity: 2 }, timeout_ms: 10000, assertions: [{ path: "/response/total", op: "equals", value: 120 }] }
  : { url: "https://example.com/health", method: "GET", timeout_ms: 10000, assertions: [{ path: "/status_code", op: "equals", value: 200 }] };
function Badge({ value }: { value: string }) {
  const color = value === "passed" ? "#16a34a" : value === "failed" || value === "error" ? "#dc2626" : "#64748b";
  return <span style={{ color, background: `${color}15`, border: `1px solid ${color}30` }} className="inline-flex rounded-full px-2 py-0.5 text-xs font-medium">{value}</span>;
}

export default function TestsPanel(props: Props) { return <Panel key={`${props.projectId}:${props.installId}`} {...props} />; }
function Panel({ projectId, installId }: Props) {
  const [suites, setSuites] = useState<Suite[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [checks, setChecks] = useState<Check[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [run, setRun] = useState<Run | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [suiteForm, setSuiteForm] = useState<Partial<Suite> | null>(null);
  const [edit, setEdit] = useState<Check | null>(null);
  const [definition, setDefinition] = useState("");
  const [tab, setTab] = useState<"checks" | "runs">("checks");
  const generation = useRef(0);
  const detailGeneration = useRef(0);
  const suite = suites.find(s => s.id === selected);
  const rpc = useCallback(async (tool: string, args: Record<string, unknown> = {}) => {
    const query = new URLSearchParams({ project_id: projectId, install_id: String(installId) });
    const res = await fetch(`/api/apps/tests/tools/call?${query}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ tool, args }) });
    const text = await res.text();
    let data: any;
    try { data = JSON.parse(text); } catch { throw new Error(`Request failed (${res.status})`); }
    if (!res.ok) throw new Error(data.error || `Request failed (${res.status})`);
    return data;
  }, [projectId, installId]);
  const loadSuites = useCallback(async () => {
    const data = await rpc("tests_suites_list");
    setSuites(data.suites);
    setSelected(old => data.suites.some((s: Suite) => s.id === old) ? old : data.suites[0]?.id ?? null);
  }, [rpc]);
  useEffect(() => { let active = true; loadSuites().catch(e => { if (active) setError(e.message); }).finally(() => { if (active) setLoading(false); }); return () => { active = false; }; }, [loadSuites]);
  const loadDetails = useCallback(async () => {
    if (!selected) return;
    const version = ++generation.current;
    const [c, r] = await Promise.all([rpc("tests_checks_list", { suite_id: selected }), rpc("tests_runs_list", { suite_id: selected })]);
    if (version !== generation.current) return;
    setChecks(c.checks); setRuns(r.runs);
  }, [rpc, selected]);
  useEffect(() => {
    setChecks([]); setRuns([]); setRun(null); setEdit(null); detailGeneration.current++;
    loadDetails().catch(e => setError(e.message));
    return () => { generation.current++; };
  }, [loadDetails]);
  const pending = runs.some(r => r.status === "queued" || r.status === "running");
  useEffect(() => {
    if (!pending) return;
    const timer = setInterval(() => { loadDetails().catch(e => setError(e.message)); }, 2000);
    return () => clearInterval(timer);
  }, [pending, loadDetails]);
  useEffect(() => {
    if (!run || !["queued", "running"].includes(run.status)) return;
    let active = true;
    const timer = setInterval(() => { rpc("tests_run_get", { id: run.id }).then(d => { if (active) setRun(d.run); }).catch(e => { if (active) setError(e.message); }); }, 1500);
    return () => { active = false; clearInterval(timer); };
  }, [rpc, run?.id, run?.status]);
  async function act(fn: () => Promise<void>) { setBusy(true); setError(""); try { await fn(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function showRun(id: number) { const version = ++detailGeneration.current; const d = await rpc("tests_run_get", { id }); if (version === detailGeneration.current) setRun(d.run); }
  function editCheck(c: Check) { setEdit(c); setDefinition(pretty(c.definition)); }
  const last = runs[0];

  return <div className="h-full overflow-auto bg-surface text-text p-5" style={{ minHeight: 500 }}>
    <header className="flex flex-wrap items-center justify-between gap-3 mb-5">
      <div><h1 className="text-xl font-semibold">Tests</h1><p className="text-sm text-text-dim mt-1">Check your functions and endpoints. Keep the evidence from every run.</p></div>
      <button className={primary} disabled={busy} onClick={() => setSuiteForm({ name: "", environment: "test", description: "" })}>+ New suite</button>
    </header>
    {error && <div role="alert" className="mb-4 rounded-lg border p-3 text-sm" style={{ borderColor: "#dc262655", color: "#dc2626" }}>{error}</div>}
    {suiteForm && <form className="mb-5 rounded-xl border border-border p-4 space-y-3" onSubmit={e => { e.preventDefault(); act(async () => { const d = await rpc("tests_suite_save", suiteForm); await loadSuites(); setSelected(d.suite.id); setSuiteForm(null); }); }}>
      <h2 className="font-semibold">{suiteForm.id ? "Edit suite" : "New suite"}</h2>
      <div className="grid gap-3 sm:grid-cols-2"><label className="text-sm">Name<input required maxLength={200} className={input} value={suiteForm.name} onChange={e => setSuiteForm({ ...suiteForm, name: e.target.value })} placeholder="Checkout smoke tests" /></label>
      <label className="text-sm">Environment label<input className={input} maxLength={200} value={suiteForm.environment} onChange={e => setSuiteForm({ ...suiteForm, environment: e.target.value })} placeholder="staging" /></label></div>
      <label className="block text-sm">Description<textarea className={input} maxLength={5000} value={suiteForm.description} onChange={e => setSuiteForm({ ...suiteForm, description: e.target.value })} /></label>
      <p className="text-xs text-text-dim">The label describes the target. Each check defines its own function or URL.</p>
      <div className="flex gap-2"><button className={primary} disabled={busy}>Save suite</button><button type="button" className={button} onClick={() => setSuiteForm(null)}>Cancel</button></div>
    </form>}
    {loading ? <p className="p-8 text-text-dim">Loading tests…</p> : suites.length === 0 ? <div className="rounded-xl border border-dashed border-border p-12 text-center"><h2 className="font-semibold text-lg">Your first check starts here</h2><p className="text-text-dim mt-2 mb-5">Create a suite, add expected results, and run it against your app.</p><button className={primary} onClick={() => setSuiteForm({ name: "", environment: "test", description: "" })}>Create a suite</button></div> : <>
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <label className="flex-1 min-w-0"><span className="sr-only">Suite</span><select className={input} value={selected ?? ""} disabled={busy} onChange={e => setSelected(Number(e.target.value))}>{suites.map(s => <option key={s.id} value={s.id}>{s.name} · {s.environment}</option>)}</select></label>
        <button className={button} disabled={busy || !suite} onClick={() => suite && setSuiteForm(suite)}>Edit suite</button>
        <button className={primary} disabled={busy || !checks.some(c => c.enabled)} onClick={() => act(async () => { const d = await rpc("tests_run", { suite_id: selected, request_key: crypto.randomUUID() }); setRun(d.run); setTab("runs"); await loadDetails(); })}>{busy ? "Working…" : "Run suite"}</button>
      </div>
      {suite?.description && <p className="text-sm text-text-dim mb-4">{suite.description}</p>}
      <div className="grid grid-cols-3 gap-3 mb-5">{[["Enabled checks", checks.filter(c => c.enabled).length], ["Latest result", last?.status ?? "No runs yet"], ["Latest pass count", last ? `${last.passed} / ${last.passed + last.failed}` : "—"]].map(([label, value]) => <div key={label} className="rounded-xl border border-border p-4"><p className="text-xs text-text-dim">{label}</p><p className="font-semibold mt-2">{value}</p></div>)}</div>
      <nav className="flex gap-2 mb-4" aria-label="Tests views"><button className={tab === "checks" ? primary : button} onClick={() => setTab("checks")}>Checks ({checks.length})</button><button className={tab === "runs" ? primary : button} onClick={() => setTab("runs")}>Run history</button><button className={`${button} ml-auto`} disabled={busy} onClick={() => act(loadDetails)}>Refresh</button></nav>
      {tab === "checks" ? <div className="space-y-3">
        <div className="flex justify-between items-center"><p className="text-sm text-text-dim">Assertions run against the returned JSON. Disabled checks are skipped.</p><button className={button} disabled={busy} onClick={() => editCheck({ suite_id: selected!, name: "", kind: "function", enabled: true, definition: sample("function") })}>+ Add check</button></div>
        {!checks.length && <div className="rounded-xl border border-dashed border-border p-8 text-center text-text-dim">No checks yet. Add a function or HTTP check to this suite.</div>}
        {checks.map(c => <div key={c.id} className="rounded-xl border border-border p-4 flex flex-wrap items-center gap-3"><div className="flex-1"><p className="font-medium">{c.name}</p><p className="text-xs text-text-dim mt-1">{c.kind} · {c.definition.assertions.length} assertions · {c.definition.timeout_ms / 1000}s limit</p></div><Badge value={c.enabled ? "enabled" : "disabled"} /><button className={button} disabled={busy} onClick={() => editCheck(c)}>Edit</button><button className={button} disabled={busy} onClick={() => act(async () => { await rpc("tests_check_save", { ...c, enabled: !c.enabled }); await loadDetails(); })}>{c.enabled ? "Disable" : "Enable"}</button><button className={button} disabled={busy} onClick={() => act(async () => { await rpc("tests_check_delete", { id: c.id }); await loadDetails(); })}>Delete</button></div>)}
        {edit && <form className="rounded-xl border border-border p-4 space-y-3" onSubmit={e => { e.preventDefault(); act(async () => { const parsed = JSON.parse(definition); await rpc("tests_check_save", { ...edit, definition: parsed }); setEdit(null); await loadDetails(); }); }}>
          <h2 className="font-semibold">{edit.id ? "Edit check" : "New check"}</h2>
          <label className="block text-sm">Name<input className={input} required maxLength={200} value={edit.name} onChange={e => setEdit({ ...edit, name: e.target.value })} placeholder="Returns the expected total" /></label>
          <label className="block text-sm">Runner<select className={input} value={edit.kind} onChange={e => { const kind = e.target.value as Check["kind"]; setEdit({ ...edit, kind }); setDefinition(pretty(sample(kind))); }}><option value="function">Function</option><option value="http">HTTP</option></select></label>
          <label className="block text-sm">Definition and assertions<textarea spellCheck={false} className={`${input} font-mono text-xs mt-1`} style={{ minHeight: 290 }} required value={definition} onChange={e => setDefinition(e.target.value)} /></label>
          <p className="text-xs text-text-dim">{edit.kind === "function" ? "Invokes the active function version in this project. Assert on /response/field. A timeout here does not stop the function itself." : "Assert on /status_code, /body, or /json/field. Redirects are not followed. Private targets require an operator setting."} Operators: equals, not_equals, exists, contains, lt, lte, gt, gte.</p>
          <div className="flex gap-2"><button className={primary} disabled={busy}>Save check</button><button className={button} type="button" onClick={() => setEdit(null)}>Cancel</button></div>
        </form>}
        <details className="text-sm text-text-dim pt-5"><summary className="cursor-pointer">Suite management</summary><p className="my-3">Archiving hides this suite. Existing queued runs and history remain.</p><button className={button} disabled={busy} onClick={() => act(async () => { await rpc("tests_suite_archive", { id: selected }); await loadSuites(); })}>Archive suite</button></details>
      </div> : <div className="space-y-4">
        {!runs.length && <div className="border border-dashed border-border rounded-xl p-8 text-center text-text-dim">No runs yet. Run the suite to save your first result.</div>}
        {runs.map(r => <button key={r.id} className="w-full rounded-xl border border-border p-4 flex flex-wrap text-left items-center gap-3 hover:bg-surface-2" style={run?.id === r.id ? { borderColor: "var(--color-accent, #6366f1)" } : {}} onClick={() => act(() => showRun(r.id))}><span className="font-medium">Run #{r.id}</span><Badge value={r.status} /><span className="text-sm text-text-dim">{r.passed} passed · {r.failed} failed</span><span className="text-xs text-text-dim ml-auto">{new Date(r.created_at).toLocaleString()}</span></button>)}
        {run && <section className="rounded-xl border border-border p-4 space-y-3"><div className="flex gap-3 items-center"><h2 className="font-semibold">Run #{run.id} · {run.environment}</h2><Badge value={run.status} /></div>
          {run.error && <p role="alert" style={{ color: "#dc2626" }}>{run.error}</p>}
          {["queued", "running"].includes(run.status) && <p className="text-sm text-text-dim">Results update automatically while this run is in progress.</p>}
          {run.results?.map(r => <details key={r.check_id} className="rounded-lg border border-border p-3" open={r.status !== "passed"}><summary className="cursor-pointer flex items-center gap-3"><Badge value={r.status} /><span className="font-medium">{r.name}</span><span className="text-xs text-text-dim ml-auto">{r.duration_ms} ms</span></summary>
            {r.error && <p className="text-sm mt-3" style={{ color: "#dc2626" }}>{r.error}</p>}
            {r.assertions.map((a, i) => <div key={i} className="border-t border-border mt-3 pt-3 text-xs"><Badge value={a.passed ? "passed" : "failed"} /> <code>{a.path || "/ (root)"} {a.op} {a.op === "exists" ? "" : pretty(a.value)}</code><p className="text-text-dim mt-1 break-all">Actual: {a.exists ? pretty(a.actual) : "path is missing"}</p></div>)}
            <p className="text-xs font-medium mt-4 mb-2">Saved output</p><pre className="bg-surface-2 rounded p-3 text-xs overflow-auto" style={{ maxHeight: 280 }}>{pretty(r.output)}</pre>
          </details>)}
          {run.checks && <details className="text-xs text-text-dim"><summary className="cursor-pointer">Definitions used for this run</summary><pre className="mt-3 overflow-auto" style={{ maxHeight: 320 }}>{pretty(run.checks)}</pre></details>}
        </section>}
      </div>}
    </>}
  </div>;
}
