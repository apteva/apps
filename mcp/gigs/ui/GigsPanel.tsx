// GigsPanel — dashboard surface for the gigs app. Four tabs:
//   Queue, Templates, Instructions, Workers
// All API calls go through /api/apps/gigs/* (the platform's reverse
// proxy). Worker creation lives on the Workers tab and can either
// promote an existing CRM contact or create/match one by channel.

import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

const API = "/api/apps/gigs";
const CRM_API = "/api/apps/crm";
const STORAGE_API = "/api/apps/storage";

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
interface SubmissionFileRef {
  storage_file_id?: number;
  filename?: string;
  mime?: string;
}
interface InstructionResponse {
  key?: string;
  step?: number;
  sort_order?: number;
  instruction_kind?: string;
  note?: string;
  files?: SubmissionFileRef[];
}
interface Submission {
  id: number;
  assignment_id: number;
  payload: Record<string, unknown>;
  attachment_file_ids?: number[];
  channel?: string;
  submitted_at?: string;
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
    submitted_at?: string;
    worker_url?: string;
    submission?: Submission;
  }>;
  result?: Record<string, unknown>;
  submission?: Submission;
  rejection_reason?: string;
}
interface StorageFile {
  id: number;
  name: string;
  folder: string;
  content_type?: string;
  size_bytes?: number;
  url?: string;
}
interface StorageFolder {
  name: string;
  path: string;
  file_count?: number;
  size_bytes?: number;
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

async function crmApi<T>(
  path: string,
  projectId: string,
  init?: RequestInit,
): Promise<T> {
  const sep = path.includes("?") ? "&" : "?";
  const res = await fetch(`${CRM_API}${path}${sep}project_id=${encodeURIComponent(projectId)}`, {
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

async function storageApi<T>(
  path: string,
  projectId: string,
  init?: RequestInit,
): Promise<T> {
  const sep = path.includes("?") ? "&" : "?";
  const res = await fetch(`${STORAGE_API}${path}${sep}project_id=${encodeURIComponent(projectId)}`, {
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

function contactName(c: CrmContact): string {
  return c.display_name || c.primary_email || c.primary_phone || `Contact #${c.id}`;
}

function contactLine(c: CrmContact): string {
  return [c.primary_email || c.primary_phone, c.company].filter(Boolean).join(" · ");
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
  const [adding, setAdding] = useState(false);
  const [status, setStatus] = useState("open,offered,accepted,submitted");
  const [err, setErr] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const data = await api<{ gigs: Gig[] }>(`/gigs?status=${encodeURIComponent(status)}`, projectId);
      setGigs(data.gigs || []);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [projectId, status]);
  useEffect(() => { reload(); }, [reload]);

  return (
    <div className="grid grid-cols-[minmax(280px,360px)_1fr] h-full">
      <aside className="border-r border-border overflow-auto">
        <div className="sticky top-0 z-10 bg-bg border-b border-border p-3 space-y-2">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold">Gigs</h2>
            <button
              onClick={() => { setAdding(true); setSelected(null); }}
              className="flex items-center gap-1 px-2 py-1 text-sm border border-border rounded"
            >
              <Icon name="plus" /> New gig
            </button>
          </div>
          <select value={status} onChange={(e) => setStatus(e.target.value)} className="w-full text-sm border border-border rounded px-2 py-1 bg-bg">
            <option value="open,offered,accepted,submitted">Active + submitted</option>
            <option value="open">Open</option>
            <option value="offered,accepted">Assigned</option>
            <option value="submitted">Submitted</option>
            <option value="reviewed">Reviewed</option>
            <option value="rejected,cancelled,expired">Closed without acceptance</option>
            <option value="open,offered,accepted,submitted,reviewed,rejected,cancelled,expired">All</option>
          </select>
        </div>
        {err && <div className="m-3 p-2 text-rose-600 text-sm">{err}</div>}
        {gigs?.length === 0 && <div className="p-6 text-text-muted text-sm">No gigs in this view.</div>}
        {gigs?.map((g) => (
          <button
            key={g.id}
            onClick={() => { setSelected(g); setAdding(false); }}
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
        {adding ? (
          <NewGigForm
            projectId={projectId}
            onDone={(gig) => {
              setAdding(false);
              setSelected(gig);
              reload();
            }}
            onCancel={() => setAdding(false)}
          />
        ) : selected ? <GigDetail gig={selected} projectId={projectId} onChange={reload} /> : (
          <div className="text-text-muted text-sm">Pick a gig.</div>
        )}
      </section>
    </div>
  );
}

function GigDetail({ gig, projectId, onChange }: { gig: Gig; projectId: string; onChange: () => void }) {
  const [full, setFull] = useState<Gig | null>(null);
  const [busy, setBusy] = useState(false);
  const [assigning, setAssigning] = useState(false);
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
              {a.submitted_at && <span className="ml-2 text-xs text-text-muted">submitted {formatDate(a.submitted_at)}</span>}
            </div>
            {a.worker_url && (
              <a className="text-sky-600 text-xs underline" target="_blank" rel="noreferrer" href={a.worker_url}>worker link</a>
            )}
          </div>
        ))}
      </div>
      {(g.status === "open" || g.status === "offered") && (
        <div className="mt-3">
          {assigning ? (
            <AssignGigForm
              projectId={projectId}
              busy={busy}
              onCancel={() => setAssigning(false)}
              onAssign={(workerId, notifyWorker) =>
                doAction("assign", { worker_id: workerId, mode: "direct", notify_worker: notifyWorker }).then(() => setAssigning(false))
              }
            />
          ) : (
            <button onClick={() => setAssigning(true)} className="px-3 py-2 text-sm border border-border rounded">
              Assign worker
            </button>
          )}
        </div>
      )}

      <SubmissionReview gig={g} projectId={projectId} busy={busy} onAction={doAction} />

      <div className="mt-6 flex gap-2">
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

function SubmissionReview({
  gig, projectId, busy, onAction,
}: {
  gig: Gig;
  projectId: string;
  busy: boolean;
  onAction: (path: string, body: unknown) => Promise<void>;
}) {
  const submission = gig.submission;
  const payload = submission?.payload || gig.result || {};
  const responses = useMemo(() => instructionResponsesFromPayload(payload), [payload]);
  const extraEntries = useMemo(
    () => Object.entries(payload).filter(([key]) => key !== "instruction_responses"),
    [payload],
  );
  const fileIDs = useMemo(() => collectSubmissionFileIds(submission, payload), [submission, payload]);
  const fileKey = fileIDs.join(",");
  const [files, setFiles] = useState<Record<number, StorageFile>>({});
  const [fileErr, setFileErr] = useState<string | null>(null);
  const [acceptNotes, setAcceptNotes] = useState("");
  const [rejectReason, setRejectReason] = useState("");
  const [reopen, setReopen] = useState(true);

  useEffect(() => {
    if (!fileKey) {
      setFiles({});
      setFileErr(null);
      return;
    }
    let cancelled = false;
    storageApi<{ files: StorageFile[] }>(`/files?ids=${encodeURIComponent(fileKey)}`, projectId)
      .then((data) => {
        if (cancelled) return;
        const next: Record<number, StorageFile> = {};
        for (const file of data.files || []) next[file.id] = file;
        setFiles(next);
        setFileErr(null);
      })
      .catch((e) => {
        if (!cancelled) setFileErr((e as Error).message);
      });
    return () => { cancelled = true; };
  }, [fileKey, projectId]);

  if (!submission && !gig.result && gig.status !== "submitted") return null;

  return (
    <section className="mt-6 border border-border rounded overflow-hidden">
      <div className="p-3 bg-bg-subtle border-b border-border flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-text-muted uppercase tracking-wide">
            {gig.status === "reviewed" ? "Accepted submission" : "Submission"}
          </h3>
          <div className="mt-1 text-xs text-text-muted">
            {submission ? (
              <>
                #{submission.id} from assignment #{submission.assignment_id}
                {submission.channel ? ` via ${submission.channel}` : ""}
                {submission.submitted_at ? ` · ${formatDate(submission.submitted_at)}` : ""}
              </>
            ) : (
              "No submission row was returned for this gig."
            )}
          </div>
        </div>
        {gig.status === "submitted" && <Pill tone="info">awaiting review</Pill>}
        {gig.status === "reviewed" && <Pill tone="success">accepted</Pill>}
      </div>

      {submission || gig.result ? (
        <div className="p-3 space-y-4">
          {responses.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold text-text-muted uppercase tracking-wide">Instruction responses</h4>
              <div className="mt-2 divide-y divide-border border border-border rounded">
                {responses.map((response, index) => (
                  <div key={response.key || `${response.step || index}-${index}`} className="p-3 text-sm space-y-2">
                    <div className="flex items-center justify-between gap-2">
                      <div className="font-medium">
                        Step {response.step || index + 1}
                        {response.instruction_kind ? <span className="ml-2 text-xs text-text-muted">{response.instruction_kind}</span> : null}
                      </div>
                      {response.key ? <span className="text-xs text-text-muted">{response.key}</span> : null}
                    </div>
                    {response.note ? (
                      <div className="whitespace-pre-wrap text-sm">{response.note}</div>
                    ) : (
                      <div className="text-xs text-text-muted">No notes for this step.</div>
                    )}
                    {(response.files || []).length > 0 && (
                      <div className="flex flex-wrap gap-2">
                        {(response.files || []).map((file, fileIndex) => (
                          <SubmissionFileLink
                            key={`${file.storage_file_id || file.filename || "file"}-${fileIndex}`}
                            id={file.storage_file_id}
                            label={file.filename}
                            mime={file.mime}
                            meta={file.storage_file_id ? files[file.storage_file_id] : undefined}
                            projectId={projectId}
                          />
                        ))}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}

          {submission?.attachment_file_ids?.length ? (
            <div>
              <h4 className="text-xs font-semibold text-text-muted uppercase tracking-wide">All submitted files</h4>
              <div className="mt-2 flex flex-wrap gap-2">
                {submission.attachment_file_ids.map((id) => (
                  <SubmissionFileLink key={id} id={id} meta={files[id]} projectId={projectId} />
                ))}
              </div>
              {fileErr && <div className="mt-2 text-xs text-amber-600">Could not load file metadata: {fileErr}</div>}
            </div>
          ) : null}

          {extraEntries.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold text-text-muted uppercase tracking-wide">Result fields</h4>
              <div className="mt-2 border border-border rounded divide-y divide-border">
                {extraEntries.map(([key, value]) => (
                  <div key={key} className="p-3 grid grid-cols-1 md:grid-cols-[160px_1fr] gap-2 text-sm">
                    <div className="text-text-muted">{key}</div>
                    <div className="min-w-0">{renderPayloadValue(value, files, projectId)}</div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {responses.length === 0 && extraEntries.length === 0 && (
            <div className="text-sm text-text-muted">The worker submitted without notes, result fields, or files.</div>
          )}

          {gig.status === "submitted" && (
            <div className="pt-3 border-t border-border space-y-3">
              <textarea
                value={acceptNotes}
                onChange={(e) => setAcceptNotes(e.target.value)}
                rows={2}
                className="w-full px-2 py-1 text-sm border border-border rounded bg-bg"
                placeholder="Acceptance notes for CRM timeline"
              />
              <div className="flex flex-wrap gap-2">
                <button
                  disabled={busy}
                  onClick={() => onAction("accept", { notes: acceptNotes })}
                  className="px-3 py-2 text-sm bg-emerald-600 text-white rounded disabled:opacity-50"
                >
                  Accept submission
                </button>
                <input
                  value={rejectReason}
                  onChange={(e) => setRejectReason(e.target.value)}
                  placeholder="Rejection reason"
                  className="min-w-[220px] flex-1 px-2 py-1 text-sm border border-border rounded bg-bg"
                />
                <label className="flex items-center gap-2 text-sm text-text-muted">
                  <input type="checkbox" checked={reopen} onChange={(e) => setReopen(e.target.checked)} />
                  Reopen gig
                </label>
                <button
                  disabled={busy || !rejectReason.trim()}
                  onClick={() => onAction("reject", { reason: rejectReason.trim(), reopen })}
                  className="px-3 py-2 text-sm border border-border rounded text-rose-600 disabled:opacity-50"
                >
                  Reject submission
                </button>
              </div>
            </div>
          )}
        </div>
      ) : (
        <div className="p-3 text-sm text-amber-600">
          This gig is marked submitted, but the latest submission was not returned by the backend.
        </div>
      )}
    </section>
  );
}

function SubmissionFileLink({
  id, label, mime, meta, projectId,
}: {
  id?: number;
  label?: string;
  mime?: string;
  meta?: StorageFile;
  projectId: string;
}) {
  if (!id) {
    return <span className="px-2 py-1 text-xs border border-border rounded text-text-muted">{label || "File"}</span>;
  }
  const href = meta?.url || `/api/apps/storage/files/${id}/content?project_id=${encodeURIComponent(projectId)}`;
  const name = label || meta?.name || `File #${id}`;
  const detail = meta ? [meta.content_type, formatBytes(meta.size_bytes)].filter(Boolean).join(" · ") : mime || `Storage #${id}`;
  return (
    <a
      className="inline-flex flex-col px-2 py-1 text-xs border border-border rounded hover:bg-bg-subtle"
      href={href}
      target="_blank"
      rel="noreferrer"
    >
      <span className="font-medium text-sky-600">{name}</span>
      <span className="text-text-muted">{detail}</span>
    </a>
  );
}

function NewGigForm({
  projectId, onDone, onCancel,
}: { projectId: string; onDone: (gig: Gig) => void; onCancel: () => void }) {
  const [mode, setMode] = useState<"template" | "instructions">("template");
  const [templates, setTemplates] = useState<Template[]>([]);
  const [instructions, setInstructions] = useState<Instruction[]>([]);
  const [templateId, setTemplateId] = useState("");
  const [title, setTitle] = useState("");
  const [varsText, setVarsText] = useState("{}");
  const [workerId, setWorkerId] = useState("");
  const [notifyWorker, setNotifyWorker] = useState(false);
  const [deadlineLocal, setDeadlineLocal] = useState("");
  const [priority, setPriority] = useState("");
  const [selectedInstructionIds, setSelectedInstructionIds] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api<{ templates: Template[] }>("/templates?include_archived=false", projectId)
      .then((d) => {
        const active = (d.templates || []).filter((t) => t.current_version?.status === "active");
        setTemplates(active);
        if (!templateId && active[0]) setTemplateId(String(active[0].id));
      })
      .catch(() => setTemplates([]));
    api<{ instructions: Instruction[] }>("/instructions?include_archived=false", projectId)
      .then((d) => setInstructions(d.instructions || []))
      .catch(() => setInstructions([]));
  }, [projectId, templateId]);

  const selectedTemplate = templates.find((t) => String(t.id) === templateId);
  const selectedInstructions = selectedInstructionIds
    .map((id) => instructions.find((i) => i.id === id))
    .filter(Boolean) as Instruction[];

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const vars = parseJSONMap(varsText, "Vars JSON");
      const body: Record<string, unknown> = {
        vars,
        deadline_at: deadlineLocal ? new Date(deadlineLocal).toISOString() : undefined,
        priority: priority || undefined,
        worker_id: workerId ? Number(workerId) : undefined,
        notify_worker: workerId ? notifyWorker : undefined,
      };
      if (mode === "template") {
        if (!templateId) throw new Error("Select a published template");
        body.template_id = Number(templateId);
      } else {
        if (!title.trim()) throw new Error("Title required");
        if (selectedInstructionIds.length === 0) throw new Error("Add at least one instruction");
        body.title = title.trim();
        body.instructions = selectedInstructionIds.map((id) => ({ instruction_id: id }));
      }
      const data = await api<{ gig: Gig }>("/gigs", projectId, {
        method: "POST",
        body: JSON.stringify(body),
      });
      onDone(data.gig);
    } catch (e2) {
      setErr((e2 as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">Create gig</h2>
        <button type="button" onClick={onCancel} className="px-3 py-1 text-sm border border-border rounded">Cancel</button>
      </div>
      <div className="inline-flex rounded border border-border overflow-hidden text-sm">
        <button type="button" onClick={() => setMode("template")} className={"px-3 py-1 " + (mode === "template" ? "bg-sky-600 text-white" : "bg-bg text-text-muted")}>From template</button>
        <button type="button" onClick={() => setMode("instructions")} className={"px-3 py-1 border-l border-border " + (mode === "instructions" ? "bg-sky-600 text-white" : "bg-bg text-text-muted")}>From instructions</button>
      </div>

      {mode === "template" ? (
        <div className="space-y-2">
          <select value={templateId} onChange={(e) => setTemplateId(e.target.value)} className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" required>
            <option value="">Select published template</option>
            {templates.map((t) => <option key={t.id} value={t.id}>{t.name} /{t.slug}</option>)}
          </select>
          {selectedTemplate?.current_version?.derived?.variables?.length ? (
            <div className="text-xs text-text-muted">
              Vars expected: {selectedTemplate.current_version.derived.variables.map((v) => v.name).join(", ")}
            </div>
          ) : null}
        </div>
      ) : (
        <div className="space-y-3">
          <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Gig title" className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" required={mode === "instructions"} />
          <InstructionOrderEditor
            instructions={instructions}
            selectedIds={selectedInstructionIds}
            onChange={setSelectedInstructionIds}
          />
          {selectedInstructions.length > 0 && (
            <div className="text-xs text-text-muted">
              {selectedInstructions.length} instruction{selectedInstructions.length === 1 ? "" : "s"} selected
            </div>
          )}
        </div>
      )}

      <div className="grid grid-cols-1 md:grid-cols-3 gap-2">
        <WorkerSelect projectId={projectId} value={workerId} onChange={setWorkerId} allowEmpty />
        <input type="datetime-local" value={deadlineLocal} onChange={(e) => setDeadlineLocal(e.target.value)} className="px-2 py-1 text-sm border border-border rounded bg-bg" />
        <select value={priority} onChange={(e) => setPriority(e.target.value)} className="px-2 py-1 text-sm border border-border rounded bg-bg">
          <option value="">Priority</option>
          <option value="low">Low</option>
          <option value="normal">Normal</option>
          <option value="high">High</option>
          <option value="urgent">Urgent</option>
        </select>
      </div>
      {workerId && (
        <label className="inline-flex items-center gap-2 text-sm text-text-muted">
          <input
            type="checkbox"
            checked={notifyWorker}
            onChange={(e) => setNotifyWorker(e.target.checked)}
            className="h-4 w-4 rounded border-border"
          />
          Notify selected worker now
        </label>
      )}

      <textarea
        value={varsText}
        onChange={(e) => setVarsText(e.target.value)}
        rows={5}
        className="w-full px-2 py-1 text-sm font-mono border border-border rounded bg-bg"
        placeholder='{"customer_name":"Acme"}'
      />
      {err && <div className="text-rose-600 text-xs">{err}</div>}
      <button disabled={busy} className="px-3 py-2 text-sm bg-sky-600 text-white rounded disabled:opacity-50">Create gig</button>
    </form>
  );
}

function AssignGigForm({
  projectId, busy, onAssign, onCancel,
}: { projectId: string; busy: boolean; onAssign: (workerId: number, notifyWorker: boolean) => Promise<void>; onCancel: () => void }) {
  const [workerId, setWorkerId] = useState("");
  const [notifyWorker, setNotifyWorker] = useState(false);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (workerId) onAssign(Number(workerId), notifyWorker);
      }}
      className="p-3 border border-border rounded bg-bg-subtle space-y-2"
    >
      <WorkerSelect projectId={projectId} value={workerId} onChange={setWorkerId} />
      <label className="inline-flex items-center gap-2 text-sm text-text-muted">
        <input
          type="checkbox"
          checked={notifyWorker}
          onChange={(e) => setNotifyWorker(e.target.checked)}
          className="h-4 w-4 rounded border-border"
        />
        Notify worker now
      </label>
      <div className="flex gap-2">
        <button disabled={busy || !workerId} className="px-3 py-1 text-sm bg-sky-600 text-white rounded disabled:opacity-50">Assign</button>
        <button type="button" onClick={onCancel} className="px-3 py-1 text-sm border border-border rounded">Cancel</button>
      </div>
    </form>
  );
}

function WorkerSelect({
  projectId, value, onChange, allowEmpty,
}: { projectId: string; value: string; onChange: (v: string) => void; allowEmpty?: boolean }) {
  const [workers, setWorkers] = useState<Worker[]>([]);
  useEffect(() => {
    api<{ workers: Worker[] }>("/workers?status=active&include_contact=true", projectId)
      .then((d) => setWorkers(d.workers || []))
      .catch(() => setWorkers([]));
  }, [projectId]);
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} className="px-2 py-1 text-sm border border-border rounded bg-bg">
      {allowEmpty && <option value="">No worker yet</option>}
      {!allowEmpty && <option value="">Select worker</option>}
      {workers.map((w) => (
        <option key={w.id} value={w.id}>
          {w.contact ? contactName(w.contact) : `Worker #${w.id}`}{w.open_assignments != null ? ` (${w.open_assignments} open)` : ""}
        </option>
      ))}
    </select>
  );
}

function InstructionOrderEditor({
  instructions, selectedIds, onChange,
}: { instructions: Instruction[]; selectedIds: number[]; onChange: (ids: number[]) => void }) {
  const [pickId, setPickId] = useState("");
  const available = instructions.filter((i) => i.current_version_id || i.current_version);
  const selected = selectedIds
    .map((id) => instructions.find((i) => i.id === id))
    .filter(Boolean) as Instruction[];

  const move = (index: number, delta: number) => {
    const next = selectedIds.slice();
    const target = index + delta;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    onChange(next);
  };

  return (
    <div className="space-y-2">
      <div className="grid grid-cols-[1fr_auto] gap-2">
        <select value={pickId} onChange={(e) => setPickId(e.target.value)} className="px-2 py-1 text-sm border border-border rounded bg-bg">
          <option value="">Add instruction</option>
          {available.map((i) => (
            <option key={i.id} value={i.id}>{i.name} /{i.slug} · {i.kind}</option>
          ))}
        </select>
        <button
          type="button"
          disabled={!pickId}
          onClick={() => {
            onChange([...selectedIds, Number(pickId)]);
            setPickId("");
          }}
          className="px-3 py-1 text-sm border border-border rounded disabled:opacity-50"
        >
          Add
        </button>
      </div>
      <div className="border border-border rounded divide-y divide-border bg-bg">
        {selected.length === 0 && <div className="p-3 text-xs text-text-muted">No instructions selected.</div>}
        {selected.map((i, index) => (
          <div key={`${i.id}-${index}`} className="p-2 flex items-start gap-2 text-sm">
            <span className="text-text-muted mt-0.5">{index + 1}</span>
            <span className="text-text-muted mt-0.5"><Icon name={kindIcon(i.kind)} /></span>
            <div className="flex-1 min-w-0">
              <div className="truncate">{i.name}</div>
              <div className="text-xs text-text-muted truncate">/{i.slug} · {i.kind}</div>
            </div>
            <div className="flex gap-1">
              <button type="button" onClick={() => move(index, -1)} disabled={index === 0} className="px-2 py-1 text-xs border border-border rounded disabled:opacity-50">Up</button>
              <button type="button" onClick={() => move(index, 1)} disabled={index === selected.length - 1} className="px-2 py-1 text-xs border border-border rounded disabled:opacity-50">Down</button>
              <button type="button" onClick={() => onChange(selectedIds.filter((_, i2) => i2 !== index))} className="px-2 py-1 text-xs border border-border rounded text-rose-600">Remove</button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function instructionLabelById(instructions: Instruction[], id: number): string {
  const found = instructions.find((i) => i.id === id);
  return found ? found.name : `Instruction #${id}`;
}

function parseJSONMap(raw: string, label: string): Record<string, unknown> {
  const trimmed = raw.trim();
  if (!trimmed) return {};
  const parsed = JSON.parse(trimmed);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object`);
  }
  return parsed as Record<string, unknown>;
}

function asRecord(v: unknown): Record<string, unknown> | null {
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  return v as Record<string, unknown>;
}

function numberValue(v: unknown): number | undefined {
  if (typeof v === "number" && Number.isFinite(v)) return v;
  if (typeof v === "string" && /^\d+$/.test(v)) return Number(v);
  return undefined;
}

function instructionResponsesFromPayload(payload: Record<string, unknown>): InstructionResponse[] {
  const raw = payload.instruction_responses;
  if (!Array.isArray(raw)) return [];
  return raw.map((item) => {
    const record = asRecord(item);
    if (!record) return null;
    const files = Array.isArray(record.files)
      ? record.files.map((file) => {
          const f = asRecord(file);
          if (!f) return null;
          return {
            storage_file_id: numberValue(f.storage_file_id),
            filename: typeof f.filename === "string" ? f.filename : undefined,
            mime: typeof f.mime === "string" ? f.mime : undefined,
          };
        }).filter(Boolean) as SubmissionFileRef[]
      : [];
    return {
      key: typeof record.key === "string" ? record.key : undefined,
      step: numberValue(record.step),
      sort_order: numberValue(record.sort_order),
      instruction_kind: typeof record.instruction_kind === "string" ? record.instruction_kind : undefined,
      note: typeof record.note === "string" ? record.note : undefined,
      files,
    };
  }).filter(Boolean) as InstructionResponse[];
}

function collectSubmissionFileIds(submission: Submission | undefined, payload: Record<string, unknown>): number[] {
  const ids = new Set<number>();
  for (const id of submission?.attachment_file_ids || []) {
    if (Number.isFinite(id)) ids.add(id);
  }
  collectFileIdsFromValue(payload, ids);
  return Array.from(ids).sort((a, b) => a - b);
}

function collectFileIdsFromValue(value: unknown, ids: Set<number>) {
  if (Array.isArray(value)) {
    for (const item of value) collectFileIdsFromValue(item, ids);
    return;
  }
  const record = asRecord(value);
  if (!record) return;
  const id = numberValue(record.storage_file_id);
  if (id) ids.add(id);
  for (const child of Object.values(record)) collectFileIdsFromValue(child, ids);
}

function renderPayloadValue(value: unknown, files: Record<number, StorageFile>, projectId: string): React.ReactNode {
  const record = asRecord(value);
  const fileID = record ? numberValue(record.storage_file_id) : undefined;
  if (fileID) {
    return (
      <SubmissionFileLink
        id={fileID}
        label={typeof record?.filename === "string" ? record.filename : undefined}
        mime={typeof record?.mime === "string" ? record.mime : undefined}
        meta={files[fileID]}
        projectId={projectId}
      />
    );
  }
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return <span className="whitespace-pre-wrap break-words">{String(value)}</span>;
  }
  if (value == null) return <span className="text-text-muted">empty</span>;
  return (
    <pre className="text-xs bg-bg-subtle border border-border rounded p-2 overflow-auto whitespace-pre-wrap">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}

function formatBytes(bytes?: number): string {
  if (!bytes || bytes <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function summariseBody(kind: string, body: Record<string, unknown>): string {
  const response = body.response_mode && body.response_mode !== "none" ? ` · response: ${String(body.response_mode)}` : "";
  if (kind === "text") return String(body.markdown || "").slice(0, 80) + response;
  if (kind === "warning" || kind === "checklist_item" || kind === "confirmation") return String(body.text || "");
  if (kind === "link") return String(body.label || body.url || "");
  if (kind === "audio" || kind === "video" || kind === "image" || kind === "document")
    return (body.caption ? String(body.caption) : `[${kind}]`) + response;
  if (kind.startsWith("input_")) return String(body.label || "");
  return "";
}

// ─── Templates ──────────────────────────────────────────────────

function TemplatesTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Template[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<Template | null>(null);
  const [instructions, setInstructions] = useState<Instruction[]>([]);

  const reload = useCallback(() => {
    api<{ templates: Template[] }>("/templates?include_archived=false", projectId)
      .then((d) => {
        setItems(d.templates || []);
        setSelected((cur) => cur ? (d.templates || []).find((t) => t.id === cur.id) || cur : cur);
      })
      .catch(() => setItems([]));
    api<{ instructions: Instruction[] }>("/instructions?include_archived=false", projectId)
      .then((d) => setInstructions(d.instructions || []))
      .catch(() => setInstructions([]));
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
      {selected && (
        <TemplateComposer
          projectId={projectId}
          template={selected}
          instructions={instructions}
          onDone={reload}
          onClose={() => setSelected(null)}
        />
      )}
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
            {t.current_version?.composition && t.current_version.composition.length > 0 && (
              <div className="mt-2 space-y-1">
                {t.current_version.composition.slice(0, 3).map((c) => (
                  <div key={`${c.sort_order}-${c.instruction_id}`} className="text-xs text-text-muted truncate">
                    {c.sort_order + 1}. {instructionLabelById(instructions, c.instruction_id)} · {c.kind}
                  </div>
                ))}
                {t.current_version.composition.length > 3 && (
                  <div className="text-xs text-text-muted">+{t.current_version.composition.length - 3} more</div>
                )}
              </div>
            )}
            <div className="mt-3 flex gap-2">
              <button onClick={() => setSelected(t)} className="px-2 py-1 text-xs border border-border rounded">Manage instructions</button>
            {t.current_version?.status === "draft" && (
              <button
                onClick={() => api(`/templates/${t.id}/publish`, projectId, { method: "POST" }).then(reload)}
                className="px-2 py-1 text-xs border border-border rounded"
              >
                Publish v{t.current_version.version}
              </button>
            )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function TemplateComposer({
  projectId, template, instructions, onDone, onClose,
}: {
  projectId: string;
  template: Template;
  instructions: Instruction[];
  onDone: () => void;
  onClose: () => void;
}) {
  const initial = (template.current_version?.composition || []).map((c) => c.instruction_id);
  const [selectedIds, setSelectedIds] = useState<number[]>(initial);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setSelectedIds((template.current_version?.composition || []).map((c) => c.instruction_id));
  }, [template.id, template.current_version?.id]);

  const save = async () => {
    setBusy(true); setErr(null);
    try {
      await api(`/templates/${template.id}/instructions`, projectId, {
        method: "PUT",
        body: JSON.stringify({
          instructions: selectedIds.map((id) => ({ instruction_id: id })),
        }),
      });
      onDone();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="p-3 border border-border rounded bg-bg-subtle space-y-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">{template.name}</h3>
          <div className="text-xs text-text-muted">/{template.slug} · v{template.current_version?.version || 1} · {template.current_version?.status || "draft"}</div>
        </div>
        <button onClick={onClose} className="px-2 py-1 text-xs border border-border rounded">Close</button>
      </div>
      <InstructionOrderEditor instructions={instructions} selectedIds={selectedIds} onChange={setSelectedIds} />
      {err && <div className="text-rose-600 text-xs">{err}</div>}
      <div className="flex gap-2">
        <button disabled={busy} onClick={save} className="px-3 py-1 text-sm bg-sky-600 text-white rounded disabled:opacity-50">Save composition</button>
        {template.current_version?.status === "draft" && (
          <button
            disabled={busy}
            onClick={() => api(`/templates/${template.id}/publish`, projectId, { method: "POST" }).then(onDone)}
            className="px-3 py-1 text-sm border border-border rounded"
          >
            Publish
          </button>
        )}
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
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Instruction | null>(null);

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
        <div className="flex items-center gap-2">
          <select value={kind} onChange={(e) => setKind(e.target.value)} className="text-sm border border-border rounded px-2 py-1 bg-bg">
            <option value="">All kinds</option>
            {ALL_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
          </select>
          <button onClick={() => { setAdding(true); setEditing(null); }} className="flex items-center gap-1 px-2 py-1 text-sm border border-border rounded">
            <Icon name="plus" /> New
          </button>
        </div>
      </div>
      {adding && <NewInstructionForm projectId={projectId} onDone={() => { setAdding(false); reload(); }} onCancel={() => setAdding(false)} />}
      {editing && (
        <NewInstructionForm
          projectId={projectId}
          instruction={editing}
          onDone={() => { setEditing(null); reload(); }}
          onCancel={() => setEditing(null)}
        />
      )}
      {items?.length === 0 && <div className="p-4 border border-border rounded text-sm text-text-muted">No instructions yet.</div>}
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
                  {i.current_version?.body && (
                    <div className="mt-1 text-xs text-text-muted line-clamp-2">
                      {summariseBody(i.kind, i.current_version.body)}
                    </div>
                  )}
                  <div className="mt-2 flex flex-wrap gap-1">
                    {ALL_KINDS.includes(i.kind) && (
                      <button
                        type="button"
                        onClick={() => { setAdding(false); setEditing(i); }}
                        className="px-2 py-1 text-xs border border-border rounded"
                      >
                        Edit
                      </button>
                    )}
                    {i.current_version?.status === "draft" && (
                      <button
                        onClick={() => api(`/instructions/${i.id}/publish`, projectId, { method: "POST" }).then(reload)}
                        className="px-2 py-1 text-xs border border-border rounded"
                      >
                        Publish
                      </button>
                    )}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function NewInstructionForm({
  projectId, instruction, onDone, onCancel,
}: { projectId: string; instruction?: Instruction; onDone: () => void; onCancel: () => void }) {
  const initialBody = instruction?.current_version?.body || {};
  const editMode = Boolean(instruction);
  const [name, setName] = useState(instruction?.name || "");
  const [kind, setKind] = useState(instruction?.kind || "text");
  const [slug, setSlug] = useState(instruction?.slug || "");
  const [text, setText] = useState(instructionTextFromBody(instruction?.kind || "text", initialBody));
  const [responseMode, setResponseMode] = useState<"none" | "optional" | "required">(responseModeFromBody(initialBody));
  const [selectedFile, setSelectedFile] = useState<StorageFile | null>(storageFileFromBody(instruction?.kind || "text", initialBody));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!editMode) setSelectedFile(null);
  }, [editMode, kind]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const body = buildInstructionBody(kind, text, selectedFile, responseMode);
      if (instruction) {
        await api(`/instructions/${instruction.id}`, projectId, {
          method: "PATCH",
          body: JSON.stringify({ body }),
        });
      } else {
        await api("/instructions", projectId, {
          method: "POST",
          body: JSON.stringify({
            name,
            kind,
            body,
            slug: slug || undefined,
          }),
        });
      }
      onDone();
    } catch (e2) {
      setErr((e2 as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="p-3 border border-border rounded space-y-2 bg-bg-subtle">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">{editMode ? "Edit instruction draft" : "Create instruction"}</h3>
        {editMode && <Pill>Current v{instruction?.current_version?.version}</Pill>}
      </div>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-2">
        <input value={name} onChange={(e) => setName(e.target.value)} disabled={editMode} placeholder="Instruction name" required className="px-2 py-1 text-sm border border-border rounded bg-bg disabled:opacity-60" />
        <select value={kind} onChange={(e) => setKind(e.target.value)} disabled={editMode} className="px-2 py-1 text-sm border border-border rounded bg-bg disabled:opacity-60">
          {ALL_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
        <input value={slug} onChange={(e) => setSlug(e.target.value)} disabled={editMode} placeholder="Slug (optional)" className="px-2 py-1 text-sm border border-border rounded bg-bg disabled:opacity-60" />
      </div>

      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={kind === "text" ? "Instruction text" : "Text shown with this media"}
        rows={4}
        required
        className="w-full px-2 py-1 text-sm border border-border rounded bg-bg"
      />
      <select
        value={responseMode}
        onChange={(e) => setResponseMode(e.target.value as "none" | "optional" | "required")}
        className="w-full px-2 py-1 text-sm border border-border rounded bg-bg"
      >
        <option value="none">No worker response</option>
        <option value="optional">Optional notes/files response</option>
        <option value="required">Required notes/files response</option>
      </select>

      {(kind === "audio" || kind === "video") && (
        <StorageFilePicker
          projectId={projectId}
          kind={kind}
          selected={selectedFile}
          onSelect={setSelectedFile}
        />
      )}

      {err && <div className="text-rose-600 text-xs">{err}</div>}
      <div className="flex gap-2">
        <button disabled={busy || ((kind === "audio" || kind === "video") && !selectedFile)} className="px-3 py-1 text-sm bg-sky-600 text-white rounded disabled:opacity-50">{editMode ? "Save draft" : "Create draft"}</button>
        <button type="button" onClick={onCancel} className="px-3 py-1 text-sm border border-border rounded">Cancel</button>
      </div>
    </form>
  );
}

function StorageFilePicker({
  projectId, kind, selected, onSelect,
}: { projectId: string; kind: "audio" | "video"; selected: StorageFile | null; onSelect: (file: StorageFile | null) => void }) {
  const [query, setQuery] = useState("");
  const [folder, setFolder] = useState("");
  const [folders, setFolders] = useState<StorageFolder[]>([]);
  const [files, setFiles] = useState<StorageFile[]>([]);
  const [busy, setBusy] = useState(false);
  const [folderBusy, setFolderBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const loadFolders = async () => {
      setFolderBusy(true);
      try {
        const params = new URLSearchParams();
        if (folder.trim()) params.set("parent", folder.trim());
        const data = await storageApi<{ folder_details?: StorageFolder[]; folders?: string[] }>(
          `/folders${params.toString() ? `?${params.toString()}` : ""}`,
          projectId,
          { signal: controller.signal },
        );
        const details = data.folder_details || (data.folders || []).map((name) => ({
          name,
          path: joinStorageFolder(folder, name),
        }));
        setFolders(details);
      } catch {
        if (!controller.signal.aborted) setFolders([]);
      } finally {
        if (!controller.signal.aborted) setFolderBusy(false);
      }
    };
    loadFolders();
    return () => controller.abort();
  }, [folder, projectId]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      setBusy(true);
      setErr(null);
      try {
        const params = new URLSearchParams({ limit: "40", content_type: kind + "/" });
        if (query.trim()) params.set("q", query.trim());
        if (folder.trim()) params.set("folder", folder.trim());
        const data = await storageApi<{ files: StorageFile[] }>(`/files?${params.toString()}`, projectId, { signal: controller.signal });
        const wantedPrefix = kind + "/";
        const byKind = (data.files || []).filter((file) => {
          const ct = (file.content_type || "").toLowerCase();
          const name = file.name.toLowerCase();
          return ct.startsWith(wantedPrefix) || fileExtensionMatchesKind(name, kind);
        });
        setFiles(byKind);
      } catch (e) {
        if ((e as Error).name !== "AbortError") {
          setErr((e as Error).message);
          setFiles([]);
        }
      } finally {
        if (!controller.signal.aborted) setBusy(false);
      }
    }, query ? 250 : 0);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [folder, kind, projectId, query]);

  const parentFolder = storageParentFolder(folder);

  return (
    <div className="space-y-2">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
        <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={`Search ${kind} files`} className="px-2 py-1 text-sm border border-border rounded bg-bg" />
        <input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="Current folder, e.g. /recordings/" className="px-2 py-1 text-sm border border-border rounded bg-bg" />
      </div>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <button type="button" onClick={() => setFolder("")} className="px-2 py-1 border border-border rounded">Root</button>
        <button type="button" disabled={!folder} onClick={() => setFolder(parentFolder)} className="px-2 py-1 border border-border rounded disabled:opacity-50">Up</button>
        <span className="text-text-muted truncate">/{folder.replace(/^\/|\/$/g, "")}</span>
      </div>
      <div className="border border-border rounded bg-bg p-2">
        {folderBusy && <div className="text-xs text-text-muted">Loading folders…</div>}
        {!folderBusy && folders.length === 0 && <div className="text-xs text-text-muted">No child folders.</div>}
        {!folderBusy && folders.length > 0 && (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-1">
            {folders.map((f) => (
              <button
                type="button"
                key={f.path}
                onClick={() => setFolder(f.path)}
                className="p-2 text-left border border-border rounded hover:bg-bg-subtle"
              >
                <div className="text-sm truncate">{f.name}</div>
                <div className="text-xs text-text-muted">{f.file_count || 0} files · {formatBytes(f.size_bytes || 0)}</div>
              </button>
            ))}
          </div>
        )}
      </div>
      {selected && (
        <div className="flex items-center justify-between gap-3 p-2 border border-sky-500/30 rounded bg-sky-500/10">
          <div className="min-w-0">
            <div className="text-sm truncate">{selected.name}</div>
            <div className="text-xs text-text-muted truncate">{selected.folder} · {formatBytes(selected.size_bytes || 0)}</div>
          </div>
          <button type="button" onClick={() => onSelect(null)} className="text-xs px-2 py-1 border border-border rounded">Clear</button>
        </div>
      )}
      <div className="border border-border rounded divide-y divide-border bg-bg max-h-56 overflow-auto">
        {busy && <div className="p-2 text-xs text-text-muted">Loading files…</div>}
        {!busy && err && <div className="p-2 text-xs text-rose-600">{err}</div>}
        {!busy && !err && files.length === 0 && <div className="p-2 text-xs text-text-muted">No {kind} files found.</div>}
        {!busy && files.map((file) => (
          <button
            type="button"
            key={file.id}
            onClick={() => onSelect(file)}
            className={"w-full p-2 text-left hover:bg-bg-subtle " + (selected?.id === file.id ? "bg-sky-500/10" : "")}
          >
            <div className="text-sm truncate">{file.name}</div>
            <div className="text-xs text-text-muted truncate">{file.folder} · {file.content_type || "unknown"} · {formatBytes(file.size_bytes || 0)}</div>
          </button>
        ))}
      </div>
    </div>
  );
}

function instructionTextFromBody(kind: string, body: Record<string, unknown>): string {
  if (kind === "text") return String(body.markdown || "");
  return String(body.caption || body.transcript || "");
}

function responseModeFromBody(body: Record<string, unknown>): "none" | "optional" | "required" {
  const mode = String(body.response_mode || "none");
  if (mode === "optional" || mode === "required") return mode;
  return "none";
}

function storageFileFromBody(kind: string, body: Record<string, unknown>): StorageFile | null {
  if (kind !== "audio" && kind !== "video") return null;
  const id = Number(body.storage_file_id || 0);
  if (!id) return null;
  return {
    id,
    name: `Storage file #${id}`,
    folder: "",
    content_type: `${kind}/`,
  };
}

function buildInstructionBody(
  kind: string,
  text: string,
  selectedFile: StorageFile | null,
  responseMode: "none" | "optional" | "required",
): Record<string, unknown> {
  const response = responseMode === "none" ? {} : { response_mode: responseMode };
  if (kind === "text") return { markdown: text, ...response };
  return {
    storage_file_id: selectedFile?.id,
    caption: text,
    ...response,
  };
}

function fileExtensionMatchesKind(name: string, kind: "audio" | "video"): boolean {
  const audio = [".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac"];
  const video = [".mp4", ".mov", ".webm", ".m4v", ".ogv"];
  return (kind === "audio" ? audio : video).some((ext) => name.endsWith(ext));
}

function joinStorageFolder(parent: string, name: string): string {
  const cleanParent = parent && parent !== "/" ? parent.replace(/\/?$/, "/") : "/";
  return `${cleanParent}${name.replace(/^\/|\/$/g, "")}/`;
}

function storageParentFolder(folder: string): string {
  const clean = folder.replace(/^\/|\/$/g, "");
  if (!clean) return "";
  const parts = clean.split("/");
  parts.pop();
  return parts.length ? `/${parts.join("/")}/` : "";
}

function formatBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = n;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return `${value.toFixed(value >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

const ALL_KINDS = ["text", "audio", "video"];

// ─── Workers ────────────────────────────────────────────────────

function WorkersTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Worker[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [skills, setSkills] = useState<Skill[]>([]);
  const existingContactIds = useMemo(
    () => new Set((items || []).map((wk) => wk.contact_id)),
    [items],
  );

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
          existingContactIds={existingContactIds}
          onDone={() => { setAdding(false); reload(); }}
          onCancel={() => setAdding(false)}
        />
      )}
      <div className="border border-border rounded divide-y divide-border">
        {items?.length === 0 && <div className="p-4 text-text-muted text-sm">No workers yet.</div>}
        {items?.map((wk) => (
          <div key={wk.id} className="p-3 flex items-start gap-3">
            <span className="text-text-muted mt-1"><Icon name="user" /></span>
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium">{wk.contact ? contactName(wk.contact) : `Worker #${wk.id}`}</div>
              <div className="text-xs text-text-muted">
                {wk.contact ? contactLine(wk.contact) || "—" : "—"}
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
  projectId, skills, existingContactIds, onDone, onCancel,
}: {
  projectId: string;
  skills: Skill[];
  existingContactIds: Set<number>;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [mode, setMode] = useState<"crm" | "new">("crm");
  const [crmQuery, setCrmQuery] = useState("");
  const [crmResults, setCrmResults] = useState<CrmContact[]>([]);
  const [crmBusy, setCrmBusy] = useState(false);
  const [crmErr, setCrmErr] = useState<string | null>(null);
  const [selectedContact, setSelectedContact] = useState<CrmContact | null>(null);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [company, setCompany] = useState("");
  const [channel, setChannel] = useState("");
  const [notes, setNotes] = useState("");
  const [skillIds, setSkillIds] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (mode !== "crm") return;
    const controller = new AbortController();
    const query = crmQuery.trim();
    const timer = window.setTimeout(async () => {
      setCrmBusy(true);
      setCrmErr(null);
      try {
        const path = `/contacts?limit=12${query ? `&q=${encodeURIComponent(query)}` : ""}`;
        const data = await crmApi<{ contacts: CrmContact[] }>(path, projectId, { signal: controller.signal });
        setCrmResults(data.contacts || []);
      } catch (e) {
        if ((e as Error).name !== "AbortError") {
          setCrmErr((e as Error).message);
          setCrmResults([]);
        }
      } finally {
        if (!controller.signal.aborted) setCrmBusy(false);
      }
    }, query ? 250 : 0);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [crmQuery, mode, projectId]);

  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        if (mode === "crm" && !selectedContact) { setErr("Select a CRM contact"); return; }
        if (mode === "crm" && selectedContact && existingContactIds.has(selectedContact.id)) {
          setErr("That contact is already a worker");
          return;
        }
        if (mode === "new" && !email && !phone) { setErr("Email or phone required"); return; }
        setBusy(true); setErr(null);
        try {
          if (mode === "crm" && selectedContact) {
            await api("/workers/promote", projectId, {
              method: "POST",
              body: JSON.stringify({
                contact_id: selectedContact.id,
                default_channel: channel || undefined,
                notes: notes || undefined,
                skill_ids: skillIds.length ? skillIds : undefined,
              }),
            });
          } else {
            await api("/workers", projectId, {
              method: "POST",
              body: JSON.stringify({
                name, email: email || undefined, phone: phone || undefined,
                company: company || undefined,
                default_channel: channel || undefined,
                notes: notes || undefined,
                skill_ids: skillIds.length ? skillIds : undefined,
              }),
            });
          }
          onDone();
        } catch (e2) { setErr((e2 as Error).message); }
        finally { setBusy(false); }
      }}
      className="p-3 border border-border rounded space-y-2 bg-bg-subtle"
    >
      <div className="inline-flex rounded border border-border overflow-hidden text-sm">
        <button
          type="button"
          onClick={() => { setMode("crm"); setErr(null); }}
          className={"px-3 py-1 " + (mode === "crm" ? "bg-sky-600 text-white" : "bg-bg text-text-muted")}
        >
          From CRM
        </button>
        <button
          type="button"
          onClick={() => { setMode("new"); setErr(null); }}
          className={"px-3 py-1 border-l border-border " + (mode === "new" ? "bg-sky-600 text-white" : "bg-bg text-text-muted")}
        >
          New contact
        </button>
      </div>

      {mode === "crm" ? (
        <div className="space-y-2">
          <input
            value={crmQuery}
            onChange={(e) => setCrmQuery(e.target.value)}
            placeholder="Search CRM contacts"
            className="w-full px-2 py-1 text-sm border border-border rounded bg-bg"
          />
          <div className="border border-border rounded divide-y divide-border bg-bg max-h-52 overflow-auto">
            {crmBusy && <div className="p-2 text-xs text-text-muted">Searching…</div>}
            {!crmBusy && crmErr && <div className="p-2 text-xs text-rose-600">{crmErr}</div>}
            {!crmBusy && !crmErr && crmResults.length === 0 && (
              <div className="p-2 text-xs text-text-muted">No contacts found</div>
            )}
            {!crmBusy && crmResults.map((c) => {
              const alreadyWorker = existingContactIds.has(c.id);
              const selected = selectedContact?.id === c.id;
              return (
                <button
                  type="button"
                  key={c.id}
                  disabled={alreadyWorker}
                  onClick={() => { setSelectedContact(c); setErr(null); }}
                  className={
                    "w-full text-left p-2 flex items-center justify-between gap-3 " +
                    (selected ? "bg-sky-500/10" : "hover:bg-bg-subtle ") +
                    (alreadyWorker ? "opacity-60 cursor-not-allowed" : "")
                  }
                >
                  <span className="min-w-0">
                    <span className="block text-sm font-medium truncate">{contactName(c)}</span>
                    <span className="block text-xs text-text-muted truncate">{contactLine(c) || "—"}</span>
                  </span>
                  {alreadyWorker ? <Pill>Already worker</Pill> : selected ? <Pill tone="info">Selected</Pill> : null}
                </button>
              );
            })}
          </div>
        </div>
      ) : (
        <>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Display name" required={mode === "new"} className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" />
          <div className="grid grid-cols-2 gap-2">
            <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email" type="email" className="px-2 py-1 text-sm border border-border rounded bg-bg" />
            <input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="Phone (E.164)" className="px-2 py-1 text-sm border border-border rounded bg-bg" />
          </div>
          <input value={company} onChange={(e) => setCompany(e.target.value)} placeholder="Company (optional)" className="w-full px-2 py-1 text-sm border border-border rounded bg-bg" />
        </>
      )}

      <select value={channel} onChange={(e) => setChannel(e.target.value)} className="px-2 py-1 text-sm border border-border rounded bg-bg">
        <option value="">Default channel — let CRM pick</option>
        <option value="email">Email</option>
        <option value="sms">SMS</option>
        <option value="whatsapp">WhatsApp</option>
      </select>
      <textarea
        value={notes}
        onChange={(e) => setNotes(e.target.value)}
        placeholder="Notes (optional)"
        rows={2}
        className="w-full px-2 py-1 text-sm border border-border rounded bg-bg"
      />
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
        <button disabled={busy || (mode === "crm" && !selectedContact)} className="px-3 py-1 text-sm bg-sky-600 text-white rounded disabled:opacity-50">Add worker</button>
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
