// OperationsPanel — the owner's view of the operations app.
//
// Overview (figures, what needs the owner, a board by phase), Work
// (search and filters), Agenda (a day per resource), Resources and
// Setup (brands, packs, policies, rate book, recurring work). A
// drawer runs one work item through its life: quote, schedule,
// forms, completion, invoice and payment.
//
// Every action goes through POST /api/apps/operations/call/<tool>,
// the same code path agents use, so the guards are the same.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

// ─── types (as the tools return them) ──────────────────────────────

interface Brand { id: number; key: string; name: string; legal_name?: string; email?: string; phone?: string; website?: string; number_prefix: string; playbook?: string; status: string }
interface Work {
  id: number; number: string; brand_id: number; brand?: string; work_type: string; mode: string; phase: string; status: string;
  hold_from_status?: string; priority: string; source: string; customer_name: string; customer_email: string; customer_phone: string;
  site_id: number | null; party_size: number; summary: string; intake: Record<string, unknown>; duration_min: number; currency: string;
  estimate_cents: number; quote_status: string; invoice_ref?: string; invoiced_cents: number; paid_at?: string; sla_due_at?: string;
  created_at: string; updated_at: string; closed_at?: string; agreement_id?: number;
}
interface Line { code: string; kind: string; description: string; qty: number; unit?: string; unit_price_cents: number; total_cents: number; cost_cents: number }
interface SlotResource { id: number; name: string; kind: string; role?: string }
interface Slot {
  id: number; work_id: number; starts_at: string; ends_at: string; status: string; eta?: string; notes: string; resources: SlotResource[];
  work?: { number: string; work_type: string; status: string; summary: string; customer_name: string; customer_phone: string; party_size: number; priority: string };
}
interface FormField { key: string; label: string; type: string; required?: boolean; options?: string[]; min?: number; max?: number }
interface Form { key: string; brand_id: number; name: string; purpose: string; schema: { fields: FormField[] } }
interface FormResponse { id: number; form_key: string; data: Record<string, unknown>; submitted_at: string; responder: string }
interface WorkDetail {
  work: Work; phases: string[]; can_move_to: string[]; required_forms?: Record<string, string[]>; estimate_lines: Line[]; actual_lines: Line[];
  slots: Slot[]; forms: FormResponse[]; playbook?: string; brand?: Brand;
}
interface WorkEvent { id: number; at: string; actor: string; kind: string; from?: string; to?: string; payload?: Record<string, unknown> }
interface WorkType { key: string; name: string; mode: string; brand_id: number; phases: string[]; intake_form_key?: string | null; active: boolean; default_duration_min?: number }
interface RateItem { code: string; name: string; brand_id: number; pricing: { mode: string; unit?: string; unit_cents?: number; cents?: number; formula?: string; quote_only?: boolean }; cost_cents?: number; active: boolean }
interface Modifier { key: string; name?: string; brand_id: number; amount?: { pct?: number; cents?: number } }
interface Resource {
  id: number; kind: string; name: string; site_id: number | null; zone: string; capacity: number; quals: string[]; brand_ids: number[];
  cost_rate_cents: number; needs_shift: boolean; active: boolean;
}
interface Site { id: number; brand_id: number; kind: string; name: string; address: string; timezone: string }
interface Agreement { id: number; name: string; brand?: string; work_type: string; customer_name: string; recurrence: { every: number; unit: string; weekdays?: number[]; time?: string }; next_on: string; status: string; auto_book: boolean }
interface Metrics {
  open_by_phase?: Record<string, number>; created: number; closed: number; cancelled: number; waiting_on_owner: number;
  quote_acceptance_pct?: number; invoiced_cents: number; margin_pct?: number; median_minutes_to_first_slot?: number;
  utilisation?: { resource_id: number; name: string; utilisation_pct: number; booked_minutes: number; shift_minutes: number }[];
  by_brand?: { brand: string; name: string; created: number; closed: number; open: number; invoiced_cents: number }[];
}
interface PackInfo { key: string; name: string; description: string; work_types: string[] }
interface InstalledPack { key: string; name: string; version: string; brand?: string; installed_at: string }
interface SlotOption { starts_at: string; ends_at: string; resources: SlotResource[]; score: number }
interface Config {
  work_types?: WorkType[]; forms?: Form[]; rate_items?: RateItem[]; modifiers?: Modifier[];
  policies?: Record<string, unknown>; brand_policies?: Record<string, Record<string, unknown>>;
}
interface AppEventEnvelope { topic: string; seq: number; data: unknown }

type Call = <T,>(tool: string, args?: Record<string, unknown>) => Promise<T>;
type Tab = "overview" | "work" | "agenda" | "resources" | "setup";

const API = "/api/apps/operations";
const PHASES = ["intake", "quote", "schedule", "execute", "bill"];
const PHASE_LABEL: Record<string, string> = { intake: "Intake", quote: "Quote", schedule: "To schedule", execute: "Scheduled", bill: "To bill", closed: "Closed", cancelled: "Cancelled" };
const PHASE_TONE: Record<string, string> = {
  intake: "text-blue", quote: "text-yellow", schedule: "text-accent", execute: "text-green", bill: "text-yellow", closed: "text-text-dim", cancelled: "text-text-dim",
};
const PHASE_COLOR: Record<string, string> = {
  intake: "var(--color-blue)", quote: "var(--color-yellow)", schedule: "var(--color-accent)", execute: "var(--color-green)", bill: "var(--color-yellow)",
};
const PRIORITY_TONE: Record<string, string> = { p1: "bg-red/15 text-red", p2: "bg-yellow/15 text-yellow", p3: "bg-bg-input text-text-muted", p4: "bg-bg-input text-text-dim" };
const RESOURCE_KINDS = ["person", "crew", "space", "equipment", "vehicle"];
const KIND_LABEL: Record<string, string> = { person: "People", crew: "Crews", space: "Spaces", equipment: "Equipment", vehicle: "Vehicles" };

// ─── helpers ───────────────────────────────────────────────────────

const human = (s: string) => (s ? (s.charAt(0).toUpperCase() + s.slice(1)).replace(/_/g, " ") : "");
function money(cents: number | undefined | null, currency = "EUR"): string {
  if (cents == null) return "—";
  try { return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "EUR" }).format(cents / 100); }
  catch { return `${(cents / 100).toFixed(2)} ${currency}`; }
}
function when(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const today = new Date();
  const sameDay = d.toDateString() === today.toDateString();
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return sameDay ? `Today ${time}` : `${d.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" })} ${time}`;
}
function ago(iso?: string): string {
  if (!iso) return "";
  const m = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (m < 1) return "just now";
  if (m < 60) return `${m} min ago`;
  if (m < 60 * 24) return `${Math.round(m / 60)} h ago`;
  return `${Math.round(m / 1440)} d ago`;
}
function minutes(m?: number): string {
  if (m == null) return "—";
  if (m < 60) return `${Math.round(m)} min`;
  if (m < 60 * 48) return `${Math.round(m / 6) / 10} h`;
  return `${Math.round(m / 144) / 10} d`;
}
const startOfDay = (d: Date) => { const x = new Date(d); x.setHours(0, 0, 0, 0); return x; };
const addDays = (d: Date, n: number) => { const x = new Date(d); x.setDate(x.getDate() + n); return x; };
const isoDate = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const slaLate = (w: Work) => !!w.sla_due_at && new Date(w.sla_due_at).getTime() < Date.now() && (w.phase === "intake" || w.phase === "quote" || w.phase === "schedule");
const openPhase = (p: string) => p !== "closed" && p !== "cancelled";

// ─── API + live events ─────────────────────────────────────────────

function useCall(projectId: string, installId: number): Call {
  return useCallback(async <T,>(tool: string, args: Record<string, unknown> = {}): Promise<T> => {
    const q = new URLSearchParams({ project_id: projectId, install_id: String(installId) });
    const res = await fetch(`${API}/call/${tool}?${q}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(args),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || `${tool} failed (${res.status})`);
    return res.json();
  }, [projectId, installId]);
}

// Shared app-event bridge when the dashboard provides one, otherwise
// an EventSource of our own.
function useAppEvents(projectId: string, onEvent: (ev: AppEventEnvelope) => void) {
  const ref = useRef(onEvent);
  ref.current = onEvent;
  useEffect(() => {
    if (!projectId) return;
    const handler = (ev: AppEventEnvelope) => ref.current(ev);
    const bridge = (window as unknown as { __aptevaAppEvents?: { subscribe(app: string, pid: string, fn: (ev: AppEventEnvelope) => void): () => void } }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe("operations", projectId, handler);
    let lastSeq = 0;
    let es: EventSource | null = null;
    let timer: number | null = null;
    let cancelled = false;
    const connect = () => {
      if (cancelled) return;
      es = new EventSource(`/api/app-events/operations?project_id=${encodeURIComponent(projectId)}${lastSeq ? `&since=${lastSeq}` : ""}`, { withCredentials: true });
      es.onmessage = (e) => {
        try { const ev = JSON.parse(e.data) as AppEventEnvelope; if (ev.seq <= lastSeq) return; lastSeq = ev.seq; handler(ev); } catch { /* ignore */ }
      };
      es.onerror = () => { if (es && es.readyState === EventSource.CLOSED) { if (timer) window.clearTimeout(timer); timer = window.setTimeout(connect, 2000); } };
    };
    connect();
    return () => { cancelled = true; if (timer) window.clearTimeout(timer); es?.close(); };
  }, [projectId]);
}

// ─── icons (Lucide geometry, ISC) ──────────────────────────────────

function Icon({ d, size = 14 }: { d: ReactNode; size?: number }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{d}</svg>;
}
const I = {
  plus: <><path d="M5 12h14" /><path d="M12 5v14" /></>,
  x: <><path d="M18 6 6 18" /><path d="m6 6 12 12" /></>,
  refresh: <><path d="M21 12a9 9 0 0 0-9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" /><path d="M3 3v5h5" /><path d="M3 12a9 9 0 0 0 9 9 9.75 9.75 0 0 0 6.74-2.74L21 16" /><path d="M16 16h5v5" /></>,
  left: <path d="m15 18-6-6 6-6" />,
  right: <path d="m9 18 6-6-6-6" />,
  alert: <><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" /><path d="M12 9v4" /><path d="M12 17h.01" /></>,
  clock: <><circle cx="12" cy="12" r="10" /><path d="M12 6v6l4 2" /></>,
  phone: <path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72c.13.96.36 1.9.7 2.81a2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45c.91.34 1.85.57 2.81.7A2 2 0 0 1 22 16.92z" />,
  mail: <><rect width="20" height="16" x="2" y="4" rx="2" /><path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7" /></>,
  users: <><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><path d="M22 21v-2a4 4 0 0 0-3-3.87" /><path d="M16 3.13a4 4 0 0 1 0 7.75" /></>,
  repeat: <><path d="m17 2 4 4-4 4" /><path d="M3 11v-1a4 4 0 0 1 4-4h14" /><path d="m7 22-4-4 4-4" /><path d="M21 13v1a4 4 0 0 1-4 4H3" /></>,
  inbox: <><polyline points="22 12 16 12 14 15 10 15 8 12 2 12" /><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z" /></>,
};

// ─── small UI pieces ───────────────────────────────────────────────

const btn = "inline-flex items-center gap-1.5 px-2.5 py-1 text-xs border border-border rounded text-text hover:bg-bg-hover disabled:opacity-50 whitespace-nowrap";
const btnPrimary = "inline-flex items-center gap-1.5 px-2.5 py-1 text-xs border border-accent bg-accent text-bg rounded hover:bg-accent-hover disabled:opacity-50 whitespace-nowrap";
const btnDanger = "inline-flex items-center gap-1.5 px-2.5 py-1 text-xs border border-red/40 text-red rounded hover:bg-red/10 disabled:opacity-50 whitespace-nowrap";
const input = "bg-bg-input border border-border rounded px-2 py-1 text-sm text-text min-w-0";
const sectionTitle = "text-xs uppercase tracking-wide text-text-dim mb-2";

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" onClick={onClick} className={`px-3 py-1.5 rounded-t border-b-2 ${active ? "border-accent text-accent" : "border-transparent text-text-muted hover:text-text"}`}>{children}</button>;
}
function PhaseBadge({ phase, status }: { phase: string; status?: string }) {
  const hold = status === "on_hold" || status === "needs_owner";
  return <span className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs bg-bg-input ${hold ? (status === "needs_owner" ? "text-red" : "text-yellow") : PHASE_TONE[phase] ?? "text-text-muted"}`}>
    {hold ? human(status!) : status && status !== phase ? human(status) : PHASE_LABEL[phase] ?? human(phase)}
  </span>;
}
function Priority({ p }: { p: string }) {
  return <span className={`rounded px-1.5 py-0.5 text-xs font-medium uppercase ${PRIORITY_TONE[p] ?? PRIORITY_TONE.p3}`}>{p}</span>;
}
function BrandTag({ brand, brands }: { brand?: string; brands: Brand[] }) {
  if (!brand) return null;
  const b = brands.find((x) => x.key === brand);
  return <span className="rounded px-1.5 py-0.5 text-xs border border-border text-text-muted">{b?.name ?? brand}</span>;
}
function Stat({ label, value, hint, tone }: { label: string; value: ReactNode; hint?: ReactNode; tone?: string }) {
  return <div className="border border-border rounded-lg bg-bg-card px-4 py-3 min-w-0">
    <div className="text-xs text-text-muted">{label}</div>
    <div className={`text-xl font-semibold mt-1 ${tone ?? "text-text"}`}>{value}</div>
    {hint != null && <div className="text-xs text-text-dim mt-0.5 truncate">{hint}</div>}
  </div>;
}
function Empty({ children }: { children: ReactNode }) {
  return <div className="text-sm text-text-muted text-center py-10 px-4">{children}</div>;
}
function Modal({ title, onClose, children, footer, wide }: { title: string; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return <div className="absolute inset-0 bg-black/40 flex items-center justify-center z-30 p-4" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}>
    <div className={`bg-bg border border-border rounded-lg shadow-lg w-full ${wide ? "max-w-2xl" : "max-w-md"} max-h-full flex flex-col`}>
      <header className="flex items-center justify-between px-4 py-2 border-b border-border">
        <div className="text-sm text-text font-medium">{title}</div>
        <button type="button" onClick={onClose} className="text-text-dim hover:text-text p-1" aria-label="Close"><Icon d={I.x} /></button>
      </header>
      <div className="p-4 space-y-3 text-sm overflow-auto">{children}</div>
      {footer && <footer className="flex items-center justify-end gap-2 px-4 py-3 border-t border-border">{footer}</footer>}
    </div>
  </div>;
}
function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return <label className="flex flex-col gap-1 text-xs text-text-muted min-w-0">
    <span>{label}</span>{children}{hint && <span className="text-text-dim">{hint}</span>}
  </label>;
}

// Renders a form schema (intake questions, checklists, sign-offs).
function FormFields({ fields, values, onChange }: { fields: FormField[]; values: Record<string, unknown>; onChange: (v: Record<string, unknown>) => void }) {
  const set = (k: string, v: unknown) => onChange({ ...values, [k]: v });
  return <div className="grid grid-cols-2 gap-3">
    {fields.map((f) => {
      const v = values[f.key];
      const label = f.label + (f.required ? " *" : "");
      if (f.type === "bool") {
        return <label key={f.key} className="col-span-2 flex items-center gap-2 text-sm text-text">
          <input type="checkbox" checked={v === true} onChange={(e) => set(f.key, e.target.checked)} />{label}
        </label>;
      }
      if (f.type === "choice" && f.options?.length) {
        return <Field key={f.key} label={label}>
          <select className={input} value={String(v ?? "")} onChange={(e) => set(f.key, e.target.value || undefined)}>
            <option value="">—</option>{f.options.map((o) => <option key={o} value={o}>{human(o)}</option>)}
          </select>
        </Field>;
      }
      if (f.type === "number") {
        return <Field key={f.key} label={label}>
          <input className={input} type="number" min={f.min} max={f.max} value={v == null ? "" : String(v)}
            onChange={(e) => set(f.key, e.target.value === "" ? undefined : Number(e.target.value))} />
        </Field>;
      }
      const long = f.type === "text" || f.type === "textarea";
      return <Field key={f.key} label={label}>
        {long
          ? <textarea className={`${input} col-span-2`} rows={2} value={String(v ?? "")} onChange={(e) => set(f.key, e.target.value || undefined)} />
          : <input className={input} type={f.type === "date" ? "date" : f.type === "time" ? "time" : "text"} value={String(v ?? "")} onChange={(e) => set(f.key, e.target.value || undefined)} />}
      </Field>;
    })}
  </div>;
}

// ─── panel ─────────────────────────────────────────────────────────

export default function OperationsPanel({ projectId, installId }: NativePanelProps) {
  const call = useCall(projectId, installId);
  const [tab, setTab] = useState<Tab>("overview");
  const [brands, setBrands] = useState<Brand[]>([]);
  const [brand, setBrand] = useState("");
  const [openWork, setOpenWork] = useState<string | null>(null);
  const [intakeOpen, setIntakeOpen] = useState(false);
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
  const [rev, setRev] = useState(0);
  const bump = useCallback(() => setRev((r) => r + 1), []);
  const report = useCallback((e: unknown) => setError((e as Error).message || String(e)), []);
  const notify = useCallback((m: string) => { setToast(m); window.setTimeout(() => setToast((t) => (t === m ? "" : t)), 3500); }, []);

  const loadBrands = useCallback(() => {
    call<{ brands: Brand[] }>("brands_list").then((r) => setBrands(r.brands ?? [])).catch(report);
  }, [call, report]);
  useEffect(loadBrands, [loadBrands, rev]);

  // Agents and other panels change work too; refresh when they do.
  const pending = useRef<number | null>(null);
  useAppEvents(projectId, () => {
    if (pending.current) window.clearTimeout(pending.current);
    pending.current = window.setTimeout(bump, 400);
  });

  const activeBrands = brands.filter((b) => b.status === "active");
  const ctx: Ctx = { call, brand, brands, rev, bump, report, notify, open: setOpenWork };

  return (
    <div className="h-full flex flex-col relative text-text">
      <div className="flex items-center gap-3 border-b border-border px-3 pt-2">
        <nav className="flex gap-1 text-xs flex-1 min-w-0 overflow-x-auto">
          <TabButton active={tab === "overview"} onClick={() => setTab("overview")}>Overview</TabButton>
          <TabButton active={tab === "work"} onClick={() => setTab("work")}>Work</TabButton>
          <TabButton active={tab === "agenda"} onClick={() => setTab("agenda")}>Agenda</TabButton>
          <TabButton active={tab === "resources"} onClick={() => setTab("resources")}>Resources</TabButton>
          <TabButton active={tab === "setup"} onClick={() => setTab("setup")}>Setup</TabButton>
        </nav>
        <div className="flex items-center gap-2 pb-1.5">
          {activeBrands.length > 0 && (
            <select className={`${input} text-xs`} value={brand} onChange={(e) => setBrand(e.target.value)} aria-label="Brand">
              <option value="">All brands</option>
              {activeBrands.map((b) => <option key={b.key} value={b.key}>{b.name}</option>)}
            </select>
          )}
          <button type="button" className={btn} onClick={bump} title="Refresh"><Icon d={I.refresh} /></button>
          <button type="button" className={btnPrimary} onClick={() => setIntakeOpen(true)}><Icon d={I.plus} />New work</button>
        </div>
      </div>

      <div className="flex-1 min-h-0 overflow-auto">
        {tab === "overview" && <Overview ctx={ctx} />}
        {tab === "work" && <WorkTab ctx={ctx} />}
        {tab === "agenda" && <AgendaTab ctx={ctx} />}
        {tab === "resources" && <ResourcesTab ctx={ctx} />}
        {tab === "setup" && <SetupTab ctx={ctx} />}
      </div>

      {openWork && <WorkDrawer ctx={ctx} number={openWork} onClose={() => setOpenWork(null)} />}
      {intakeOpen && <IntakeDialog ctx={ctx} onClose={() => setIntakeOpen(false)} onCreated={(n) => { setIntakeOpen(false); bump(); setOpenWork(n); }} />}

      {error && (
        <div className="absolute bottom-4 right-4 z-40 max-w-sm border border-red/50 bg-red/10 text-text rounded shadow-lg flex items-start gap-2 p-3 text-sm">
          <span className="text-red shrink-0"><Icon d={I.alert} /></span>
          <span className="flex-1 break-words">{error}</span>
          <button type="button" onClick={() => setError("")} className="text-text-dim hover:text-text shrink-0" aria-label="Dismiss"><Icon d={I.x} /></button>
        </div>
      )}
      {toast && !error && <div className="absolute bottom-4 right-4 z-40 border border-border bg-bg-card rounded shadow-lg px-3 py-2 text-sm">{toast}</div>}
    </div>
  );
}

interface Ctx {
  call: Call; brand: string; brands: Brand[]; rev: number; bump: () => void;
  report: (e: unknown) => void; notify: (m: string) => void; open: (number: string) => void;
}
const withBrand = (ctx: Ctx, args: Record<string, unknown> = {}) => (ctx.brand ? { ...args, brand: ctx.brand } : args);

// ─── Overview ──────────────────────────────────────────────────────

function Overview({ ctx }: { ctx: Ctx }) {
  const { call, brand, rev, report } = ctx;
  const [m, setM] = useState<Metrics | null>(null);
  const [work, setWork] = useState<Work[]>([]);
  const [today, setToday] = useState<Slot[]>([]);
  const [currency, setCurrency] = useState("EUR");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let live = true;
    setLoading(true);
    const from = startOfDay(new Date());
    Promise.all([
      call<Metrics>("metrics", withBrand(ctx)),
      call<{ work: Work[] }>("work_search", withBrand(ctx, { phase: "open", limit: 300 })),
      call<{ slots: Slot[] }>("slots_list", withBrand(ctx, { from: from.toISOString(), to: addDays(from, 1).toISOString() })),
      call<Config>("config_get", withBrand(ctx, { section: "policies" })),
    ]).then(([mm, ww, ss, cfg]) => {
      if (!live) return;
      setM(mm); setWork(ww.work ?? []); setToday((ss.slots ?? []).filter((s) => s.status !== "cancelled"));
      const cur = (brand && cfg.brand_policies?.[brand]?.currency) || cfg.policies?.currency;
      if (typeof cur === "string" && cur) setCurrency(cur);
    }).catch(report).finally(() => live && setLoading(false));
    return () => { live = false; };
  }, [call, brand, rev]); // eslint-disable-line react-hooks/exhaustive-deps

  const attention = work.filter((w) => w.status === "needs_owner" || slaLate(w) || w.priority === "p1");
  const byPhase = useMemo(() => {
    const g: Record<string, Work[]> = {};
    for (const p of PHASES) g[p] = [];
    for (const w of work) (g[w.phase] ??= []).push(w);
    return g;
  }, [work]);

  if (loading && !m) return <Empty>Loading…</Empty>;
  const openCount = work.length;
  return (
    <div className="p-4 space-y-5">
      <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
        <Stat label="Open work" value={openCount} hint={`${m?.created ?? 0} new · ${m?.closed ?? 0} closed in 30 days`} />
        <Stat label="Waiting on you" value={m?.waiting_on_owner ?? 0} tone={(m?.waiting_on_owner ?? 0) > 0 ? "text-red" : undefined} hint="needs an owner decision" />
        <Stat label="Booked today" value={today.length} hint={today[0] ? `next ${when(today.find((s) => new Date(s.ends_at) > new Date())?.starts_at)}` : "nothing booked"} />
        <Stat label="Invoiced (30 days)" value={money(m?.invoiced_cents ?? 0, currency)} hint={m?.margin_pct != null ? `${m.margin_pct}% margin` : "no margin yet"} />
        <Stat label="Quotes accepted" value={m?.quote_acceptance_pct != null ? `${m.quote_acceptance_pct}%` : "—"} hint="of quotes sent" />
        <Stat label="Time to first slot" value={minutes(m?.median_minutes_to_first_slot)} hint="median, request to booking" />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <section className="lg:col-span-2 border border-border rounded-lg bg-bg-card">
          <header className="px-4 py-2 border-b border-border flex items-center gap-2">
            <span className="text-red"><Icon d={I.alert} /></span>
            <h2 className="text-sm font-medium">Needs attention</h2>
            <span className="text-xs text-text-dim">{attention.length}</span>
          </header>
          {attention.length === 0 ? <Empty>Nothing is waiting on you.</Empty> : (
            <ul>
              {attention.slice(0, 12).map((w) => (
                <li key={w.id}>
                  <button type="button" onClick={() => ctx.open(w.number)} className="w-full text-left px-4 py-2 border-b border-border hover:bg-bg-hover flex items-center gap-3">
                    <Priority p={w.priority} />
                    <span className="font-mono text-xs text-text-muted w-20 shrink-0">{w.number}</span>
                    <span className="flex-1 min-w-0 truncate text-sm">{w.customer_name || human(w.work_type)}<span className="text-text-dim"> · {w.summary || human(w.work_type)}</span></span>
                    <span className="text-xs text-text-dim shrink-0">{w.status === "needs_owner" ? "decision needed" : slaLate(w) ? "past its SLA" : "urgent"}</span>
                    <PhaseBadge phase={w.phase} status={w.status} />
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="border border-border rounded-lg bg-bg-card">
          <header className="px-4 py-2 border-b border-border flex items-center gap-2">
            <span className="text-text-muted"><Icon d={I.clock} /></span><h2 className="text-sm font-medium">Today</h2>
          </header>
          {today.length === 0 ? <Empty>No bookings today.</Empty> : (
            <ul>
              {today.slice(0, 10).map((s) => (
                <li key={s.id}>
                  <button type="button" onClick={() => s.work && ctx.open(s.work.number)} className="w-full text-left px-4 py-2 border-b border-border hover:bg-bg-hover">
                    <div className="flex items-center gap-2 text-sm">
                      <span className="font-mono text-xs text-text-muted">{new Date(s.starts_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}</span>
                      <span className="truncate flex-1">{s.work?.customer_name || s.work?.number}</span>
                      {s.status !== "booked" && <span className="text-xs text-text-dim">{human(s.status)}</span>}
                    </div>
                    <div className="text-xs text-text-dim truncate">{human(s.work?.work_type ?? "")}{s.work?.party_size ? ` · ${s.work.party_size} guests` : ""}{s.resources.length ? ` · ${s.resources.map((r) => r.name).join(", ")}` : ""}</div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      <section>
        <h2 className={sectionTitle}>Open work by phase</h2>
        <div className="grid gap-3" style={{ gridTemplateColumns: `repeat(${PHASES.length}, minmax(180px, 1fr))`, overflowX: "auto" }}>
          {PHASES.map((p) => (
            <div key={p} className="border border-border rounded-lg bg-bg-subtle min-w-0 flex flex-col" style={{ maxHeight: 420 }}>
              <div className="px-3 py-2 border-b border-border flex items-center gap-2 text-xs">
                <span style={{ width: 8, height: 8, borderRadius: 999, background: PHASE_COLOR[p] }} />
                <span className="font-medium">{PHASE_LABEL[p]}</span>
                <span className="text-text-dim ml-auto">{byPhase[p]?.length ?? 0}</span>
              </div>
              <div className="p-2 space-y-2 overflow-auto">
                {(byPhase[p] ?? []).map((w) => <WorkCard key={w.id} w={w} ctx={ctx} />)}
                {(byPhase[p] ?? []).length === 0 && <div className="text-xs text-text-dim text-center py-4">Empty</div>}
              </div>
            </div>
          ))}
        </div>
      </section>

      {!brand && m?.by_brand && m.by_brand.length > 1 && (
        <section>
          <h2 className={sectionTitle}>By brand, last 30 days</h2>
          <div className="border border-border rounded-lg overflow-hidden">
            <table className="w-full text-sm">
              <thead className="bg-bg-subtle text-xs text-text-muted"><tr>
                <th className="text-left px-3 py-2 font-normal">Brand</th><th className="text-right px-3 py-2 font-normal">Open</th>
                <th className="text-right px-3 py-2 font-normal">New</th><th className="text-right px-3 py-2 font-normal">Closed</th>
                <th className="text-right px-3 py-2 font-normal">Invoiced</th>
              </tr></thead>
              <tbody>{m.by_brand.map((b) => (
                <tr key={b.brand || "-"} className="border-t border-border">
                  <td className="px-3 py-2">{b.name}</td><td className="px-3 py-2 text-right">{b.open}</td><td className="px-3 py-2 text-right">{b.created}</td>
                  <td className="px-3 py-2 text-right">{b.closed}</td><td className="px-3 py-2 text-right">{money(b.invoiced_cents, currency)}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </section>
      )}

      {m?.utilisation && m.utilisation.length > 0 && (
        <section>
          <h2 className={sectionTitle}>Utilisation, last 30 days</h2>
          <div className="grid gap-2 md:grid-cols-2">
            {m.utilisation.map((u) => (
              <div key={u.resource_id} className="flex items-center gap-3 text-sm">
                <span className="w-32 truncate">{u.name}</span>
                <div className="flex-1 h-2 rounded bg-bg-input overflow-hidden">
                  <div style={{ width: `${Math.min(100, u.utilisation_pct)}%`, height: "100%", background: "var(--color-accent)" }} />
                </div>
                <span className="w-12 text-right text-xs text-text-muted">{Math.round(u.utilisation_pct)}%</span>
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function WorkCard({ w, ctx }: { w: Work; ctx: Ctx }) {
  return (
    <button type="button" onClick={() => ctx.open(w.number)} className="w-full text-left border border-border rounded bg-bg-card p-2 hover:border-accent">
      <div className="flex items-center gap-1.5 text-xs">
        <span className="font-mono text-text-muted">{w.number}</span>
        {w.priority !== "p3" && <Priority p={w.priority} />}
        {w.status === "needs_owner" && <span className="text-red">owner</span>}
        {slaLate(w) && <span className="text-red">late</span>}
      </div>
      <div className="text-sm truncate mt-1">{w.customer_name || human(w.work_type)}</div>
      <div className="text-xs text-text-dim truncate">{w.summary || human(w.work_type)}{w.party_size ? ` · ${w.party_size}` : ""}</div>
      {w.estimate_cents > 0 && <div className="text-xs text-text-muted mt-1">{money(w.estimate_cents, w.currency)}</div>}
    </button>
  );
}

// ─── Work tab ──────────────────────────────────────────────────────

function WorkTab({ ctx }: { ctx: Ctx }) {
  const { call, brand, rev, report, brands } = ctx;
  const [q, setQ] = useState("");
  const [phase, setPhase] = useState("open");
  const [workType, setWorkType] = useState("");
  const [priority, setPriority] = useState("");
  const [late, setLate] = useState(false);
  const [rows, setRows] = useState<Work[]>([]);
  const [types, setTypes] = useState<WorkType[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => { call<Config>("config_get", withBrand(ctx, { section: "work_types" })).then((c) => setTypes(c.work_types ?? [])).catch(report); }, [call, brand]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    let live = true;
    const t = window.setTimeout(() => {
      setLoading(true);
      const args: Record<string, unknown> = { limit: 500 };
      if (q.trim()) args.q = q.trim();
      if (phase) args.phase = phase;
      if (workType) args.work_type = workType;
      if (priority) args.priority = priority;
      if (late) args.sla_breached = true;
      call<{ work: Work[] }>("work_search", withBrand(ctx, args)).then((r) => live && setRows(r.work ?? [])).catch(report).finally(() => live && setLoading(false));
    }, 200);
    return () => { live = false; window.clearTimeout(t); };
  }, [call, brand, rev, q, phase, workType, priority, late]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="flex flex-col h-full">
      <div className="flex flex-wrap items-center gap-2 p-3 border-b border-border">
        <input className={`${input} w-64`} placeholder="Search number, customer, summary" value={q} onChange={(e) => setQ(e.target.value)} />
        <select className={input} value={phase} onChange={(e) => setPhase(e.target.value)}>
          <option value="open">All open</option><option value="">Everything</option>
          {[...PHASES, "closed", "cancelled"].map((p) => <option key={p} value={p}>{PHASE_LABEL[p]}</option>)}
        </select>
        <select className={input} value={workType} onChange={(e) => setWorkType(e.target.value)}>
          <option value="">All types</option>{uniqueByKey(types).map((t) => <option key={t.key} value={t.key}>{t.name}</option>)}
        </select>
        <select className={input} value={priority} onChange={(e) => setPriority(e.target.value)}>
          <option value="">Any priority</option>{["p1", "p2", "p3", "p4"].map((p) => <option key={p} value={p}>{p.toUpperCase()}</option>)}
        </select>
        <label className="flex items-center gap-1.5 text-xs text-text-muted"><input type="checkbox" checked={late} onChange={(e) => setLate(e.target.checked)} />Past SLA, not booked</label>
        <span className="ml-auto text-xs text-text-dim">{loading ? "Loading…" : `${rows.length} item${rows.length === 1 ? "" : "s"}`}</span>
      </div>
      {rows.length === 0 && !loading ? <Empty>No work matches.</Empty> : (
        <table className="w-full text-sm">
          <thead className="text-xs text-text-muted bg-bg-subtle sticky top-0"><tr>
            <th className="text-left font-normal px-3 py-2">Number</th><th className="text-left font-normal px-3 py-2">Customer</th>
            <th className="text-left font-normal px-3 py-2">Type</th><th className="text-left font-normal px-3 py-2">Status</th>
            <th className="text-left font-normal px-3 py-2">Priority</th><th className="text-right font-normal px-3 py-2">Estimate</th>
            <th className="text-right font-normal px-3 py-2">Updated</th>
          </tr></thead>
          <tbody>
            {rows.map((w) => (
              <tr key={w.id} onClick={() => ctx.open(w.number)} className="border-t border-border hover:bg-bg-hover cursor-pointer">
                <td className="px-3 py-2 font-mono text-xs whitespace-nowrap">{w.number}</td>
                <td className="px-3 py-2 min-w-0"><div className="truncate">{w.customer_name || "—"}</div>{w.summary && <div className="text-xs text-text-dim truncate">{w.summary}</div>}</td>
                <td className="px-3 py-2 text-text-muted whitespace-nowrap">{human(w.work_type)} {!brand && <BrandTag brand={w.brand} brands={brands} />}</td>
                <td className="px-3 py-2 whitespace-nowrap"><PhaseBadge phase={w.phase} status={w.status} />{slaLate(w) && <span className="ml-1 text-xs text-red">late</span>}</td>
                <td className="px-3 py-2"><Priority p={w.priority} /></td>
                <td className="px-3 py-2 text-right whitespace-nowrap">{w.estimate_cents ? money(w.estimate_cents, w.currency) : "—"}</td>
                <td className="px-3 py-2 text-right text-xs text-text-dim whitespace-nowrap">{ago(w.updated_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function uniqueByKey<T extends { key: string }>(list: T[]): T[] {
  const seen = new Set<string>();
  return list.filter((x) => (seen.has(x.key) ? false : (seen.add(x.key), true)));
}

// ─── Agenda ────────────────────────────────────────────────────────

function AgendaTab({ ctx }: { ctx: Ctx }) {
  const { call, brand, rev, report } = ctx;
  const [day, setDay] = useState(() => startOfDay(new Date()));
  const [slots, setSlots] = useState<Slot[]>([]);
  const [resources, setResources] = useState<Resource[]>([]);
  const [loading, setLoading] = useState(true);
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const t = window.setInterval(() => setNow(Date.now()), 60000); return () => window.clearInterval(t); }, []);

  useEffect(() => {
    let live = true;
    setLoading(true);
    Promise.all([
      call<{ slots: Slot[] }>("slots_list", withBrand(ctx, { from: day.toISOString(), to: addDays(day, 1).toISOString() })),
      call<{ resources: Resource[] }>("resources_list", withBrand(ctx)),
    ]).then(([s, r]) => {
      if (!live) return;
      setSlots((s.slots ?? []).filter((x) => x.status !== "cancelled"));
      setResources(r.resources ?? []);
    }).catch(report).finally(() => live && setLoading(false));
    return () => { live = false; };
  }, [call, brand, rev, day]); // eslint-disable-line react-hooks/exhaustive-deps

  // Hours shown: 7–21 by default, widened to fit the day's slots.
  const dayStart = day.getTime();
  let startH = 7, endH = 21;
  for (const s of slots) {
    const a = (new Date(s.starts_at).getTime() - dayStart) / 3600000;
    const b = (new Date(s.ends_at).getTime() - dayStart) / 3600000;
    startH = Math.max(0, Math.min(startH, Math.floor(a)));
    endH = Math.min(24, Math.max(endH, Math.ceil(b)));
  }
  const span = endH - startH;
  const pos = (iso: string) => Math.max(0, Math.min(100, (((new Date(iso).getTime() - dayStart) / 3600000 - startH) / span) * 100));

  // One row per resource with slots or that takes bookings, people first.
  const used = new Set(slots.flatMap((s) => s.resources.map((r) => r.id)));
  const order = ["person", "crew", "space", "vehicle", "equipment"];
  const rows = resources.filter((r) => used.has(r.id) || r.kind === "person" || r.kind === "space")
    .sort((a, b) => order.indexOf(a.kind) - order.indexOf(b.kind) || a.name.localeCompare(b.name));
  const unassigned = slots.filter((s) => s.resources.length === 0);
  const isToday = isoDate(day) === isoDate(new Date());
  const nowPct = isToday ? pos(new Date(now).toISOString()) : -1;

  const block = (s: Slot) => {
    const left = pos(s.starts_at), width = Math.max(1.2, pos(s.ends_at) - left);
    const color = s.status === "missed" ? "var(--color-red)" : s.status === "done" ? "var(--color-text-dim)" : s.status === "in_progress" ? "var(--color-green)" : "var(--color-accent)";
    return (
      <button key={s.id} type="button" onClick={() => s.work && ctx.open(s.work.number)}
        title={`${s.work?.number ?? ""} ${s.work?.customer_name ?? ""} · ${when(s.starts_at)}–${new Date(s.ends_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`}
        className="absolute rounded text-left text-xs px-1.5 overflow-hidden whitespace-nowrap hover:opacity-90"
        style={{ left: `${left}%`, width: `${width}%`, top: 4, bottom: 4, background: `color-mix(in srgb, ${color} 22%, transparent)`, borderLeft: `3px solid ${color}` }}>
        <span className="font-medium">{s.work?.customer_name || s.work?.number}</span>
        <span className="text-text-muted"> {s.work?.party_size ? `${s.work.party_size}p · ` : ""}{human(s.work?.work_type ?? "")}</span>
      </button>
    );
  };

  return (
    <div className="p-4 space-y-3">
      <div className="flex items-center gap-2">
        <button type="button" className={btn} onClick={() => setDay(addDays(day, -1))} aria-label="Previous day"><Icon d={I.left} /></button>
        <button type="button" className={btn} onClick={() => setDay(startOfDay(new Date()))}>Today</button>
        <button type="button" className={btn} onClick={() => setDay(addDays(day, 1))} aria-label="Next day"><Icon d={I.right} /></button>
        <input type="date" className={input} value={isoDate(day)} onChange={(e) => { if (e.target.value) setDay(startOfDay(new Date(e.target.value + "T00:00"))); }} />
        <h2 className="text-sm font-medium ml-2">{day.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" })}</h2>
        <span className="ml-auto text-xs text-text-dim">{loading ? "Loading…" : `${slots.length} booking${slots.length === 1 ? "" : "s"}`}</span>
      </div>

      <div className="border border-border rounded-lg overflow-hidden">
        <div className="flex border-b border-border bg-bg-subtle text-xs text-text-dim">
          <div className="shrink-0 px-3 py-1.5 border-r border-border" style={{ width: 160 }}>Resource</div>
          <div className="relative flex-1" style={{ height: 26 }}>
            {Array.from({ length: span + 1 }, (_, i) => (
              <span key={i} className="absolute top-1.5" style={{ left: `${(i / span) * 100}%`, transform: i === 0 ? "none" : i === span ? "translateX(-100%)" : "translateX(-50%)" }}>
                {String(startH + i).padStart(2, "0")}
              </span>
            ))}
          </div>
        </div>
        {rows.length === 0 && unassigned.length === 0 && <Empty>No resources yet. Add people and spaces under Resources.</Empty>}
        {[...rows.map((r) => ({ key: `r${r.id}`, label: r.name, sub: r.kind === "space" && r.capacity ? `${human(r.kind)} · ${r.capacity}` : human(r.kind), items: slots.filter((s) => s.resources.some((x) => x.id === r.id)) })),
          ...(unassigned.length ? [{ key: "none", label: "Unassigned", sub: "", items: unassigned }] : [])].map((row) => (
          <div key={row.key} className="flex border-b border-border last:border-b-0">
            <div className="shrink-0 px-3 py-2 border-r border-border min-w-0" style={{ width: 160 }}>
              <div className="text-sm truncate">{row.label}</div>
              {row.sub && <div className="text-xs text-text-dim truncate">{row.sub}</div>}
            </div>
            <div className="relative flex-1" style={{ height: 48 }}>
              {Array.from({ length: span - 1 }, (_, i) => (
                <div key={i} className="absolute top-0 bottom-0 border-l border-border" style={{ left: `${((i + 1) / span) * 100}%`, opacity: 0.5 }} />
              ))}
              {nowPct >= 0 && nowPct <= 100 && <div className="absolute top-0 bottom-0" style={{ left: `${nowPct}%`, width: 2, background: "var(--color-red)", zIndex: 2 }} />}
              {row.items.map(block)}
            </div>
          </div>
        ))}
      </div>

      {slots.length > 0 && (
        <section>
          <h2 className={sectionTitle}>List</h2>
          <div className="border border-border rounded-lg divide-y divide-border">
            {slots.map((s) => (
              <button key={s.id} type="button" onClick={() => s.work && ctx.open(s.work.number)} className="w-full text-left px-3 py-2 hover:bg-bg-hover flex items-center gap-3 text-sm">
                <span className="font-mono text-xs text-text-muted w-24 shrink-0">
                  {new Date(s.starts_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}–{new Date(s.ends_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
                </span>
                <span className="font-mono text-xs text-text-dim w-20 shrink-0">{s.work?.number}</span>
                <span className="flex-1 min-w-0 truncate">{s.work?.customer_name || "—"}<span className="text-text-dim"> · {human(s.work?.work_type ?? "")}{s.work?.party_size ? ` · ${s.work.party_size} guests` : ""}</span></span>
                <span className="text-xs text-text-muted truncate max-w-xs">{s.resources.map((r) => r.name).join(", ")}</span>
                <span className="text-xs text-text-dim w-20 text-right">{human(s.status)}</span>
              </button>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

// ─── Work drawer ───────────────────────────────────────────────────

function WorkDrawer({ ctx, number, onClose }: { ctx: Ctx; number: string; onClose: () => void }) {
  const { call, rev, report, notify, bump, brands } = ctx;
  const [d, setD] = useState<WorkDetail | null>(null);
  const [events, setEvents] = useState<WorkEvent[]>([]);
  const [forms, setForms] = useState<Form[]>([]);
  const [busy, setBusy] = useState(false);
  const [blocked, setBlocked] = useState<string[]>([]);
  const [note, setNote] = useState("");
  const [panel, setPanel] = useState<"" | "quote" | "schedule" | "form" | "invoice">("");
  const [formKey, setFormKey] = useState("");

  const load = useCallback(() => {
    Promise.all([
      call<WorkDetail>("work_get", { number }),
      call<{ events: WorkEvent[] }>("work_timeline", { number }),
    ]).then(([dd, ev]) => {
      setD(dd); setEvents(ev.events ?? []);
      return call<Config>("config_get", { section: "forms", ...(dd.work.brand ? { brand: dd.work.brand } : {}) });
    }).then((c) => setForms(c.forms ?? [])).catch(report);
  }, [call, number, report]);
  useEffect(load, [load, rev]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape" && !panel) onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, panel]);

  // Runs a tool; guard refusals come back as {ok:false, blocked:{reasons}}.
  const act = async (tool: string, args: Record<string, unknown>, done?: string) => {
    setBusy(true); setBlocked([]);
    try {
      const r = await call<{ ok?: boolean; blocked?: { reasons?: string[] } }>(tool, { number, actor: "owner", ...args });
      if (r && r.ok === false) { setBlocked(r.blocked?.reasons ?? ["Not allowed"]); return false; }
      if (done) notify(done);
      bump(); load();
      return true;
    } catch (e) { report(e); return false; }
    finally { setBusy(false); }
  };

  if (!d) {
    return <Drawer onClose={onClose}><Empty>Loading {number}…</Empty></Drawer>;
  }
  const w = d.work;
  const open = openPhase(w.phase);
  const required = Object.values(d.required_forms ?? {}).flat();
  const submitted = new Set(d.forms.map((f) => f.form_key));
  const missingForms = required.filter((k) => !submitted.has(k));
  const activeSlots = d.slots.filter((s) => s.status === "booked" || s.status === "in_progress");
  const moves = d.can_move_to.filter((s) => s !== w.status);
  const intakeForm = forms.find((f) => f.purpose === "intake");
  const fieldLabel = (k: string) => intakeForm?.schema.fields.find((f) => f.key === k)?.label ?? human(k);

  return (
    <Drawer onClose={onClose}>
      <header className="px-5 py-4 border-b border-border">
        <div className="flex items-start gap-3">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="font-mono text-sm text-text-muted">{w.number}</span>
              <PhaseBadge phase={w.phase} status={w.status} />
              <Priority p={w.priority} />
              <BrandTag brand={w.brand} brands={brands} />
            </div>
            <h2 className="text-lg font-semibold mt-1 truncate">{w.customer_name || human(w.work_type)}</h2>
            <div className="text-sm text-text-muted">{human(w.work_type)}{w.summary ? ` · ${w.summary}` : ""}</div>
          </div>
          <button type="button" onClick={onClose} className="text-text-dim hover:text-text p-1" aria-label="Close"><Icon d={I.x} size={16} /></button>
        </div>
        <PhaseTrack phases={d.phases} current={w.phase} />
      </header>

      <div className="flex-1 overflow-auto px-5 py-4 space-y-5">
        {blocked.length > 0 && (
          <div className="border border-yellow/40 bg-yellow/10 rounded p-3 text-sm">
            <div className="font-medium mb-1">Not yet</div>
            <ul className="list-disc pl-5 text-text-muted">{blocked.map((b) => <li key={b}>{b}</li>)}</ul>
          </div>
        )}

        {open && (
          <section className="space-y-2">
            <h3 className={sectionTitle}>Next step</h3>
            <div className="flex flex-wrap gap-2">
              {w.status === "needs_owner" && w.quote_status === "needs_owner" && <button type="button" disabled={busy} className={btnPrimary} onClick={() => act("quote_decide", { decision: "approve" }, "Quote approved")}>Approve quote</button>}
              {(w.phase === "intake" || w.phase === "quote") && d.phases.includes("quote") && <button type="button" disabled={busy} className={btn} onClick={() => setPanel("quote")}>{w.estimate_cents ? "Re-price" : "Build quote"}</button>}
              {w.quote_status && ["draft", "approved"].includes(w.quote_status) && <button type="button" disabled={busy} className={btn} onClick={() => act("quote_decide", { decision: "send" }, "Marked as sent")}>Mark quote sent</button>}
              {w.quote_status && ["draft", "approved", "sent"].includes(w.quote_status) && <button type="button" disabled={busy} className={btnPrimary} onClick={() => act("quote_decide", { decision: "accept" }, "Quote accepted")}>Customer accepted</button>}
              {w.quote_status && ["draft", "approved", "sent", "needs_owner"].includes(w.quote_status) && <button type="button" disabled={busy} className={btnDanger} onClick={() => act("quote_decide", { decision: "decline" }, "Quote declined")}>Declined</button>}
              {(w.phase === "schedule" || (w.phase === "intake" && !d.phases.includes("quote"))) && <button type="button" disabled={busy} className={btnPrimary} onClick={() => setPanel("schedule")}>Find a slot</button>}
              {w.phase === "execute" && activeSlots.length === 0 && <button type="button" disabled={busy} className={btn} onClick={() => setPanel("schedule")}>Book again</button>}
              {missingForms.map((k) => <button key={k} type="button" disabled={busy} className={btn} onClick={() => { setFormKey(k); setPanel("form"); }}>Fill {forms.find((f) => f.key === k)?.name ?? human(k)}</button>)}
              {w.phase === "execute" && <button type="button" disabled={busy} className={btnPrimary} onClick={() => act("work_complete", {}, "Completed")}>Complete</button>}
              {w.phase === "bill" && !w.invoice_ref && <button type="button" disabled={busy} className={btnPrimary} onClick={() => setPanel("invoice")}>Invoice</button>}
              {w.phase === "bill" && <button type="button" disabled={busy} className={btn} onClick={() => act("work_mark_paid", {}, "Marked paid")}>Mark paid</button>}
              <MoveMenu moves={moves} status={w.status} busy={busy} onMove={(to, reason) => act("work_transition", { to, reason }, `Moved to ${human(to)}`)} />
            </div>
          </section>
        )}

        <section className="grid grid-cols-2 gap-4 text-sm">
          <div className="space-y-1">
            <h3 className={sectionTitle}>Customer</h3>
            <div>{w.customer_name || "—"}</div>
            {w.customer_phone && <a className="flex items-center gap-1.5 text-text-muted hover:text-accent" href={`tel:${w.customer_phone}`}><Icon d={I.phone} size={12} />{w.customer_phone}</a>}
            {w.customer_email && <a className="flex items-center gap-1.5 text-text-muted hover:text-accent" href={`mailto:${w.customer_email}`}><Icon d={I.mail} size={12} />{w.customer_email}</a>}
            {w.party_size > 0 && <div className="flex items-center gap-1.5 text-text-muted"><Icon d={I.users} size={12} />{w.party_size} guests</div>}
          </div>
          <div className="space-y-1">
            <h3 className={sectionTitle}>Details</h3>
            <div className="text-text-muted">Source: {human(w.source)}</div>
            <div className="text-text-muted">Created {when(w.created_at)}</div>
            {w.sla_due_at && open && <div className={slaLate(w) ? "text-red" : "text-text-muted"}>Book by {when(w.sla_due_at)}</div>}
            {w.duration_min > 0 && <div className="text-text-muted">Takes about {minutes(w.duration_min)}</div>}
            {w.agreement_id && <div className="flex items-center gap-1.5 text-text-muted"><Icon d={I.repeat} size={12} />From a recurring agreement</div>}
          </div>
        </section>

        {Object.keys(w.intake ?? {}).length > 0 && (
          <section>
            <h3 className={sectionTitle}>Request</h3>
            <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
              {Object.entries(w.intake).map(([k, v]) => (
                <div key={k} className="contents"><dt className="text-text-muted">{fieldLabel(k)}</dt><dd>{v === true ? "Yes" : v === false ? "No" : human(String(v))}</dd></div>
              ))}
            </dl>
          </section>
        )}

        {(d.estimate_lines.length > 0 || d.actual_lines.length > 0) && (
          <section>
            <h3 className={sectionTitle}>{d.actual_lines.length ? "Actual" : "Estimate"}{w.quote_status ? ` · quote ${human(w.quote_status).toLowerCase()}` : ""}</h3>
            <Lines lines={d.actual_lines.length ? d.actual_lines : d.estimate_lines} currency={w.currency} />
            {w.invoice_ref && <div className="text-xs text-text-muted mt-2">Invoice {w.invoice_ref}{w.invoiced_cents ? ` · ${money(w.invoiced_cents, w.currency)}` : ""}{w.paid_at ? ` · paid ${when(w.paid_at)}` : ""}</div>}
          </section>
        )}

        {d.slots.length > 0 && (
          <section>
            <h3 className={sectionTitle}>Bookings</h3>
            <div className="space-y-2">
              {d.slots.map((s) => (
                <div key={s.id} className={`border border-border rounded p-2 text-sm ${s.status === "cancelled" ? "opacity-60" : ""}`}>
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{when(s.starts_at)}</span>
                    <span className="text-text-dim">to {new Date(s.ends_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}</span>
                    <span className="ml-auto text-xs text-text-muted">{human(s.status)}</span>
                  </div>
                  {s.resources.length > 0 && <div className="text-xs text-text-muted mt-0.5">{s.resources.map((r) => r.name).join(", ")}</div>}
                  {(s.status === "booked" || s.status === "in_progress") && (
                    <div className="flex gap-2 mt-2">
                      {s.status === "booked" && <button type="button" disabled={busy} className={btn} onClick={() => act("slots_update", { slot_id: s.id, action: "start" }, "Started")}>Start</button>}
                      {s.status === "booked" && <button type="button" disabled={busy} className={btn} onClick={() => act("slots_update", { slot_id: s.id, action: "missed" }, "Marked as no-show")}>No-show</button>}
                      <button type="button" disabled={busy} className={btnDanger} onClick={() => act("slots_update", { slot_id: s.id, action: "cancel" }, "Booking cancelled")}>Cancel booking</button>
                    </div>
                  )}
                </div>
              ))}
            </div>
          </section>
        )}

        {d.forms.length > 0 && (
          <section>
            <h3 className={sectionTitle}>Forms</h3>
            <div className="space-y-1 text-sm">
              {d.forms.map((f) => (
                <details key={f.id} className="border border-border rounded px-2 py-1.5">
                  <summary className="cursor-pointer">{forms.find((x) => x.key === f.form_key)?.name ?? human(f.form_key)} <span className="text-xs text-text-dim">· {ago(f.submitted_at)}</span></summary>
                  <dl className="grid grid-cols-2 gap-x-4 gap-y-1 mt-2 text-xs">
                    {Object.entries(f.data ?? {}).map(([k, v]) => <div key={k} className="contents"><dt className="text-text-muted">{human(k)}</dt><dd>{v === true ? "Yes" : v === false ? "No" : String(v)}</dd></div>)}
                  </dl>
                </details>
              ))}
            </div>
          </section>
        )}

        {(d.brand?.playbook || d.playbook) && (
          <details className="text-sm">
            <summary className={`${sectionTitle} cursor-pointer`}>Playbook for agents</summary>
            <div className="text-text-muted whitespace-pre-wrap">{[d.brand?.playbook, d.playbook].filter(Boolean).join("\n\n")}</div>
          </details>
        )}

        <section>
          <h3 className={sectionTitle}>Timeline</h3>
          <form className="flex gap-2 mb-3" onSubmit={async (e) => { e.preventDefault(); if (!note.trim()) return; if (await act("work_note", { text: note.trim() })) setNote(""); }}>
            <input className={`${input} flex-1`} placeholder="Add a note" value={note} onChange={(e) => setNote(e.target.value)} />
            <button type="submit" disabled={busy || !note.trim()} className={btn}>Add</button>
          </form>
          <ol className="space-y-2">
            {[...events].reverse().map((ev) => (
              <li key={ev.id} className="flex gap-3 text-sm">
                <span className="mt-1.5 shrink-0" style={{ width: 6, height: 6, borderRadius: 999, background: "var(--color-border-strong)" }} />
                <div className="min-w-0 flex-1">
                  <div><span className="text-text">{eventLabel(ev)}</span> <span className="text-xs text-text-dim">{ev.actor ? `${ev.actor} · ` : ""}{when(ev.at)}</span></div>
                  {eventDetail(ev) && <div className="text-xs text-text-muted break-words">{eventDetail(ev)}</div>}
                </div>
              </li>
            ))}
          </ol>
        </section>
      </div>

      {panel === "quote" && <QuoteDialog ctx={ctx} work={w} onClose={() => setPanel("")} onDone={() => { setPanel(""); bump(); load(); }} />}
      {panel === "schedule" && <ScheduleDialog ctx={ctx} work={w} onClose={() => setPanel("")} onDone={() => { setPanel(""); bump(); load(); }} />}
      {panel === "form" && <FormDialog ctx={ctx} work={w} form={forms.find((f) => f.key === formKey)} formKey={formKey} onClose={() => setPanel("")} onDone={() => { setPanel(""); bump(); load(); }} />}
      {panel === "invoice" && <InvoiceDialog work={w} busy={busy} onClose={() => setPanel("")} onSubmit={async (args) => { if (await act("work_invoice", args, "Invoiced")) setPanel(""); }} />}
    </Drawer>
  );
}

function Drawer({ children, onClose }: { children: ReactNode; onClose: () => void }) {
  return (
    <div className="absolute inset-0 z-20 flex justify-end bg-black/30" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <aside className="relative h-full bg-bg border-l border-border shadow-xl flex flex-col" style={{ width: 620, maxWidth: "100%" }}>{children}</aside>
    </div>
  );
}

function PhaseTrack({ phases, current }: { phases: string[]; current: string }) {
  const at = phases.indexOf(current);
  return (
    <div className="flex gap-1 mt-3">
      {phases.map((p, i) => (
        <div key={p} className="flex-1 min-w-0">
          <div style={{ height: 4, borderRadius: 2, background: current === "cancelled" ? "var(--color-border)" : i <= at ? (PHASE_COLOR[p] ?? "var(--color-text-dim)") : "var(--color-border)" }} />
          <div className={`text-xs mt-1 truncate ${i === at ? "text-text" : "text-text-dim"}`}>{PHASE_LABEL[p] ?? human(p)}</div>
        </div>
      ))}
    </div>
  );
}

function MoveMenu({ moves, status, busy, onMove }: { moves: string[]; status: string; busy: boolean; onMove: (to: string, reason?: string) => void }) {
  const extra = status === "on_hold" || status === "needs_owner" ? ["resume"] : ["on_hold", "needs_owner"];
  const options = [...moves, ...extra, "cancelled"];
  return (
    <select className={`${input} text-xs`} value="" disabled={busy} onChange={(e) => {
      const to = e.target.value;
      if (!to) return;
      const reason = to === "cancelled" || to === "on_hold" || to === "needs_owner" ? window.prompt(`Reason for ${human(to).toLowerCase()}?`) ?? undefined : undefined;
      if ((to === "cancelled") && reason === undefined) return;
      onMove(to, reason);
    }}>
      <option value="">Move to…</option>
      {options.map((o) => <option key={o} value={o}>{o === "needs_owner" ? "Escalate to owner" : o === "resume" ? "Resume" : human(o)}</option>)}
    </select>
  );
}

function Lines({ lines, currency }: { lines: Line[]; currency: string }) {
  const total = lines.reduce((t, l) => t + l.total_cents, 0);
  return (
    <table className="w-full text-sm">
      <tbody>
        {lines.map((l, i) => (
          <tr key={i} className="border-b border-border">
            <td className="py-1.5 pr-2">{l.description || l.code}{l.kind === "modifier" && <span className="text-xs text-text-dim"> · adjustment</span>}</td>
            <td className="py-1.5 px-2 text-right text-text-muted whitespace-nowrap">{l.kind === "item" ? `${l.qty} ${l.unit ?? ""}` : ""}</td>
            <td className="py-1.5 pl-2 text-right whitespace-nowrap">{money(l.total_cents, currency)}</td>
          </tr>
        ))}
        <tr><td className="pt-2 font-medium">Total</td><td /><td className="pt-2 text-right font-semibold">{money(total, currency)}</td></tr>
      </tbody>
    </table>
  );
}

function eventLabel(ev: WorkEvent): string {
  switch (ev.kind) {
    case "status": return ev.to ? `Moved to ${human(ev.to).toLowerCase()}` : "Status changed";
    case "note": return "Note";
    case "created": return "Created";
    default: return human(ev.kind);
  }
}
function eventDetail(ev: WorkEvent): string {
  const p = ev.payload ?? {};
  if (typeof p.text === "string") return p.text;
  if (typeof p.reason === "string" && p.reason) return p.reason;
  if (Array.isArray(p.flags) && p.flags.length) return `Flags: ${p.flags.join(", ")}`;
  if (typeof p.starts_at === "string") return when(p.starts_at);
  return "";
}

// ─── quote / schedule / form / invoice dialogs ─────────────────────

function QuoteDialog({ ctx, work, onClose, onDone }: { ctx: Ctx; work: Work; onClose: () => void; onDone: () => void }) {
  const { call, report } = ctx;
  const [items, setItems] = useState<RateItem[]>([]);
  const [picked, setPicked] = useState<{ code: string; qty: string }[]>([]);
  const [discount, setDiscount] = useState("");
  const [preview, setPreview] = useState<{ lines: Line[]; total_cents: number; margin_pct: number | null; flags: string[]; needs_owner: boolean } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    call<Config>("config_get", { section: "rate_items", ...(work.brand ? { brand: work.brand } : {}) })
      .then((c) => setItems((c.rate_items ?? []).filter((r) => r.active))).catch(report);
  }, [call, work.brand, report]);

  const args = () => {
    const out: Record<string, unknown> = {
      items: picked.filter((p) => p.code).map((p) => (p.qty.trim() ? { code: p.code, qty: Number(p.qty) } : { code: p.code })),
    };
    if (discount.trim()) out.discount_pct = Number(discount);
    return out;
  };
  useEffect(() => {
    if (!picked.some((p) => p.code)) { setPreview(null); return; }
    const t = window.setTimeout(() => {
      call<typeof preview>("price_preview", { ...args(), answers: work.intake, work_type: work.work_type, priority: work.priority, party_size: work.party_size || undefined, ...(work.brand ? { brand: work.brand } : {}) })
        .then(setPreview).catch(() => setPreview(null));
    }, 250);
    return () => window.clearTimeout(t);
  }, [picked, discount]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    setBusy(true);
    try { await call("quote_build", { number: work.number, actor: "owner", ...args() }); ctx.notify("Quote saved"); onDone(); }
    catch (e) { report(e); } finally { setBusy(false); }
  };

  return (
    <Modal title={`Quote for ${work.number}`} onClose={onClose} wide footer={<>
      <button type="button" className={btn} onClick={onClose}>Cancel</button>
      <button type="button" className={btnPrimary} disabled={busy || !picked.some((p) => p.code)} onClick={save}>Save quote</button>
    </>}>
      <p className="text-xs text-text-muted">Prices come from the rate book. Leave the quantity empty to use the answer from the request.</p>
      {picked.map((p, i) => (
        <div key={i} className="flex gap-2">
          <select className={`${input} flex-1`} value={p.code} onChange={(e) => setPicked(picked.map((x, j) => (j === i ? { ...x, code: e.target.value } : x)))}>
            <option value="">Choose an item…</option>
            {items.map((r) => <option key={`${r.brand_id}-${r.code}`} value={r.code}>{r.name} · {ratePrice(r)}</option>)}
          </select>
          <input className={`${input} w-24`} type="number" min={0} step="any" placeholder="Qty" value={p.qty} onChange={(e) => setPicked(picked.map((x, j) => (j === i ? { ...x, qty: e.target.value } : x)))} />
          <button type="button" className={btn} onClick={() => setPicked(picked.filter((_, j) => j !== i))} aria-label="Remove"><Icon d={I.x} /></button>
        </div>
      ))}
      <div className="flex items-center gap-2">
        <button type="button" className={btn} onClick={() => setPicked([...picked, { code: "", qty: "" }])}><Icon d={I.plus} />Add item</button>
        <label className="ml-auto flex items-center gap-2 text-xs text-text-muted">Discount %<input className={`${input} w-20`} type="number" min={0} max={100} value={discount} onChange={(e) => setDiscount(e.target.value)} /></label>
      </div>
      {preview && (
        <div className="border border-border rounded p-3 space-y-2">
          <Lines lines={preview.lines} currency={work.currency} />
          <div className="text-xs text-text-muted">{preview.margin_pct != null ? `Margin ${preview.margin_pct}%` : ""}</div>
          {preview.needs_owner && <div className="text-xs text-yellow">Needs your approval: {preview.flags.join(", ")}</div>}
        </div>
      )}
    </Modal>
  );
}

function ratePrice(r: RateItem): string {
  const p = r.pricing ?? { mode: "" };
  if (p.quote_only) return "on quote";
  if (p.mode === "formula") return "calculated";
  const c = p.unit_cents ?? p.cents;
  if (c == null) return human(p.mode);
  return `${(c / 100).toFixed(2)}${p.unit ? ` / ${p.unit}` : ""}`;
}

function ScheduleDialog({ ctx, work, onClose, onDone }: { ctx: Ctx; work: Work; onClose: () => void; onDone: () => void }) {
  const { call, report } = ctx;
  const [from, setFrom] = useState(() => isoDate(new Date()));
  const [options, setOptions] = useState<SlotOption[] | null>(null);
  const [reqs, setReqs] = useState<unknown[]>([]);
  const [busy, setBusy] = useState(false);
  const [blocked, setBlocked] = useState<string[]>([]);
  const [manual, setManual] = useState("");

  const search = useCallback(() => {
    setOptions(null);
    const start = new Date(from + "T00:00");
    const f = start.getTime() < Date.now() ? new Date() : start;
    call<{ options: SlotOption[]; requirements: unknown[] }>("schedule_suggest", { number: work.number, from: f.toISOString(), to: addDays(start, 7).toISOString(), limit: 12 })
      .then((r) => { setOptions(r.options ?? []); setReqs(r.requirements ?? []); }).catch((e) => { setOptions([]); report(e); });
  }, [call, from, work.number, report]);
  useEffect(search, [search]);

  const book = async (args: Record<string, unknown>) => {
    setBusy(true); setBlocked([]);
    try {
      const r = await call<{ ok?: boolean; blocked?: { reasons?: string[] } }>("schedule_book", { number: work.number, actor: "owner", ...args });
      if (r && r.ok === false) { setBlocked(r.blocked?.reasons ?? ["Could not book"]); return; }
      ctx.notify("Booked"); onDone();
    } catch (e) { report(e); } finally { setBusy(false); }
  };

  return (
    <Modal title={`Book ${work.number}`} onClose={onClose} wide>
      <div className="flex items-center gap-2">
        <Field label="From"><input className={input} type="date" value={from} onChange={(e) => setFrom(e.target.value)} /></Field>
        <div className="text-xs text-text-muted self-end pb-1.5">Searches 7 days. {reqs.length ? `Needs ${reqs.length} kind${reqs.length > 1 ? "s" : ""} of resource.` : ""}</div>
      </div>
      {blocked.length > 0 && <div className="border border-yellow/40 bg-yellow/10 rounded p-2 text-xs">{blocked.join("; ")}</div>}
      {options === null ? <Empty>Looking for free slots…</Empty> : options.length === 0 ? <Empty>No free slot found in that week. Try a later date or check shifts and opening hours.</Empty> : (
        <div className="border border-border rounded divide-y divide-border">
          {options.map((o, i) => (
            <div key={i} className="flex items-center gap-3 px-3 py-2">
              <div className="flex-1 min-w-0">
                <div className="text-sm">{when(o.starts_at)} <span className="text-text-dim">to {new Date(o.ends_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}</span>{i === 0 && <span className="ml-2 text-xs text-green">best</span>}</div>
                <div className="text-xs text-text-muted truncate">{o.resources.map((r) => r.name).join(", ") || "No resources needed"}</div>
              </div>
              <button type="button" className={i === 0 ? btnPrimary : btn} disabled={busy} onClick={() => book({ starts_at: o.starts_at, resource_ids: o.resources.map((r) => r.id) })}>Book</button>
            </div>
          ))}
        </div>
      )}
      <div className="flex items-end gap-2 pt-2 border-t border-border">
        <Field label="Or pick a time (resources chosen for you)"><input className={input} type="datetime-local" value={manual} onChange={(e) => setManual(e.target.value)} /></Field>
        <button type="button" className={btn} disabled={busy || !manual} onClick={() => book({ starts_at: new Date(manual).toISOString() })}>Book this time</button>
      </div>
    </Modal>
  );
}

function FormDialog({ ctx, work, form, formKey, onClose, onDone }: { ctx: Ctx; work: Work; form?: Form; formKey: string; onClose: () => void; onDone: () => void }) {
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [busy, setBusy] = useState(false);
  const save = async () => {
    setBusy(true);
    try { await ctx.call("forms_submit", { form_key: formKey, work_id: work.id, data: values, responder: "owner" }); ctx.notify("Form saved"); onDone(); }
    catch (e) { ctx.report(e); } finally { setBusy(false); }
  };
  return (
    <Modal title={form?.name ?? human(formKey)} onClose={onClose} wide footer={<>
      <button type="button" className={btn} onClick={onClose}>Cancel</button>
      <button type="button" className={btnPrimary} disabled={busy} onClick={save}>Submit</button>
    </>}>
      {form ? <FormFields fields={form.schema.fields ?? []} values={values} onChange={setValues} /> : <Empty>This form is not configured.</Empty>}
    </Modal>
  );
}

function InvoiceDialog({ work, busy, onClose, onSubmit }: { work: Work; busy: boolean; onClose: () => void; onSubmit: (args: Record<string, unknown>) => void }) {
  const [external, setExternal] = useState("");
  return (
    <Modal title={`Invoice ${work.number}`} onClose={onClose} footer={<>
      <button type="button" className={btn} onClick={onClose}>Cancel</button>
      {external.trim()
        ? <button type="button" className={btnPrimary} disabled={busy} onClick={() => onSubmit({ external_ref: external.trim() })}>Record invoice</button>
        : <button type="button" className={btnPrimary} disabled={busy || !work.customer_email} onClick={() => onSubmit({ finalize: true })}>Invoice through Billing</button>}
    </>}>
      <p className="text-text-muted">Billing creates the invoice and sends it to the customer{work.customer_email ? ` at ${work.customer_email}` : ""}.</p>
      {!work.customer_email && <p className="text-yellow text-xs">The customer has no email, so Billing can't send it. Record an invoice made elsewhere instead.</p>}
      <Field label="Or record an invoice made elsewhere" hint="Its number or reference"><input className={input} value={external} onChange={(e) => setExternal(e.target.value)} /></Field>
    </Modal>
  );
}

// ─── New work ──────────────────────────────────────────────────────

function IntakeDialog({ ctx, onClose, onCreated }: { ctx: Ctx; onClose: () => void; onCreated: (number: string) => void }) {
  const { call, report, brands } = ctx;
  const active = brands.filter((b) => b.status === "active");
  const [brand, setBrand] = useState(ctx.brand || (active.length === 1 ? active[0].key : ""));
  const [types, setTypes] = useState<WorkType[]>([]);
  const [forms, setForms] = useState<Form[]>([]);
  const [type, setType] = useState("");
  const [customer, setCustomer] = useState({ name: "", email: "", phone: "" });
  const [summary, setSummary] = useState("");
  const [address, setAddress] = useState("");
  const [partySize, setPartySize] = useState("");
  const [priority, setPriority] = useState("");
  const [answers, setAnswers] = useState<Record<string, unknown>>({});
  const [missing, setMissing] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const scope = brand ? { brand } : {};
    Promise.all([call<Config>("config_get", { section: "work_types", ...scope }), call<Config>("config_get", { section: "forms", ...scope })])
      .then(([a, b]) => { setTypes((a.work_types ?? []).filter((t) => t.active && t.mode !== "internal")); setForms(b.forms ?? []); }).catch(report);
  }, [call, brand, report]);

  const wt = types.find((t) => t.key === type);
  const form = wt?.intake_form_key ? forms.find((f) => f.key === wt.intake_form_key) : undefined;
  const hasPartySize = form?.schema.fields.some((f) => f.key === "party_size");

  const create = async () => {
    setBusy(true); setMissing([]);
    try {
      const args: Record<string, unknown> = { work_type: type, source: "owner", answers };
      if (brand) args.brand = brand;
      if (customer.name || customer.email || customer.phone) args.customer = customer;
      if (summary.trim()) args.summary = summary.trim();
      if (address.trim()) args.address = address.trim();
      if (partySize) args.party_size = Number(partySize);
      else if (typeof answers.party_size === "number") args.party_size = answers.party_size;
      if (priority) args.priority = priority;
      const r = await call<{ ok?: boolean; brand_needed?: { reason: string }; work?: Work; missing_answers?: string[] }>("work_intake", args);
      if (r.ok === false && r.brand_needed) { report(new Error(r.brand_needed.reason || "Choose a brand")); return; }
      if (r.work) {
        if (r.missing_answers?.length) ctx.notify(`Created ${r.work.number}; still missing: ${r.missing_answers.join(", ")}`);
        onCreated(r.work.number);
      }
    } catch (e) { report(e); } finally { setBusy(false); }
  };

  return (
    <Modal title="New work" onClose={onClose} wide footer={<>
      <button type="button" className={btn} onClick={onClose}>Cancel</button>
      <button type="button" className={btnPrimary} disabled={busy || !type} onClick={create}>Create</button>
    </>}>
      <div className="grid grid-cols-2 gap-3">
        {active.length > 0 && (
          <Field label="Brand">
            <select className={input} value={brand} onChange={(e) => { setBrand(e.target.value); setType(""); }}>
              <option value="">{active.length > 1 ? "Choose…" : "None"}</option>
              {active.map((b) => <option key={b.key} value={b.key}>{b.name}</option>)}
            </select>
          </Field>
        )}
        <Field label="Type of work">
          <select className={input} value={type} onChange={(e) => { setType(e.target.value); setAnswers({}); }}>
            <option value="">Choose…</option>
            {uniqueByKey(types).map((t) => <option key={t.key} value={t.key}>{t.name}</option>)}
          </select>
        </Field>
        {types.length === 0 && <div className="col-span-2 text-xs text-yellow">No work types yet. Install a pack under Setup first.</div>}
        <Field label="Customer name"><input className={input} value={customer.name} onChange={(e) => setCustomer({ ...customer, name: e.target.value })} /></Field>
        <Field label="Phone"><input className={input} value={customer.phone} onChange={(e) => setCustomer({ ...customer, phone: e.target.value })} /></Field>
        <Field label="Email"><input className={input} type="email" value={customer.email} onChange={(e) => setCustomer({ ...customer, email: e.target.value })} /></Field>
        <Field label="Priority">
          <select className={input} value={priority} onChange={(e) => setPriority(e.target.value)}>
            <option value="">From the type's rules</option>
            <option value="p1">P1 · urgent</option><option value="p2">P2</option><option value="p3">P3</option><option value="p4">P4</option>
          </select>
        </Field>
        {wt?.mode === "at_customer" && <div className="col-span-2"><Field label="Address"><input className={`${input} w-full`} value={address} onChange={(e) => setAddress(e.target.value)} /></Field></div>}
        {wt && wt.mode === "at_site" && !hasPartySize && <Field label="Party size"><input className={input} type="number" min={1} value={partySize} onChange={(e) => setPartySize(e.target.value)} /></Field>}
        <div className="col-span-2"><Field label="Summary"><input className={`${input} w-full`} value={summary} onChange={(e) => setSummary(e.target.value)} placeholder="What's needed, in a line" /></Field></div>
      </div>
      {form && (
        <div className="pt-3 border-t border-border">
          <h3 className={sectionTitle}>{form.name}</h3>
          <FormFields fields={form.schema.fields ?? []} values={answers} onChange={setAnswers} />
        </div>
      )}
      {missing.length > 0 && <div className="text-xs text-yellow">Missing: {missing.join(", ")}</div>}
    </Modal>
  );
}

// ─── Resources ─────────────────────────────────────────────────────

function ResourcesTab({ ctx }: { ctx: Ctx }) {
  const { call, brand, rev, report, brands } = ctx;
  const [rows, setRows] = useState<Resource[]>([]);
  const [sites, setSites] = useState<Site[]>([]);
  const [inactive, setInactive] = useState(false);
  const [edit, setEdit] = useState<Partial<Resource> | null>(null);

  useEffect(() => {
    Promise.all([
      call<{ resources: Resource[] }>("resources_list", withBrand(ctx, { include_inactive: inactive })),
      call<{ sites: Site[] }>("sites_list", withBrand(ctx, { kind: "own" })),
    ]).then(([r, s]) => { setRows(r.resources ?? []); setSites(s.sites ?? []); }).catch(report);
  }, [call, brand, rev, inactive]); // eslint-disable-line react-hooks/exhaustive-deps

  const brandNames = (ids: number[]) => (ids?.length ? ids.map((id) => brands.find((b) => b.id === id)?.name ?? `#${id}`).join(", ") : "All brands");
  const groups = RESOURCE_KINDS.map((k) => ({ kind: k, items: rows.filter((r) => r.kind === k) })).filter((g) => g.items.length);

  return (
    <div className="p-4 space-y-4">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-medium">Resources</h2>
        <span className="text-xs text-text-dim">People, crews, spaces, equipment and vehicles that bookings need.</span>
        <label className="ml-auto flex items-center gap-1.5 text-xs text-text-muted"><input type="checkbox" checked={inactive} onChange={(e) => setInactive(e.target.checked)} />Show inactive</label>
        <button type="button" className={btnPrimary} onClick={() => setEdit({ kind: "person", active: true, needs_shift: true, quals: [], brand_ids: [] })}><Icon d={I.plus} />Add</button>
      </div>
      {groups.length === 0 && <Empty>No resources yet.</Empty>}
      {groups.map((g) => (
        <section key={g.kind}>
          <h3 className={sectionTitle}>{KIND_LABEL[g.kind]} <span className="normal-case">· {g.items.length}</span></h3>
          <div className="border border-border rounded-lg divide-y divide-border">
            {g.items.map((r) => (
              <button key={r.id} type="button" onClick={() => setEdit(r)} className={`w-full text-left px-3 py-2 hover:bg-bg-hover flex items-center gap-3 text-sm ${r.active ? "" : "opacity-60"}`}>
                <span className="w-48 truncate font-medium">{r.name}</span>
                <span className="flex-1 min-w-0 truncate text-xs text-text-muted">
                  {[r.zone && `Zone ${r.zone}`, r.capacity > 0 && `Capacity ${r.capacity}`, r.quals?.length && r.quals.map(human).join(", "),
                    r.site_id && sites.find((s) => s.id === r.site_id)?.name].filter(Boolean).join(" · ")}
                </span>
                {brands.length > 0 && <span className="text-xs text-text-dim truncate max-w-xs">{brandNames(r.brand_ids)}</span>}
                {!r.active && <span className="text-xs text-text-dim">inactive</span>}
              </button>
            ))}
          </div>
        </section>
      ))}
      {edit && <ResourceDialog ctx={ctx} value={edit} sites={sites} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); ctx.bump(); }} />}
    </div>
  );
}

function ResourceDialog({ ctx, value, sites, onClose, onSaved }: { ctx: Ctx; value: Partial<Resource>; sites: Site[]; onClose: () => void; onSaved: () => void }) {
  const [r, setR] = useState<Partial<Resource>>(value);
  const [quals, setQuals] = useState((value.quals ?? []).join(", "));
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<Resource>) => setR({ ...r, ...patch });
  const save = async () => {
    setBusy(true);
    try {
      await ctx.call("resources_upsert", {
        id: r.id, kind: r.kind, name: r.name, zone: r.zone ?? "", capacity: r.capacity ?? 0, site_id: r.site_id ?? undefined,
        quals: quals.split(",").map((q) => q.trim()).filter(Boolean), cost_rate_cents: r.cost_rate_cents ?? 0,
        needs_shift: r.needs_shift ?? false, active: r.active ?? true,
        brands: (r.brand_ids ?? []).map((id) => ctx.brands.find((b) => b.id === id)?.key).filter(Boolean),
      });
      ctx.notify("Saved"); onSaved();
    } catch (e) { ctx.report(e); } finally { setBusy(false); }
  };
  const toggleBrand = (id: number) => {
    const cur = r.brand_ids ?? [];
    set({ brand_ids: cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id] });
  };
  return (
    <Modal title={r.id ? `Edit ${value.name}` : "Add a resource"} onClose={onClose} wide footer={<>
      <button type="button" className={btn} onClick={onClose}>Cancel</button>
      <button type="button" className={btnPrimary} disabled={busy || !r.name?.trim()} onClick={save}>Save</button>
    </>}>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Name"><input className={input} value={r.name ?? ""} onChange={(e) => set({ name: e.target.value })} /></Field>
        <Field label="Kind">
          <select className={input} value={r.kind} onChange={(e) => set({ kind: e.target.value, needs_shift: e.target.value === "person" || e.target.value === "crew" })}>
            {RESOURCE_KINDS.map((k) => <option key={k} value={k}>{human(k)}</option>)}
          </select>
        </Field>
        <Field label="Skills or qualifications" hint="Comma separated, e.g. gas_safe, colour"><input className={input} value={quals} onChange={(e) => setQuals(e.target.value)} /></Field>
        <Field label={r.kind === "space" ? "Seats or capacity" : "Capacity"}><input className={input} type="number" min={0} value={r.capacity ?? 0} onChange={(e) => set({ capacity: Number(e.target.value) })} /></Field>
        <Field label="Zone" hint="Groups spaces, e.g. terrace"><input className={input} value={r.zone ?? ""} onChange={(e) => set({ zone: e.target.value })} /></Field>
        <Field label="Based at">
          <select className={input} value={r.site_id ?? ""} onChange={(e) => set({ site_id: e.target.value ? Number(e.target.value) : null })}>
            <option value="">—</option>{sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select>
        </Field>
        <Field label="Cost per hour" hint="For margins"><input className={input} type="number" min={0} step="0.01" value={(r.cost_rate_cents ?? 0) / 100} onChange={(e) => set({ cost_rate_cents: Math.round(Number(e.target.value) * 100) })} /></Field>
        <div className="flex flex-col gap-2 justify-end">
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={r.needs_shift ?? false} onChange={(e) => set({ needs_shift: e.target.checked })} />Only works inside shifts</label>
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={r.active ?? true} onChange={(e) => set({ active: e.target.checked })} />Active</label>
        </div>
      </div>
      {ctx.brands.length > 0 && (
        <div className="pt-3 border-t border-border">
          <div className="text-xs text-text-muted mb-2">Works for {(r.brand_ids ?? []).length === 0 ? "every brand" : "these brands only"}</div>
          <div className="flex flex-wrap gap-2">
            {ctx.brands.map((b) => {
              const on = (r.brand_ids ?? []).includes(b.id);
              return <button key={b.id} type="button" onClick={() => toggleBrand(b.id)} className={`px-2.5 py-1 rounded-full text-xs border ${on ? "border-accent text-accent bg-accent/10" : "border-border text-text-muted"}`}>{b.name}</button>;
            })}
          </div>
        </div>
      )}
    </Modal>
  );
}

// ─── Setup ─────────────────────────────────────────────────────────

function SetupTab({ ctx }: { ctx: Ctx }) {
  const { call, brand, rev, report, brands } = ctx;
  const [packs, setPacks] = useState<{ builtin: PackInfo[]; installed: InstalledPack[] } | null>(null);
  const [cfg, setCfg] = useState<Config>({});
  const [agreements, setAgreements] = useState<Agreement[]>([]);
  const [editBrand, setEditBrand] = useState<Partial<Brand> | null>(null);
  const [installFor, setInstallFor] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    Promise.all([
      call<{ builtin: PackInfo[]; installed: InstalledPack[] }>("packs_list"),
      call<Config>("config_get", withBrand(ctx)),
      call<{ agreements: Agreement[] }>("agreements_list"),
    ]).then(([p, c, a]) => { setPacks(p); setCfg(c); setAgreements(a.agreements ?? []); }).catch(report);
  }, [call, brand, rev]); // eslint-disable-line react-hooks/exhaustive-deps

  const install = async (key: string) => {
    setBusy(true);
    try { await call("packs_install", { key, ...(installFor ? { brand: installFor } : {}) }); ctx.notify("Pack installed"); ctx.bump(); }
    catch (e) { report(e); } finally { setBusy(false); }
  };
  const setAgreement = async (id: number, status: string) => {
    try { await call("agreements_update", { id, status }); ctx.bump(); } catch (e) { report(e); }
  };
  const brandName = (id: number) => (id ? brands.find((b) => b.id === id)?.name ?? `#${id}` : "Shared");
  const policies = brand ? { ...(cfg.policies ?? {}), ...(cfg.brand_policies?.[brand] ?? {}) } : cfg.policies ?? {};

  return (
    <div className="p-4 space-y-6 max-w-5xl">
      <section>
        <div className="flex items-center gap-2 mb-2">
          <h2 className="text-sm font-medium">Brands</h2>
          <span className="text-xs text-text-dim">Names your customers see. Staff and equipment are shared unless limited.</span>
          <button type="button" className={`${btnPrimary} ml-auto`} onClick={() => setEditBrand({ status: "active" })}><Icon d={I.plus} />Add brand</button>
        </div>
        {brands.length === 0 ? <div className="border border-dashed border-border rounded-lg p-4 text-sm text-text-muted">No brands. Everything runs under one name, numbered W-00001. Add brands to run several businesses on the same staff.</div> : (
          <div className="grid gap-3 md:grid-cols-2">
            {brands.map((b) => (
              <button key={b.id} type="button" onClick={() => setEditBrand(b)} className={`text-left border border-border rounded-lg p-3 hover:border-accent ${b.status === "archived" ? "opacity-60" : ""}`}>
                <div className="flex items-center gap-2">
                  <span className="font-medium">{b.name}</span>
                  <span className="font-mono text-xs text-text-dim">{b.number_prefix}-00001</span>
                  {b.status === "archived" && <span className="text-xs text-text-dim ml-auto">archived</span>}
                </div>
                <div className="text-xs text-text-muted mt-1 truncate">{[b.email, b.phone, b.website].filter(Boolean).join(" · ") || "No contact details"}</div>
              </button>
            ))}
          </div>
        )}
      </section>

      <section>
        <div className="flex items-center gap-2 mb-2">
          <h2 className="text-sm font-medium">Business packs</h2>
          <span className="text-xs text-text-dim">Ready-made work types, forms, prices and rules. Installing again updates them.</span>
          {brands.length > 0 && (
            <label className="ml-auto flex items-center gap-2 text-xs text-text-muted">Install for
              <select className={`${input} text-xs`} value={installFor} onChange={(e) => setInstallFor(e.target.value)}>
                <option value="">Every brand</option>{brands.filter((b) => b.status === "active").map((b) => <option key={b.key} value={b.key}>{b.name}</option>)}
              </select>
            </label>
          )}
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          {(packs?.builtin ?? []).map((p) => {
            const inst = (packs?.installed ?? []).filter((x) => x.key === p.key);
            return (
              <div key={p.key} className="border border-border rounded-lg p-3 flex flex-col gap-2">
                <div className="flex items-center gap-2"><span className="font-medium">{p.name}</span>
                  {inst.length > 0 && <span className="text-xs text-green ml-auto">{inst.map((x) => (x.brand ? brands.find((b) => b.key === x.brand)?.name ?? x.brand : "shared")).join(", ")}</span>}
                </div>
                <div className="text-xs text-text-muted flex-1">{p.description}</div>
                <div className="text-xs text-text-dim">{p.work_types.map(human).join(", ")}</div>
                <button type="button" className={btn} disabled={busy} onClick={() => install(p.key)}>{inst.some((x) => (x.brand ?? "") === installFor) ? "Update" : "Install"}</button>
              </div>
            );
          })}
        </div>
      </section>

      <section>
        <h2 className="text-sm font-medium mb-2">Rules{brand ? ` for ${brands.find((b) => b.key === brand)?.name}` : ""}</h2>
        <PoliciesEditor ctx={ctx} policies={policies} brandPolicies={brand ? cfg.brand_policies?.[brand] ?? {} : null} />
      </section>

      <section>
        <h2 className="text-sm font-medium mb-2">Work types</h2>
        <div className="border border-border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-bg-subtle text-xs text-text-muted"><tr>
              <th className="text-left font-normal px-3 py-2">Name</th><th className="text-left font-normal px-3 py-2">Where</th>
              <th className="text-left font-normal px-3 py-2">Steps</th><th className="text-left font-normal px-3 py-2">Brand</th>
            </tr></thead>
            <tbody>{(cfg.work_types ?? []).map((t) => (
              <tr key={`${t.brand_id}-${t.key}`} className={`border-t border-border ${t.active ? "" : "opacity-60"}`}>
                <td className="px-3 py-2">{t.name}</td>
                <td className="px-3 py-2 text-text-muted">{t.mode === "at_customer" ? "At the customer" : t.mode === "at_site" ? "On premises" : t.mode === "internal" ? "Internal" : human(t.mode)}</td>
                <td className="px-3 py-2 text-xs text-text-muted">{t.phases.map((p) => PHASE_LABEL[p] ?? human(p)).join(" → ")}</td>
                <td className="px-3 py-2 text-xs text-text-muted">{brandName(t.brand_id)}</td>
              </tr>
            ))}</tbody>
          </table>
          {(cfg.work_types ?? []).length === 0 && <Empty>No work types. Install a pack above.</Empty>}
        </div>
      </section>

      <section>
        <h2 className="text-sm font-medium mb-2">Rate book</h2>
        <div className="border border-border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-bg-subtle text-xs text-text-muted"><tr>
              <th className="text-left font-normal px-3 py-2">Item</th><th className="text-left font-normal px-3 py-2">Code</th>
              <th className="text-right font-normal px-3 py-2">Price</th><th className="text-right font-normal px-3 py-2">Cost</th>
              <th className="text-left font-normal px-3 py-2">Brand</th>
            </tr></thead>
            <tbody>{(cfg.rate_items ?? []).map((r) => (
              <tr key={`${r.brand_id}-${r.code}`} className={`border-t border-border ${r.active ? "" : "opacity-60"}`}>
                <td className="px-3 py-2">{r.name}</td><td className="px-3 py-2 font-mono text-xs text-text-dim">{r.code}</td>
                <td className="px-3 py-2 text-right whitespace-nowrap">{ratePrice(r)}</td>
                <td className="px-3 py-2 text-right text-text-muted whitespace-nowrap">{r.cost_cents ? (r.cost_cents / 100).toFixed(2) : "—"}</td>
                <td className="px-3 py-2 text-xs text-text-muted">{brandName(r.brand_id)}</td>
              </tr>
            ))}</tbody>
          </table>
          {(cfg.rate_items ?? []).length === 0 && <Empty>No rate items yet.</Empty>}
        </div>
      </section>

      <section>
        <h2 className="text-sm font-medium mb-2">Recurring work</h2>
        {agreements.length === 0 ? <div className="text-sm text-text-muted">No agreements. Agents create them with agreements_create, for example a weekly clean or an annual service.</div> : (
          <div className="border border-border rounded-lg divide-y divide-border">
            {agreements.filter((a) => !brand || a.brand === brand).map((a) => (
              <div key={a.id} className="flex items-center gap-3 px-3 py-2 text-sm">
                <span className="text-text-muted"><Icon d={I.repeat} /></span>
                <div className="flex-1 min-w-0">
                  <div className="truncate">{a.name || human(a.work_type)} <span className="text-text-dim">· {a.customer_name || "internal"}</span></div>
                  <div className="text-xs text-text-muted">Every {a.recurrence.every > 1 ? `${a.recurrence.every} ` : ""}{a.recurrence.unit}{a.recurrence.every > 1 ? "s" : ""}{a.recurrence.time ? ` at ${a.recurrence.time}` : ""} · next {a.next_on}{a.auto_book ? " · books itself" : ""}</div>
                </div>
                <span className={`text-xs ${a.status === "active" ? "text-green" : "text-text-dim"}`}>{human(a.status)}</span>
                {a.status === "active" && <button type="button" className={btn} onClick={() => setAgreement(a.id, "paused")}>Pause</button>}
                {a.status === "paused" && <button type="button" className={btn} onClick={() => setAgreement(a.id, "active")}>Resume</button>}
              </div>
            ))}
          </div>
        )}
      </section>

      {editBrand && <BrandDialog ctx={ctx} value={editBrand} onClose={() => setEditBrand(null)} onSaved={() => { setEditBrand(null); ctx.bump(); }} />}
    </div>
  );
}

const POLICY_FIELDS: { key: string; label: string; hint: string; kind: "text" | "money" | "pct" }[] = [
  { key: "currency", label: "Currency", hint: "e.g. EUR", kind: "text" },
  { key: "timezone", label: "Timezone", hint: "e.g. Europe/Paris", kind: "text" },
  { key: "auto_quote_max_cents", label: "Approve quotes above", hint: "Agents need your approval above this", kind: "money" },
  { key: "max_discount_pct", label: "Largest discount without you", hint: "Percent", kind: "pct" },
  { key: "min_margin_pct", label: "Lowest margin without you", hint: "Percent", kind: "pct" },
];

function PoliciesEditor({ ctx, policies, brandPolicies }: { ctx: Ctx; policies: Record<string, unknown>; brandPolicies: Record<string, unknown> | null }) {
  const [draft, setDraft] = useState<Record<string, string>>({});
  const shown = (k: string, kind: string) => {
    if (k in draft) return draft[k];
    const v = policies[k];
    if (v == null) return "";
    return kind === "money" ? String(Number(v) / 100) : String(v);
  };
  const save = async (k: string, kind: string) => {
    if (!(k in draft)) return;
    const raw = draft[k].trim();
    if (raw === "" && !ctx.brand) { setDraft(({ [k]: _, ...rest }) => rest); return; }
    const value = raw === "" ? null : kind === "text" ? raw : kind === "money" ? Math.round(Number(raw) * 100) : Number(raw);
    try {
      await ctx.call("policies_set", { key: k, value, ...(ctx.brand ? { brand: ctx.brand } : {}) });
      setDraft(({ [k]: _, ...rest }) => rest);
      ctx.notify("Saved"); ctx.bump();
    } catch (e) { ctx.report(e); }
  };
  return (
    <div className="grid gap-3 md:grid-cols-3">
      {POLICY_FIELDS.map((f) => (
        <Field key={f.key} label={f.label + (brandPolicies && f.key in brandPolicies ? " (this brand)" : "")} hint={f.hint}>
          <input className={input} value={shown(f.key, f.kind)} onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })}
            onBlur={() => save(f.key, f.kind)} onKeyDown={(e) => { if (e.key === "Enter") (e.target as HTMLInputElement).blur(); }} />
        </Field>
      ))}
    </div>
  );
}

function BrandDialog({ ctx, value, onClose, onSaved }: { ctx: Ctx; value: Partial<Brand>; onClose: () => void; onSaved: () => void }) {
  const [b, setB] = useState<Partial<Brand>>(value);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<Brand>) => setB({ ...b, ...patch });
  const save = async () => {
    setBusy(true);
    try {
      await ctx.call("brands_upsert", {
        id: b.id, key: b.key || slug(b.name ?? ""), name: b.name, legal_name: b.legal_name ?? "", email: b.email ?? "", phone: b.phone ?? "",
        website: b.website ?? "", number_prefix: b.number_prefix || undefined, playbook: b.playbook ?? "", status: b.status ?? "active",
      });
      ctx.notify("Brand saved"); onSaved();
    } catch (e) { ctx.report(e); } finally { setBusy(false); }
  };
  return (
    <Modal title={b.id ? `Edit ${value.name}` : "Add a brand"} onClose={onClose} wide footer={<>
      {b.id && <button type="button" className={`${b.status === "archived" ? btn : btnDanger} mr-auto`} disabled={busy} onClick={() => set({ status: b.status === "archived" ? "active" : "archived" })}>
        {b.status === "archived" ? "Restore" : "Archive"}
      </button>}
      <button type="button" className={btn} onClick={onClose}>Cancel</button>
      <button type="button" className={btnPrimary} disabled={busy || !b.name?.trim()} onClick={save}>Save</button>
    </>}>
      {b.status === "archived" && <div className="text-xs text-yellow">Archived brands take no new work. Save to apply.</div>}
      <div className="grid grid-cols-2 gap-3">
        <Field label="Name"><input className={input} value={b.name ?? ""} onChange={(e) => set({ name: e.target.value })} /></Field>
        <Field label="Key" hint={b.id ? "Can't change" : "Used by agents, lowercase"}><input className={input} disabled={!!b.id} value={b.key ?? slug(b.name ?? "")} onChange={(e) => set({ key: e.target.value })} /></Field>
        <Field label="Legal name"><input className={input} value={b.legal_name ?? ""} onChange={(e) => set({ legal_name: e.target.value })} /></Field>
        <Field label="Job number prefix" hint="1–8 letters or digits; fixed once it has work"><input className={input} value={b.number_prefix ?? ""} placeholder="From the name" onChange={(e) => set({ number_prefix: e.target.value.toUpperCase() })} /></Field>
        <Field label="Email"><input className={input} type="email" value={b.email ?? ""} onChange={(e) => set({ email: e.target.value })} /></Field>
        <Field label="Phone"><input className={input} value={b.phone ?? ""} onChange={(e) => set({ phone: e.target.value })} /></Field>
        <div className="col-span-2"><Field label="Website"><input className={`${input} w-full`} value={b.website ?? ""} onChange={(e) => set({ website: e.target.value })} /></Field></div>
        <div className="col-span-2"><Field label="Playbook" hint="How agents speak and act for this brand"><textarea className={`${input} w-full`} rows={4} value={b.playbook ?? ""} onChange={(e) => set({ playbook: e.target.value })} /></Field></div>
      </div>
    </Modal>
  );
}

const slug = (s: string) => s.toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40);
