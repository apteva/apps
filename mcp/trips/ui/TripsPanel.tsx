// TripsPanel — plan + budget trips end-to-end. Calendar mirrored.
//
// Two screens:
//   List view   — cards for each trip, "+ New trip" CTA
//   Detail view — selected trip with Overview / Itinerary / Budget / Todos tabs
//
// All money is stored as integer minor units server-side; the UI uses
// fmtMoney(minor, currency) for display. Colors in SVG come from CSS
// variables because the dashboard's Tailwind JIT doesn't scan apps/mcp/*/ui/.

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/trips";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Trip {
  id: number;
  name: string;
  purpose: string;
  status: "planning" | "booked" | "in_progress" | "done" | "cancelled";
  start_at: string;
  end_at: string;
  home_currency: string;
  total_budget?: number;
  participants: string[];
  notes: string;
  color: string;
  calendar_id?: number;
  sync_calendar: boolean;
  archived: boolean;
  // Populated by trips_list only — minor units in trip.home_currency.
  total_planned?: number;
  total_actual?: number;
}

interface Destination {
  id: number;
  trip_id: number;
  place_name: string;
  country: string;
  arrive_at: string;
  depart_at: string;
  order_idx: number;
  notes: string;
}

interface TransportLeg {
  id: number;
  trip_id: number;
  kind: string;
  provider: string;
  reference: string;
  depart_at: string;
  arrive_at: string;
  depart_location: string;
  arrive_location: string;
  cost_estimated?: number;
  cost_actual?: number;
  currency: string;
  booked: boolean;
  confirmation_number: string;
  notes: string;
}

interface Accommodation {
  id: number;
  trip_id: number;
  destination_id?: number;
  name: string;
  kind: string;
  address: string;
  check_in_at: string;
  check_out_at: string;
  cost_estimated?: number;
  cost_actual?: number;
  currency: string;
  booked: boolean;
  notes: string;
}

interface Activity {
  id: number;
  trip_id: number;
  destination_id?: number;
  name: string;
  category: "food" | "activity" | "shopping" | "transport_local" | "other";
  start_at?: string;
  end_at?: string;
  location: string;
  cost_estimated?: number;
  cost_actual?: number;
  currency: string;
  booked: boolean;
  notes: string;
}

interface Todo {
  id: number;
  trip_id: number;
  label: string;
  due_at?: string;
  done: boolean;
}

interface BudgetCategoryRow {
  category: string;
  cap: number;
  capped: boolean;
  planned: number;
  actual: number;
  delta: number;
}

interface BudgetSummary {
  home_currency: string;
  categories: BudgetCategoryRow[];
  total_planned: number;
  total_actual: number;
  total_cap: number;
}

interface TripDashboard {
  trip: Trip;
  destinations: Destination[];
  transport_legs: TransportLeg[];
  accommodations: Accommodation[];
  activities: Activity[];
  todos: Todo[];
  budget: BudgetSummary;
}

type Tab = "overview" | "itinerary" | "budget" | "todos";

// ─── App event subscription (inlined, mirrors finance pattern) ───

interface AppEventEnvelope<T = unknown> {
  topic: string; app: string; project_id: string;
  install_id: number; seq: number; time: string; data: T;
}
function useAppEvents<T = unknown>(app: string, projectId: string | undefined | null, onEvent: (ev: AppEventEnvelope<T>) => void) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: { subscribe(a: string, p: string, fn: (ev: AppEventEnvelope<T>) => void): () => void };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, handler);
    let lastSeq = 0; let es: EventSource | null = null; let cancelled = false; let timer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url = `/api/app-events/${encodeURIComponent(app)}?project_id=${encodeURIComponent(projectId)}` + (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => { try { const ev = JSON.parse(e.data) as AppEventEnvelope<T>; if (ev.seq <= lastSeq) return; lastSeq = ev.seq; handlerRef.current(ev); } catch {} };
      es.onerror = () => { if (es && es.readyState === EventSource.CLOSED) { if (timer) window.clearTimeout(timer); timer = window.setTimeout(connect, 2000); } };
    };
    connect();
    return () => { cancelled = true; if (timer) window.clearTimeout(timer); if (es) es.close(); };
  }, [app, projectId]);
}

// ─── Helpers ──────────────────────────────────────────────────────

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(`${API}${path}`, {
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
  return r.json();
}

function fmtMoney(minor: number | null | undefined, currency: string, opts?: { signed?: boolean }): string {
  if (minor == null) return "—";
  const v = minor / 100;
  const s = new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 2 }).format(v);
  if (opts?.signed && v > 0) return "+" + s;
  return s;
}

function fmtDate(s: string): string {
  return new Date(s).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}

function fmtDateShort(s: string): string {
  return new Date(s).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function fmtTime(s: string): string {
  return new Date(s).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function daysUntil(startAt: string, endAt: string): { label: string; tone: "future" | "active" | "past" } {
  const now = Date.now();
  const start = new Date(startAt).getTime();
  const end = new Date(endAt).getTime();
  const dayMs = 24 * 60 * 60 * 1000;
  if (now < start) {
    const d = Math.ceil((start - now) / dayMs);
    return { label: `In ${d} day${d === 1 ? "" : "s"}`, tone: "future" };
  }
  if (now <= end) {
    const d = Math.ceil((end - now) / dayMs);
    return { label: `${d} day${d === 1 ? "" : "s"} left`, tone: "active" };
  }
  const d = Math.ceil((now - end) / dayMs);
  return { label: `${d} day${d === 1 ? "" : "s"} ago`, tone: "past" };
}

const STATUS_LABEL: Record<Trip["status"], string> = {
  planning: "Planning",
  booked: "Booked",
  in_progress: "In progress",
  done: "Done",
  cancelled: "Cancelled",
};

const BUDGET_LABEL: Record<string, string> = {
  transport: "Transport",
  lodging: "Lodging",
  food: "Food",
  activities: "Activities",
  shopping: "Shopping",
  other: "Other",
};

// ─── UI context (confirm + toast) ────────────────────────────────
//
// Replaces window.confirm() and window.alert(). The provider holds
// both pieces of state; nested components reach for them via useUI().
// Confirm uses an imperative Promise<boolean> API so handler code
// reads naturally: `if (!await ui.confirm({...})) return;`.

interface ConfirmOpts {
  title: string;
  message?: string;
  confirmLabel?: string;
  danger?: boolean;
}
interface UICtxValue {
  confirm: (opts: ConfirmOpts) => Promise<boolean>;
  notify: (message: string, kind?: "error" | "info") => void;
}
const UICtx = createContext<UICtxValue | null>(null);
function useUI(): UICtxValue {
  const c = useContext(UICtx);
  if (!c) throw new Error("useUI must be used inside <UIProvider>");
  return c;
}

function UIProvider({ children }: { children: React.ReactNode }) {
  const [confirmState, setConfirmState] = useState<ConfirmOpts | null>(null);
  const [toast, setToast] = useState<{ id: number; message: string; kind: "error" | "info" } | null>(null);
  const resolverRef = useRef<((v: boolean) => void) | null>(null);
  const toastTimer = useRef<number | null>(null);

  const confirm = useCallback((opts: ConfirmOpts) => {
    return new Promise<boolean>((resolve) => {
      resolverRef.current = resolve;
      setConfirmState(opts);
    });
  }, []);

  const notify = useCallback((message: string, kind: "error" | "info" = "error") => {
    if (toastTimer.current) window.clearTimeout(toastTimer.current);
    setToast({ id: Date.now(), message, kind });
    toastTimer.current = window.setTimeout(() => setToast(null), 5000);
  }, []);

  const close = (result: boolean) => {
    resolverRef.current?.(result);
    resolverRef.current = null;
    setConfirmState(null);
  };

  return (
    <UICtx.Provider value={{ confirm, notify }}>
      {children}
      {confirmState && (
        <ConfirmDialog
          {...confirmState}
          onConfirm={() => close(true)}
          onCancel={() => close(false)}
        />
      )}
      {toast && (
        <div
          role="status"
          className={`fixed bottom-4 right-4 z-50 max-w-sm rounded-md border px-4 py-3 text-sm shadow-lg ${
            toast.kind === "error"
              ? "border-error/30 bg-error/10 text-error"
              : "border-border bg-bg-card text-text"
          }`}
        >
          {toast.message}
        </div>
      )}
    </UICtx.Provider>
  );
}

function ConfirmDialog({ title, message, confirmLabel = "Delete", danger = true, onConfirm, onCancel }: ConfirmOpts & { onConfirm: () => void; onCancel: () => void }) {
  // Esc cancels, Enter confirms. Capture both at document level so the
  // user doesn't need to focus the buttons first.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); onCancel(); }
      if (e.key === "Enter") { e.preventDefault(); onConfirm(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onConfirm, onCancel]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-bg-overlay p-4">
      <div className="w-full max-w-sm rounded-lg border border-border bg-bg-card p-5 shadow-xl">
        <h3 className="text-base font-semibold">{title}</h3>
        {message && <p className="mt-2 text-sm text-text-muted">{message}</p>}
        <div className="mt-4 flex justify-end gap-2">
          <button
            onClick={onCancel}
            className="rounded-md border border-border bg-transparent px-4 py-2 text-sm text-text hover:bg-bg-hover"
          >Cancel</button>
          <button
            onClick={onConfirm}
            autoFocus
            className={`rounded-md px-4 py-2 text-sm ${
              danger
                ? "bg-error text-bg hover:opacity-90"
                : "bg-accent text-bg hover:bg-accent-hover"
            }`}
          >{confirmLabel}</button>
        </div>
      </div>
    </div>
  );
}

// ─── Icons ───────────────────────────────────────────────────────

function Icon({ name, size = 16 }: { name: string; size?: number }) {
  const common = {
    width: size, height: size, viewBox: "0 0 24 24", fill: "none",
    stroke: "currentColor", strokeWidth: 1.75,
    strokeLinecap: "round" as const, strokeLinejoin: "round" as const,
  };
  switch (name) {
    case "map":
      return <svg {...common}><polygon points="1 6 8 3 16 6 23 3 23 18 16 21 8 18 1 21"/><line x1="8" y1="3" x2="8" y2="18"/><line x1="16" y1="6" x2="16" y2="21"/></svg>;
    case "plus":
      return <svg {...common}><path d="M12 5v14M5 12h14"/></svg>;
    case "chevron-left":
      return <svg {...common}><polyline points="15 18 9 12 15 6"/></svg>;
    case "chevron-right":
      return <svg {...common}><polyline points="9 18 15 12 9 6"/></svg>;
    case "x":
      return <svg {...common}><path d="M18 6L6 18M6 6l12 12"/></svg>;
    case "plane":
      return <svg {...common}><path d="M17.8 19.2 16 11l3.5-3.5C21 6 21.5 4 21 3c-1-.5-3 0-4.5 1.5L13 8 4.8 6.2c-.5-.1-.9.1-1.1.5l-.3.5c-.2.5-.1 1 .3 1.3L9 12l-2 3H4l-1 1 3 2 2 3 1-1v-3l3-2 3.5 5.3c.3.4.8.5 1.3.3l.5-.2c.4-.3.6-.7.5-1.2z"/></svg>;
    case "train":
      return <svg {...common}><rect x="4" y="3" width="16" height="16" rx="2"/><path d="M4 11h16M8 15h.01M16 15h.01M12 3v8"/><path d="M8 19l-2 3M16 19l2 3"/></svg>;
    case "car":
      return <svg {...common}><path d="M14 16H9m10 0h2v-3.5a4 4 0 0 0-.65-2.2L19 7a2 2 0 0 0-1.66-1H6.66A2 2 0 0 0 5 7L3.65 10.3A4 4 0 0 0 3 12.5V16h2"/><circle cx="7" cy="17" r="2"/><circle cx="17" cy="17" r="2"/></svg>;
    case "bed":
      return <svg {...common}><path d="M3 7v13M3 15h18v5M21 11V7H8v8"/></svg>;
    case "compass":
      return <svg {...common}><circle cx="12" cy="12" r="10"/><polygon points="16.24 7.76 14.12 14.12 7.76 16.24 9.88 9.88 16.24 7.76"/></svg>;
    case "check":
      return <svg {...common}><polyline points="20 6 9 17 4 12"/></svg>;
    case "trash":
      return <svg {...common}><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>;
    case "edit":
      return <svg {...common}><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>;
    case "clock":
      return <svg {...common}><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>;
    default:
      return null;
  }
}

function transportIcon(kind: string): string {
  switch (kind) {
    case "flight": return "plane";
    case "train": return "train";
    case "car": case "bus": case "ferry": return "car";
    default: return "compass";
  }
}

// ─── Panel ───────────────────────────────────────────────────────

export default function TripsPanel(props: NativePanelProps) {
  // Wrap the whole panel so every nested component can reach the
  // confirm + toast helpers via useUI().
  return (
    <UIProvider>
      <TripsPanelInner {...props} />
    </UIProvider>
  );
}

function TripsPanelInner({ projectId }: NativePanelProps) {
  const [trips, setTrips] = useState<Trip[]>([]);
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      const r = await api<{ trips: Trip[] }>("/trips");
      setTrips(r.trips ?? []);
      setError("");
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);
  useAppEvents("trips", projectId, () => refresh());

  if (selectedID != null) {
    return (
      <TripDetail
        tripID={selectedID}
        onBack={() => setSelectedID(null)}
        onChanged={refresh}
      />
    );
  }

  return (
    <div className="flex h-full flex-col gap-3 p-4">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Icon name="map" size={20} />
          <h1 className="text-lg font-semibold">Trips</h1>
        </div>
        <button
          onClick={() => setShowNew(true)}
          className="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover"
        >
          <Icon name="plus" size={14} /> New trip
        </button>
      </header>

      {error && (
        <div className="rounded-md border border-error/30 bg-error/10 px-3 py-2 text-sm text-error">
          {error}
        </div>
      )}

      <div className="flex-1 overflow-auto">
        {trips.length === 0 ? (
          <EmptyState message="No trips yet. Click 'New trip' to plan one." />
        ) : (
          <div className="flex flex-col gap-3">
            <OverallBudgetBar trips={trips} />
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {trips.map(t => <TripCard key={t.id} trip={t} onOpen={() => setSelectedID(t.id)} />)}
            </div>
          </div>
        )}
      </div>

      {showNew && (
        <NewTripDialog
          onClose={() => setShowNew(false)}
          onCreated={async (id) => { setShowNew(false); await refresh(); setSelectedID(id); }}
        />
      )}
    </div>
  );
}

// ─── List view ───────────────────────────────────────────────────

function TripCard({ trip, onOpen }: { trip: Trip; onOpen: () => void }) {
  const days = daysUntil(trip.start_at, trip.end_at);
  const toneClass =
    days.tone === "active" ? "bg-success/20 text-success"
    : days.tone === "future" ? "bg-accent/20 text-accent"
    : "bg-bg-hover text-text-muted";

  const planned = trip.total_planned ?? 0;
  const actual = trip.total_actual ?? 0;
  // Bar fill = actual / planned, clamped to 100%. Over-budget swaps
  // green for red and overflows visually (still capped to 100% width
  // so cards stay comparable).
  const target = planned > 0 ? planned : actual;
  const pct = target > 0 ? Math.min(100, (actual / target) * 100) : 0;
  const over = planned > 0 && actual > planned;
  const barColor = over ? "bg-error" : actual === 0 ? "bg-bg-hover" : "bg-success";
  const hasAnyMoney = planned > 0 || actual > 0;

  return (
    <button
      onClick={onOpen}
      className="flex flex-col overflow-hidden rounded-lg border border-border bg-bg-card text-left transition hover:border-border-strong hover:shadow-sm"
    >
      <div className="h-1.5" style={{ background: trip.color }} />
      <div className="flex-1 p-4">
        <div className="mb-1 flex items-center justify-between">
          <span className={`rounded-full px-2 py-0.5 text-xs ${toneClass}`}>{days.label}</span>
          <span className="text-xs text-text-muted">{STATUS_LABEL[trip.status]}</span>
        </div>
        <h3 className="text-base font-semibold">{trip.name}</h3>
        <p className="text-xs text-text-muted">
          {fmtDateShort(trip.start_at)} – {fmtDateShort(trip.end_at)}
        </p>

        {hasAnyMoney && (
          <div className="mt-3">
            <div className="flex items-center justify-between text-xs">
              <span className="text-text-muted">Actual</span>
              <span className={`tabular-nums ${over ? "text-error" : "text-text"}`}>
                {fmtMoney(actual, trip.home_currency)}
                {planned > 0 && <span className="text-text-dim"> / {fmtMoney(planned, trip.home_currency)}</span>}
              </span>
            </div>
            <div className="mt-1 h-1 w-full overflow-hidden rounded-full bg-bg-hover">
              <div className={`h-full ${barColor}`} style={{ width: `${pct}%` }} />
            </div>
          </div>
        )}
      </div>
    </button>
  );
}

// OverallBudgetBar aggregates planned + actual across the visible trips,
// grouped by home_currency (no FX in v0.2, so we don't mix currencies).
// Single-currency users see one tidy line; multi-currency users see one
// per currency.
function OverallBudgetBar({ trips }: { trips: Trip[] }) {
  const totals = useMemo(() => {
    const byCcy: Record<string, { planned: number; actual: number; count: number }> = {};
    for (const t of trips) {
      if (t.status === "cancelled") continue;
      const ccy = t.home_currency || "EUR";
      const row = byCcy[ccy] ?? { planned: 0, actual: 0, count: 0 };
      row.planned += t.total_planned ?? 0;
      row.actual += t.total_actual ?? 0;
      row.count += 1;
      byCcy[ccy] = row;
    }
    return Object.entries(byCcy)
      .filter(([, r]) => r.planned > 0 || r.actual > 0)
      .sort(([, a], [, b]) => b.planned - a.planned);
  }, [trips]);

  if (totals.length === 0) return null;
  return (
    <section className="rounded-lg border border-border bg-bg-card p-4">
      <div className="mb-2 text-xs uppercase tracking-wide text-text-muted">
        Across {trips.filter(t => t.status !== "cancelled").length} trip{trips.length === 1 ? "" : "s"}
      </div>
      <div className="flex flex-col gap-3 sm:flex-row sm:gap-6">
        {totals.map(([ccy, r]) => {
          const target = r.planned > 0 ? r.planned : r.actual;
          const pct = target > 0 ? Math.min(100, (r.actual / target) * 100) : 0;
          const over = r.planned > 0 && r.actual > r.planned;
          const barColor = over ? "bg-error" : "bg-success";
          return (
            <div key={ccy} className="flex-1 min-w-0">
              <div className="flex items-baseline justify-between">
                <span className="text-xs text-text-muted">{ccy}</span>
                <span className={`text-sm tabular-nums ${over ? "text-error" : "text-text"}`}>
                  {fmtMoney(r.actual, ccy)}
                  {r.planned > 0 && <span className="text-text-dim"> / {fmtMoney(r.planned, ccy)}</span>}
                </span>
              </div>
              <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-bg-hover">
                <div className={`h-full ${barColor}`} style={{ width: `${pct}%` }} />
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}

// ─── Detail view ─────────────────────────────────────────────────

function TripDetail({ tripID, onBack, onChanged }: { tripID: number; onBack: () => void; onChanged: () => void }) {
  const ui = useUI();
  const [data, setData] = useState<TripDashboard | null>(null);
  const [tab, setTab] = useState<Tab>("overview");
  const [error, setError] = useState("");
  const [showEdit, setShowEdit] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const d = await api<TripDashboard>(`/dashboard?trip_id=${tripID}`);
      setData(d);
      setError("");
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [tripID]);
  useEffect(() => { refresh(); }, [refresh]);

  const deleteTrip = async () => {
    if (!await ui.confirm({
      title: "Delete trip?",
      message: "All its destinations, transport, accommodation, activities and tagged calendar events go with it.",
      confirmLabel: "Delete trip",
    })) return;
    try {
      await api<unknown>(`/trips/${tripID}`, { method: "DELETE" });
      onChanged();
      onBack();
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    }
  };

  if (!data) {
    return (
      <div className="flex h-full items-center justify-center p-4">
        {error ? <div className="text-sm text-error">{error}</div> : <div className="text-sm text-text-muted">Loading…</div>}
      </div>
    );
  }

  const trip = data.trip;
  const days = daysUntil(trip.start_at, trip.end_at);

  return (
    <div className="flex h-full flex-col gap-3 p-4">
      <header className="flex items-center justify-between">
        <button onClick={onBack} className="flex items-center gap-1 text-sm text-text-muted hover:text-text">
          <Icon name="chevron-left" size={14} /> Trips
        </button>
        <div className="flex items-center gap-2">
          <button onClick={() => setShowEdit(true)} title="Edit trip" className="p-1 text-text-muted hover:text-text">
            <Icon name="edit" size={14} />
          </button>
          <button onClick={deleteTrip} title="Delete trip" className="p-1 text-text-muted hover:text-error">
            <Icon name="trash" size={14} />
          </button>
          <nav className="flex rounded-md border border-border overflow-hidden text-sm">
            {(["overview", "itinerary", "budget", "todos"] as Tab[]).map(t => (
              <button
                key={t}
                onClick={() => setTab(t)}
                className={`px-3 py-1.5 capitalize ${tab === t ? "bg-accent text-bg" : "hover:bg-bg-hover"}`}
              >{t}</button>
            ))}
          </nav>
        </div>
      </header>

      <section className="overflow-hidden rounded-lg border border-border bg-bg-card">
        <div className="h-1.5" style={{ background: trip.color }} />
        <div className="flex items-center justify-between p-4">
          <div>
            <h2 className="text-xl font-semibold">{trip.name}</h2>
            <p className="text-sm text-text-muted">
              {fmtDate(trip.start_at)} – {fmtDate(trip.end_at)} • {days.label}
              {!trip.sync_calendar && <span className="ml-2 rounded-full bg-warn/20 px-2 py-0.5 text-xs text-warn">calendar sync off</span>}
            </p>
          </div>
          <div className="text-right">
            <div className="text-xs uppercase tracking-wide text-text-muted">Planned</div>
            <div className="text-lg font-semibold tabular-nums">
              {fmtMoney(data.budget.total_planned, trip.home_currency)}
            </div>
            <div className="text-xs text-text-muted tabular-nums">
              actual {fmtMoney(data.budget.total_actual, trip.home_currency)}
            </div>
          </div>
        </div>
      </section>

      <div className="flex-1 overflow-auto">
        {tab === "overview" && <OverviewTab data={data} onChanged={() => { refresh(); onChanged(); }} />}
        {tab === "itinerary" && <ItineraryTab data={data} onChanged={() => { refresh(); onChanged(); }} />}
        {tab === "budget" && <BudgetTab data={data} onChanged={() => { refresh(); onChanged(); }} />}
        {tab === "todos" && <TodosTab data={data} onChanged={() => { refresh(); onChanged(); }} />}
      </div>

      {showEdit && (
        <TripEditDialog
          trip={trip}
          onClose={() => setShowEdit(false)}
          onSaved={() => { setShowEdit(false); refresh(); onChanged(); }}
        />
      )}
    </div>
  );
}

// ─── Overview tab ────────────────────────────────────────────────

function OverviewTab({ data, onChanged: _ }: { data: TripDashboard; onChanged: () => void }) {
  const trip = data.trip;
  const budget = data.budget;
  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
      <section className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card">
        <div className="mb-3 text-xs uppercase tracking-wide text-text-muted">Budget by category</div>
        <ul className="space-y-2">
          {budget.categories.filter(c => c.planned > 0 || c.actual > 0 || c.capped).map(c => (
            <BudgetBar key={c.category} row={c} currency={trip.home_currency} />
          ))}
          {budget.categories.every(c => c.planned === 0 && c.actual === 0 && !c.capped) && (
            <EmptyState message="No budget data yet — add items in Itinerary." />
          )}
        </ul>
      </section>
      <section className="rounded-lg border border-border bg-bg-card p-4 border-border bg-bg-card">
        <div className="mb-3 text-xs uppercase tracking-wide text-text-muted">Next up</div>
        <UpcomingList data={data} />
      </section>
    </div>
  );
}

function UpcomingList({ data }: { data: TripDashboard }) {
  type Item = { kind: "transport" | "accommodation" | "activity"; when: string; title: string; subtitle: string };
  const items: Item[] = [];
  for (const l of data.transport_legs) {
    items.push({
      kind: "transport",
      when: l.depart_at,
      title: `${l.provider} ${l.reference}`.trim() || l.kind,
      subtitle: `${l.depart_location || "—"} → ${l.arrive_location || "—"}`,
    });
  }
  for (const a of data.accommodations) {
    items.push({ kind: "accommodation", when: a.check_in_at, title: a.name, subtitle: a.address });
  }
  for (const a of data.activities) {
    if (a.start_at) items.push({ kind: "activity", when: a.start_at, title: a.name, subtitle: a.location });
  }
  const now = Date.now();
  const upcoming = items.filter(i => new Date(i.when).getTime() >= now).sort((a, b) => a.when.localeCompare(b.when)).slice(0, 5);
  if (upcoming.length === 0) return <EmptyState message="Nothing upcoming." />;
  return (
    <ul className="divide-y divide-border-subtle text-sm">
      {upcoming.map((i, idx) => (
        <li key={idx} className="flex items-center gap-3 py-2">
          <Icon name={i.kind === "transport" ? "plane" : i.kind === "accommodation" ? "bed" : "compass"} size={16} />
          <div className="flex-1 min-w-0">
            <div className="truncate font-medium">{i.title}</div>
            <div className="truncate text-xs text-text-muted">{i.subtitle}</div>
          </div>
          <div className="text-xs text-text-muted">{fmtDateShort(i.when)} {fmtTime(i.when)}</div>
        </li>
      ))}
    </ul>
  );
}

// ─── Itinerary tab ───────────────────────────────────────────────

type ItemKind = "transport" | "accommodation" | "activity";
type ItemData = TransportLeg | Accommodation | Activity;

function ItineraryTab({ data, onChanged }: { data: TripDashboard; onChanged: () => void }) {
  const [showAdd, setShowAdd] = useState<ItemKind | null>(null);
  const [editItem, setEditItem] = useState<{ kind: ItemKind; data: ItemData } | null>(null);

  // Build a flat timeline of every dated item, sorted.
  type Item =
    | { kind: "transport"; data: TransportLeg; when: string }
    | { kind: "accommodation"; data: Accommodation; when: string }
    | { kind: "activity"; data: Activity; when: string };
  const items: Item[] = [];
  for (const l of data.transport_legs) items.push({ kind: "transport", data: l, when: l.depart_at });
  for (const a of data.accommodations) items.push({ kind: "accommodation", data: a, when: a.check_in_at });
  for (const a of data.activities) if (a.start_at) items.push({ kind: "activity", data: a, when: a.start_at });
  items.sort((a, b) => a.when.localeCompare(b.when));

  return (
    <div className="space-y-3">
      <DestinationsSection data={data} onChanged={onChanged} />

      <div className="flex flex-wrap gap-2">
        <button onClick={() => setShowAdd("transport")} className="btn-secondary"><Icon name="plane" size={14} /> Transport</button>
        <button onClick={() => setShowAdd("accommodation")} className="btn-secondary"><Icon name="bed" size={14} /> Stay</button>
        <button onClick={() => setShowAdd("activity")} className="btn-secondary"><Icon name="compass" size={14} /> Activity</button>
      </div>
      {items.length === 0 ? (
        <EmptyState message="Empty itinerary — add transport, stays, or activities above." />
      ) : (
        <ol className="space-y-2">
          {items.map((it) => (
            <ItineraryRow
              key={`${it.kind}-${it.data.id}`}
              item={it}
              trip={data.trip}
              onEdit={() => setEditItem({ kind: it.kind, data: it.data })}
              onChanged={onChanged}
            />
          ))}
        </ol>
      )}
      {showAdd && (
        <ItemDialog
          kind={showAdd}
          trip={data.trip}
          onClose={() => setShowAdd(null)}
          onSaved={() => { setShowAdd(null); onChanged(); }}
        />
      )}
      {editItem && (
        <ItemDialog
          kind={editItem.kind}
          trip={data.trip}
          existing={editItem.data}
          onClose={() => setEditItem(null)}
          onSaved={() => { setEditItem(null); onChanged(); }}
        />
      )}
      <style>{`.btn-secondary { display: inline-flex; align-items: center; gap: 6px; padding: 0.4rem 0.75rem; border-radius: 0.375rem; background: var(--bg-card); color: var(--text); font-size: 0.875rem; border: 1px solid var(--border); }
      .btn-secondary:hover { background: var(--bg-hover); }`}</style>
    </div>
  );
}

function ItineraryRow({
  item, trip, onEdit, onChanged,
}: {
  item: { kind: string; data: ItemData; when: string };
  trip: Trip;
  onEdit: () => void;
  onChanged: () => void;
}) {
  const ui = useUI();
  const [busy, setBusy] = useState(false);
  const remove = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (busy) return;
    const label = item.kind === "transport" ? "transport leg" : item.kind === "accommodation" ? "stay" : "activity";
    if (!await ui.confirm({
      title: `Delete this ${label}?`,
      message: "Its calendar event is removed too.",
      confirmLabel: "Delete",
    })) return;
    setBusy(true);
    try {
      const path = item.kind === "transport" ? "/transport-legs/" : item.kind === "accommodation" ? "/accommodations/" : "/activities/";
      await api<unknown>(path + item.data.id, { method: "DELETE" });
      onChanged();
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };
  const markBooked = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (busy) return;
    setBusy(true);
    try {
      const path =
        item.kind === "transport" ? `/transport-legs/${item.data.id}/booked`
        : item.kind === "accommodation" ? `/accommodations/${item.data.id}/booked`
        : `/activities/${item.data.id}/booked`;
      await api<unknown>(path, { method: "POST", body: "{}" });
      onChanged();
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const cost = item.data.cost_actual ?? item.data.cost_estimated;
  const costCcy = item.data.currency || trip.home_currency;
  let title = "";
  let subtitle = "";
  let when2 = "";
  let icon = "compass";
  if (item.kind === "transport") {
    const l = item.data as TransportLeg;
    icon = transportIcon(l.kind);
    title = `${l.provider} ${l.reference}`.trim() || l.kind;
    subtitle = `${l.depart_location || "—"} → ${l.arrive_location || "—"}`;
    when2 = `${fmtDateShort(l.depart_at)} ${fmtTime(l.depart_at)} – ${fmtTime(l.arrive_at)}`;
  } else if (item.kind === "accommodation") {
    const a = item.data as Accommodation;
    icon = "bed";
    title = a.name;
    subtitle = a.address;
    when2 = `${fmtDateShort(a.check_in_at)} – ${fmtDateShort(a.check_out_at)}`;
  } else {
    const a = item.data as Activity;
    icon = "compass";
    title = a.name;
    subtitle = a.location || BUDGET_LABEL[a.category] || a.category;
    when2 = a.start_at ? `${fmtDateShort(a.start_at)} ${fmtTime(a.start_at)}` : "Unscheduled";
  }

  return (
    <li
      onClick={onEdit}
      role="button"
      tabIndex={0}
      className="flex items-start gap-3 rounded-lg border border-border bg-bg-card p-3 cursor-pointer hover:border-border-strong hover:bg-bg-hover"
    >
      <div className="mt-1 text-text-muted"><Icon name={icon} size={16} /></div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="truncate font-medium">{title}</span>
          {item.data.booked
            ? <span className="rounded-full bg-success/20 px-2 py-0.5 text-xs text-success">Booked</span>
            : (
              <button
                onClick={markBooked}
                disabled={busy}
                className="rounded-full border border-border px-2 py-0.5 text-xs text-text-muted hover:border-success hover:text-success"
                title="Mark as booked"
              >Mark booked</button>
            )
          }
        </div>
        {subtitle && <div className="text-xs text-text-muted">{subtitle}</div>}
        <div className="mt-0.5 text-xs text-text-dim">{when2}</div>
      </div>
      <div className="flex flex-col items-end gap-1 text-right text-sm">
        <div className="tabular-nums">{cost != null ? fmtMoney(cost, costCcy) : "—"}</div>
        <button onClick={remove} disabled={busy} className="text-text-dim hover:text-error" title="Delete">
          <Icon name="trash" size={12} />
        </button>
      </div>
    </li>
  );
}

// ─── Destinations section ────────────────────────────────────────

function DestinationsSection({ data, onChanged }: { data: TripDashboard; onChanged: () => void }) {
  const ui = useUI();
  const [editing, setEditing] = useState<Destination | null>(null);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState(false);

  const dests = data.destinations;
  const tripID = data.trip.id;

  const move = async (dest: Destination, dir: -1 | 1) => {
    if (busy) return;
    const idx = dests.findIndex(d => d.id === dest.id);
    if (idx < 0) return;
    const swapIdx = idx + dir;
    if (swapIdx < 0 || swapIdx >= dests.length) return;
    const order = dests.map(d => d.id);
    [order[idx], order[swapIdx]] = [order[swapIdx], order[idx]];
    setBusy(true);
    try {
      await api<unknown>("/destinations/reorder", {
        method: "POST",
        body: JSON.stringify({ trip_id: tripID, order }),
      });
      onChanged();
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async (dest: Destination) => {
    if (!await ui.confirm({
      title: "Delete destination?",
      message: `"${dest.place_name}" will be removed from this trip.`,
      confirmLabel: "Delete",
    })) return;
    setBusy(true);
    try {
      await api<unknown>(`/destinations/${dest.id}`, { method: "DELETE" });
      onChanged();
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="rounded-lg border border-border bg-bg-card">
      <header className="flex items-center justify-between border-b border-border-subtle px-4 py-2">
        <span className="text-xs uppercase tracking-wide text-text-muted">Destinations</span>
        <button onClick={() => setAdding(true)} className="flex items-center gap-1 text-xs text-text-muted hover:text-text">
          <Icon name="plus" size={12} /> Add
        </button>
      </header>
      {dests.length === 0 ? (
        <EmptyState message="No destinations yet." />
      ) : (
        <ul className="divide-y divide-border-subtle text-sm">
          {dests.map((d, i) => (
            <li key={d.id} className="flex items-center gap-2 px-4 py-2">
              <div className="flex-1 min-w-0">
                <div className="truncate font-medium">
                  {d.place_name}
                  {d.country && <span className="ml-1.5 text-xs text-text-dim">{d.country}</span>}
                </div>
                <div className="text-xs text-text-muted">
                  {fmtDateShort(d.arrive_at)} – {fmtDateShort(d.depart_at)}
                </div>
              </div>
              <button onClick={() => move(d, -1)} disabled={busy || i === 0} className="p-1 text-text-dim hover:text-text disabled:opacity-30" title="Move up"><Icon name="chevron-left" size={12} /></button>
              <button onClick={() => move(d, 1)} disabled={busy || i === dests.length - 1} className="p-1 text-text-dim hover:text-text disabled:opacity-30" title="Move down"><Icon name="chevron-right" size={12} /></button>
              <button onClick={() => setEditing(d)} className="p-1 text-text-dim hover:text-text" title="Edit"><Icon name="edit" size={12} /></button>
              <button onClick={() => remove(d)} disabled={busy} className="p-1 text-text-dim hover:text-error" title="Delete"><Icon name="trash" size={12} /></button>
            </li>
          ))}
        </ul>
      )}
      {adding && (
        <DestinationDialog
          trip={data.trip}
          onClose={() => setAdding(false)}
          onSaved={() => { setAdding(false); onChanged(); }}
        />
      )}
      {editing && (
        <DestinationDialog
          trip={data.trip}
          existing={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); onChanged(); }}
        />
      )}
    </section>
  );
}

// ─── Budget tab ──────────────────────────────────────────────────

function BudgetTab({ data, onChanged }: { data: TripDashboard; onChanged: () => void }) {
  const trip = data.trip;
  const [editing, setEditing] = useState(false);
  return (
    <div className="space-y-3">
      <div className="overflow-hidden rounded-lg border border-border bg-bg-card">
        <header className="flex items-center justify-between border-b border-border-subtle px-4 py-2 border-border-subtle">
          <span className="text-xs uppercase tracking-wide text-text-muted">Per-category</span>
          <button onClick={() => setEditing(v => !v)} className="text-xs text-text-muted hover:text-text">
            {editing ? "Done" : "Set caps"}
          </button>
        </header>
        <table className="w-full text-sm">
          <thead className="border-b border-border-subtle text-left text-xs uppercase tracking-wide text-text-muted">
            <tr>
              <th className="px-3 py-2">Category</th>
              <th className="px-3 py-2 text-right">Cap</th>
              <th className="px-3 py-2 text-right">Planned</th>
              <th className="px-3 py-2 text-right">Actual</th>
              <th className="px-3 py-2 text-right">Δ</th>
            </tr>
          </thead>
          <tbody>
            {data.budget.categories.map(c => (
              <BudgetRow key={c.category} row={c} currency={trip.home_currency} tripID={trip.id} editing={editing} onChanged={onChanged} />
            ))}
            <tr className="border-t border-border font-medium border-border">
              <td className="px-3 py-2">Total</td>
              <td className="px-3 py-2 text-right tabular-nums">{fmtMoney(data.budget.total_cap, trip.home_currency)}</td>
              <td className="px-3 py-2 text-right tabular-nums">{fmtMoney(data.budget.total_planned, trip.home_currency)}</td>
              <td className="px-3 py-2 text-right tabular-nums">{fmtMoney(data.budget.total_actual, trip.home_currency)}</td>
              <td className="px-3 py-2 text-right tabular-nums">{fmtMoney(data.budget.total_planned - data.budget.total_actual, trip.home_currency, { signed: true })}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  );
}

function BudgetRow({ row, currency, tripID, editing, onChanged }: { row: BudgetCategoryRow; currency: string; tripID: number; editing: boolean; onChanged: () => void }) {
  const [val, setVal] = useState(row.cap > 0 ? (row.cap / 100).toString() : "");
  const save = async () => {
    const minor = parseMoneyDecimal(val);
    await api<unknown>("/budget", {
      method: "POST",
      body: JSON.stringify({ trip_id: tripID, category: row.category, amount: minor }),
    });
    onChanged();
  };
  return (
    <tr className="border-b border-border-subtle last:border-0 border-border-subtle">
      <td className="px-3 py-2">{BUDGET_LABEL[row.category]}</td>
      <td className="px-3 py-2 text-right tabular-nums">
        {editing ? (
          <input
            value={val}
            onChange={e => setVal(e.target.value)}
            onBlur={save}
            className="w-20 rounded border border-border px-2 py-0.5 text-right text-sm border-border bg-bg-card"
            placeholder="—"
          />
        ) : (
          row.capped ? fmtMoney(row.cap, currency) : <span className="text-text-dim">—</span>
        )}
      </td>
      <td className="px-3 py-2 text-right tabular-nums">{row.planned > 0 ? fmtMoney(row.planned, currency) : <span className="text-text-dim">—</span>}</td>
      <td className="px-3 py-2 text-right tabular-nums">{row.actual > 0 ? fmtMoney(row.actual, currency) : <span className="text-text-dim">—</span>}</td>
      <td className={`px-3 py-2 text-right tabular-nums ${row.delta < 0 ? "text-error" : ""}`}>
        {row.capped || row.planned > 0 ? fmtMoney(row.delta, currency, { signed: true }) : <span className="text-text-dim">—</span>}
      </td>
    </tr>
  );
}

function BudgetBar({ row, currency }: { row: BudgetCategoryRow; currency: string }) {
  // Bar fills against cap when capped, otherwise against planned.
  const target = row.capped ? row.cap : row.planned;
  const pct = target > 0 ? Math.min(100, (row.actual / target) * 100) : 0;
  const over = row.capped && row.actual > row.cap;
  const barColor = over ? "bg-error" : pct >= 75 ? "bg-warn" : "bg-success";
  return (
    <li>
      <div className="mb-1 flex items-center justify-between text-sm">
        <span>{BUDGET_LABEL[row.category]}</span>
        <span className="tabular-nums text-xs text-text-muted">
          {fmtMoney(row.actual, currency)} / {fmtMoney(target, currency)}
        </span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-bg-hover">
        <div className={`h-full ${barColor}`} style={{ width: `${pct}%` }} />
      </div>
    </li>
  );
}

// ─── Todos tab ───────────────────────────────────────────────────

function TodosTab({ data, onChanged }: { data: TripDashboard; onChanged: () => void }) {
  const [label, setLabel] = useState("");
  const trip = data.trip;
  const add = async () => {
    if (!label.trim()) return;
    await api<Todo>("/todos", { method: "POST", body: JSON.stringify({ trip_id: trip.id, label }) });
    setLabel("");
    onChanged();
  };
  const toggle = async (id: number) => {
    await api<Todo>(`/todos/${id}/toggle`, { method: "POST", body: "{}" });
    onChanged();
  };
  const remove = async (id: number) => {
    await api<unknown>(`/todos/${id}`, { method: "DELETE" });
    onChanged();
  };
  return (
    <div className="rounded-lg border border-border bg-bg-card">
      <div className="flex items-center gap-2 border-b border-border-subtle p-3 border-border-subtle">
        <input
          value={label}
          onChange={e => setLabel(e.target.value)}
          onKeyDown={e => { if (e.key === "Enter") add(); }}
          placeholder="Add a packing item or errand"
          className="flex-1 rounded border border-border px-2 py-1 text-sm border-border bg-bg-card"
        />
        <button onClick={add} className="rounded bg-accent px-3 py-1 text-sm text-bg hover:bg-accent-hover">Add</button>
      </div>
      {data.todos.length === 0 ? (
        <EmptyState message="No todos yet." />
      ) : (
        <ul className="divide-y divide-border-subtle text-sm">
          {data.todos.map(t => (
            <li key={t.id} className="flex items-center gap-3 px-3 py-2">
              <button
                onClick={() => toggle(t.id)}
                className={`flex h-5 w-5 items-center justify-center rounded border ${t.done ? "border-success bg-success text-bg" : "border-border"}`}
              >
                {t.done && <Icon name="check" size={12} />}
              </button>
              <span className={`flex-1 ${t.done ? "text-text-dim line-through" : ""}`}>{t.label}</span>
              {t.due_at && <span className="text-xs text-text-muted">{fmtDateShort(t.due_at)}</span>}
              <button onClick={() => remove(t.id)} className="text-text-dim hover:text-error">
                <Icon name="trash" size={12} />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ─── Dialogs ─────────────────────────────────────────────────────

function NewTripDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (id: number) => void }) {
  const [name, setName] = useState("");
  const [startAt, setStartAt] = useState(() => new Date().toISOString().slice(0, 10));
  const [endAt, setEndAt] = useState(() => {
    const d = new Date();
    d.setDate(d.getDate() + 7);
    return d.toISOString().slice(0, 10);
  });
  const [currency, setCurrency] = useState("EUR");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const submit = async () => {
    setBusy(true); setErr("");
    try {
      const trip = await api<Trip>("/trips", {
        method: "POST",
        body: JSON.stringify({
          name, start_at: startAt + "T00:00:00Z", end_at: endAt + "T23:59:59Z",
          home_currency: currency,
        }),
      });
      onCreated(trip.id);
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };
  return (
    <Dialog title="New trip" onClose={onClose}>
      <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} className="input" autoFocus placeholder="Paris weekend" /></Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="From"><input type="date" value={startAt} onChange={e => setStartAt(e.target.value)} className="input" /></Field>
        <Field label="To"><input type="date" value={endAt} onChange={e => setEndAt(e.target.value)} className="input" /></Field>
      </div>
      <Field label="Home currency"><input value={currency} onChange={e => setCurrency(e.target.value.toUpperCase())} className="input uppercase" maxLength={3} /></Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
        <button onClick={submit} disabled={busy || !name} className="btn-dialog-primary">{busy ? "Creating…" : "Create"}</button>
      </DialogActions>
    </Dialog>
  );
}

function ItemDialog({ kind, trip, existing, onClose, onSaved }: {
  kind: "transport" | "accommodation" | "activity";
  trip: Trip;
  existing?: ItemData;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isEdit = existing != null;
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  // Prefill the right initial value per field based on what (if anything)
  // we're editing. Each branch coerces the existing row to its concrete
  // type — TypeScript can't narrow off `kind` for the union, so we cast.
  const t = (kind === "transport" && existing) ? existing as TransportLeg : null;
  const a = (kind === "accommodation" && existing) ? existing as Accommodation : null;
  const c = (kind === "activity" && existing) ? existing as Activity : null;

  const [name, setName] = useState(a?.name ?? c?.name ?? "");
  const [cost, setCost] = useState(() => {
    const v = existing?.cost_actual ?? existing?.cost_estimated;
    return v != null ? (v / 100).toFixed(2) : "";
  });
  const [currency, setCurrency] = useState(existing?.currency ?? trip.home_currency);
  const [notes, setNotes] = useState(existing?.notes ?? "");

  const [tKind, setTKind] = useState<TransportLeg["kind"]>(t?.kind ?? "flight");
  const [provider, setProvider] = useState(t?.provider ?? "");
  const [reference, setReference] = useState(t?.reference ?? "");
  const [departAt, setDepartAt] = useState(t?.depart_at.slice(0, 16) ?? trip.start_at.slice(0, 16));
  const [arriveAt, setArriveAt] = useState(t?.arrive_at.slice(0, 16) ?? trip.start_at.slice(0, 16));
  const [departLoc, setDepartLoc] = useState(t?.depart_location ?? "");
  const [arriveLoc, setArriveLoc] = useState(t?.arrive_location ?? "");

  const [aKind, setAKind] = useState<Accommodation["kind"]>(a?.kind ?? "hotel");
  const [address, setAddress] = useState(a?.address ?? "");
  const [checkIn, setCheckIn] = useState(a?.check_in_at.slice(0, 10) ?? trip.start_at.slice(0, 10));
  const [checkOut, setCheckOut] = useState(a?.check_out_at.slice(0, 10) ?? trip.end_at.slice(0, 10));

  const [actCategory, setActCategory] = useState<Activity["category"]>(c?.category ?? "activity");
  const [actStart, setActStart] = useState(c?.start_at?.slice(0, 16) ?? "");
  const [actLocation, setActLocation] = useState(c?.location ?? "");

  const submit = async () => {
    setBusy(true); setErr("");
    try {
      const cents = cost.trim() ? parseMoneyDecimal(cost) : undefined;
      // Field name for the cost field switches based on edit mode:
      // when we already have a cost_actual we update it; otherwise we
      // edit cost_estimated. The mark-booked button is the dedicated
      // path for the planned→actual transition.
      const costField = isEdit && existing?.cost_actual != null ? "cost_actual" : "cost_estimated";
      if (kind === "transport") {
        const body = {
          trip_id: trip.id, kind: tKind,
          depart_at: ensureRfc3339(departAt), arrive_at: ensureRfc3339(arriveAt),
          provider, reference, depart_location: departLoc, arrive_location: arriveLoc,
          [costField]: cents, currency, notes,
        };
        if (isEdit) {
          await api<TransportLeg>(`/transport-legs/${existing!.id}`, { method: "PATCH", body: JSON.stringify(body) });
        } else {
          await api<TransportLeg>("/transport-legs", { method: "POST", body: JSON.stringify(body) });
        }
      } else if (kind === "accommodation") {
        const body = {
          trip_id: trip.id, name, kind: aKind, address,
          check_in_at: checkIn + "T15:00:00Z", check_out_at: checkOut + "T11:00:00Z",
          [costField]: cents, currency, notes,
        };
        if (isEdit) {
          await api<Accommodation>(`/accommodations/${existing!.id}`, { method: "PATCH", body: JSON.stringify(body) });
        } else {
          await api<Accommodation>("/accommodations", { method: "POST", body: JSON.stringify(body) });
        }
      } else {
        const body = {
          trip_id: trip.id, name, category: actCategory,
          start_at: actStart ? ensureRfc3339(actStart) : undefined,
          location: actLocation, [costField]: cents, currency, notes,
        };
        if (isEdit) {
          await api<Activity>(`/activities/${existing!.id}`, { method: "PATCH", body: JSON.stringify(body) });
        } else {
          await api<Activity>("/activities", { method: "POST", body: JSON.stringify(body) });
        }
      }
      onSaved();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const titlePrefix = isEdit ? "Edit" : "Add";
  const title = kind === "transport" ? `${titlePrefix} transport` : kind === "accommodation" ? `${titlePrefix} accommodation` : `${titlePrefix} activity`;

  return (
    <Dialog title={title} onClose={onClose}>
      {kind === "transport" && (
        <>
          <Field label="Kind">
            <select value={tKind} onChange={e => setTKind(e.target.value)} className="input">
              <option value="flight">Flight</option><option value="train">Train</option>
              <option value="car">Car</option><option value="bus">Bus</option>
              <option value="ferry">Ferry</option><option value="other">Other</option>
            </select>
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Provider"><input value={provider} onChange={e => setProvider(e.target.value)} className="input" placeholder="Air France" /></Field>
            <Field label="Reference"><input value={reference} onChange={e => setReference(e.target.value)} className="input" placeholder="AF1234" /></Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Depart"><input type="datetime-local" value={departAt} onChange={e => setDepartAt(e.target.value)} className="input" /></Field>
            <Field label="Arrive"><input type="datetime-local" value={arriveAt} onChange={e => setArriveAt(e.target.value)} className="input" /></Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="From"><input value={departLoc} onChange={e => setDepartLoc(e.target.value)} className="input" placeholder="CDG" /></Field>
            <Field label="To"><input value={arriveLoc} onChange={e => setArriveLoc(e.target.value)} className="input" placeholder="LIN" /></Field>
          </div>
        </>
      )}
      {kind === "accommodation" && (
        <>
          <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} className="input" autoFocus placeholder="Hotel des Saints" /></Field>
          <Field label="Kind">
            <select value={aKind} onChange={e => setAKind(e.target.value)} className="input">
              <option value="hotel">Hotel</option><option value="airbnb">Airbnb</option>
              <option value="hostel">Hostel</option><option value="rental">Rental</option>
              <option value="friend">Friend</option><option value="other">Other</option>
            </select>
          </Field>
          <Field label="Address"><input value={address} onChange={e => setAddress(e.target.value)} className="input" /></Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Check-in"><input type="date" value={checkIn} onChange={e => setCheckIn(e.target.value)} className="input" /></Field>
            <Field label="Check-out"><input type="date" value={checkOut} onChange={e => setCheckOut(e.target.value)} className="input" /></Field>
          </div>
        </>
      )}
      {kind === "activity" && (
        <>
          <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} className="input" autoFocus placeholder="Louvre" /></Field>
          <Field label="Category">
            <select value={actCategory} onChange={e => setActCategory(e.target.value)} className="input">
              <option value="activity">Activity</option><option value="food">Food</option>
              <option value="shopping">Shopping</option><option value="transport_local">Local transport</option>
              <option value="other">Other</option>
            </select>
          </Field>
          <Field label="When (optional)"><input type="datetime-local" value={actStart} onChange={e => setActStart(e.target.value)} className="input" /></Field>
          <Field label="Location"><input value={actLocation} onChange={e => setActLocation(e.target.value)} className="input" /></Field>
        </>
      )}
      <div className="grid grid-cols-2 gap-3">
        <Field label={`Cost (${currency})`}><input value={cost} onChange={e => setCost(e.target.value)} className="input" placeholder="0.00" /></Field>
        <Field label="Currency"><input value={currency} onChange={e => setCurrency(e.target.value.toUpperCase())} className="input uppercase" maxLength={3} /></Field>
      </div>
      <Field label="Notes"><input value={notes} onChange={e => setNotes(e.target.value)} className="input" /></Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
        <button onClick={submit} disabled={busy} className="btn-dialog-primary">{busy ? "Saving…" : isEdit ? "Update" : "Save"}</button>
      </DialogActions>
    </Dialog>
  );
}

// ─── Destination + Trip-edit dialogs ─────────────────────────────

function DestinationDialog({ trip, existing, onClose, onSaved }: {
  trip: Trip;
  existing?: Destination;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isEdit = existing != null;
  const [placeName, setPlaceName] = useState(existing?.place_name ?? "");
  const [country, setCountry] = useState(existing?.country ?? "");
  const [arriveAt, setArriveAt] = useState(existing?.arrive_at.slice(0, 10) ?? trip.start_at.slice(0, 10));
  const [departAt, setDepartAt] = useState(existing?.depart_at.slice(0, 10) ?? trip.end_at.slice(0, 10));
  const [notes, setNotes] = useState(existing?.notes ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setBusy(true); setErr("");
    try {
      const body: Record<string, unknown> = {
        trip_id: trip.id,
        place_name: placeName,
        country,
        arrive_at: arriveAt + "T00:00:00Z",
        depart_at: departAt + "T23:59:59Z",
        notes,
      };
      if (isEdit) {
        await api<Destination>(`/destinations/${existing!.id}`, { method: "PATCH", body: JSON.stringify(body) });
      } else {
        await api<Destination>("/destinations", { method: "POST", body: JSON.stringify(body) });
      }
      onSaved();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Dialog title={isEdit ? "Edit destination" : "Add destination"} onClose={onClose}>
      <Field label="Place"><input value={placeName} onChange={e => setPlaceName(e.target.value)} className="input" autoFocus placeholder="Paris" /></Field>
      <Field label="Country (ISO-2)"><input value={country} onChange={e => setCountry(e.target.value.toUpperCase())} className="input uppercase" maxLength={2} placeholder="FR" /></Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Arrive"><input type="date" value={arriveAt} onChange={e => setArriveAt(e.target.value)} className="input" /></Field>
        <Field label="Depart"><input type="date" value={departAt} onChange={e => setDepartAt(e.target.value)} className="input" /></Field>
      </div>
      <Field label="Notes"><input value={notes} onChange={e => setNotes(e.target.value)} className="input" /></Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
        <button onClick={submit} disabled={busy || !placeName} className="btn-dialog-primary">{busy ? "Saving…" : isEdit ? "Update" : "Save"}</button>
      </DialogActions>
    </Dialog>
  );
}

function TripEditDialog({ trip, onClose, onSaved }: { trip: Trip; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(trip.name);
  const [startAt, setStartAt] = useState(trip.start_at.slice(0, 10));
  const [endAt, setEndAt] = useState(trip.end_at.slice(0, 10));
  const [status, setStatus] = useState<Trip["status"]>(trip.status);
  const [color, setColor] = useState(trip.color);
  const [syncCalendar, setSyncCalendar] = useState(trip.sync_calendar);
  const [notes, setNotes] = useState(trip.notes);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setBusy(true); setErr("");
    try {
      await api<Trip>(`/trips/${trip.id}`, {
        method: "PATCH",
        body: JSON.stringify({
          name,
          start_at: startAt + "T00:00:00Z",
          end_at: endAt + "T23:59:59Z",
          status,
          color,
          sync_calendar: syncCalendar,
          notes,
        }),
      });
      onSaved();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Dialog title="Edit trip" onClose={onClose}>
      <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} className="input" autoFocus /></Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="From"><input type="date" value={startAt} onChange={e => setStartAt(e.target.value)} className="input" /></Field>
        <Field label="To"><input type="date" value={endAt} onChange={e => setEndAt(e.target.value)} className="input" /></Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Status">
          <select value={status} onChange={e => setStatus(e.target.value as Trip["status"])} className="input">
            <option value="planning">Planning</option>
            <option value="booked">Booked</option>
            <option value="in_progress">In progress</option>
            <option value="done">Done</option>
            <option value="cancelled">Cancelled</option>
          </select>
        </Field>
        <Field label="Color">
          <input type="color" value={color} onChange={e => setColor(e.target.value)} className="input" style={{ padding: "4px", height: "38px" }} />
        </Field>
      </div>
      <Field label="Calendar sync">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={syncCalendar} onChange={e => setSyncCalendar(e.target.checked)} />
          Mirror itinerary into a dedicated calendar
        </label>
      </Field>
      <Field label="Notes"><input value={notes} onChange={e => setNotes(e.target.value)} className="input" /></Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
        <button onClick={submit} disabled={busy || !name} className="btn-dialog-primary">{busy ? "Saving…" : "Update"}</button>
      </DialogActions>
    </Dialog>
  );
}

// ─── Generic UI bits ─────────────────────────────────────────────

function Dialog({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-bg-overlay p-4">
      <div className="w-full max-w-md rounded-lg border border-border bg-bg-card p-5 shadow-xl border-border bg-bg-card">
        <header className="mb-3 flex items-center justify-between">
          <h3 className="text-base font-semibold">{title}</h3>
          <button onClick={onClose} className="text-text-dim hover:text-text"><Icon name="x" size={18} /></button>
        </header>
        <div className="space-y-3">{children}</div>
        <style>{`
          .input { width: 100%; padding: 0.5rem 0.75rem; border-radius: 0.375rem; border: 1px solid var(--border); background: var(--bg-input); color: var(--text); }
          .input:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
          .btn-dialog-primary { padding: 0.5rem 1rem; border-radius: 0.375rem; background: var(--accent); color: var(--bg); font-size: 0.875rem; }
          .btn-dialog-primary:disabled { opacity: 0.5; }
          .btn-dialog-primary:hover:not(:disabled) { background: var(--accent-hover); }
          .btn-dialog-secondary { padding: 0.5rem 1rem; border-radius: 0.375rem; background: transparent; color: var(--text); font-size: 0.875rem; border: 1px solid var(--border); }
          .btn-dialog-secondary:hover { background: var(--bg-hover); }
        `}</style>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs font-medium uppercase tracking-wide text-text-muted">{label}</span>
      {children}
    </label>
  );
}

function DialogActions({ children }: { children: React.ReactNode }) {
  return <div className="mt-4 flex justify-end gap-2">{children}</div>;
}

function EmptyState({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-8 text-center text-sm text-text-muted">
      <Icon name="map" size={24} />
      <p>{message}</p>
    </div>
  );
}

// ─── helpers ─────────────────────────────────────────────────────

// Lightweight echo of finance's parseMoneyToMinor.
function parseMoneyDecimal(s: string): number {
  s = s.trim();
  if (!s) return 0;
  let neg = false;
  if (s.startsWith("(") && s.endsWith(")")) { neg = true; s = s.slice(1, -1); }
  if (s.startsWith("-")) { neg = true; s = s.slice(1); }
  s = s.replace(/\s/g, "");
  const lastDot = s.lastIndexOf(".");
  const lastComma = s.lastIndexOf(",");
  let integer = s, fraction = "";
  if (lastDot >= 0 && lastComma >= 0) {
    if (lastDot > lastComma) { integer = s.slice(0, lastDot).replace(/,/g, ""); fraction = s.slice(lastDot + 1); }
    else { integer = s.slice(0, lastComma).replace(/\./g, ""); fraction = s.slice(lastComma + 1); }
  } else if (lastDot >= 0) { integer = s.slice(0, lastDot); fraction = s.slice(lastDot + 1); }
  else if (lastComma >= 0) { integer = s.slice(0, lastComma); fraction = s.slice(lastComma + 1); }
  if (fraction.length > 2) { integer += fraction; fraction = ""; }
  if (!fraction) fraction = "00";
  if (fraction.length === 1) fraction += "0";
  fraction = fraction.slice(0, 2);
  const v = parseInt(integer || "0", 10) * 100 + parseInt(fraction, 10);
  return neg ? -v : v;
}

// ensureRfc3339 accepts the value an <input type="datetime-local">
// produces ("2026-06-05T08:30") and tacks on ":00Z" so the server's
// time parser is happy.
function ensureRfc3339(s: string): string {
  if (!s) return s;
  if (s.endsWith("Z")) return s;
  if (s.length === 16) return s + ":00Z";
  if (s.length === 19) return s + "Z";
  return s;
}
