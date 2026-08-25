// GigsPanel — dashboard surface for the gigs app. Four tabs:
//   Queue, Offers, Rates, Marketplace, Templates, Instructions, Workers
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
  pay_profile?: { worker_id:number; currency?:string; pay_grade?: PayGrade };
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
  overrides?: Record<string, unknown>;
}
interface TemplateVersion {
  id: number;
  version: number;
  status: "draft" | "active" | "archived";
  title_template: string;
  default_due_hours?: number;
  default_access_grace_days?: number;
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
  scheduled_for?: string;
  due_at?: string;
  deadline_at?: string;
  overdue_at?: string;
  overdue?: boolean;
  access_expires_at?: string;
  access_expiry_source?: "none" | "due" | "custom";
  template_version_id?: number;
  composition?: Array<{
    sort_order: number;
    instruction_kind: string;
    instruction_name?: string;
    rendered_body: Record<string, unknown>;
    result_key?: string;
  }>;
  assignments?: Array<{
    id: number;
    worker_id: number;
    status: string;
    mode?: string;
    notify_worker?: boolean;
    submitted_at?: string;
    worker_url?: string;
    access_expires_at?: string;
    access_expiry_source?: "none" | "due" | "custom";
    submission?: Submission;
  }>;
  result?: Record<string, unknown>;
  submission?: Submission;
  submissions?: Submission[];
  rejection_reason?: string;
  compensation?: GigCompensation;
}
interface PayGrade { id: number; slug: string; name: string; rank: number; description?: string; default_pricing_model?: string; default_amount_minor?: number; currency?: string; active: boolean; }
interface RateCard { id: number; template_id?: number; offer_package_id?: number; pay_grade_id?: number; worker_id?: number; pricing_model: string; amount_minor: number; currency: string; unit?: string; status: string; source: string; }
interface OfferPackage { id: number; offer_id: number; slug: string; name: string; tier?: string; description?: string; scope?: Record<string, unknown>; pricing_model: string; quantity?: number; unit?: string; delivery_days?: number; revisions?: number; customer_amount_minor?: number; currency?: string; catalog_price_id?: number; active: boolean; sort_order: number; }
interface StandardOffer { id: number; template_id: number; slug: string; name: string; description?: string; category?: string; visibility: string; status: string; version: number; catalog_product_id?: number; packages?: OfferPackage[]; }
interface GigCompensation { pricing_model: string; rate_amount_minor: number; quantity: number; unit?: string; worker_amount_minor: number; customer_amount_minor?: number; currency: string; rate_source: string; payable_status: string; payable_bill_id?: number; }
interface JobPost { id: number; title: string; description?: string; status: string; visibility: string; budget_min_minor?: number; budget_max_minor?: number; currency?: string; deadline_at?: string; }
interface ContractRecord { id: number; title: string; source_type: string; worker_id?: number; pricing_model: string; customer_amount_minor?: number; worker_amount_minor?: number; currency: string; status: string; milestones?: Array<{ id:number; title:string; status:string; worker_amount_minor?:number; currency:string }>; }
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

type ContentBlockType = "markdown" | "image" | "callout" | "divider";
interface ContentBlockDraft {
  key: string;
  type: ContentBlockType;
  markdown: string;
  caption: string;
  alt: string;
  text: string;
  tone: "info" | "tip" | "warning";
  file: StorageFile | null;
}
interface PublicDomain {
  id: number;
  hostname: string;
  apex_domain: string;
  dns_name: string;
  dns_type: string;
  dns_value: string;
  dns_managed: boolean;
  is_default: boolean;
  status: string;
  status_detail?: string;
}
interface AvailableDomain {
  id: number;
  name: string;
  dns_provider_slug?: string;
}
interface PublicDomainsResponse {
  public_domains: PublicDomain[];
  available_domains: AvailableDomain[];
  suggested_dns_target?: string;
  domains_bound: boolean;
  domains_error?: string;
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
  const j = await parseResponse(res);
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
  const j = await parseResponse(res);
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
  const j = await parseResponse(res);
  if (!res.ok || (j && typeof j === "object" && "error" in j && (j as any).error)) {
    throw new Error((j as any)?.error || res.statusText);
  }
  return j as T;
}

async function parseResponse(res: Response): Promise<any> {
  const text = await res.text();
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(res.ok ? "The server returned an invalid response." : text.slice(0, 300));
  }
}

// ─── small UI primitives ─────────────────────────────────────────

function Pill({ children, tone }: { children: React.ReactNode; tone?: string }) {
  const t = tone || "default";
  const colors: Record<string, string> = {
    default: "bg-bg-input text-text-muted border-border",
    success: "bg-green/10 text-green border-green/30",
    warn:    "bg-yellow/10  text-yellow  border-yellow/30",
    danger:  "bg-red/10   text-red   border-red/30",
    info:    "bg-accent/10    text-accent    border-accent/30",
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

function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}

type ButtonTone = "primary" | "secondary" | "danger" | "success" | "ghost";
type ButtonSize = "xs" | "sm" | "md";

function Button({
  tone = "secondary",
  size = "sm",
  className,
  children,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  tone?: ButtonTone;
  size?: ButtonSize;
}) {
  const tones: Record<ButtonTone, string> = {
    primary: "bg-accent text-white border-accent hover:opacity-90 hover:border-accent",
    secondary: "bg-bg-card border-border text-text hover:bg-bg-input hover:border-text-muted/40",
    danger: "bg-red text-white border-red hover:opacity-90 hover:border-red",
    success: "bg-green text-white border-green hover:opacity-90 hover:border-green",
    ghost: "bg-transparent border-transparent text-text-muted hover:text-text hover:bg-bg-input",
  };
  const sizes: Record<ButtonSize, string> = {
    xs: "px-2 py-1 text-xs",
    sm: "px-3 py-1.5 text-sm",
    md: "px-4 py-2 text-sm",
  };
  return (
    <button
      {...props}
      className={cx(
        "inline-flex items-center justify-center gap-1.5 rounded border font-medium transition-colors",
        "focus:outline-none focus:ring-2 focus:ring-accent/40 disabled:opacity-50 disabled:cursor-not-allowed",
        tones[tone],
        sizes[size],
        className,
      )}
    >
      {children}
    </button>
  );
}

function Field({
  as = "input",
  className,
  ...props
}: (
  React.InputHTMLAttributes<HTMLInputElement> |
  React.SelectHTMLAttributes<HTMLSelectElement> |
  React.TextareaHTMLAttributes<HTMLTextAreaElement>
) & { as?: "input" | "select" | "textarea" }) {
  const cls = cx(
    "w-full rounded border border-border bg-bg-input px-3 py-2 text-sm text-text",
    "placeholder:text-text-muted focus:outline-none focus:ring-2 focus:ring-accent/30 focus:border-accent/60",
    "disabled:opacity-60 disabled:cursor-not-allowed",
    className,
  );
  if (as === "select") return <select {...(props as React.SelectHTMLAttributes<HTMLSelectElement>)} className={cls} />;
  if (as === "textarea") return <textarea {...(props as React.TextareaHTMLAttributes<HTMLTextAreaElement>)} className={cls} />;
  return <input {...(props as React.InputHTMLAttributes<HTMLInputElement>)} className={cls} />;
}

function Panel({
  children,
  className,
}: { children: React.ReactNode; className?: string }) {
  return (
    <section className={cx("rounded border border-border bg-bg-card", className)}>
      {children}
    </section>
  );
}

function SectionHeading({
  title,
  action,
  className,
}: { title: string; action?: React.ReactNode; className?: string }) {
  return (
    <div className={cx("flex items-center justify-between gap-3", className)}>
      <h2 className="text-lg font-semibold text-text">{title}</h2>
      {action}
    </div>
  );
}

function Segmented<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: Array<{ value: T; label: string }>;
  onChange: (value: T) => void;
}) {
  return (
    <div className="inline-flex rounded border border-border bg-bg-input p-0.5 text-sm">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          onClick={() => onChange(option.value)}
          className={cx(
            "px-3 py-1.5 rounded transition-colors",
            value === option.value
              ? "bg-accent text-white"
              : "text-text-muted hover:text-text hover:bg-bg-subtle",
          )}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

function contactName(c: CrmContact): string {
  return c.display_name || c.primary_email || c.primary_phone || `Contact #${c.id}`;
}

function contactLine(c: CrmContact): string {
  return [c.primary_email || c.primary_phone, c.company].filter(Boolean).join(" · ");
}

// ─── shell ───────────────────────────────────────────────────────

type Tab = "queue" | "offers" | "rates" | "marketplace" | "templates" | "instructions" | "workers" | "public-links";

export default function GigsPanel(props: NativePanelProps) {
  const { projectId } = props;
  const [tab, setTab] = useState<Tab>("queue");
  return (
    <div className="flex flex-col h-full bg-bg text-text">
      <nav className="flex gap-1 border-b border-border px-3 pt-3 overflow-x-auto shrink-0">
        {(["queue","offers","rates","marketplace","templates","instructions","workers","public-links"] as Tab[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={
              "px-4 py-2 text-sm font-medium capitalize rounded-t border transition-colors " +
              (tab === t
                ? "bg-bg-subtle text-text border-border border-b-bg-subtle"
                : "border-transparent text-text-muted hover:text-text hover:bg-bg-subtle")
            }
          >
            {t === "public-links" ? "Public links" : t}
          </button>
        ))}
      </nav>
      <div className="flex-1 overflow-auto">
        {tab === "queue"        && <QueueTab projectId={projectId} />}
        {tab === "offers"       && <OffersTab projectId={projectId} />}
        {tab === "rates"        && <RatesTab projectId={projectId} />}
        {tab === "marketplace"  && <MarketplaceTab projectId={projectId} />}
        {tab === "templates"    && <TemplatesTab projectId={projectId} />}
        {tab === "instructions" && <InstructionsTab projectId={projectId} />}
        {tab === "workers"      && <WorkersTab projectId={projectId} />}
        {tab === "public-links" && <PublicLinksTab projectId={projectId} />}
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
      const data = await api<{ gigs: Gig[] }>(`/gigs?summary=true&status=${encodeURIComponent(status)}`, projectId);
      setGigs(data.gigs || []);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [projectId, status]);
  useEffect(() => { reload(); }, [reload]);

  return (
    <div className="grid grid-cols-1 lg:grid-cols-[minmax(300px,380px)_1fr] h-full">
      <aside className={cx("border-r border-border overflow-auto bg-bg", (selected || adding) && "hidden lg:block")}>
        <div className="sticky top-0 z-10 bg-bg border-b border-border p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-text">Gigs</h2>
            <Button
              onClick={() => { setAdding(true); setSelected(null); }}
              tone="primary"
              size="sm"
            >
              <Icon name="plus" /> New gig
            </Button>
          </div>
          <Field as="select" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="open,offered,accepted,submitted">Active + submitted</option>
            <option value="open">Open</option>
            <option value="offered,accepted">Assigned</option>
            <option value="submitted">Submitted</option>
            <option value="reviewed">Reviewed</option>
            <option value="cancelled,expired">Closed without acceptance</option>
            <option value="open,offered,accepted,submitted,reviewed,cancelled,expired">All</option>
          </Field>
        </div>
        {err && <div className="m-3 p-3 rounded border border-red/30 bg-red/10 text-red text-sm">{err}</div>}
        {gigs?.length === 0 && <div className="p-6 text-text-muted text-sm">No gigs in this view.</div>}
        {gigs?.map((g) => (
          <button
            key={g.id}
            onClick={() => { setSelected(g); setAdding(false); }}
            className={
              "w-full text-left px-4 py-3 border-b border-border transition-colors " +
              (selected?.id === g.id ? "bg-accent/10 border-l-4 border-l-accent" : "hover:bg-bg-subtle border-l-4 border-l-transparent")
            }
          >
            <div className="text-sm truncate">{g.title}</div>
            <div className="flex items-center gap-2 mt-1">
              <Pill tone={gigPillTone(g.status)}>{g.status}</Pill>
              {g.scheduled_for && <span className="text-xs text-text-muted">scheduled {formatDate(g.scheduled_for)}</span>}
              {g.overdue ? <Pill tone="danger">overdue</Pill> : g.due_at && <span className="text-xs text-text-muted">due {formatDate(g.due_at)}</span>}
            </div>
          </button>
        ))}
      </aside>
      <section className={cx("p-4 md:p-5 overflow-auto", !selected && !adding && "hidden lg:block")}>
        {(selected || adding) && (
          <Button className="mb-4 lg:hidden" tone="ghost" onClick={() => { setSelected(null); setAdding(false); }}>
            Back to gigs
          </Button>
        )}
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
          <Panel className="p-6 text-sm text-text-muted">Pick a gig.</Panel>
        )}
      </section>
    </div>
  );
}

function GigDetail({ gig, projectId, onChange }: { gig: Gig; projectId: string; onChange: () => void }) {
  const [full, setFull] = useState<Gig | null>(null);
  const [busy, setBusy] = useState(false);
  const [assigning, setAssigning] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [cancelReason, setCancelReason] = useState("");
  useEffect(() => {
    setActionErr(null);
    api<{ gig: Gig }>(`/gigs/${gig.id}`, projectId).then((d) => setFull(d.gig)).catch((e) => {
      setFull(gig);
      setActionErr((e as Error).message);
    });
  }, [gig.id, projectId, gig]);
  const g = full || gig;

  const doAction = async (path: string, body: unknown) => {
    setBusy(true);
    setActionErr(null);
    try {
      await api(`/gigs/${gig.id}/${path}`, projectId, { method: "POST", body: JSON.stringify(body || {}) });
      onChange();
      const d = await api<{ gig: Gig }>(`/gigs/${gig.id}`, projectId);
      setFull(d.gig);
      return true;
    } catch (e) { setActionErr((e as Error).message); return false; } finally { setBusy(false); }
  };

  return (
    <div className="space-y-5">
      {actionErr && <div role="alert" className="p-3 rounded border border-red/30 bg-red/10 text-red text-sm">{actionErr}</div>}
      <Panel className="p-4">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-xl font-semibold text-text">{g.title}</h2>
            <div className="flex items-center gap-2 mt-2 text-sm text-text-muted">
              <Pill tone={gigPillTone(g.status)}>{g.status}</Pill>
              {g.scheduled_for && <span>scheduled {formatDate(g.scheduled_for)}</span>}
              {g.overdue ? <Pill tone="danger">overdue</Pill> : g.due_at && <span>due {formatDate(g.due_at)}</span>}
            </div>
          </div>
          {!(["reviewed", "cancelled", "expired"] as string[]).includes(g.status) && (
            <Button tone="ghost" onClick={() => setEditingSchedule((value) => !value)}>
              {editingSchedule ? "Close timing" : "Edit timing"}
            </Button>
          )}
        </div>
      </Panel>

      {editingSchedule ? (
        <ScheduleEditor
          gig={g}
          busy={busy}
          onCancel={() => setEditingSchedule(false)}
          onSave={async (body) => {
            const saved = await doAction("schedule", body);
            if (saved) setEditingSchedule(false);
            return saved;
          }}
        />
      ) : (
        <Panel className="p-4">
          <div className="grid grid-cols-1 gap-3 text-sm md:grid-cols-3">
            <TimingValue label="Scheduled for" value={g.scheduled_for} empty="Not scheduled" />
            <TimingValue label="Due by" value={g.due_at} empty="No due date" tone={g.overdue ? "danger" : undefined} />
            <TimingValue label="Worker access" value={g.access_expires_at} empty="No automatic expiry" />
          </div>
        </Panel>
      )}

      {g.compensation && (
        <Panel className="p-4">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-xs uppercase tracking-wide text-text-muted">Agreed worker compensation</div>
              <div className="mt-1 text-lg font-semibold">{formatMoneyMinor(g.compensation.worker_amount_minor, g.compensation.currency)}</div>
              <div className="text-xs text-text-muted">
                {g.compensation.pricing_model}{g.compensation.unit ? ` · ${g.compensation.quantity} ${g.compensation.unit}` : ""} · {g.compensation.rate_source}
              </div>
            </div>
            <Pill tone={g.compensation.payable_status === "created" ? "success" : g.compensation.payable_status === "failed" ? "danger" : "default"}>
              payable: {g.compensation.payable_status}
            </Pill>
          </div>
        </Panel>
      )}

      <div>
        <h3 className="text-sm font-semibold text-text-muted uppercase tracking-wide">Composition</h3>
        <Panel className="mt-2 divide-y divide-border overflow-hidden">
          {(g.composition || []).map((c, i) => (
            <div key={i} className="p-3 text-sm flex gap-3 items-start">
              <span className="text-accent mt-0.5"><Icon name={kindIcon(c.instruction_kind)} /></span>
              <div className="flex-1 min-w-0">
                <div className="font-medium text-text">{c.instruction_name || `Instruction ${i + 1}`}</div>
                <div className="text-xs text-text-muted">{c.instruction_kind}{c.result_key ? ` -> ${c.result_key}` : ""}</div>
                <div className="truncate text-text">{summariseBody(c.instruction_kind, c.rendered_body)}</div>
              </div>
            </div>
          ))}
        </Panel>
      </div>

      <div>
        <h3 className="text-sm font-semibold text-text-muted uppercase tracking-wide">Assignments</h3>
        <Panel className="mt-2 divide-y divide-border overflow-hidden">
          {(g.assignments || []).length === 0 && <div className="p-3 text-text-muted text-sm">Unassigned.</div>}
          {(g.assignments || []).map((a) => (
            <div key={a.id} className="p-3 text-sm flex items-center justify-between gap-3">
              <div>
                Worker #{a.worker_id} <Pill tone={a.status === "submitted" ? "info" : "default"}>{a.status}</Pill>
                {a.mode && <span className="ml-2 text-xs text-text-muted">{a.mode}</span>}
                {a.submitted_at && <span className="ml-2 text-xs text-text-muted">submitted {formatDate(a.submitted_at)}</span>}
              </div>
              {a.worker_url && (
                <a className="text-accent text-xs underline" target="_blank" rel="noreferrer" href={a.worker_url}>worker link</a>
              )}
            </div>
          ))}
        </Panel>
      </div>
      {(g.status === "open" || g.status === "offered") && (
        <div>
          {assigning ? (
            <AssignGigForm
              projectId={projectId}
              busy={busy}
              onCancel={() => setAssigning(false)}
              onAssign={(workerId, mode, notifyWorker, publicDomainId) =>
                doAction("assign", {
                  worker_id: workerId,
                  mode,
                  notify_worker: notifyWorker,
                  public_domain_id: publicDomainId || undefined,
                }).then(() => setAssigning(false))
              }
            />
          ) : (
            <Button onClick={() => setAssigning(true)} tone="secondary" size="md">
              Assign worker
            </Button>
          )}
        </div>
      )}

      <SubmissionReview gig={g} projectId={projectId} busy={busy} onAction={doAction} />

      <div className="flex gap-2">
        {(g.status === "open" || g.status === "offered" || g.status === "accepted") && (
          cancelOpen ? (
            <Panel className="w-full p-3 space-y-2">
              <label className="block text-sm font-medium text-text">Cancel gig</label>
              <Field value={cancelReason} onChange={(e) => setCancelReason(e.target.value)} placeholder="Reason (optional)" />
              <div className="flex gap-2">
                <Button disabled={busy} tone="danger" onClick={() => doAction("cancel", { reason: cancelReason.trim() }).then(() => setCancelOpen(false))}>Confirm cancel</Button>
                <Button disabled={busy} tone="ghost" onClick={() => setCancelOpen(false)}>Keep gig</Button>
              </div>
            </Panel>
          ) : <Button disabled={busy} tone="danger" size="md" onClick={() => setCancelOpen(true)}>Cancel gig</Button>
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
  const submissions = useMemo(() => {
    if (gig.submissions?.length) return gig.submissions;
    return gig.submission ? [gig.submission] : [];
  }, [gig.submissions, gig.submission]);
  const [selectedSubmissionId, setSelectedSubmissionId] = useState<number | null>(null);
  useEffect(() => {
    if (!submissions.length) {
      setSelectedSubmissionId(null);
      return;
    }
    if (!submissions.some((item) => item.id === selectedSubmissionId)) {
      setSelectedSubmissionId(submissions[0].id);
    }
  }, [submissions, selectedSubmissionId]);
  const submission = submissions.find((item) => item.id === selectedSubmissionId) || submissions[0];
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
    <Panel className="overflow-hidden">
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

      {submissions.length > 1 && (
        <div className="flex gap-2 overflow-x-auto p-3 border-b border-border bg-bg">
          {submissions.map((item) => (
            <Button
              key={item.id}
              tone={item.id === submission?.id ? "primary" : "secondary"}
              size="xs"
              onClick={() => setSelectedSubmissionId(item.id)}
            >
              Submission #{item.id} · assignment #{item.assignment_id}
            </Button>
          ))}
        </div>
      )}

      {submission || gig.result ? (
        <div className="p-3 space-y-4">
          {responses.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold text-text-muted uppercase tracking-wide">Instruction responses</h4>
              <Panel className="mt-2 divide-y divide-border overflow-hidden bg-bg">
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
              </Panel>
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
              {fileErr && <div className="mt-2 text-xs text-yellow">Could not load file metadata: {fileErr}</div>}
            </div>
          ) : null}

          {extraEntries.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold text-text-muted uppercase tracking-wide">Result fields</h4>
              <Panel className="mt-2 divide-y divide-border overflow-hidden bg-bg">
                {extraEntries.map(([key, value]) => (
                  <div key={key} className="p-3 grid grid-cols-1 md:grid-cols-[160px_1fr] gap-2 text-sm">
                    <div className="text-text-muted">{key}</div>
                    <div className="min-w-0">{renderPayloadValue(value, files, projectId)}</div>
                  </div>
                ))}
              </Panel>
            </div>
          )}

          {responses.length === 0 && extraEntries.length === 0 && (
            <div className="text-sm text-text-muted">The worker submitted without notes, result fields, or files.</div>
          )}

          {gig.status === "submitted" && (
            <div className="pt-3 border-t border-border space-y-3">
              <Field
                as="textarea"
                value={acceptNotes}
                onChange={(e) => setAcceptNotes(e.target.value)}
                rows={2}
                placeholder="Acceptance notes for CRM timeline"
              />
              <div className="flex flex-wrap gap-2">
                <Button
                  disabled={busy}
                  onClick={() => onAction("accept", { submission_id: submission?.id, notes: acceptNotes })}
                  tone="success"
                  size="md"
                >
                  Accept submission
                </Button>
                <Field
                  value={rejectReason}
                  onChange={(e) => setRejectReason(e.target.value)}
                  placeholder="Rejection reason"
                  className="min-w-[220px] flex-1"
                />
                <label className="flex items-center gap-2 text-sm text-text-muted">
                  <input type="checkbox" checked={reopen} onChange={(e) => setReopen(e.target.checked)} />
                  Reopen gig
                </label>
                <Button
                  disabled={busy || !rejectReason.trim()}
                  onClick={() => onAction("reject", { submission_id: submission?.id, reason: rejectReason.trim(), reopen })}
                  tone="danger"
                  size="md"
                >
                  Reject submission
                </Button>
              </div>
            </div>
          )}
        </div>
      ) : (
        <div className="p-3 text-sm text-yellow">
          This gig is marked submitted, but the latest submission was not returned by the backend.
        </div>
      )}
    </Panel>
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
    return <span className="px-2 py-1 text-xs border border-border rounded bg-bg text-text-muted">{label || "File"}</span>;
  }
  const href = meta?.url || `/api/apps/storage/files/${id}/content?project_id=${encodeURIComponent(projectId)}`;
  const name = label || meta?.name || `File #${id}`;
  const detail = meta ? [meta.content_type, formatBytes(meta.size_bytes)].filter(Boolean).join(" · ") : mime || `Storage #${id}`;
  return (
    <a
      className="inline-flex flex-col px-2 py-1 text-xs border border-border rounded bg-bg hover:bg-bg-subtle"
      href={href}
      target="_blank"
      rel="noreferrer"
    >
      <span className="font-medium text-accent">{name}</span>
      <span className="text-text-muted">{detail}</span>
    </a>
  );
}

function TimingValue({ label, value, empty, tone }: { label: string; value?: string; empty: string; tone?: "danger" }) {
  return (
    <div>
      <div className="text-xs font-medium text-text-muted">{label}</div>
      <div className={"mt-1 " + (tone === "danger" ? "text-red" : "text-text")}>{value ? formatDate(value) : empty}</div>
    </div>
  );
}

function inferredAccessMode(gig: Gig): "none" | "7" | "14" | "custom" {
  if (!gig.access_expires_at) return "none";
  if (gig.access_expiry_source === "due" && gig.due_at) {
    const days = Math.round((Date.parse(gig.access_expires_at) - Date.parse(gig.due_at)) / 86_400_000);
    if (days === 7 || days === 14) return String(days) as "7" | "14";
  }
  return "custom";
}

function ScheduleEditor({ gig, busy, onSave, onCancel }: {
  gig: Gig;
  busy: boolean;
  onSave: (body: Record<string, unknown>) => Promise<boolean>;
  onCancel: () => void;
}) {
  const [scheduledLocal, setScheduledLocal] = useState(toDateTimeLocal(gig.scheduled_for));
  const [dueLocal, setDueLocal] = useState(toDateTimeLocal(gig.due_at));
  const [accessMode, setAccessMode] = useState<"none" | "7" | "14" | "custom">(inferredAccessMode(gig));
  const [accessLocal, setAccessLocal] = useState(toDateTimeLocal(gig.access_expires_at));
  return (
    <Panel className="p-4">
      <form className="space-y-4" onSubmit={async (event) => {
        event.preventDefault();
        const body: Record<string, unknown> = {
          scheduled_for: scheduledLocal ? new Date(scheduledLocal).toISOString() : null,
          due_at: dueLocal ? new Date(dueLocal).toISOString() : null,
        };
        if (accessMode === "none") body.access_expires_at = null;
        if (accessMode === "7" || accessMode === "14") body.access_grace_days = Number(accessMode);
        if (accessMode === "custom") body.access_expires_at = accessLocal ? new Date(accessLocal).toISOString() : null;
        await onSave(body);
      }}>
        <div>
          <div className="text-sm font-semibold">Timing and access</div>
          <div className="mt-0.5 text-xs text-text-muted">Changing the due date never closes the worker page.</div>
        </div>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label><span className="mb-1 block text-xs text-text-muted">Scheduled for</span><Field type="datetime-local" value={scheduledLocal} onChange={(e) => setScheduledLocal(e.target.value)} /></label>
          <label><span className="mb-1 block text-xs text-text-muted">Due by</span><Field type="datetime-local" value={dueLocal} onChange={(e) => setDueLocal(e.target.value)} /></label>
          <label><span className="mb-1 block text-xs text-text-muted">Worker access ends</span><Field as="select" value={accessMode} onChange={(e) => setAccessMode(e.target.value as typeof accessMode)}><option value="none">No automatic expiry</option><option value="7">7 days after due</option><option value="14">14 days after due</option><option value="custom">Custom date</option></Field></label>
          {accessMode === "custom" && <label><span className="mb-1 block text-xs text-text-muted">Access expires at</span><Field type="datetime-local" value={accessLocal} onChange={(e) => setAccessLocal(e.target.value)} required /></label>}
        </div>
        <div className="flex gap-2"><Button tone="primary" disabled={busy}>Save timing</Button><Button type="button" tone="ghost" onClick={onCancel} disabled={busy}>Cancel</Button></div>
      </form>
    </Panel>
  );
}

function NewGigForm({
  projectId, onDone, onCancel,
}: { projectId: string; onDone: (gig: Gig) => void; onCancel: () => void }) {
  const [mode, setMode] = useState<"offer" | "template" | "instructions">("offer");
  const [templates, setTemplates] = useState<Template[]>([]);
  const [instructions, setInstructions] = useState<Instruction[]>([]);
  const [offers, setOffers] = useState<StandardOffer[]>([]);
  const [offerId, setOfferId] = useState("");
  const [packageId, setPackageId] = useState("");
  const [quantity, setQuantity] = useState("1");
  const [templateId, setTemplateId] = useState("");
  const [title, setTitle] = useState("");
  const [varsText, setVarsText] = useState("{}");
  const [workerId, setWorkerId] = useState("");
  const [notifyWorker, setNotifyWorker] = useState(false);
  const [publicDomainId, setPublicDomainId] = useState("");
  const [scheduledLocal, setScheduledLocal] = useState("");
  const [dueLocal, setDueLocal] = useState("");
  const [accessMode, setAccessMode] = useState<"none" | "7" | "14" | "custom">("none");
  const [accessLocal, setAccessLocal] = useState("");
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
    api<{ offers: StandardOffer[] }>("/offers?status=active", projectId)
      .then((d) => {
        setOffers(d.offers || []);
        if (!offerId && d.offers?.[0]) {
          setOfferId(String(d.offers[0].id));
          const first = d.offers[0].packages?.find((p) => p.active);
          if (first) { setPackageId(String(first.id)); setQuantity(String(first.quantity || 1)); }
        }
      }).catch(() => setOffers([]));
  }, [projectId, templateId, offerId]);

  const selectedTemplate = templates.find((t) => String(t.id) === templateId);
  const selectedOffer = offers.find((o) => String(o.id) === offerId);
  const selectedPackage = selectedOffer?.packages?.find((p) => String(p.id) === packageId);
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
        scheduled_for: scheduledLocal ? new Date(scheduledLocal).toISOString() : undefined,
        due_at: dueLocal ? new Date(dueLocal).toISOString() : undefined,
        access_grace_days: accessMode === "7" || accessMode === "14" ? Number(accessMode) : undefined,
        access_expires_at: accessMode === "custom" && accessLocal ? new Date(accessLocal).toISOString() : undefined,
        priority: priority || undefined,
        worker_id: workerId ? Number(workerId) : undefined,
        notify_worker: workerId ? notifyWorker : undefined,
        public_domain_id: workerId && publicDomainId ? Number(publicDomainId) : undefined,
      };
      if (mode === "offer") {
        if (!offerId || !packageId) throw new Error("Select a published offer package");
        body.offer_id = Number(offerId); body.package_id = Number(packageId); body.quantity = Number(quantity || 1);
      } else if (mode === "template") {
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
    <Panel className="p-4">
    <form onSubmit={submit} className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">Create gig</h2>
        <Button type="button" onClick={onCancel} tone="ghost">Cancel</Button>
      </div>
      <Segmented
        value={mode}
        onChange={setMode}
        options={[
          { value: "offer", label: "Standard offer" },
          { value: "template", label: "From template" },
          { value: "instructions", label: "From instructions" },
        ]}
      />

      {mode === "offer" ? (
        <div className="space-y-2">
          <Field as="select" value={offerId} onChange={(e) => {
            const next = offers.find((o) => String(o.id) === e.target.value); setOfferId(e.target.value);
            const first = next?.packages?.find((p) => p.active); setPackageId(first ? String(first.id) : ""); setQuantity(String(first?.quantity || 1));
          }} required><option value="">Select standard offer</option>{offers.map((o)=><option key={o.id} value={o.id}>{o.name}</option>)}</Field>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
            <Field as="select" value={packageId} onChange={(e) => { const p=selectedOffer?.packages?.find((x)=>String(x.id)===e.target.value);setPackageId(e.target.value);setQuantity(String(p?.quantity||1)); }} required><option value="">Select package</option>{selectedOffer?.packages?.filter((p)=>p.active).map((p)=><option key={p.id} value={p.id}>{p.name} · {formatMoneyMinor(p.customer_amount_minor||0,p.currency||"EUR")}</option>)}</Field>
            <Field type="number" min="0.01" step="0.01" value={quantity} onChange={(e)=>setQuantity(e.target.value)} placeholder="Quantity" required/>
          </div>
          {selectedPackage && <div className="text-xs text-text-muted">{selectedPackage.description || selectedPackage.tier || selectedPackage.slug} · {selectedPackage.delivery_days || "—"} days · {selectedPackage.revisions ?? 0} revisions</div>}
        </div>
      ) : mode === "template" ? (
        <div className="space-y-2">
          <Field as="select" value={templateId} onChange={(e) => setTemplateId(e.target.value)} required>
            <option value="">Select published template</option>
            {templates.map((t) => <option key={t.id} value={t.id}>{t.name} /{t.slug}</option>)}
          </Field>
          {selectedTemplate?.current_version?.derived?.variables?.length ? (
            <div className="text-xs text-text-muted">
              Vars expected: {selectedTemplate.current_version.derived.variables.map((v) => v.name).join(", ")}
            </div>
          ) : null}
        </div>
      ) : (
        <div className="space-y-3">
          <Field value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Gig title" required={mode === "instructions"} />
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

      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <WorkerSelect projectId={projectId} value={workerId} onChange={setWorkerId} allowEmpty />
        <Field as="select" value={priority} onChange={(e) => setPriority(e.target.value)}>
          <option value="">Priority</option>
          <option value="low">Low</option>
          <option value="normal">Normal</option>
          <option value="high">High</option>
          <option value="urgent">Urgent</option>
        </Field>
      </div>

      <Panel className="p-4 space-y-3 bg-bg-subtle">
        <div>
          <div className="text-sm font-semibold text-text">Timing</div>
          <div className="mt-0.5 text-xs text-text-muted">The work date, submission target, and worker-link expiry are independent.</div>
        </div>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-text-muted">Scheduled for</span>
            <Field type="datetime-local" value={scheduledLocal} onChange={(e) => setScheduledLocal(e.target.value)} />
            <span className="mt-1 block text-[11px] text-text-dim">Intended recording or work date.</span>
          </label>
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-text-muted">Due by</span>
            <Field type="datetime-local" value={dueLocal} onChange={(e) => setDueLocal(e.target.value)} />
            <span className="mt-1 block text-[11px] text-text-dim">Soft target. Late submissions remain open.</span>
          </label>
        </div>
        <details>
          <summary className="cursor-pointer text-xs font-medium text-text-muted">Advanced worker access</summary>
          <div className="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
            <label className="block">
              <span className="mb-1 block text-xs font-medium text-text-muted">Worker access ends</span>
              <Field as="select" value={accessMode} onChange={(e) => setAccessMode(e.target.value as typeof accessMode)}>
                <option value="none">No automatic expiry</option>
                <option value="7">7 days after due</option>
                <option value="14">14 days after due</option>
                <option value="custom">Custom date</option>
              </Field>
            </label>
            {accessMode === "custom" && (
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-text-muted">Access expires at</span>
                <Field type="datetime-local" value={accessLocal} onChange={(e) => setAccessLocal(e.target.value)} required />
              </label>
            )}
          </div>
        </details>
      </Panel>
      {workerId && (
        <div className="grid gap-3 md:grid-cols-2">
          <PublicDomainSelect projectId={projectId} value={publicDomainId} onChange={setPublicDomainId} />
          <label className="inline-flex items-center gap-2 text-sm text-text-muted">
            <input
              type="checkbox"
              checked={notifyWorker}
              onChange={(e) => setNotifyWorker(e.target.checked)}
              className="h-4 w-4 rounded border-border"
            />
            Notify selected worker now
          </label>
        </div>
      )}

      <Field
        as="textarea"
        value={varsText}
        onChange={(e) => setVarsText(e.target.value)}
        rows={5}
        className="font-mono"
        placeholder='{"customer_name":"Acme"}'
      />
      {err && <div className="text-red text-xs">{err}</div>}
      <Button disabled={busy} tone="primary" size="md">Create gig</Button>
    </form>
    </Panel>
  );
}

function AssignGigForm({
  projectId, busy, onAssign, onCancel,
}: { projectId: string; busy: boolean; onAssign: (workerId: number, mode: string, notifyWorker: boolean, publicDomainId: number) => Promise<void>; onCancel: () => void }) {
  const [workerId, setWorkerId] = useState("");
  const [mode, setMode] = useState("direct");
  const [notifyWorker, setNotifyWorker] = useState(false);
  const [publicDomainId, setPublicDomainId] = useState("");
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (workerId) onAssign(Number(workerId), mode, notifyWorker, Number(publicDomainId) || 0);
      }}
      className="p-3 border border-border rounded bg-bg-subtle space-y-3"
    >
      <WorkerSelect projectId={projectId} value={workerId} onChange={setWorkerId} />
      <Field as="select" value={mode} onChange={(e) => setMode(e.target.value)}>
        <option value="direct">Direct assignment</option>
        <option value="broadcast">Broadcast</option>
        <option value="first-come">First come</option>
      </Field>
      <PublicDomainSelect projectId={projectId} value={publicDomainId} onChange={setPublicDomainId} />
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
        <Button disabled={busy || !workerId} tone="primary">Assign</Button>
        <Button type="button" onClick={onCancel} tone="ghost">Cancel</Button>
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
    <Field as="select" value={value} onChange={(e) => onChange(e.target.value)}>
      {allowEmpty && <option value="">No worker yet</option>}
      {!allowEmpty && <option value="">Select worker</option>}
      {workers.map((w) => (
        <option key={w.id} value={w.id}>
          {w.contact ? contactName(w.contact) : `Worker #${w.id}`}{w.open_assignments != null ? ` (${w.open_assignments} open)` : ""}
        </option>
      ))}
    </Field>
  );
}

function PublicDomainSelect({
  projectId, value, onChange,
}: { projectId: string; value: string; onChange: (value: string) => void }) {
  const [domains, setDomains] = useState<PublicDomain[]>([]);
  useEffect(() => {
    api<PublicDomainsResponse>("/public-domains", projectId)
      .then((data) => setDomains((data.public_domains || []).filter((item) => item.status === "active")))
      .catch(() => setDomains([]));
  }, [projectId]);
  return (
    <Field as="select" value={value} onChange={(event) => onChange(event.target.value)}>
      <option value="">Default public link</option>
      {domains.map((item) => (
        <option key={item.id} value={item.id}>
          {item.hostname}{item.is_default ? " (default)" : ""}
        </option>
      ))}
    </Field>
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
        <Field as="select" value={pickId} onChange={(e) => setPickId(e.target.value)}>
          <option value="">Add instruction</option>
          {available.map((i) => (
            <option key={i.id} value={i.id}>{i.name} /{i.slug} · {i.kind}</option>
          ))}
        </Field>
        <Button
          type="button"
          disabled={!pickId}
          onClick={() => {
            onChange([...selectedIds, Number(pickId)]);
            setPickId("");
          }}
        >
          Add
        </Button>
      </div>
      <Panel className="divide-y divide-border overflow-hidden bg-bg">
        {selected.length === 0 && <div className="p-3 text-xs text-text-muted">No instructions selected.</div>}
        {selected.map((i, index) => (
          <div key={`${i.id}-${index}`} className="p-2 flex items-start gap-2 text-sm">
            <span className="mt-0.5 inline-flex h-5 min-w-5 items-center justify-center rounded bg-accent text-white text-xs">{index + 1}</span>
            <span className="text-accent mt-0.5"><Icon name={kindIcon(i.kind)} /></span>
            <div className="flex-1 min-w-0">
              <div className="truncate">{i.name}</div>
              <div className="text-xs text-text-muted truncate">/{i.slug} · {i.kind}</div>
              {i.current_version?.body && (
                <div className="text-xs text-text-muted truncate">{summariseBody(i.kind, i.current_version.body)}</div>
              )}
            </div>
            <div className="flex gap-1">
              <Button type="button" onClick={() => move(index, -1)} disabled={index === 0} size="xs">Up</Button>
              <Button type="button" onClick={() => move(index, 1)} disabled={index === selected.length - 1} size="xs">Down</Button>
              <Button type="button" onClick={() => onChange(selectedIds.filter((_, i2) => i2 !== index))} tone="danger" size="xs">Remove</Button>
            </div>
          </div>
        ))}
      </Panel>
    </div>
  );
}

function instructionLabelById(instructions: Instruction[], id: number): string {
  const found = instructions.find((i) => i.id === id);
  return found ? found.name : `Instruction #${id}`;
}

function preserveCompositionMetadata(selectedIds: number[], existing: CompositionItem[]): Array<Record<string, unknown>> {
  const remaining = existing.slice();
  return selectedIds.map((instructionId) => {
    const index = remaining.findIndex((item) => item.instruction_id === instructionId);
    if (index < 0) return { instruction_id: instructionId };
    const item = remaining.splice(index, 1)[0];
    return {
      instruction_id: instructionId,
      instruction_version_id: item.instruction_version_id || undefined,
      result_key: item.result_key || undefined,
      overrides: item.overrides,
    };
  });
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
  const spec = asRecord(body.response);
  const note = asRecord(spec?.note);
  const files = asRecord(spec?.files);
  const responseParts: string[] = [];
  if (note?.enabled === true || note?.required === true) responseParts.push(note.required === true ? "note required" : "note optional");
  if (files?.enabled === true || files?.required === true) {
    const accept = Array.isArray(files.accept) && files.accept.length ? ` (${files.accept.join(", ")})` : "";
    responseParts.push((files.required === true ? "files required" : "files optional") + accept);
  }
  if (!responseParts.length && body.response_mode && body.response_mode !== "none") responseParts.push(`legacy ${String(body.response_mode)}`);
  const response = responseParts.length ? ` · ${responseParts.join(" + ")}` : "";
  if (kind === "text") return String(body.markdown || "").slice(0, 80) + response;
  if (kind === "content") {
    const blocks = Array.isArray(body.blocks) ? body.blocks : [];
    const images = blocks.filter((raw) => asRecord(raw)?.type === "image").length;
    const firstText = blocks.map((raw) => asRecord(raw)).find((block) => block?.type === "markdown" || block?.type === "callout");
    const preview = String(firstText?.markdown || firstText?.text || "Mixed content").slice(0, 60);
    return `${preview} · ${blocks.length} blocks${images ? ` · ${images} image${images === 1 ? "" : "s"}` : ""}${response}`;
  }
  if (kind === "warning" || kind === "checklist_item" || kind === "confirmation") return String(body.text || "");
  if (kind === "link") return String(body.label || body.url || "");
  if (kind === "audio" || kind === "video" || kind === "image" || kind === "document")
    return (body.caption ? String(body.caption) : `[${kind}]`) + response;
  if (kind.startsWith("input_")) return String(body.label || "");
  return "";
}

// ─── Offers ─────────────────────────────────────────────────────

function OffersTab({ projectId }: { projectId: string }) {
  const [offers, setOffers] = useState<StandardOffer[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [templateId, setTemplateId] = useState("");
  const [description, setDescription] = useState("");
  const [category, setCategory] = useState("");
  const [visibility, setVisibility] = useState("private");
  const [packagesText, setPackagesText] = useState("[]");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const reload = useCallback(async () => {
    try {
      const [offerData, templateData] = await Promise.all([
        api<{ offers: StandardOffer[] }>("/offers", projectId),
        api<{ templates: Template[] }>("/templates?include_archived=false", projectId),
      ]);
      setOffers(offerData.offers || []);
      setTemplates((templateData.templates || []).filter((t) => t.current_version?.status === "active"));
      if (!selectedId && offerData.offers?.[0]) setSelectedId(offerData.offers[0].id);
    } catch (e) { setErr((e as Error).message); }
  }, [projectId, selectedId]);
  useEffect(() => { reload(); }, [reload]);
  const selected = offers.find((o) => o.id === selectedId);
  useEffect(() => {
    if (selected) setPackagesText(JSON.stringify((selected.packages || []).filter((p) => p.active).map((p) => ({
      slug:p.slug,name:p.name,tier:p.tier,description:p.description,scope:p.scope,pricing_model:p.pricing_model,
      quantity:p.quantity,unit:p.unit,delivery_days:p.delivery_days,revisions:p.revisions,
      customer_amount_minor:p.customer_amount_minor,currency:p.currency,sort_order:p.sort_order,
    })), null, 2));
  }, [selectedId, selected?.version]);
  const create = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      const data = await api<{ offer: StandardOffer }>("/offers", projectId, { method:"POST", body:JSON.stringify({name,template_id:Number(templateId),description,category,visibility}) });
      setName(""); setDescription(""); setCategory(""); setSelectedId(data.offer.id); await reload();
    } catch (e2) { setErr((e2 as Error).message); } finally { setBusy(false); }
  };
  const savePackages = async () => {
    if (!selected) return; setBusy(true); setErr(null);
    try {
      const packages = JSON.parse(packagesText);
      if (!Array.isArray(packages)) throw new Error("Packages JSON must be an array");
      await api(`/offers/${selected.id}/packages`, projectId, {method:"PUT",body:JSON.stringify({packages})}); await reload();
    } catch (e) { setErr((e as Error).message); } finally { setBusy(false); }
  };
  const publish = async () => { if (!selected) return; setBusy(true);setErr(null);try{await api(`/offers/${selected.id}/publish`,projectId,{method:"POST",body:"{}"});await reload();}catch(e){setErr((e as Error).message);}finally{setBusy(false);} };
  return <div className="grid grid-cols-1 xl:grid-cols-[360px_1fr] min-h-full">
    <aside className="border-r border-border p-4 space-y-4">
      <div><h2 className="font-semibold">Standard offers</h2><p className="text-xs text-text-muted mt-1">What the business sells, backed by executable templates.</p></div>
      <div className="space-y-1">{offers.map((o)=><button key={o.id} onClick={()=>setSelectedId(o.id)} className={cx("w-full text-left rounded border p-3",selectedId===o.id?"border-accent bg-accent/10":"border-border hover:bg-bg-subtle")}><div className="flex justify-between gap-2"><span className="font-medium">{o.name}</span><Pill tone={o.status==="active"?"success":"default"}>{o.status}</Pill></div><div className="text-xs text-text-muted mt-1">/{o.slug} · v{o.version} · {o.packages?.filter((p)=>p.active).length||0} packages</div></button>)}</div>
      <Panel className="p-3"><form onSubmit={create} className="space-y-2"><h3 className="text-sm font-semibold">New offer</h3><Field value={name} onChange={(e)=>setName(e.target.value)} placeholder="Offer name" required/><Field as="select" value={templateId} onChange={(e)=>setTemplateId(e.target.value)} required><option value="">Published template…</option>{templates.map((t)=><option key={t.id} value={t.id}>{t.name}</option>)}</Field><Field value={description} onChange={(e)=>setDescription(e.target.value)} placeholder="Description"/><Field value={category} onChange={(e)=>setCategory(e.target.value)} placeholder="Category"/><Field as="select" value={visibility} onChange={(e)=>setVisibility(e.target.value)}><option value="private">Private</option><option value="unlisted">Unlisted</option><option value="public">Public</option></Field><Button disabled={busy} tone="primary">Create offer</Button></form></Panel>
    </aside>
    <section className="p-5 space-y-4">{err&&<div className="text-red text-sm border border-red/30 bg-red/10 rounded p-3">{err}</div>}{selected?<><Panel className="p-4"><div className="flex justify-between items-start gap-3"><div><h2 className="text-xl font-semibold">{selected.name}</h2><p className="text-sm text-text-muted mt-1">{selected.description||"No description"}</p><div className="text-xs text-text-muted mt-2">Template #{selected.template_id} · Catalog product {selected.catalog_product_id||"not published"}</div></div><Button disabled={busy||selected.status==="active"} tone="primary" onClick={publish}>{selected.status==="active"?"Published":"Publish to Catalog"}</Button></div></Panel><Panel className="p-4 space-y-3"><div><h3 className="font-semibold">Packages</h3><p className="text-xs text-text-muted">Define Basic/Standard/Premium or any custom tiers. Amounts use minor currency units.</p></div><Field as="textarea" rows={18} value={packagesText} onChange={(e)=>setPackagesText(e.target.value)} className="font-mono text-xs"/><Button disabled={busy} tone="primary" onClick={savePackages}>Save packages</Button></Panel></>:<Panel className="p-6 text-text-muted">Create or select an offer.</Panel>}</section>
  </div>;
}

// ─── Rates ──────────────────────────────────────────────────────

function RatesTab({ projectId }: { projectId: string }) {
  const [grades,setGrades]=useState<PayGrade[]>([]); const [rates,setRates]=useState<RateCard[]>([]); const [workers,setWorkers]=useState<Worker[]>([]); const [templates,setTemplates]=useState<Template[]>([]); const [offers,setOffers]=useState<StandardOffer[]>([]);
  const [gradeName,setGradeName]=useState(""); const [gradeRank,setGradeRank]=useState("0"); const [gradeAmount,setGradeAmount]=useState(""); const [gradeCurrency,setGradeCurrency]=useState("EUR");
  const [scopeType,setScopeType]=useState<"grade"|"worker">("grade"); const [scopeId,setScopeId]=useState(""); const [templateId,setTemplateId]=useState(""); const [packageId,setPackageId]=useState(""); const [model,setModel]=useState("fixed"); const [amount,setAmount]=useState(""); const [currency,setCurrency]=useState("EUR"); const [unit,setUnit]=useState(""); const [busy,setBusy]=useState(false); const [err,setErr]=useState<string|null>(null);
  const [profileWorkerId,setProfileWorkerId]=useState(""); const [profileGradeId,setProfileGradeId]=useState("");
  const reload=useCallback(async()=>{try{const [g,r,w,t,o]=await Promise.all([api<{pay_grades:PayGrade[]}>("/pay-grades?include_inactive=true",projectId),api<{rates:RateCard[]}>("/rates?include_archived=true",projectId),api<{workers:Worker[]}>("/workers?include_contact=true",projectId),api<{templates:Template[]}>("/templates",projectId),api<{offers:StandardOffer[]}>("/offers",projectId)]);setGrades(g.pay_grades||[]);setRates(r.rates||[]);setWorkers(w.workers||[]);setTemplates(t.templates||[]);setOffers(o.offers||[]);}catch(e){setErr((e as Error).message);}},[projectId]); useEffect(()=>{reload();},[reload]);
  const addGrade=async(e:React.FormEvent)=>{e.preventDefault();setBusy(true);setErr(null);try{await api("/pay-grades",projectId,{method:"POST",body:JSON.stringify({name:gradeName,rank:Number(gradeRank),default_pricing_model:gradeAmount?"fixed":undefined,default_amount_minor:gradeAmount?Number(gradeAmount):undefined,currency:gradeAmount?gradeCurrency:undefined})});setGradeName("");setGradeAmount("");await reload();}catch(e2){setErr((e2 as Error).message);}finally{setBusy(false);}};
  const addRate=async(e:React.FormEvent)=>{e.preventDefault();setBusy(true);setErr(null);try{const body:Record<string,unknown>={pricing_model:model,amount_minor:Number(amount),currency,unit:unit||undefined,template_id:templateId?Number(templateId):undefined,offer_package_id:packageId?Number(packageId):undefined};body[scopeType==="grade"?"pay_grade_id":"worker_id"]=Number(scopeId);await api("/rates",projectId,{method:"POST",body:JSON.stringify(body)});setAmount("");await reload();}catch(e2){setErr((e2 as Error).message);}finally{setBusy(false);}};
  const assignGrade=async(e:React.FormEvent)=>{e.preventDefault();setBusy(true);setErr(null);try{await api(`/workers/${profileWorkerId}/pay-grade`,projectId,{method:"PUT",body:JSON.stringify({pay_grade_id:Number(profileGradeId)})});setProfileWorkerId("");setProfileGradeId("");await reload();}catch(e2){setErr((e2 as Error).message);}finally{setBusy(false);}};
  const allPackages=offers.flatMap((o)=>(o.packages||[]).filter((p)=>p.active).map((p)=>({...p,offerName:o.name})));
  return <div className="p-5 space-y-5"><div><h2 className="text-xl font-semibold">Pay grades and rate cards</h2><p className="text-sm text-text-muted">Worker commercial levels are separate from skill proficiency. Specific rates override general defaults.</p></div>{err&&<div className="text-red text-sm border border-red/30 bg-red/10 rounded p-3">{err}</div>}<Panel className="p-4"><form onSubmit={assignGrade} className="flex flex-wrap items-end gap-2"><div className="min-w-56 flex-1"><div className="text-xs text-text-muted mb-1">Worker</div><Field as="select" value={profileWorkerId} onChange={(e)=>setProfileWorkerId(e.target.value)} required><option value="">Select worker…</option>{workers.map((w)=><option key={w.id} value={w.id}>{contactName(w.contact||{id:w.contact_id})}{w.pay_profile?.pay_grade?` · ${w.pay_profile.pay_grade.name}`:" · no grade"}</option>)}</Field></div><div className="min-w-48 flex-1"><div className="text-xs text-text-muted mb-1">Commercial level</div><Field as="select" value={profileGradeId} onChange={(e)=>setProfileGradeId(e.target.value)} required><option value="">Select grade…</option>{grades.filter((g)=>g.active).map((g)=><option key={g.id} value={g.id}>{g.name}</option>)}</Field></div><Button disabled={busy||!profileWorkerId||!profileGradeId} tone="primary">Assign grade</Button></form></Panel><div className="grid grid-cols-1 xl:grid-cols-2 gap-4"><Panel className="p-4"><form onSubmit={addGrade} className="space-y-3"><h3 className="font-semibold">Add pay grade</h3><div className="grid grid-cols-2 gap-2"><Field value={gradeName} onChange={(e)=>setGradeName(e.target.value)} placeholder="Standard, Senior…" required/><Field type="number" value={gradeRank} onChange={(e)=>setGradeRank(e.target.value)} placeholder="Rank"/><Field type="number" min="0" value={gradeAmount} onChange={(e)=>setGradeAmount(e.target.value)} placeholder="General default (minor units)"/><Field value={gradeCurrency} onChange={(e)=>setGradeCurrency(e.target.value.toUpperCase())} maxLength={3}/></div><Button disabled={busy} tone="primary">Add grade</Button></form><div className="mt-4 divide-y divide-border">{grades.map((g)=><div key={g.id} className="py-2 flex justify-between"><div><span className="font-medium">L{g.rank} · {g.name}</span><div className="text-xs text-text-muted">/{g.slug}</div></div><span className="text-sm">{g.default_amount_minor?formatMoneyMinor(g.default_amount_minor,g.currency||"EUR"):"No general default"}</span></div>)}</div></Panel><Panel className="p-4"><form onSubmit={addRate} className="space-y-3"><h3 className="font-semibold">Add effective rate</h3><div className="grid grid-cols-2 gap-2"><Field as="select" value={scopeType} onChange={(e)=>{setScopeType(e.target.value as "grade"|"worker");setScopeId("");}}><option value="grade">Pay grade</option><option value="worker">Worker override</option></Field><Field as="select" value={scopeId} onChange={(e)=>setScopeId(e.target.value)} required><option value="">Select…</option>{scopeType==="grade"?grades.filter((g)=>g.active).map((g)=><option key={g.id} value={g.id}>{g.name}</option>):workers.map((w)=><option key={w.id} value={w.id}>{contactName(w.contact||{id:w.contact_id})}</option>)}</Field><Field as="select" value={templateId} onChange={(e)=>{setTemplateId(e.target.value);setPackageId("");}}><option value="">General / any template</option>{templates.map((t)=><option key={t.id} value={t.id}>{t.name}</option>)}</Field><Field as="select" value={packageId} onChange={(e)=>{setPackageId(e.target.value);setTemplateId("");}}><option value="">No package override</option>{allPackages.map((p)=><option key={p.id} value={p.id}>{p.offerName} · {p.name}</option>)}</Field><Field as="select" value={model} onChange={(e)=>setModel(e.target.value)}>{["fixed","hourly","per_unit","daily","milestone","recurring"].map((m)=><option key={m}>{m}</option>)}</Field><Field value={unit} onChange={(e)=>setUnit(e.target.value)} placeholder="Unit (optional)"/><Field type="number" min="0" value={amount} onChange={(e)=>setAmount(e.target.value)} placeholder="Amount in minor units" required/><Field value={currency} onChange={(e)=>setCurrency(e.target.value.toUpperCase())} maxLength={3} required/></div><Button disabled={busy} tone="primary">Set rate</Button></form></Panel></div><Panel className="overflow-hidden"><div className="p-3 border-b border-border font-semibold">Rate history</div><div className="divide-y divide-border">{rates.map((r)=><div key={r.id} className="p-3 grid grid-cols-[1fr_auto] gap-3"><div><span className="font-medium">{r.worker_id?`Worker #${r.worker_id}`:`Grade #${r.pay_grade_id}`}</span><div className="text-xs text-text-muted">{r.offer_package_id?`Package #${r.offer_package_id}`:r.template_id?`Template #${r.template_id}`:"General"} · {r.pricing_model}{r.unit?`/${r.unit}`:""} · {r.source}</div></div><div className="text-right"><div>{formatMoneyMinor(r.amount_minor,r.currency)}</div><Pill tone={r.status==="active"?"success":"default"}>{r.status}</Pill></div></div>)}</div></Panel></div>;
}

// ─── Marketplace ────────────────────────────────────────────────

function MarketplaceTab({projectId}:{projectId:string}){
  const [jobs,setJobs]=useState<JobPost[]>([]);const [contracts,setContracts]=useState<ContractRecord[]>([]);const [title,setTitle]=useState("");const [description,setDescription]=useState("");const [budgetMin,setBudgetMin]=useState("");const [budgetMax,setBudgetMax]=useState("");const [currency,setCurrency]=useState("EUR");const [visibility,setVisibility]=useState("private");const [busy,setBusy]=useState(false);const [err,setErr]=useState<string|null>(null);
  const reload=useCallback(async()=>{try{const [j,c]=await Promise.all([api<{job_posts:JobPost[]}>("/job-posts",projectId),api<{contracts:ContractRecord[]}>("/contracts",projectId)]);setJobs(j.job_posts||[]);setContracts(c.contracts||[]);}catch(e){setErr((e as Error).message);}},[projectId]);useEffect(()=>{reload();},[reload]);
  const create=async(e:React.FormEvent)=>{e.preventDefault();setBusy(true);setErr(null);try{await api("/job-posts",projectId,{method:"POST",body:JSON.stringify({title,description,budget_min_minor:budgetMin?Number(budgetMin):undefined,budget_max_minor:budgetMax?Number(budgetMax):undefined,currency:(budgetMin||budgetMax)?currency:undefined,visibility,publish:true,pricing_models:["fixed","hourly"]})});setTitle("");setDescription("");setBudgetMin("");setBudgetMax("");await reload();}catch(e2){setErr((e2 as Error).message);}finally{setBusy(false);}};
  return <div className="p-5 space-y-5"><div><h2 className="text-xl font-semibold">Marketplace</h2><p className="text-sm text-text-muted">Demand-first job posts and accepted contracts. Provider proposals can be submitted through MCP or the HTTP API.</p></div>{err&&<div className="text-red text-sm border border-red/30 bg-red/10 rounded p-3">{err}</div>}<div className="grid grid-cols-1 xl:grid-cols-[380px_1fr] gap-4"><Panel className="p-4"><form onSubmit={create} className="space-y-2"><h3 className="font-semibold">Post a job</h3><Field value={title} onChange={(e)=>setTitle(e.target.value)} placeholder="Job title" required/><Field as="textarea" rows={4} value={description} onChange={(e)=>setDescription(e.target.value)} placeholder="Scope and deliverables"/><div className="grid grid-cols-3 gap-2"><Field type="number" min="0" value={budgetMin} onChange={(e)=>setBudgetMin(e.target.value)} placeholder="Min"/><Field type="number" min="0" value={budgetMax} onChange={(e)=>setBudgetMax(e.target.value)} placeholder="Max"/><Field value={currency} onChange={(e)=>setCurrency(e.target.value.toUpperCase())} maxLength={3}/></div><Field as="select" value={visibility} onChange={(e)=>setVisibility(e.target.value)}><option value="private">Private</option><option value="unlisted">Unlisted</option><option value="public">Public</option></Field><Button disabled={busy} tone="primary">Publish job</Button></form></Panel><Panel className="overflow-hidden"><div className="p-3 border-b border-border font-semibold">Job posts</div><div className="divide-y divide-border">{jobs.length===0&&<div className="p-4 text-sm text-text-muted">No job posts.</div>}{jobs.map((j)=><div key={j.id} className="p-3"><div className="flex justify-between gap-2"><span className="font-medium">{j.title}</span><Pill tone={j.status==="open"?"success":"default"}>{j.status}</Pill></div><div className="text-xs text-text-muted mt-1">{j.visibility}{j.budget_max_minor?` · ${formatMoneyMinor(j.budget_min_minor||0,j.currency||"EUR")}–${formatMoneyMinor(j.budget_max_minor,j.currency||"EUR")}`:""}</div></div>)}</div></Panel></div><Panel className="overflow-hidden"><div className="p-3 border-b border-border font-semibold">Contracts</div><div className="divide-y divide-border">{contracts.length===0&&<div className="p-4 text-sm text-text-muted">No contracts yet.</div>}{contracts.map((c)=><div key={c.id} className="p-3 flex justify-between gap-3"><div><div className="font-medium">{c.title}</div><div className="text-xs text-text-muted">{c.source_type} · {c.pricing_model} · worker {c.worker_id||"unassigned"} · {c.milestones?.length||0} milestones</div></div><div className="text-right"><Pill tone={c.status==="active"?"success":"default"}>{c.status}</Pill><div className="text-sm mt-1">{formatMoneyMinor(c.worker_amount_minor||0,c.currency)}</div></div></div>)}</div></Panel></div>;
}

// ─── Templates ──────────────────────────────────────────────────

function TemplatesTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Template[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<Template | null>(null);
  const [instructions, setInstructions] = useState<Instruction[]>([]);
  const [loadErr, setLoadErr] = useState<string | null>(null);

  const reload = useCallback(() => {
    setLoadErr(null);
    api<{ templates: Template[] }>("/templates?include_archived=false", projectId)
      .then((d) => {
        setItems(d.templates || []);
        setSelected((cur) => cur ? (d.templates || []).find((t) => t.id === cur.id) || cur : cur);
      })
      .catch((e) => { setItems([]); setLoadErr((e as Error).message); });
    api<{ instructions: Instruction[] }>("/instructions?include_archived=false", projectId)
      .then((d) => setInstructions(d.instructions || []))
      .catch((e) => { setInstructions([]); setLoadErr((e as Error).message); });
  }, [projectId]);
  useEffect(() => { reload(); }, [reload]);

  return (
    <div className="p-5 space-y-4">
      <SectionHeading
        title="Templates"
        action={(
        <Button onClick={() => setAdding(true)} tone="primary">
          <Icon name="plus" /> New
        </Button>
        )}
      />
      {loadErr && <div role="alert" className="p-3 rounded border border-red/30 bg-red/10 text-red text-sm">{loadErr}</div>}
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
          <Panel key={t.id} className="p-3">
            <div className="flex items-center justify-between">
              <div className="font-medium text-sm">{t.name}</div>
              {t.current_version && (
                <Pill tone={t.current_version.status === "active" ? "success" : "warn"}>
                  v{t.current_version.version} · {t.current_version.status}
                </Pill>
              )}
            </div>
            <div className="mt-1 text-xs text-text-muted">/{t.slug} · {t.kind}</div>
            {(t.current_version?.default_due_hours || t.current_version?.default_access_grace_days) && (
              <div className="mt-1 text-xs text-text-muted">
                {t.current_version.default_due_hours ? `due +${t.current_version.default_due_hours}h` : "no default due"}
                {t.current_version.default_access_grace_days ? ` · access +${t.current_version.default_access_grace_days}d` : " · no access expiry"}
              </div>
            )}
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
              <Button onClick={() => setSelected(t)} size="xs">Manage instructions</Button>
            {t.current_version?.status === "draft" && (
              <Button
                onClick={() => api(`/templates/${t.id}/publish`, projectId, { method: "POST" }).then(reload)}
                tone="primary"
                size="xs"
              >
                Publish v{t.current_version.version}
              </Button>
            )}
            </div>
          </Panel>
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
          instructions: preserveCompositionMetadata(selectedIds, template.current_version?.composition || []),
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
    <Panel className="p-3 space-y-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">{template.name}</h3>
          <div className="text-xs text-text-muted">/{template.slug} · v{template.current_version?.version || 1} · {template.current_version?.status || "draft"}</div>
        </div>
        <Button onClick={onClose} tone="ghost" size="xs">Close</Button>
      </div>
      <InstructionOrderEditor instructions={instructions} selectedIds={selectedIds} onChange={setSelectedIds} />
      {err && <div className="text-red text-xs">{err}</div>}
      <div className="flex gap-2">
        <Button disabled={busy} onClick={save} tone="primary">Save composition</Button>
        {template.current_version?.status === "draft" && (
          <Button
            disabled={busy}
            onClick={() => api(`/templates/${template.id}/publish`, projectId, { method: "POST" }).then(onDone)}
          >
            Publish
          </Button>
        )}
      </div>
    </Panel>
  );
}

function NewTemplateForm({ projectId, onDone }: { projectId: string; onDone: () => void }) {
  const [name, setName] = useState("");
  const [title, setTitle] = useState("");
  const [dueHours, setDueHours] = useState("");
  const [accessGraceDays, setAccessGraceDays] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setErr(null);
        try {
          await api(`/templates`, projectId, { method: "POST", body: JSON.stringify({
            name,
            title_template: title,
            default_due_hours: dueHours ? Number(dueHours) : undefined,
            default_access_grace_days: accessGraceDays ? Number(accessGraceDays) : undefined,
          }) });
          onDone();
        } catch (error) { setErr((error as Error).message); } finally { setBusy(false); }
      }}
      className="p-3 border border-border rounded space-y-2 bg-bg-subtle"
    >
      <Field value={name} onChange={(e) => setName(e.target.value)} placeholder="Template name" required />
      <Field value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Title with {{vars}}" required />
      <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
        <Field type="number" min="1" value={dueHours} onChange={(e) => setDueHours(e.target.value)} placeholder="Default soft due (hours)" />
        <Field as="select" value={accessGraceDays} onChange={(e) => setAccessGraceDays(e.target.value)}>
          <option value="">No automatic access expiry</option>
          <option value="7">Access ends 7 days after due</option>
          <option value="14">Access ends 14 days after due</option>
        </Field>
      </div>
      {err && <div role="alert" className="text-xs text-red">{err}</div>}
      <Button disabled={busy} tone="primary">Create draft</Button>
    </form>
  );
}

// ─── Instructions ───────────────────────────────────────────────

function InstructionsTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Instruction[] | null>(null);
  const [kind, setKind] = useState<string>("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Instruction | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);

  const reload = useCallback(() => {
    setLoadErr(null);
    const k = kind ? `&kind=${encodeURIComponent(kind)}` : "";
    api<{ instructions: Instruction[] }>(`/instructions?include_archived=false${k}`, projectId)
      .then((d) => setItems(d.instructions || []))
      .catch((e) => { setItems([]); setLoadErr((e as Error).message); });
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
    <div className="p-5 space-y-4">
      <SectionHeading
        title="Instruction library"
        action={(
        <div className="flex items-center gap-2">
          <Field as="select" value={kind} onChange={(e) => setKind(e.target.value)} className="w-auto min-w-[140px] py-1.5">
            <option value="">All kinds</option>
            {ALL_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
          </Field>
          <Button onClick={() => { setAdding(true); setEditing(null); }} tone="primary">
            <Icon name="plus" /> New
          </Button>
        </div>
        )}
      />
      {loadErr && <div role="alert" className="p-3 rounded border border-red/30 bg-red/10 text-red text-sm">{loadErr}</div>}
      {adding && <NewInstructionForm projectId={projectId} onDone={() => { setAdding(false); reload(); }} onCancel={() => setAdding(false)} />}
      {editing && (
        <NewInstructionForm
          projectId={projectId}
          instruction={editing}
          onDone={() => { setEditing(null); reload(); }}
          onCancel={() => setEditing(null)}
        />
      )}
      {items?.length === 0 && <Panel className="p-4 text-sm text-text-muted">No instructions yet.</Panel>}
      {Object.entries(groups).map(([fam, list]) => (
        <div key={fam}>
          <h3 className="text-xs uppercase tracking-wide text-text-muted mb-2">{fam}</h3>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-2">
            {list.map((i) => (
              <Panel key={i.id} className="p-3 flex items-start gap-2">
                <span className="text-accent mt-0.5"><Icon name={kindIcon(i.kind)} /></span>
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
                      <Button
                        type="button"
                        onClick={() => { setAdding(false); setEditing(i); }}
                        size="xs"
                      >
                        Edit
                      </Button>
                    )}
                    {i.current_version?.status === "draft" && (
                      <Button
                        onClick={() => api(`/instructions/${i.id}/publish`, projectId, { method: "POST" }).then(reload)}
                        tone="primary"
                        size="xs"
                      >
                        Publish
                      </Button>
                    )}
                  </div>
                </div>
              </Panel>
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
  const [responsePreset, setResponsePreset] = useState<ResponsePreset>(responsePresetFromBody(initialBody));
  const initialResponseFiles = responseFileSettingsFromBody(initialBody);
  const [responseAccept, setResponseAccept] = useState(initialResponseFiles.accept);
  const [responseMin, setResponseMin] = useState(initialResponseFiles.min);
  const [responseMax, setResponseMax] = useState(initialResponseFiles.max);
  const [responseMaxSize, setResponseMaxSize] = useState(initialResponseFiles.maxSize);
  const [selectedFile, setSelectedFile] = useState<StorageFile | null>(storageFileFromBody(instruction?.kind || "text", initialBody));
  const [contentBlocks, setContentBlocks] = useState<ContentBlockDraft[]>(contentBlocksFromBody(initialBody));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!editMode) setSelectedFile(null);
  }, [editMode, kind]);

  useEffect(() => {
    if (kind === "content" && contentBlocks.length === 0) setContentBlocks([newContentBlock("markdown")]);
  }, [contentBlocks.length, kind]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const body = buildInstructionBody(kind, text, selectedFile, contentBlocks, responsePreset, responseAccept, responseMin, responseMax, responseMaxSize);
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
    <Panel className="p-4">
    <form onSubmit={submit} className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">{editMode ? "Edit instruction draft" : "Create instruction"}</h3>
        {editMode && <Pill>Current v{instruction?.current_version?.version}</Pill>}
      </div>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-2">
        <Field value={name} onChange={(e) => setName(e.target.value)} disabled={editMode} placeholder="Instruction name" required />
        <Field as="select" value={kind} onChange={(e) => setKind(e.target.value)} disabled={editMode}>
          {ALL_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
        </Field>
        <Field value={slug} onChange={(e) => setSlug(e.target.value)} disabled={editMode} placeholder="Slug (optional)" />
      </div>

      {kind === "content" ? (
        <ContentBlockEditor projectId={projectId} blocks={contentBlocks} onChange={setContentBlocks} />
      ) : (
        <Field
          as="textarea"
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={kind === "text" ? "Instruction text" : "Text shown with this media"}
          rows={4}
          required
        />
      )}
      <Field
        as="select"
        value={responsePreset}
        onChange={(e) => setResponsePreset(e.target.value as ResponsePreset)}
      >
        <option value="none">No worker response</option>
        <option value="note_optional">Optional note</option>
        <option value="note_required">Required note</option>
        <option value="files_optional">Optional files</option>
        <option value="files_required">Required files</option>
        <option value="files_required_note_optional">Required files + optional note</option>
        <option value="files_required_note_required">Required files + required note</option>
        <option value="note_files_optional">Optional note and files</option>
      </Field>

      {responsePreset.includes("files") && (
        <div className="grid grid-cols-1 md:grid-cols-4 gap-2">
          <Field value={responseAccept} onChange={(e) => setResponseAccept(e.target.value)} placeholder="Accepted types, e.g. video/*" />
          <Field type="number" min="0" value={responseMin} onChange={(e) => setResponseMin(e.target.value)} placeholder="Minimum files" />
          <Field type="number" min="1" value={responseMax} onChange={(e) => setResponseMax(e.target.value)} placeholder="Maximum files (optional)" />
          <Field type="number" min="1" value={responseMaxSize} onChange={(e) => setResponseMaxSize(e.target.value)} placeholder="Max MB per file" />
        </div>
      )}

      {(kind === "audio" || kind === "video" || kind === "image") && (
        <StorageFilePicker
          projectId={projectId}
          kind={kind as "audio" | "video" | "image"}
          selected={selectedFile}
          onSelect={setSelectedFile}
        />
      )}

      {err && <div className="text-red text-xs">{err}</div>}
      <div className="flex gap-2">
        <Button disabled={busy || ((kind === "audio" || kind === "video" || kind === "image") && !selectedFile) || (kind === "content" && !contentBlocksValid(contentBlocks))} tone="primary">{editMode ? "Save draft" : "Create draft"}</Button>
        <Button type="button" onClick={onCancel} tone="ghost">Cancel</Button>
      </div>
    </form>
    </Panel>
  );
}

function ContentBlockEditor({
  projectId, blocks, onChange,
}: { projectId: string; blocks: ContentBlockDraft[]; onChange: (blocks: ContentBlockDraft[]) => void }) {
  const patchBlock = (index: number, patch: Partial<ContentBlockDraft>) => {
    onChange(blocks.map((block, current) => current === index ? { ...block, ...patch } : block));
  };
  const moveBlock = (index: number, direction: -1 | 1) => {
    const target = index + direction;
    if (target < 0 || target >= blocks.length) return;
    const next = [...blocks];
    [next[index], next[target]] = [next[target], next[index]];
    onChange(next);
  };
  const removeBlock = (index: number) => onChange(blocks.filter((_, current) => current !== index));
  const addBlock = (type: ContentBlockType) => onChange([...blocks, newContentBlock(type)]);

  return (
    <div className="space-y-3">
      <div>
        <div className="text-sm font-medium">Instruction content</div>
        <div className="text-xs text-text-muted mt-1">Text and images render together inside one numbered worker instruction.</div>
      </div>
      {blocks.map((block, index) => (
        <Panel key={block.key} className="p-3 space-y-3 bg-bg">
          <div className="flex flex-wrap items-center gap-2">
            <Pill>Block {index + 1}</Pill>
            <Field
              as="select"
              value={block.type}
              onChange={(e) => patchBlock(index, { ...newContentBlock(e.target.value as ContentBlockType), key: block.key })}
              className="max-w-44"
            >
              <option value="markdown">Text</option>
              <option value="image">Image</option>
              <option value="callout">Callout</option>
              <option value="divider">Divider</option>
            </Field>
            <div className="ml-auto flex gap-1">
              <Button type="button" size="xs" disabled={index === 0} onClick={() => moveBlock(index, -1)}>Up</Button>
              <Button type="button" size="xs" disabled={index === blocks.length - 1} onClick={() => moveBlock(index, 1)}>Down</Button>
              <Button type="button" size="xs" tone="danger" onClick={() => removeBlock(index)}>Remove</Button>
            </div>
          </div>
          {block.type === "markdown" && (
            <Field as="textarea" rows={5} value={block.markdown} onChange={(e) => patchBlock(index, { markdown: e.target.value })} placeholder="Instruction text. Template variables such as {{model}} are supported." required />
          )}
          {block.type === "image" && (
            <div className="space-y-2">
              <StorageFilePicker projectId={projectId} kind="image" selected={block.file} onSelect={(file) => patchBlock(index, { file })} />
              <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                <Field value={block.caption} onChange={(e) => patchBlock(index, { caption: e.target.value })} placeholder="Caption (optional)" />
                <Field value={block.alt} onChange={(e) => patchBlock(index, { alt: e.target.value })} placeholder="Accessibility description (optional)" />
              </div>
            </div>
          )}
          {block.type === "callout" && (
            <div className="grid grid-cols-1 md:grid-cols-[160px_1fr] gap-2">
              <Field as="select" value={block.tone} onChange={(e) => patchBlock(index, { tone: e.target.value as ContentBlockDraft["tone"] })}>
                <option value="info">Information</option>
                <option value="tip">Tip</option>
                <option value="warning">Warning</option>
              </Field>
              <Field as="textarea" rows={3} value={block.text} onChange={(e) => patchBlock(index, { text: e.target.value })} placeholder="Important note" required />
            </div>
          )}
          {block.type === "divider" && <div className="border-t border-border" />}
        </Panel>
      ))}
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="xs" onClick={() => addBlock("markdown")}>+ Text</Button>
        <Button type="button" size="xs" onClick={() => addBlock("image")}>+ Image</Button>
        <Button type="button" size="xs" onClick={() => addBlock("callout")}>+ Callout</Button>
        <Button type="button" size="xs" onClick={() => addBlock("divider")}>+ Divider</Button>
      </div>
    </div>
  );
}

function StorageFilePicker({
  projectId, kind, selected, onSelect,
}: { projectId: string; kind: "audio" | "video" | "image"; selected: StorageFile | null; onSelect: (file: StorageFile | null) => void }) {
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
        <Field value={query} onChange={(e) => setQuery(e.target.value)} placeholder={`Search ${kind} files`} />
        <Field value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="Current folder, e.g. /recordings/" />
      </div>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <Button type="button" onClick={() => setFolder("")} size="xs">Root</Button>
        <Button type="button" disabled={!folder} onClick={() => setFolder(parentFolder)} size="xs">Up</Button>
        <span className="text-text-muted truncate">/{folder.replace(/^\/|\/$/g, "")}</span>
      </div>
      <Panel className="bg-bg p-2">
        {folderBusy && <div className="text-xs text-text-muted">Loading folders…</div>}
        {!folderBusy && folders.length === 0 && <div className="text-xs text-text-muted">No child folders.</div>}
        {!folderBusy && folders.length > 0 && (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-1">
            {folders.map((f) => (
              <button
                type="button"
                key={f.path}
                onClick={() => setFolder(f.path)}
                className="p-2 text-left border border-border rounded bg-bg hover:bg-bg-subtle transition-colors"
              >
                <div className="text-sm truncate">{f.name}</div>
                <div className="text-xs text-text-muted">{f.file_count || 0} files · {formatBytes(f.size_bytes || 0)}</div>
              </button>
            ))}
          </div>
        )}
      </Panel>
      {selected && (
        <div className="p-2 border border-accent/30 rounded bg-accent/10 space-y-2">
          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="text-sm truncate">{selected.name}</div>
              <div className="text-xs text-text-muted truncate">{selected.folder} · {formatBytes(selected.size_bytes || 0)}</div>
            </div>
            <Button type="button" onClick={() => onSelect(null)} size="xs">Clear</Button>
          </div>
          {kind === "image" && (
            <img
              src={selected.url || `${STORAGE_API}/files/${selected.id}/content?project_id=${encodeURIComponent(projectId)}`}
              alt={selected.name}
              className="max-h-72 w-full object-contain rounded border border-border bg-black"
            />
          )}
        </div>
      )}
      <Panel className="divide-y divide-border bg-bg max-h-56 overflow-auto">
        {busy && <div className="p-2 text-xs text-text-muted">Loading files…</div>}
        {!busy && err && <div className="p-2 text-xs text-red">{err}</div>}
        {!busy && !err && files.length === 0 && <div className="p-2 text-xs text-text-muted">No {kind} files found.</div>}
        {!busy && files.map((file) => (
          <button
            type="button"
            key={file.id}
            onClick={() => onSelect(file)}
            className={"w-full p-2 text-left hover:bg-bg-subtle " + (selected?.id === file.id ? "bg-accent/10" : "")}
          >
            <div className="text-sm truncate">{file.name}</div>
            <div className="text-xs text-text-muted truncate">{file.folder} · {file.content_type || "unknown"} · {formatBytes(file.size_bytes || 0)}</div>
          </button>
        ))}
      </Panel>
    </div>
  );
}

function instructionTextFromBody(kind: string, body: Record<string, unknown>): string {
  if (kind === "text") return String(body.markdown || "");
  return String(body.caption || body.transcript || "");
}

function newContentBlock(type: ContentBlockType): ContentBlockDraft {
  return {
    key: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
    type,
    markdown: "",
    caption: "",
    alt: "",
    text: "",
    tone: "info",
    file: null,
  };
}

function contentBlocksFromBody(body: Record<string, unknown>): ContentBlockDraft[] {
  if (!Array.isArray(body.blocks)) return [];
  return body.blocks.flatMap((raw, index) => {
    const block = asRecord(raw);
    const type = String(block?.type || "") as ContentBlockType;
    if (!block || !["markdown", "image", "callout", "divider"].includes(type)) return [];
    const storageID = Number(block.storage_file_id || 0);
    return [{
      key: `existing-${index}`,
      type,
      markdown: String(block.markdown || ""),
      caption: String(block.caption || ""),
      alt: String(block.alt || ""),
      text: String(block.text || ""),
      tone: (["info", "tip", "warning"].includes(String(block.tone)) ? String(block.tone) : "info") as ContentBlockDraft["tone"],
      file: storageID ? { id: storageID, name: `Storage file #${storageID}`, folder: "", content_type: "image/" } : null,
    }];
  });
}

function contentBlocksValid(blocks: ContentBlockDraft[]): boolean {
  return blocks.length > 0 && blocks.every((block) => {
    if (block.type === "markdown") return Boolean(block.markdown.trim());
    if (block.type === "image") return Boolean(block.file?.id);
    if (block.type === "callout") return Boolean(block.text.trim());
    return block.type === "divider";
  });
}

type ResponsePreset =
  | "none"
  | "note_optional"
  | "note_required"
  | "files_optional"
  | "files_required"
  | "files_required_note_optional"
  | "files_required_note_required"
  | "note_files_optional";

function responsePresetFromBody(body: Record<string, unknown>): ResponsePreset {
  const response = asRecord(body.response);
  if (response) {
    const note = asRecord(response.note) || {};
    const files = asRecord(response.files) || {};
    const noteEnabled = note.enabled === true || note.required === true;
    const filesEnabled = files.enabled === true || files.required === true;
    const noteRequired = note.required === true;
    const filesRequired = files.required === true;
    if (noteEnabled && filesEnabled && noteRequired && filesRequired) return "files_required_note_required";
    if (noteEnabled && filesEnabled && filesRequired) return "files_required_note_optional";
    if (noteEnabled && filesEnabled) return "note_files_optional";
    if (filesEnabled && filesRequired) return "files_required";
    if (filesEnabled) return "files_optional";
    if (noteEnabled && noteRequired) return "note_required";
    if (noteEnabled) return "note_optional";
    return "none";
  }
  const legacy = String(body.response_mode || "none");
  if (legacy === "required") return "files_required_note_optional";
  if (legacy === "optional") return "note_files_optional";
  return "none";
}

function responseFileSettingsFromBody(body: Record<string, unknown>): { accept: string; min: string; max: string; maxSize: string } {
  const files = asRecord(asRecord(body.response)?.files);
  return {
    accept: Array.isArray(files?.accept) ? files.accept.map(String).join(", ") : "",
    min: files?.min_items == null ? "1" : String(files.min_items),
    max: files?.max_items == null ? "" : String(files.max_items),
    maxSize: files?.max_size_mb == null ? "" : String(files.max_size_mb),
  };
}

function storageFileFromBody(kind: string, body: Record<string, unknown>): StorageFile | null {
  if (kind !== "audio" && kind !== "video" && kind !== "image") return null;
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
  contentBlocks: ContentBlockDraft[],
  responsePreset: ResponsePreset,
  responseAccept: string,
  responseMin: string,
  responseMax: string,
  responseMaxSize: string,
): Record<string, unknown> {
  const noteEnabled = responsePreset.includes("note");
  const noteRequired = responsePreset === "note_required" || responsePreset === "files_required_note_required";
  const filesEnabled = responsePreset.includes("files");
  const filesRequired = responsePreset.includes("files_required");
  const response = responsePreset === "none" ? {} : {
    response: {
      note: { enabled: noteEnabled, required: noteRequired },
      files: {
        enabled: filesEnabled,
        required: filesRequired,
        ...(filesEnabled && responseAccept.trim() ? { accept: responseAccept.split(",").map((item) => item.trim()).filter(Boolean) } : {}),
        ...(filesEnabled && responseMin ? { min_items: Number(responseMin) } : {}),
        ...(filesEnabled && responseMax ? { max_items: Number(responseMax) } : {}),
        ...(filesEnabled && responseMaxSize ? { max_size_mb: Number(responseMaxSize) } : {}),
      },
    },
  };
  if (kind === "text") return { markdown: text, ...response };
  if (kind === "content") return {
    blocks: contentBlocks.map((block) => {
      if (block.type === "markdown") return { type: "markdown", markdown: block.markdown };
      if (block.type === "image") return {
        type: "image",
        storage_file_id: block.file?.id,
        ...(block.caption.trim() ? { caption: block.caption } : {}),
        ...(block.alt.trim() ? { alt: block.alt } : {}),
      };
      if (block.type === "callout") return { type: "callout", text: block.text, tone: block.tone };
      return { type: "divider" };
    }),
    ...response,
  };
  return {
    storage_file_id: selectedFile?.id,
    caption: text,
    ...response,
  };
}

function fileExtensionMatchesKind(name: string, kind: "audio" | "video" | "image"): boolean {
  const audio = [".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac"];
  const video = [".mp4", ".mov", ".webm", ".m4v", ".ogv"];
  const image = [".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".heic"];
  return (kind === "audio" ? audio : kind === "video" ? video : image).some((ext) => name.endsWith(ext));
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

const ALL_KINDS = ["text", "content", "image", "audio", "video"];

// ─── Public links ──────────────────────────────────────────────

function PublicLinksTab({ projectId }: { projectId: string }) {
  const [data, setData] = useState<PublicDomainsResponse | null>(null);
  const [adding, setAdding] = useState(false);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const next = await api<PublicDomainsResponse>("/public-domains", projectId);
      setData(next);
      setErr(null);
    } catch (error) {
      setErr((error as Error).message);
    }
  }, [projectId]);

  useEffect(() => { reload(); }, [reload]);

  const makeDefault = async (id: number) => {
    setBusyId(id);
    try {
      await api(`/public-domains/${id}/default`, projectId, { method: "POST", body: "{}" });
      await reload();
    } catch (error) {
      setErr((error as Error).message);
    } finally {
      setBusyId(null);
    }
  };

  const detach = async (item: PublicDomain) => {
    const detail = item.dns_managed
      ? " Its Gigs-owned DNS record will also be removed."
      : " Its existing or manually managed DNS record will be left unchanged.";
    if (!window.confirm(`Detach ${item.hostname}?${detail}`)) return;
    setBusyId(item.id);
    try {
      await api(`/public-domains/${item.id}?remove_dns=true`, projectId, { method: "DELETE" });
      await reload();
    } catch (error) {
      setErr((error as Error).message);
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="p-5 space-y-4 max-w-5xl">
      <SectionHeading
        title="Public worker links"
        action={(
          <Button
            type="button"
            tone="primary"
            size="md"
            onClick={() => setAdding(true)}
            disabled={!data?.domains_bound || adding}
          >
            <Icon name="plus" /> Add hostname
          </Button>
        )}
      />
      <p className="text-sm text-text-muted">
        New assignments use the default hostname. Existing assignment links keep their original hostname.
      </p>

      {err && <div role="alert" className="rounded border border-red/30 bg-red/10 p-3 text-sm text-red">{err}</div>}
      {data?.domains_error && <div className="rounded border border-yellow/30 bg-yellow/10 p-3 text-sm text-yellow">{data.domains_error}</div>}
      {data && !data.domains_bound && (
        <div className="rounded border border-border bg-bg-subtle p-4 text-sm text-text-muted">
          Bind the Domains app to Gigs to publish a branded worker-link hostname.
        </div>
      )}

      {adding && data && (
        <AttachPublicDomainForm
          projectId={projectId}
          domains={data.available_domains || []}
          suggestedTarget={data.suggested_dns_target || ""}
          onDone={() => { setAdding(false); reload(); }}
          onCancel={() => setAdding(false)}
        />
      )}

      <Panel className="divide-y divide-border overflow-hidden">
        {data && data.public_domains.length === 0 && (
          <div className="p-5 text-sm text-text-muted">No custom worker-link hostnames.</div>
        )}
        {(data?.public_domains || []).map((item) => (
          <div key={item.id} className="flex flex-wrap items-center justify-between gap-4 p-4">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium break-all">{item.hostname}</span>
                {item.is_default && <Pill tone="success">default</Pill>}
                <Pill tone={item.status === "active" ? "success" : "danger"}>{item.status}</Pill>
              </div>
              <div className="mt-1 text-xs text-text-muted">
                {item.dns_type} {item.dns_name}.{item.apex_domain} → {item.dns_value}
                {item.dns_managed ? " · managed by Gigs" : " · existing/manual DNS"}
              </div>
              {item.status_detail && <div className="mt-1 text-xs text-red">{item.status_detail}</div>}
            </div>
            <div className="flex items-center gap-2">
              {!item.is_default && item.status === "active" && (
                <Button disabled={busyId !== null} onClick={() => makeDefault(item.id)}>
                  Make default
                </Button>
              )}
              <Button tone="danger" disabled={busyId !== null} onClick={() => detach(item)}>
                Detach
              </Button>
            </div>
          </div>
        ))}
      </Panel>
    </div>
  );
}

function AttachPublicDomainForm({
  projectId, domains, suggestedTarget, onDone, onCancel,
}: {
  projectId: string;
  domains: AvailableDomain[];
  suggestedTarget: string;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [apex, setApex] = useState(domains[0]?.name || "");
  const [subdomain, setSubdomain] = useState("gigs");
  const [dnsTarget, setDNSTarget] = useState(suggestedTarget);
  const [autoDNS, setAutoDNS] = useState(true);
  const [makeDefault, setMakeDefault] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const hostname = [subdomain.trim().replace(/^\.|\.$/g, ""), apex].filter(Boolean).join(".");

  return (
    <Panel className="p-4">
      <form
        className="space-y-4"
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          setErr(null);
          try {
            await api("/public-domains", projectId, {
              method: "POST",
              body: JSON.stringify({
                apex_domain: apex,
                subdomain,
                dns_target: dnsTarget,
                auto_dns: autoDNS,
                make_default: makeDefault,
              }),
            });
            onDone();
          } catch (error) {
            setErr((error as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          <label className="block">
            <span className="mb-1 block text-xs text-text-muted">Subdomain</span>
            <Field value={subdomain} onChange={(event) => setSubdomain(event.target.value)} placeholder="gigs" />
          </label>
          <label className="block">
            <span className="mb-1 block text-xs text-text-muted">Domain</span>
            <Field as="select" value={apex} onChange={(event) => setApex(event.target.value)} required>
              {domains.length === 0 && <option value="">No Domains inventory</option>}
              {domains.map((domain) => <option key={domain.id || domain.name} value={domain.name}>{domain.name}</option>)}
            </Field>
          </label>
          <label className="block">
            <span className="mb-1 block text-xs text-text-muted">DNS target</span>
            <Field value={dnsTarget} onChange={(event) => setDNSTarget(event.target.value)} required className="font-mono" />
          </label>
        </div>

        <div className="text-sm text-text-muted">
          Worker links will use <span className="font-mono text-text">https://{hostname || "hostname"}/worker/…</span>
        </div>
        <div className="flex flex-wrap gap-4 text-sm text-text-muted">
          <label className="inline-flex items-center gap-2">
            <input type="checkbox" checked={autoDNS} onChange={(event) => setAutoDNS(event.target.checked)} />
            Create DNS record through Domains
          </label>
          <label className="inline-flex items-center gap-2">
            <input type="checkbox" checked={makeDefault} onChange={(event) => setMakeDefault(event.target.checked)} />
            Use for new assignments
          </label>
        </div>
        {err && <div role="alert" className="text-sm text-red">{err}</div>}
        <div className="flex gap-2">
          <Button disabled={busy || !apex || !dnsTarget} tone="primary" size="md">
            {busy ? "Publishing…" : "Publish hostname"}
          </Button>
          <Button type="button" onClick={onCancel} disabled={busy} tone="ghost" size="md">Cancel</Button>
        </div>
      </form>
    </Panel>
  );
}

// ─── Workers ────────────────────────────────────────────────────

function WorkersTab({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<Worker[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [skills, setSkills] = useState<Skill[]>([]);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const existingContactIds = useMemo(
    () => new Set((items || []).map((wk) => wk.contact_id)),
    [items],
  );

  const reload = useCallback(() => {
    setLoadErr(null);
    api<{ workers: Worker[] }>("/workers?status=&include_contact=true", projectId)
      .then((d) => setItems(d.workers || []))
      .catch((e) => { setItems([]); setLoadErr((e as Error).message); });
    api<{ skills: Skill[] }>("/skills", projectId).then((d) => setSkills(d.skills || [])).catch((e) => setLoadErr((e as Error).message));
  }, [projectId]);
  useEffect(() => { reload(); }, [reload]);

  return (
    <div className="p-5 space-y-4">
      <SectionHeading
        title="Workers"
        action={(
        <Button onClick={() => setAdding(true)} tone="primary">
          <Icon name="plus" /> Add worker
        </Button>
        )}
      />
      {loadErr && <div role="alert" className="p-3 rounded border border-red/30 bg-red/10 text-red text-sm">{loadErr}</div>}
      {adding && (
        <NewWorkerForm
          projectId={projectId}
          skills={skills}
          existingContactIds={existingContactIds}
          onDone={() => { setAdding(false); reload(); }}
          onCancel={() => setAdding(false)}
        />
      )}
      <Panel className="divide-y divide-border overflow-hidden">
        {items?.length === 0 && <div className="p-4 text-text-muted text-sm">No workers yet.</div>}
        {items?.map((wk) => (
          <div key={wk.id} className="p-3 flex items-start gap-3">
            <span className="text-accent mt-1"><Icon name="user" /></span>
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
              {wk.pay_profile?.pay_grade && <span>{wk.pay_profile.pay_grade.name}</span>}
            </div>
          </div>
        ))}
      </Panel>
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
      className="p-4 border border-border rounded space-y-3 bg-bg-subtle"
    >
      <Segmented
        value={mode}
        onChange={(next) => { setMode(next); setErr(null); }}
        options={[
          { value: "crm", label: "From CRM" },
          { value: "new", label: "New contact" },
        ]}
      />

      {mode === "crm" ? (
        <div className="space-y-2">
          <Field
            value={crmQuery}
            onChange={(e) => setCrmQuery(e.target.value)}
            placeholder="Search CRM contacts"
          />
          <Panel className="divide-y divide-border bg-bg max-h-52 overflow-auto">
            {crmBusy && <div className="p-2 text-xs text-text-muted">Searching…</div>}
            {!crmBusy && crmErr && <div className="p-2 text-xs text-red">{crmErr}</div>}
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
                    (selected ? "bg-accent/10" : "hover:bg-bg-subtle ") +
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
          </Panel>
        </div>
      ) : (
        <>
          <Field value={name} onChange={(e) => setName(e.target.value)} placeholder="Display name" required={mode === "new"} />
          <div className="grid grid-cols-2 gap-2">
            <Field value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email" type="email" />
            <Field value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="Phone (E.164)" />
          </div>
          <Field value={company} onChange={(e) => setCompany(e.target.value)} placeholder="Company (optional)" />
        </>
      )}

      <Field as="select" value={channel} onChange={(e) => setChannel(e.target.value)}>
        <option value="">Default channel — let CRM pick</option>
        <option value="email">Email</option>
        <option value="sms">SMS</option>
        <option value="whatsapp">WhatsApp</option>
      </Field>
      <Field
        as="textarea"
        value={notes}
        onChange={(e) => setNotes(e.target.value)}
        placeholder="Notes (optional)"
        rows={2}
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
                  className={"px-2 py-0.5 text-xs rounded border " + (on ? "bg-accent/10 border-accent/30 text-accent" : "border-border text-text-muted")}
                >
                  {s.name}
                </button>
              );
            })}
          </div>
        </div>
      )}
      {err && <div className="text-red text-xs">{err}</div>}
      <div className="flex gap-2">
        <Button disabled={busy || (mode === "crm" && !selectedContact)} tone="primary">Add worker</Button>
        <Button type="button" onClick={onCancel} tone="ghost">Cancel</Button>
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

function toDateTimeLocal(iso?: string): string {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

function formatMoneyMinor(amountMinor: number, currency: string): string {
  const amount = Number(amountMinor || 0) / 100;
  try { return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD" }).format(amount); }
  catch { return `${currency || ""} ${amount.toFixed(2)}`.trim(); }
}
