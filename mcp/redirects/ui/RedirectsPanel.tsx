import { useCallback, useEffect, useRef, useState } from "react";

interface AppEventEnvelope<T = unknown> {
  topic: string;
  seq: number;
  data: T;
}

function useAppEvents<T>(app: string, projectId: string, onEvent: (event: AppEventEnvelope<T>) => void) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const bridge = (window as unknown as {
      __aptevaAppEvents?: { subscribe(app: string, projectId: string, fn: (event: AppEventEnvelope<T>) => void): () => void };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, (event) => handlerRef.current(event));

    let lastSeq = 0;
    const source = new EventSource(
      `/api/app-events/${encodeURIComponent(app)}?project_id=${encodeURIComponent(projectId)}`,
      { withCredentials: true },
    );
    source.onmessage = (message) => {
      try {
        const event = JSON.parse(message.data) as AppEventEnvelope<T>;
        if (event.seq <= lastSeq) return;
        lastSeq = event.seq;
        handlerRef.current(event);
      } catch {}
    };
    return () => source.close();
  }, [app, projectId]);
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Redirect {
  id: number;
  hostname: string;
  path: string;
  match_mode: "exact" | "prefix";
  destination: string;
  status_code: 301 | 302 | 307 | 308;
  preserve_path: boolean;
  preserve_query: boolean;
  notes?: string;
  hits: number;
  last_hit_at?: string;
  created_at?: string;
  updated_at?: string;
}

interface Meta {
  domains_available: boolean;
  domains: string[];
  public_host: string;
  ingress: Record<string, string>;
}

interface RuleDraft {
  hostname: string;
  path: string;
  destination: string;
  match_mode: "exact" | "prefix";
  status_code: 301 | 302 | 307 | 308;
  preserve_path: boolean;
  preserve_query: boolean;
  notes: string;
}

interface TestResult {
  matched: boolean;
  location?: string;
  status_code?: number;
}

const API = "/api/apps/redirects/api";
const PAGE_SIZE = 50;
const inputClass = "w-full rounded border border-border bg-bg-input px-2.5 py-1.5 text-xs text-text placeholder:text-text-dim focus:outline-none focus:ring-1 focus:ring-accent";

function emptyDraft(): RuleDraft {
  return { hostname: "", path: "/", destination: "", match_mode: "exact", status_code: 302, preserve_path: false, preserve_query: true, notes: "" };
}

function draftFromRule(rule: Redirect): RuleDraft {
  return { hostname: rule.hostname, path: rule.path, destination: rule.destination, match_mode: rule.match_mode, status_code: rule.status_code, preserve_path: rule.preserve_path, preserve_query: rule.preserve_query, notes: rule.notes || "" };
}

function validateDraft(draft: RuleDraft): string {
  const host = draft.hostname.trim().toLowerCase().replace(/\.+$/, "");
  if (!/^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(host)) {
    return "Enter a fully-qualified hostname such as go.example.com.";
  }
  if (/[?#\r\n]/.test(draft.path)) return "Path cannot contain a query string or fragment.";
  const destination = draft.destination.trim();
  if (!destination) return "Destination URL is required.";
  try {
    const parsed = new URL(destination);
    if (!["http:", "https:", "mailto:", "tel:"].includes(parsed.protocol)) return "Destination must use http, https, mailto, or tel.";
  } catch {
    return "Enter a valid absolute destination URL.";
  }
  if (draft.preserve_path && draft.match_mode !== "prefix") return "Preserve path requires prefix matching.";
  return "";
}

export default function RedirectsPanel({ projectId, installId }: NativePanelProps) {
  const [rules, setRules] = useState<Redirect[] | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [filterDraft, setFilterDraft] = useState("");
  const [filter, setFilter] = useState("");
  const [meta, setMeta] = useState<Meta | null>(null);
  const [error, setError] = useState("");
  const [warning, setWarning] = useState("");
  const [loading, setLoading] = useState(false);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [editing, setEditing] = useState<Redirect | null>(null);
  const [testing, setTesting] = useState<Redirect | null>(null);
  const [deleting, setDeleting] = useState<Redirect | null>(null);
  const [rowBusy, setRowBusy] = useState<number | null>(null);
  const [copied, setCopied] = useState<number | null>(null);
	const loadSequence = useRef(0);

  const params = useCallback((extra?: Record<string, string>) => new URLSearchParams({
    project_id: projectId,
    install_id: String(installId),
    ...extra,
  }).toString(), [projectId, installId]);

  const load = useCallback(async () => {
		const sequence = ++loadSequence.current;
    setLoading(true);
    try {
      const query = params({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE), ...(filter ? { hostname: filter } : {}) });
      const response = await fetch(`${API}/redirects?${query}`, { credentials: "same-origin" });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      const payload = await response.json() as { redirects?: Redirect[]; total?: number };
		if (sequence !== loadSequence.current) return;
      setRules(payload.redirects || []);
      setTotal(payload.total || 0);
      setError("");
    } catch (cause) {
		if (sequence !== loadSequence.current) return;
      setError("Could not load redirects: " + (cause as Error).message);
    } finally {
			if (sequence === loadSequence.current) setLoading(false);
    }
  }, [params, page, filter]);

  const loadMeta = useCallback(async () => {
    try {
      const response = await fetch(`${API}/_meta?${params()}`, { credentials: "same-origin" });
      if (!response.ok) throw new Error();
      setMeta(await response.json() as Meta);
    } catch {
      setMeta({ domains_available: false, domains: [], public_host: "", ingress: {} });
    }
  }, [params]);

  useEffect(() => { load(); }, [load]);
  useEffect(() => { loadMeta(); }, [loadMeta]);

  useAppEvents<{ redirect?: Redirect; id?: number; at?: string }>("redirects", projectId, (event) => {
    if (event.topic === "rule.hit") {
      setRules((current) => current?.map((rule) => rule.id === event.data?.id
        ? { ...rule, hits: rule.hits + 1, last_hit_at: event.data.at || rule.last_hit_at }
        : rule) || current);
      return;
    }
    if (["rule.created", "rule.updated", "rule.removed"].includes(event.topic)) {
      load();
      loadMeta();
    }
  });

  const copyLink = async (rule: Redirect) => {
		try {
			await navigator.clipboard.writeText(`https://${rule.hostname}${rule.path === "/" ? "" : rule.path}`);
			setCopied(rule.id);
			window.setTimeout(() => setCopied((id) => id === rule.id ? null : id), 1500);
		} catch (cause) {
			setError("Could not copy link: " + (cause as Error).message);
		}
  };

  const remove = async () => {
    if (!deleting) return;
    setRowBusy(deleting.id);
    setError("");
    try {
      const response = await fetch(`${API}/redirects/${deleting.id}?${params()}`, { method: "DELETE", credentials: "same-origin" });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      setDeleting(null);
      if (rules?.length === 1 && page > 0) setPage((value) => value - 1); else await load();
      await loadMeta();
    } catch (cause) {
      setError("Delete failed: " + (cause as Error).message);
    } finally {
      setRowBusy(null);
    }
  };

  const toggleDetails = (id: number) => setExpanded((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });

  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <div className="h-full min-h-0 flex flex-col text-text">
      <header className="border-b border-border px-4 py-4 md:px-6">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h1 className="text-lg font-semibold">Redirects</h1>
            <p className="mt-1 text-xs text-text-muted">Branded links and domain migrations with managed ingress.</p>
          </div>
          <div className="flex items-center gap-2">
            {meta?.public_host && <span className="hidden lg:inline text-[11px] text-text-dim">DNS target: <code>{meta.public_host}</code></span>}
            <button type="button" onClick={() => { load(); loadMeta(); }} disabled={loading} className="rounded border border-border px-2.5 py-1.5 text-xs hover:bg-bg-input disabled:opacity-50">
              {loading ? "Refreshing…" : "Refresh"}
            </button>
          </div>
        </div>
      </header>

      {error && <Banner tone="error" onClose={() => setError("")}>{error}</Banner>}
      {warning && <Banner tone="warning" onClose={() => setWarning("")}>Wiring warning: {warning}</Banner>}

      <AddForm meta={meta} params={params} onAdded={async (nextWarning) => { setWarning(nextWarning); setPage(0); await load(); await loadMeta(); }} setError={setError} />

      <div className="flex flex-wrap items-end gap-2 border-y border-border bg-bg-input/20 px-4 py-2 md:px-6">
        <label className="min-w-52 flex-1 max-w-sm">
          <span className="sr-only">Filter by exact hostname</span>
          <input className={inputClass} value={filterDraft} onChange={(event) => setFilterDraft(event.target.value)} placeholder="Filter exact hostname…" onKeyDown={(event) => {
            if (event.key === "Enter") { setPage(0); setFilter(filterDraft.trim().toLowerCase()); }
          }} />
        </label>
        <button type="button" className="rounded border border-border px-2.5 py-1.5 text-xs hover:bg-bg-input" onClick={() => { setPage(0); setFilter(filterDraft.trim().toLowerCase()); }}>Filter</button>
        {filter && <button type="button" className="px-2 py-1.5 text-xs text-text-muted hover:text-text" onClick={() => { setFilterDraft(""); setFilter(""); setPage(0); }}>Clear</button>}
        <span className="ml-auto text-xs text-text-dim">{total.toLocaleString()} rule{total === 1 ? "" : "s"}</span>
      </div>

      <main className="flex-1 min-h-0 overflow-auto">
        {rules === null ? (
          <div className="p-8 text-sm text-text-muted">Loading redirects…</div>
        ) : rules.length === 0 ? (
          <EmptyState filtered={!!filter} />
        ) : (
          <>
            <div className="hidden md:block min-w-[58rem]">
              <table className="w-full text-xs">
                <thead className="sticky top-0 z-10 bg-bg-input text-text-muted">
                  <tr><th className="px-4 py-2 text-left font-normal">Source</th><th className="px-3 py-2 text-left font-normal">Destination</th><th className="px-3 py-2 text-left font-normal">Status</th><th className="px-3 py-2 text-left font-normal">Health</th><th className="px-3 py-2 text-right font-normal">Hits</th><th className="px-4 py-2 text-right font-normal">Actions</th></tr>
                </thead>
                <tbody>{rules.map((rule) => <RuleRows key={rule.id} rule={rule} meta={meta} expanded={expanded.has(rule.id)} busy={rowBusy === rule.id} copied={copied === rule.id} onToggle={() => toggleDetails(rule.id)} onCopy={() => copyLink(rule)} onEdit={() => setEditing(rule)} onTest={() => setTesting(rule)} onDelete={() => setDeleting(rule)} />)}</tbody>
              </table>
            </div>
            <div className="md:hidden divide-y divide-border">
              {rules.map((rule) => <RuleCard key={rule.id} rule={rule} meta={meta} expanded={expanded.has(rule.id)} busy={rowBusy === rule.id} copied={copied === rule.id} onToggle={() => toggleDetails(rule.id)} onCopy={() => copyLink(rule)} onEdit={() => setEditing(rule)} onTest={() => setTesting(rule)} onDelete={() => setDeleting(rule)} />)}
            </div>
          </>
        )}
      </main>

      {total > PAGE_SIZE && <footer className="flex items-center justify-between border-t border-border px-4 py-2 text-xs md:px-6">
        <span className="text-text-dim">Page {page + 1} of {pages}</span>
        <div className="flex gap-2"><button type="button" disabled={page === 0 || loading} onClick={() => setPage((value) => Math.max(0, value - 1))} className="rounded border border-border px-2 py-1 disabled:opacity-40">Previous</button><button type="button" disabled={page + 1 >= pages || loading} onClick={() => setPage((value) => value + 1)} className="rounded border border-border px-2 py-1 disabled:opacity-40">Next</button></div>
      </footer>}

      {editing && <EditDialog rule={editing} params={params} onClose={() => setEditing(null)} onSaved={async (nextWarning) => { setEditing(null); setWarning(nextWarning); await load(); await loadMeta(); }} />}
      {testing && <TestDialog rule={testing} params={params} onClose={() => setTesting(null)} />}
      {deleting && <ConfirmDialog title="Delete redirect" confirmLabel={rowBusy === deleting.id ? "Deleting…" : "Delete redirect"} busy={rowBusy === deleting.id} onCancel={() => setDeleting(null)} onConfirm={remove}>
        Delete <code className="text-text">{deleting.hostname}{deleting.path}</code>? Public requests will stop matching immediately. DNS records are left untouched.
      </ConfirmDialog>}
    </div>
  );
}

function AddForm({ meta, params, onAdded, setError }: { meta: Meta | null; params: (extra?: Record<string, string>) => string; onAdded: (warning: string) => void; setError: (message: string) => void }) {
  const [draft, setDraft] = useState<RuleDraft>(emptyDraft());
  const [subdomain, setSubdomain] = useState("");
  const [apex, setApex] = useState("");
  const [custom, setCustom] = useState(false);
  const [open, setOpen] = useState(true);
  const [advanced, setAdvanced] = useState(false);
  const [busy, setBusy] = useState(false);
  const [validation, setValidation] = useState("");

  useEffect(() => { if (!apex && meta?.domains[0]) setApex(meta.domains[0]); }, [meta, apex]);
  const picker = !custom && !!meta?.domains_available && meta.domains.length > 0;
  const hostname = picker ? (subdomain.trim() ? `${subdomain.trim()}.${apex}` : apex) : draft.hostname;
  const current = { ...draft, hostname };

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    const issue = validateDraft(current);
    setValidation(issue);
    if (issue) return;
    setBusy(true);
    setError("");
    try {
      const response = await fetch(`${API}/redirects?${params()}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(current) });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      const payload = await response.json() as { warning?: string };
      setDraft(emptyDraft()); setSubdomain(""); setValidation("");
      onAdded(payload.warning || "");
    } catch (cause) {
      setError("Add failed: " + (cause as Error).message);
    } finally { setBusy(false); }
  };

  return <section className="px-4 py-3 md:px-6">
    <button type="button" onClick={() => setOpen((value) => !value)} className="text-xs font-medium text-accent hover:underline">{open ? "Hide new redirect form" : "+ New redirect"}</button>
    {open && <form onSubmit={submit} className="mt-3 space-y-3">
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-12">
        {picker ? <><Field label="Subdomain" className="xl:col-span-2"><input className={inputClass} value={subdomain} onChange={(event) => setSubdomain(event.target.value.toLowerCase())} placeholder="go (blank for apex)" autoCapitalize="none" spellCheck={false} /></Field><Field label="Domain" className="xl:col-span-3"><select className={inputClass} value={apex} onChange={(event) => setApex(event.target.value)}>{meta!.domains.map((domain) => <option key={domain}>{domain}</option>)}</select></Field></> : <Field label="Hostname" className="xl:col-span-5"><input className={inputClass} value={draft.hostname} onChange={(event) => setDraft({ ...draft, hostname: event.target.value.toLowerCase() })} placeholder="go.example.com" autoCapitalize="none" spellCheck={false} /></Field>}
        <Field label="Path" className="xl:col-span-2"><input className={inputClass} value={draft.path} onChange={(event) => setDraft({ ...draft, path: event.target.value })} placeholder="/" /></Field>
        <Field label="Destination URL" className="xl:col-span-5"><input type="url" className={inputClass} value={draft.destination} onChange={(event) => setDraft({ ...draft, destination: event.target.value })} placeholder="https://example.com/landing" /></Field>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        {meta?.domains_available && <button type="button" onClick={() => setCustom((value) => !value)} className="text-[11px] text-text-muted hover:text-text">{custom ? "Pick from Domains" : "Use custom hostname"}</button>}
        <button type="button" onClick={() => setAdvanced((value) => !value)} className="text-[11px] text-text-muted hover:text-text">{advanced ? "Hide" : "Show"} advanced options</button>
        <span className="ml-auto text-[11px] text-text-dim">Source: <code>{hostname || "hostname"}{draft.path === "/" ? "" : draft.path}</code></span>
      </div>
      {advanced && <AdvancedFields draft={draft} setDraft={setDraft} />}
      {validation && <p className="text-xs text-red">{validation}</p>}
      <div><button type="submit" disabled={busy} className="rounded bg-accent px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50">{busy ? "Adding…" : "Add redirect"}</button></div>
    </form>}
  </section>;
}

function AdvancedFields({ draft, setDraft }: { draft: RuleDraft; setDraft: (draft: RuleDraft) => void }) {
  return <div className="grid grid-cols-1 gap-3 rounded border border-border bg-bg-input/20 p-3 sm:grid-cols-2 lg:grid-cols-5">
    <Field label="Match"><select className={inputClass} value={draft.match_mode} onChange={(event) => { const mode = event.target.value as "exact" | "prefix"; setDraft({ ...draft, match_mode: mode, preserve_path: mode === "prefix" ? draft.preserve_path : false }); }}><option value="exact">Exact path</option><option value="prefix">Path prefix</option></select></Field>
    <Field label="HTTP status"><select className={inputClass} value={draft.status_code} onChange={(event) => setDraft({ ...draft, status_code: Number(event.target.value) as RuleDraft["status_code"] })}><option value={301}>301 permanent</option><option value={302}>302 temporary</option><option value={307}>307 preserve method</option><option value={308}>308 permanent + method</option></select></Field>
    <label className="flex items-center gap-2 self-end pb-1.5 text-xs text-text-muted"><input type="checkbox" checked={draft.preserve_path} disabled={draft.match_mode !== "prefix"} onChange={(event) => setDraft({ ...draft, preserve_path: event.target.checked })} /> Preserve remaining path</label>
    <label className="flex items-center gap-2 self-end pb-1.5 text-xs text-text-muted"><input type="checkbox" checked={draft.preserve_query} onChange={(event) => setDraft({ ...draft, preserve_query: event.target.checked })} /> Preserve query</label>
    <Field label="Notes"><input className={inputClass} value={draft.notes} onChange={(event) => setDraft({ ...draft, notes: event.target.value })} placeholder="Campaign or migration note" /></Field>
  </div>;
}

function RuleRows(props: RuleViewProps) {
  const { rule, meta, expanded } = props;
  return <><tr className="border-t border-border hover:bg-bg-input/20">
    <td className="px-4 py-2"><div className="font-mono text-accent">{rule.hostname}</div><div className="mt-0.5 font-mono text-text-muted">{rule.path} {rule.match_mode === "prefix" && <span className="text-[10px]">(prefix)</span>}</div></td>
    <td className="max-w-[28rem] truncate px-3 py-2 font-mono text-text-muted" title={rule.destination}>{rule.destination}</td>
    <td className="px-3 py-2"><StatusBadge code={rule.status_code} /></td>
    <td className="px-3 py-2"><Health rule={rule} meta={meta} /></td>
    <td className="px-3 py-2 text-right tabular-nums text-text-muted">{rule.hits.toLocaleString()}</td>
    <td className="px-4 py-2"><RuleActions {...props} /></td>
  </tr>{expanded && <tr className="border-t border-border bg-bg-input/10"><td colSpan={6} className="px-4 py-3"><RuleDetails rule={rule} meta={meta} /></td></tr>}</>;
}

function RuleCard(props: RuleViewProps) {
  const { rule, meta, expanded } = props;
  return <article className="p-4"><div className="flex items-start justify-between gap-3"><div className="min-w-0"><div className="truncate font-mono text-sm text-accent">{rule.hostname}{rule.path === "/" ? "" : rule.path}</div><div className="mt-1 truncate font-mono text-xs text-text-muted">→ {rule.destination}</div></div><StatusBadge code={rule.status_code} /></div><div className="mt-3 flex items-center justify-between"><Health rule={rule} meta={meta} /><span className="text-xs text-text-dim">{rule.hits.toLocaleString()} hits</span></div><div className="mt-3"><RuleActions {...props} /></div>{expanded && <div className="mt-3 border-t border-border pt-3"><RuleDetails rule={rule} meta={meta} /></div>}</article>;
}

interface RuleViewProps {
  rule: Redirect; meta: Meta | null; expanded: boolean; busy: boolean; copied: boolean;
  onToggle: () => void; onCopy: () => void; onEdit: () => void; onTest: () => void; onDelete: () => void;
}

function RuleActions({ expanded, busy, copied, onToggle, onCopy, onEdit, onTest, onDelete }: RuleViewProps) {
  return <div className="flex flex-wrap justify-end gap-2 text-[11px]"><button type="button" onClick={onToggle} className="text-text-muted hover:text-text">{expanded ? "Hide" : "Details"}</button><button type="button" onClick={onCopy} className="text-text-muted hover:text-text">{copied ? "Copied" : "Copy"}</button><button type="button" onClick={onTest} className="text-accent hover:underline">Test</button><button type="button" onClick={onEdit} className="text-text-muted hover:text-text">Edit</button><button type="button" disabled={busy} onClick={onDelete} className="text-red/80 hover:text-red disabled:opacity-50">Delete</button></div>;
}

function RuleDetails({ rule, meta }: { rule: Redirect; meta: Meta | null }) {
  const managed = isManaged(rule.hostname, meta?.domains || []);
  return <dl className="grid grid-cols-2 gap-x-5 gap-y-2 text-xs md:grid-cols-4 xl:grid-cols-6"><Detail label="Match" value={rule.match_mode} /><Detail label="Preserve path" value={rule.preserve_path ? "Yes" : "No"} /><Detail label="Preserve query" value={rule.preserve_query ? "Yes" : "No"} /><Detail label="Ingress" value={meta?.ingress?.[rule.hostname] || "missing"} /><Detail label="DNS" value={managed ? `managed → ${meta?.public_host || "unknown target"}` : "external"} /><Detail label="Last hit" value={rule.last_hit_at ? new Date(rule.last_hit_at).toLocaleString() : "Never"} />{rule.notes && <div className="col-span-2 md:col-span-4 xl:col-span-6"><dt className="text-text-dim">Notes</dt><dd className="mt-0.5 text-text-muted">{rule.notes}</dd></div>}</dl>;
}

function Detail({ label, value }: { label: string; value: string }) { return <div><dt className="text-text-dim">{label}</dt><dd className="mt-0.5 break-all text-text-muted">{value}</dd></div>; }

function Health({ rule, meta }: { rule: Redirect; meta: Meta | null }) {
  const status = meta?.ingress?.[rule.hostname];
  return <span className={`inline-flex items-center gap-1 text-[11px] ${status === "active" ? "text-green" : "text-amber"}`}><span className="h-1.5 w-1.5 rounded-full bg-current" />{status || "ingress missing"}</span>;
}

function isManaged(hostname: string, domains: string[]) { return domains.some((domain) => hostname === domain || hostname.endsWith(`.${domain}`)); }

function StatusBadge({ code }: { code: number }) { const permanent = code === 301 || code === 308; return <span className={`rounded border px-1.5 py-0.5 text-[10px] ${permanent ? "border-accent/40 text-accent" : "border-border text-text-muted"}`} title={permanent ? "Permanent redirect" : "Temporary redirect"}>{code}</span>; }

function EditDialog({ rule, params, onClose, onSaved }: { rule: Redirect; params: () => string; onClose: () => void; onSaved: (warning: string) => void }) {
  const [draft, setDraft] = useState<RuleDraft>(draftFromRule(rule));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const save = async () => {
    const issue = validateDraft(draft); setError(issue); if (issue) return;
    setBusy(true);
    try {
      const response = await fetch(`${API}/redirects/${rule.id}?${params()}`, { method: "PUT", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(draft) });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      const payload = await response.json() as { warning?: string };
      onSaved(payload.warning || "");
    } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); }
  };
  return <Modal title="Edit redirect" onClose={onClose} busy={busy}><div className="grid grid-cols-1 gap-3 sm:grid-cols-2"><Field label="Hostname"><input className={inputClass} value={draft.hostname} onChange={(event) => setDraft({ ...draft, hostname: event.target.value.toLowerCase() })} /></Field><Field label="Path"><input className={inputClass} value={draft.path} onChange={(event) => setDraft({ ...draft, path: event.target.value })} /></Field><Field label="Destination" className="sm:col-span-2"><input type="url" className={inputClass} value={draft.destination} onChange={(event) => setDraft({ ...draft, destination: event.target.value })} /></Field></div><div className="mt-3"><AdvancedFields draft={draft} setDraft={setDraft} /></div>{error && <p className="mt-3 text-xs text-red">{error}</p>}<div className="mt-5 flex justify-end gap-2"><button type="button" disabled={busy} onClick={onClose} className="rounded border border-border px-3 py-1.5 text-xs">Cancel</button><button type="button" disabled={busy} onClick={save} className="rounded bg-accent px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50">{busy ? "Saving…" : "Save changes"}</button></div></Modal>;
}

function TestDialog({ rule, params, onClose }: { rule: Redirect; params: () => string; onClose: () => void }) {
  const [path, setPath] = useState(rule.path);
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<TestResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const run = async () => {
    setBusy(true); setError(""); setResult(null);
    try {
      const response = await fetch(`${API}/redirects/test?${params()}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ hostname: rule.hostname, path, query: query.replace(/^\?/, "") }) });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      setResult(await response.json() as TestResult);
    } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); }
  };
  return <Modal title="Test redirect safely" onClose={onClose} busy={busy}><p className="mb-3 text-xs text-text-muted">Computes the response without navigating, incrementing hits, or caching a permanent redirect.</p><div className="grid grid-cols-1 gap-3 sm:grid-cols-2"><Field label="Path"><input className={inputClass} value={path} onChange={(event) => setPath(event.target.value)} /></Field><Field label="Query string"><input className={inputClass} value={query} onChange={(event) => setQuery(event.target.value)} placeholder="utm_source=email" /></Field></div>{result && <div className={`mt-4 rounded border p-3 text-xs ${result.matched ? "border-green/30 bg-green/5" : "border-amber/30 bg-amber/5"}`}>{result.matched ? <><div className="font-medium text-green">Matched · HTTP {result.status_code}</div><div className="mt-2 break-all font-mono text-text-muted">{result.location}</div></> : <div className="text-amber">No rule matched this request.</div>}</div>}{error && <p className="mt-3 text-xs text-red">{error}</p>}<div className="mt-5 flex justify-end gap-2"><button type="button" onClick={onClose} className="rounded border border-border px-3 py-1.5 text-xs">Close</button><button type="button" disabled={busy} onClick={run} className="rounded bg-accent px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50">{busy ? "Testing…" : "Run test"}</button></div></Modal>;
}

function Modal({ title, onClose, busy = false, children }: { title: string; onClose: () => void; busy?: boolean; children: React.ReactNode }) {
  useEffect(() => { const handler = (event: KeyboardEvent) => { if (event.key === "Escape" && !busy) onClose(); }; window.addEventListener("keydown", handler); return () => window.removeEventListener("keydown", handler); }, [busy, onClose]);
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" role="presentation" onMouseDown={() => !busy && onClose()}><div role="dialog" aria-modal="true" aria-labelledby="redirect-modal-title" className="max-h-[90vh] w-full max-w-3xl overflow-auto rounded border border-border bg-bg p-5 shadow-xl" onMouseDown={(event) => event.stopPropagation()}><h2 id="redirect-modal-title" className="mb-4 text-base font-semibold">{title}</h2>{children}</div></div>;
}

function ConfirmDialog({ title, confirmLabel, busy, onCancel, onConfirm, children }: { title: string; confirmLabel: string; busy: boolean; onCancel: () => void; onConfirm: () => void; children: React.ReactNode }) { return <Modal title={title} onClose={onCancel} busy={busy}><p className="text-sm text-text-muted">{children}</p><div className="mt-5 flex justify-end gap-2"><button type="button" disabled={busy} onClick={onCancel} className="rounded border border-border px-3 py-1.5 text-xs">Cancel</button><button type="button" disabled={busy} onClick={onConfirm} className="rounded bg-red px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50">{confirmLabel}</button></div></Modal>; }

function Field({ label, className = "", children }: { label: string; className?: string; children: React.ReactNode }) { return <label className={className}><span className="mb-1 block text-[11px] text-text-muted">{label}</span>{children}</label>; }

function Banner({ tone, onClose, children }: { tone: "error" | "warning"; onClose: () => void; children: React.ReactNode }) { return <div role="alert" className={`flex items-start gap-3 border-b border-border px-4 py-2 text-xs md:px-6 ${tone === "error" ? "text-red" : "text-amber"}`}><span className="flex-1">{children}</span><button type="button" onClick={onClose} aria-label="Dismiss">×</button></div>; }

function EmptyState({ filtered }: { filtered: boolean }) { return <div className="flex flex-col items-center p-10 text-center"><div className="text-3xl text-text-dim">↪</div><h2 className="mt-3 text-sm font-medium">{filtered ? "No matching hostname" : "No redirects yet"}</h2><p className="mt-1 max-w-md text-xs text-text-dim">{filtered ? "Clear the filter or enter the exact hostname of an existing rule." : "Create a branded short link or migrate an old path using the form above."}</p></div>; }
