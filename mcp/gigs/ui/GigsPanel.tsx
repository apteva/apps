// GigsPanel — dashboard surface for the gigs app. Four tabs:
//   Queue, Templates, Instructions, Workers
// All API calls go through /api/apps/gigs/* (the platform's reverse
// proxy). Worker creation lives on the Workers tab and dispatches in
// one shot through workers_create (CRM upsert + promote).

import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

const API = "/api/apps/gigs";

type WorkerStatus = "active" | "paused" | "retired";
type GigStatus =
  | "open"
  | "offered"
  | "accepted"
  | "submitted"
  | "reviewed"
  | "rejected"
  | "cancelled"
  | "expired";

interface CrmContact {
  id: number;
  display_name?: string;
  primary_email?: string;
  primary_phone?: string;
  company?: string;
}
interface WorkerSkill {
  skill_id: number;
  slug: string;
  name: string;
  level: number;
}
interface Worker {
  id: number;
  contact_id: number;
  status: WorkerStatus;
  default_channel?: string;
  notes?: string;
  rating_avg: number;
  accepted_count: number;
  rejected_count: number;
  open_assignments?: number;
  contact?: CrmContact;
  skills?: WorkerSkill[];
}
interface Skill {
  id: number;
  slug: string;
  name: string;
}
interface InstructionVersion {
  id: number;
  version: number;
  status: "draft" | "active" | "archived";
  body: Record<string, unknown>;
  declared_variables?: string[];
  default_result_key?: string;
  result_field?: Record<string, unknown>;
}
interface Instruction {
  id: number;
  slug: string;
  name: string;
  kind: string;
  current_version_id?: number;
  current_version?: InstructionVersion;
  archived_at?: string;
}
interface CompositionItem {
  sort_order: number;
  instruction_id: number;
  instruction_version_id: number;
  kind: string;
  body: Record<string, unknown>;
  result_key?: string;
}
interface TemplateVersion {
  id: number;
  version: number;
  status: "draft" | "active" | "archived";
  title_template: string;
  composition?: CompositionItem[];
  derived?: {
    result_schema: Record<string, unknown>;
    media_manifest: Array<Record<string, unknown>>;
    checklist: Array<Record<string, unknown>>;
    variables: Array<{ name: string; type: string; required?: boolean }>;
  };
}
interface Template {
  id: number;
  slug: string;
  name: string;
  kind: string;
  current_version?: TemplateVersion;
  archived_at?: string;
}
interface Gig {
  id: number;
  title: string;
  status: GigStatus;
  deadline_at?: string;
  template_version_id?: number;
  composition?: Array<{
    sort_order: number;
    instruction_kind: string;
    rendered_body: Record<string, unknown>;
    result_key?: string;
  }>;
  assignments?: Array<{
    id: number;
    worker_id: number;
    status: string;
    worker_url?: string;
  }>;
  result?: Record<string, unknown>;
  rejection_reason?: string;
}

// ─── api ──────────────────────────────────────────────────────────

async function api<T>(
  path: string,
  projectId: string,
  init?: RequestInit,
): Promise<T> {
  const sep = path.includes("?") ? "&" : "?";
  const res = await fetch(`${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`, {
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  const j = await res.json();
  if (!res.ok || (j && typeof j === "object" && "error" in j && (j as any).error)) {
    throw new Error((j as any)?.error || res.statusText);
  }
  return j as T;
}

// ─── small UI primitives ─────────────────────────────────────────

function Pill({ children, tone }: { children: React.ReactNode; tone?: string }) {
  const t = tone || "default";
  const colors: Record<string, string> = {
    default: "bg-bg-subtle text-text-muted border-border",
    success: "bg-emerald-500/10 text-emerald-600 border-emerald-500/30",
    warn:    "bg-amber-500/10  text-amber-600  border-amber-500/30",
    danger:  "bg-rose-500/10   text-rose-600   border-rose-500/30",
    info:    "bg-sky-500/10    text-sky-600    border-sky-500/30",
  };
  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-xs rounded border ${colors[t]}`}>
      {children}
    </span>
  );
}

function gigPillTone(s: GigStatus): string {
  switch (s) {
    case "open": return "info";
    case "offered":
    case "accepted": return "warn";
    case "submitted": return "info";
    case "reviewed": return "success";
    case "rejected":
    case "cancelled":
    case "expired": return "danger";
  }
}

function Icon({ name }: { name: string }) {
  const common = { width: 14, height: 14, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 2 } as const;
  switch (name) {
    case "text":     return (<svg {...common}><path d="M4 6h16M4 12h12M4 18h16" /></svg>);
    case "audio":    return (<svg {...common}><path d="M3 12c2 0 2-6 4-6s2 12 4 12 2-9 4-9 2 6 4 6" /></svg>);
    case "video":    return (<svg {...common}><rect x="3" y="6" width="14" height="12" rx="2" /><path d="M17 10l4-2v8l-4-2z" /></svg>);
    case "image":    return (<svg {...common}><rect x="3" y="5" width="18" height="14" rx="2" /><circle cx="9" cy="11" r="1.5" /><path d="M3 16l5-4 5 4 4-3 4 3" /></svg>);
    case "document": return (<svg {...common}><path d="M6 3h9l4 4v14H6z" /><path d="M14 3v5h5" /></svg>);
    case "link":     return (<svg {...common}><path d="M10 14a4 4 0 0 0 5 0l3-3a4 4 0 0 0-5-5l-1 1" /><path d="M14 10a4 4 0 0 0-5 0l-3 3a4 4 0 0 0 5 5l1-1" /></svg>);
    case "script":   return (<svg {...common}><path d="M8 3v18l4-3 4 3V3" /></svg>);
    case "warning":  return (<svg {...common}><path d="M12 3l10 18H2z" /><path d="M12 10v4M12 17v.5" /></svg>);
    case "example":  return (<svg {...common}><circle cx="12" cy="12" r="9" /><path d="M8 12l3 3 5-6" /></svg>);
    case "check":    return (<svg {...common}><rect x="3" y="3" width="18" height="18" rx="3" /><path d="M8 12l3 3 5-6" /></svg>);
    case "input":    return (<svg {...common}><rect x="3" y="9" width="18" height="6" rx="2" /></svg>);
    case "user":     return (<svg {...common}><circle cx="12" cy="8" r="4" /><path d="M4 21c0-4 4-7 8-7s8 3 8 7" /></svg>);
    case "clipboard":return (<svg {...common}><rect x="6" y="4" width="12" height="16" rx="2" /><path d="M9 4h6v3H9z" /></svg>);
    case "plus":     return (<svg {...common}><path d="M12 5v14M5 12h14" /></svg>);
    default:         return (<svg {...common}><circle cx="12" cy="12" r="9" /></svg>);
  }
}

function kindIcon(kind: string): string {
  if (kind.startsWith("input_")) return "input";
  if (kind === "checklist_item" || kind === "confirmation") return "check";
  if (["text","audio","video","image","document","link","script","warning","example"].includes(kind)) return kind;
  return "clipboard";
}

// ─── shell ───────────────────────────────────────────────────────

type Tab = "queue" | "templates" | "instructions" | "workers";

export default function GigsPanel(props: NativePanelProps) {
  const { projectId } = props;
  const [tab, setTab] = useState<Tab>("queue");
  return (
    <div className="flex flex-col h-full">
      <nav className="flex gap-1 border-b border-border px-3 pt-3">
        {(["queue","templates","instructions","workers"] as Tab[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={
              "px-3 py-2 text-sm capitalize rounded-t " +
              (tab === t
                ? "bg-bg text-text border border-border border-b-bg"
                : "text-text-muted hover:text-text")
            }
          >
            {t}
          </button>
        ))}
      </nav>
      <div className="flex-1 overflow-auto">
        {tab === "queue"        && <QueueTab projectId={projectId} />}
        {tab === "templates"    && <TemplatesTab projectId={projectId} />}
        {tab === "instructions" && <InstructionsTab projectId={projectId} />}
        {tab === "workers"      && <WorkersTab projectId={projectId} />}
      </div>
    </div>
  );
}

// ─── Queue ──────────────────────────────────────────────────────

function QueueTab({ projectId }: { projectId: string }) {
  const [gigs, setGigs] = useState<Gig[] | null>(null);
  const [selected, setSelected] = useState<Gig | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const data = await api<{ gigs: Gig[] }>(`/gigs?status=open,offered,accepted,submitted`, projectId);
      setGigs(data.gigs || []);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [projectId]);
  useEffect(() => { reload(); }, [reload]);

  return (
    <div className="grid grid-cols-[minmax(280px,360px)_1fr] h-full">
      <aside className="border-r border-border overflow-auto">
        {err && <div className="m-3 p-2 text-rose-600 text-sm">{err}</div>}
        {gigs?.length === 0 && <div className="p-6 text-text-muted text-sm">No open gigs.</div>}
        {gigs?.map((g) => (
          <button
            key={g.id}
            onClick={() => setSelected(g)}
            className={
              "w-full text-left px-3 py-3 border-b border-border hover:bg-bg-subtle " +
              (selected?.id === g.id ? "bg-bg-subtle" : "")
            }
          >
            <div className="text-sm truncate">{g.title}</div>
            <div className="flex items-center gap-2 mt-1">
              <Pill tone={gigPillTone(g.status)}>{g.status}</Pill>
              {g.deadline_at && (
                <span className="text-xs text-text-muted">due {formatDate(g.deadline_at)}</span>
              )}
            </div>
          </button>
        ))}
      </aside>
      <section className="p-4 overflow-auto">
        {selected ? <GigDetail gig={selected} projectId={projectId} onChange={reload} /> : (
          <div className="text-text-muted text-sm">Pick a gig.</div>
        )}
      </section>
    </div>
  );
}

function GigDetail({ gig, projectId, onChange }: { gig: Gig; projectId: string; onChange: () => void }) {
  const [full, setFull] = useState<Gig | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api<{ gig: Gig }>(`/gigs/${gig.id}`, projectId).then((d) => setFull(d.gig)).catch(() => setFull(gig));
  }, [gig.id, projectId, gig]);
  const g = full || gig;

  const doAction = async (path: string, body: unknown) => {
    setBusy(true);
    try {
      await api(`/gigs/${gig.id}/${path}`, projectId, { method: "POST", body: JSON.stringify(body || {}) });
      onChange();
      const d = await api<{ gig: Gig }>(`/gigs/${gig.id}`, projectId);
      setFull(d.gig);
    } catch (e) { alert((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <div>
      <h2 className="text-lg font-semibold">{g.title}</h2>
      <div className="flex items-center gap-2 mt-1 text-sm text-text-muted">
        <Pill tone={gigPillTone(g.status)}>{g.status}</Pill>
        {g.deadline_at && <span>due {formatDate(g.deadline_at)}</span>}
      </div>

      <h3 className="mt-6 text-sm font-semibold text-text-muted uppercase tracking-wide">Composition</h3>
      <div className="mt-2 border border-border rounded divide-y divide-border">
        {(g.composition || []).map((c, i) => (
          <div key={i} className="p-3 text-sm flex gap-3 items-start">
            <span className="text-text-muted mt-0.5"><Icon name={kindIcon(c.instruction_kind)} /></span>
            <div className="flex-1 min-w-0">
              <div className="text-xs text-text-muted">{c.instruction_kind}{c.result_key ? ` → ${c.result_key}` : ""}</div>
              <div className="truncate">{summariseBody(c.instruction_kind, c.rendered_body)}</div>
            </div>
          </div>
        ))}
      </div>

      <h3 className="mt-6 text-sm font-semibold text-text-muted uppercase tracking-wide">Assignments</h3>
      <div className="mt-2 border border-border rounded divide-y divide-border">
        {(g.assignments || []).length === 0 && <div className="p-3 text-text-muted text-sm">Unassigned.</div>}
        {(g.assignments || []).map((a) => (
          <div key={a.id} className="p-3 text-sm flex items-center justify-between gap-3">
            <div>
              Worker #{a.worker_id} <Pill tone={a.status === "submitted" ? "info" : "default"}>{a.status}</Pill>
            </div>
            {a.worker_url && (
              <a className="text-sky-600 text-xs underline" target="_blank" rel="noreferrer" href={a.worker_url}>worker link</a>
            )}
          </div>
        ))}
      </div>

      {g.result && (
        <>
          <h3 className="mt-6 text-sm font-semibold text-text-muted uppercase tracking-wide">Submission</h3>
          <pre className="mt-2 p-3 text-xs bg-bg-subtle rounded border border-border whitespace-pre-wrap">{JSON.stringify(g.result, null, 2)}</pre>
        </>
      )}

      <div className="mt-6 flex gap-2">
        {g.status === "submitted" && (
          <>
            <button disabled={busy} onClick={() => doAction("accept", {})} className="px-3 py-2 text-sm bg-emerald-600 text-white rounded">Accept</button>
            <button disabled={busy} onClick={() => {
              const reason = prompt("Rejection reason:") || "";
              if (reason) doAction("reject", { reason, reopen: true });
            }} className="px-3 py-2 text-sm border border-border rounded">Reject + reopen</button>
          </>
        )}
        {(g.status === "open" || g.status === "offered" || g.status === "accepted") && (
          <button disabled={busy} onClick={() => {
            const reason = prompt("Cancel reason:") || "";
            doAction("cancel", { reason });
          }} className="px-3 py-2 text-sm border border-border rounded text-rose-600">Cancel</button>
        )}
      </div>
    </div>
  );
}

function summariseBody(kind: string, body: Record<string, unknown>): string {
  if (kind === "text") return String(body.markdown || "").slice(0, 80);
  if (kind === "warning" || kind === "checklist_item" || kind === "confirmation") return String(body.text || "");
  if (kind === "link") return String(body.label || body.url || "");
  if (kind === "audio" || kind === "video" || kind === "image" || kind === "document")
    return body.caption ? String(body.caption) : `[${kind}]`;
  if (kind.startsWith("input_")) return String(body.label || "");
  return "";
}

// ─── Templates ──────────────────────────────────────────────────

function TemplatesTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Template[] | null>(null);
  const [adding, setAdding] = useState(false);

  const reload = useCallback(() => {
    api<{ templates: Template[] }>("/templates?include_archived=false", projectId)
      .then((d) => setItems(d.templates || []))
      .catch(() => setItems([]));
  }, [projectId]);
  useEffect(() => { reload(); }, [reload]);

  return (
    <div className="p-4 space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Templates</h2>
        <button onClick={() => setAdding(true)} className="flex items-center gap-1 px-2 py-1 text-sm border border-border rounded">
          <Icon name="plus" /> New
        </button>
      </div>
      {adding && <NewTemplateForm projectId={projectId} onDone={() => { setAdding(false); reload(); }} />}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
        {items?.map((t) => (
          <div key={t.id} className="border border-border rounded p-3 bg-bg-subtle">
            <div className="flex items-center justify-between">
              <div className="font-medium text-sm">{t.name}</div>
              {t.current_version && (
                <Pill tone={t.current_version.status === "active" ? "success" : "warn"}>
                  v{t.current_version.version} · {t.current_version.status}
                </Pill>
              )}
            </div>
            <div className="mt-1 text-xs text-text-muted">/{t.slug} · {t.kind}</div>
            {t.current_version?.derived && (
              <div className="mt-2 text-xs text-text-muted">
                {t.current_version.composition?.length || 0} instructions ·{" "}
                {t.current_version.derived.variables.length} vars ·{" "}
                {t.current_version.derived.media_manifest.length} media
              </div>
            )}
            {t.current_version?.status === "draft" && (
              <button
                onClick={() => api(`/templates/${t.id}/publish`, projectId, { method: "POST" }).then(reload)}
                className="mt-3 px-2 py-1 text-xs border border-border rounded"
              >
                Publish v{t.current_version.version}
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function NewTemplateForm({ projectId, onDone }: { projectId: string; onDone: () => void }) {
  const [name, setName] = useState("");
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        try {
          await api(`/templates`, projectId, { method: "POST", body: JSON.stringify({ name, title_template: title }) });
          onDone();
        } catch (err) { alert((err as Error).message); } finally { setBusy(false); }
      }}
      className="p-3 border border-border rounded space-y-2 bg-bg-subtle"
    >
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Template name" className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" required />
      <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Title with {{vars}}" className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" required />
      <button disabled={busy} className="px-3 py-1 text-sm border border-border rounded">Create draft</button>
    </form>
  );
}

// ─── Instructions ───────────────────────────────────────────────

function InstructionsTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Instruction[] | null>(null);
  const [kind, setKind] = useState<string>("");

  const reload = useCallback(() => {
    const k = kind ? `&kind=${encodeURIComponent(kind)}` : "";
    api<{ instructions: Instruction[] }>(`/instructions?include_archived=false${k}`, projectId)
      .then((d) => setItems(d.instructions || []))
      .catch(() => setItems([]));
  }, [projectId, kind]);
  useEffect(() => { reload(); }, [reload]);

  const groups = useMemo(() => {
    const g: Record<string, Instruction[]> = {};
    (items || []).forEach((i) => {
      const fam = i.kind.startsWith("input_") ? "Input"
        : i.kind === "checklist_item" || i.kind === "confirmation" ? "Do"
        : "Read";
      (g[fam] = g[fam] || []).push(i);
    });
    return g;
  }, [items]);

  return (
    <div className="p-4 space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Instruction library</h2>
        <select value={kind} onChange={(e) => setKind(e.target.value)} className="text-sm border border-border rounded px-2 py-1 bg-bg">
          <option value="">All kinds</option>
          {ALL_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
      </div>
      {Object.entries(groups).map(([fam, list]) => (
        <div key={fam}>
          <h3 className="text-xs uppercase tracking-wide text-text-muted mb-2">{fam}</h3>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-2">
            {list.map((i) => (
              <div key={i.id} className="border border-border rounded p-2 bg-bg-subtle flex items-start gap-2">
                <span className="text-text-muted mt-0.5"><Icon name={kindIcon(i.kind)} /></span>
                <div className="min-w-0">
                  <div className="text-sm truncate">{i.name}</div>
                  <div className="text-xs text-text-muted truncate">/{i.slug} · {i.kind}</div>
                  {i.current_version && (
                    <div className="mt-1">
                      <Pill tone={i.current_version.status === "active" ? "success" : "warn"}>
                        v{i.current_version.version} · {i.current_version.status}
                      </Pill>
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

const ALL_KINDS = [
  "text","audio","video","image","document","link","script","warning","example",
  "checklist_item","confirmation","timer_hint",
  "input_short_text","input_long_text","input_number","input_date",
  "input_choice","input_multi_choice","input_rating","input_yes_no",
  "input_photo","input_audio_recording","input_video_recording",
  "input_file","input_signature","input_location",
];

// ─── Workers ────────────────────────────────────────────────────

function WorkersTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Worker[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [skills, setSkills] = useState<Skill[]>([]);

  const reload = useCallback(() => {
    api<{ workers: Worker[] }>("/workers?status=&include_contact=true", projectId)
      .then((d) => setItems(d.workers || []))
      .catch(() => setItems([]));
    api<{ skills: Skill[] }>("/skills", projectId).then((d) => setSkills(d.skills || [])).catch(() => {});
  }, [projectId]);
  useEffect(() => { reload(); }, [reload]);

  return (
    <div className="p-4 space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Workers</h2>
        <button onClick={() => setAdding(true)} className="flex items-center gap-1 px-2 py-1 text-sm border border-border rounded">
          <Icon name="plus" /> Add worker
        </button>
      </div>
      {adding && (
        <NewWorkerForm
          projectId={projectId}
          skills={skills}
          onDone={() => { setAdding(false); reload(); }}
          onCancel={() => setAdding(false)}
        />
      )}
      <div className="border border-border rounded divide-y divide-border">
        {items?.length === 0 && <div className="p-4 text-text-muted text-sm">No workers yet. Add one to get started — they'll be created in CRM in the same step.</div>}
        {items?.map((wk) => (
          <div key={wk.id} className="p-3 flex items-start gap-3">
            <span className="text-text-muted mt-1"><Icon name="user" /></span>
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium">{wk.contact?.display_name || `Worker #${wk.id}`}</div>
              <div className="text-xs text-text-muted">
                {wk.contact?.primary_email || wk.contact?.primary_phone || "—"}
                {wk.contact?.company ? ` · ${wk.contact.company}` : ""}
              </div>
              {wk.skills && wk.skills.length > 0 && (
                <div className="flex flex-wrap gap-1 mt-1">
                  {wk.skills.map((s) => (
                    <Pill key={s.skill_id}>{s.name} · L{s.level}</Pill>
                  ))}
                </div>
              )}
            </div>
            <div className="flex flex-col items-end gap-1 text-xs text-text-muted">
              <Pill tone={wk.status === "active" ? "success" : "warn"}>{wk.status}</Pill>
              <span>{wk.accepted_count}✓ · {wk.rejected_count}✕</span>
              {wk.open_assignments != null && <span>{wk.open_assignments} open</span>}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function NewWorkerForm({
  projectId, skills, onDone, onCancel,
}: { projectId: string; skills: Skill[]; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [company, setCompany] = useState("");
  const [channel, setChannel] = useState("");
  const [skillIds, setSkillIds] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        if (!email && !phone) { setErr("Email or phone required"); return; }
        setBusy(true); setErr(null);
        try {
          await api("/workers", projectId, {
            method: "POST",
            body: JSON.stringify({
              name, email: email || undefined, phone: phone || undefined,
              company: company || undefined,
              default_channel: channel || undefined,
              skill_ids: skillIds.length ? skillIds : undefined,
            }),
          });
          onDone();
        } catch (e2) { setErr((e2 as Error).message); }
        finally { setBusy(false); }
      }}
      className="p-3 border border-border rounded space-y-2 bg-bg-subtle"
    >
      <div className="text-xs text-text-muted">
        Name + at least one of email/phone. We upsert the CRM contact and promote it to a worker in one step.
      </div>
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Display name" required className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" />
      <div className="grid grid-cols-2 gap-2">
        <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email" type="email" className="px-2 py-1 text-sm border border-border rounded bg-bg" />
        <input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="Phone (E.164)" className="px-2 py-1 text-sm border border-border rounded bg-bg" />
      </div>
      <input value={company} onChange={(e) => setCompany(e.target.value)} placeholder="Company (optional)" className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" />
      <select value={channel} onChange={(e) => setChannel(e.target.value)} className="px-2 py-1 text-sm border border-border rounded bg-bg">
        <option value="">Default channel — let CRM pick</option>
        <option value="email">Email</option>
        <option value="sms">SMS</option>
        <option value="whatsapp">WhatsApp</option>
      </select>
      {skills.length > 0 && (
        <div>
          <div className="text-xs text-text-muted mb-1">Skills</div>
          <div className="flex flex-wrap gap-1">
            {skills.map((s) => {
              const on = skillIds.includes(s.id);
              return (
                <button
                  type="button"
                  key={s.id}
                  onClick={() => setSkillIds((cur) => on ? cur.filter((x) => x !== s.id) : [...cur, s.id])}
                  className={"px-2 py-0.5 text-xs rounded border " + (on ? "bg-sky-500/10 border-sky-500/30 text-sky-700" : "border-border text-text-muted")}
                >
                  {s.name}
                </button>
              );
            })}
          </div>
        </div>
      )}
      {err && <div className="text-rose-600 text-xs">{err}</div>}
      <div className="flex gap-2">
        <button disabled={busy} className="px-3 py-1 text-sm bg-sky-600 text-white rounded">Add worker</button>
        <button type="button" onClick={onCancel} className="px-3 py-1 text-sm border border-border rounded">Cancel</button>
      </div>
    </form>
  );
}

// ─── utils ──────────────────────────────────────────────────────

function formatDate(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  } catch { return iso; }
}
