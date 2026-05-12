// TripsPanel — plan + budget trips end-to-end. Calendar mirrored.
//
// Two screens:
//   List view   — cards for each trip, "+ New trip" CTA
//   Detail view — selected trip with Overview / Itinerary / Budget / Todos tabs
//
// All money is stored as integer minor units server-side; the UI uses
// fmtMoney(minor, currency) for display. Colors in SVG come from CSS
// variables because the dashboard's Tailwind JIT doesn't scan apps/mcp/*/ui/.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

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

export default function TripsPanel({ projectId }: NativePanelProps) {
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
          className="flex items-center gap-1.5 rounded-md bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-800 dark:bg-slate-100 dark:text-slate-900 dark:hover:bg-white"
        >
          <Icon name="plus" size={14} /> New trip
        </button>
      </header>

      {error && (
        <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
          {error}
        </div>
      )}

      <div className="flex-1 overflow-auto">
        {trips.length === 0 ? (
          <EmptyState message="No trips yet. Click 'New trip' to plan one." />
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {trips.map(t => <TripCard key={t.id} trip={t} onOpen={() => setSelectedID(t.id)} />)}
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
    days.tone === "active" ? "bg-emerald-100 text-emerald-700 dark:bg-emerald-900 dark:text-emerald-300"
    : days.tone === "future" ? "bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300"
    : "bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300";
  return (
    <button
      onClick={onOpen}
      className="flex flex-col overflow-hidden rounded-lg border border-slate-200 bg-white text-left transition hover:border-slate-400 hover:shadow-sm dark:border-slate-700 dark:bg-slate-900 dark:hover:border-slate-500"
    >
      <div className="h-1.5" style={{ background: trip.color }} />
      <div className="flex-1 p-4">
        <div className="mb-1 flex items-center justify-between">
          <span className={`rounded-full px-2 py-0.5 text-xs ${toneClass}`}>{days.label}</span>
          <span className="text-xs text-slate-500">{STATUS_LABEL[trip.status]}</span>
        </div>
        <h3 className="text-base font-semibold">{trip.name}</h3>
        <p className="text-xs text-slate-500">
          {fmtDateShort(trip.start_at)} – {fmtDateShort(trip.end_at)}
        </p>
      </div>
    </button>
  );
}

// ─── Detail view ─────────────────────────────────────────────────

function TripDetail({ tripID, onBack, onChanged }: { tripID: number; onBack: () => void; onChanged: () => void }) {
  const [data, setData] = useState<TripDashboard | null>(null);
  const [tab, setTab] = useState<Tab>("overview");
  const [error, setError] = useState("");

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

  if (!data) {
    return (
      <div className="flex h-full items-center justify-center p-4">
        {error ? <div className="text-sm text-red-600">{error}</div> : <div className="text-sm text-slate-500">Loading…</div>}
      </div>
    );
  }

  const trip = data.trip;
  const days = daysUntil(trip.start_at, trip.end_at);

  return (
    <div className="flex h-full flex-col gap-3 p-4">
      <header className="flex items-center justify-between">
        <button onClick={onBack} className="flex items-center gap-1 text-sm text-slate-600 hover:text-slate-900 dark:text-slate-300 dark:hover:text-white">
          <Icon name="chevron-left" size={14} /> Trips
        </button>
        <nav className="flex rounded-md border border-slate-200 overflow-hidden text-sm dark:border-slate-700">
          {(["overview", "itinerary", "budget", "todos"] as Tab[]).map(t => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-3 py-1.5 capitalize ${tab === t ? "bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900" : "hover:bg-slate-100 dark:hover:bg-slate-800"}`}
            >{t}</button>
          ))}
        </nav>
      </header>

      <section className="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900">
        <div className="h-1.5" style={{ background: trip.color }} />
        <div className="flex items-center justify-between p-4">
          <div>
            <h2 className="text-xl font-semibold">{trip.name}</h2>
            <p className="text-sm text-slate-500">
              {fmtDate(trip.start_at)} – {fmtDate(trip.end_at)} • {days.label}
            </p>
          </div>
          <div className="text-right">
            <div className="text-xs uppercase tracking-wide text-slate-500">Planned</div>
            <div className="text-lg font-semibold tabular-nums">
              {fmtMoney(data.budget.total_planned, trip.home_currency)}
            </div>
            <div className="text-xs text-slate-500 tabular-nums">
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
    </div>
  );
}

// ─── Overview tab ────────────────────────────────────────────────

function OverviewTab({ data, onChanged: _ }: { data: TripDashboard; onChanged: () => void }) {
  const trip = data.trip;
  const budget = data.budget;
  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
      <section className="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-700 dark:bg-slate-900">
        <div className="mb-3 text-xs uppercase tracking-wide text-slate-500">Budget by category</div>
        <ul className="space-y-2">
          {budget.categories.filter(c => c.planned > 0 || c.actual > 0 || c.capped).map(c => (
            <BudgetBar key={c.category} row={c} currency={trip.home_currency} />
          ))}
          {budget.categories.every(c => c.planned === 0 && c.actual === 0 && !c.capped) && (
            <EmptyState message="No budget data yet — add items in Itinerary." />
          )}
        </ul>
      </section>
      <section className="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-700 dark:bg-slate-900">
        <div className="mb-3 text-xs uppercase tracking-wide text-slate-500">Next up</div>
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
    <ul className="divide-y divide-slate-100 dark:divide-slate-800 text-sm">
      {upcoming.map((i, idx) => (
        <li key={idx} className="flex items-center gap-3 py-2">
          <Icon name={i.kind === "transport" ? "plane" : i.kind === "accommodation" ? "bed" : "compass"} size={16} />
          <div className="flex-1 min-w-0">
            <div className="truncate font-medium">{i.title}</div>
            <div className="truncate text-xs text-slate-500">{i.subtitle}</div>
          </div>
          <div className="text-xs text-slate-500">{fmtDateShort(i.when)} {fmtTime(i.when)}</div>
        </li>
      ))}
    </ul>
  );
}

// ─── Itinerary tab ───────────────────────────────────────────────

function ItineraryTab({ data, onChanged }: { data: TripDashboard; onChanged: () => void }) {
  const [showAdd, setShowAdd] = useState<"transport" | "accommodation" | "activity" | null>(null);

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
      <div className="flex flex-wrap gap-2">
        <button onClick={() => setShowAdd("transport")} className="btn-secondary"><Icon name="plane" size={14} /> Transport</button>
        <button onClick={() => setShowAdd("accommodation")} className="btn-secondary"><Icon name="bed" size={14} /> Stay</button>
        <button onClick={() => setShowAdd("activity")} className="btn-secondary"><Icon name="compass" size={14} /> Activity</button>
      </div>
      {items.length === 0 ? (
        <EmptyState message="Empty itinerary — add transport, stays, or activities above." />
      ) : (
        <ol className="space-y-2">
          {items.map((it, idx) => <ItineraryRow key={`${it.kind}-${it.data.id}`} item={it} idx={idx} trip={data.trip} onChanged={onChanged} />)}
        </ol>
      )}
      {showAdd && <AddItemDialog kind={showAdd} trip={data.trip} onClose={() => setShowAdd(null)} onCreated={() => { setShowAdd(null); onChanged(); }} />}
      <style>{`.btn-secondary { display: inline-flex; align-items: center; gap: 6px; padding: 0.4rem 0.75rem; border-radius: 0.375rem; background: white; color: inherit; font-size: 0.875rem; border: 1px solid rgb(203 213 225); }
      .dark .btn-secondary { border-color: rgb(51 65 85); background: rgb(15 23 42); }
      .btn-secondary:hover { background: rgb(241 245 249); }
      .dark .btn-secondary:hover { background: rgb(30 41 59); }`}</style>
    </div>
  );
}

function ItineraryRow({ item, idx: _idx, trip, onChanged }: { item: { kind: string; data: TransportLeg | Accommodation | Activity; when: string }; idx: number; trip: Trip; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const remove = async () => {
    if (busy) return;
    if (!confirm("Delete this item?")) return;
    setBusy(true);
    try {
      const path = item.kind === "transport" ? "/transport-legs/" : item.kind === "accommodation" ? "/accommodations/" : "/activities/";
      await api<unknown>(path + item.data.id, { method: "DELETE" });
      onChanged();
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : String(e));
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
    <li className="flex items-start gap-3 rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-700 dark:bg-slate-900">
      <div className="mt-1 text-slate-500"><Icon name={icon} size={16} /></div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="truncate font-medium">{title}</span>
          {item.data.booked && <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-700 dark:bg-emerald-900 dark:text-emerald-300">Booked</span>}
        </div>
        {subtitle && <div className="text-xs text-slate-500">{subtitle}</div>}
        <div className="mt-0.5 text-xs text-slate-400">{when2}</div>
      </div>
      <div className="text-right text-sm">
        <div className="tabular-nums">{cost != null ? fmtMoney(cost, costCcy) : "—"}</div>
        <button onClick={remove} disabled={busy} className="mt-1 text-slate-400 hover:text-red-600">
          <Icon name="trash" size={12} />
        </button>
      </div>
    </li>
  );
}

// ─── Budget tab ──────────────────────────────────────────────────

function BudgetTab({ data, onChanged }: { data: TripDashboard; onChanged: () => void }) {
  const trip = data.trip;
  const [editing, setEditing] = useState(false);
  return (
    <div className="space-y-3">
      <div className="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900">
        <header className="flex items-center justify-between border-b border-slate-100 px-4 py-2 dark:border-slate-800">
          <span className="text-xs uppercase tracking-wide text-slate-500">Per-category</span>
          <button onClick={() => setEditing(v => !v)} className="text-xs text-slate-600 hover:text-slate-900 dark:text-slate-300 dark:hover:text-white">
            {editing ? "Done" : "Set caps"}
          </button>
        </header>
        <table className="w-full text-sm">
          <thead className="border-b border-slate-100 dark:border-slate-800 text-left text-xs uppercase tracking-wide text-slate-500">
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
            <tr className="border-t border-slate-200 font-medium dark:border-slate-700">
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
    <tr className="border-b border-slate-100 last:border-0 dark:border-slate-800">
      <td className="px-3 py-2">{BUDGET_LABEL[row.category]}</td>
      <td className="px-3 py-2 text-right tabular-nums">
        {editing ? (
          <input
            value={val}
            onChange={e => setVal(e.target.value)}
            onBlur={save}
            className="w-20 rounded border border-slate-200 px-2 py-0.5 text-right text-sm dark:border-slate-700 dark:bg-slate-900"
            placeholder="—"
          />
        ) : (
          row.capped ? fmtMoney(row.cap, currency) : <span className="text-slate-400">—</span>
        )}
      </td>
      <td className="px-3 py-2 text-right tabular-nums">{row.planned > 0 ? fmtMoney(row.planned, currency) : <span className="text-slate-400">—</span>}</td>
      <td className="px-3 py-2 text-right tabular-nums">{row.actual > 0 ? fmtMoney(row.actual, currency) : <span className="text-slate-400">—</span>}</td>
      <td className={`px-3 py-2 text-right tabular-nums ${row.delta < 0 ? "text-red-600" : ""}`}>
        {row.capped || row.planned > 0 ? fmtMoney(row.delta, currency, { signed: true }) : <span className="text-slate-400">—</span>}
      </td>
    </tr>
  );
}

function BudgetBar({ row, currency }: { row: BudgetCategoryRow; currency: string }) {
  // Bar fills against cap when capped, otherwise against planned.
  const target = row.capped ? row.cap : row.planned;
  const pct = target > 0 ? Math.min(100, (row.actual / target) * 100) : 0;
  const over = row.capped && row.actual > row.cap;
  const barColor = over ? "bg-red-500" : pct >= 75 ? "bg-amber-500" : "bg-emerald-500";
  return (
    <li>
      <div className="mb-1 flex items-center justify-between text-sm">
        <span>{BUDGET_LABEL[row.category]}</span>
        <span className="tabular-nums text-xs text-slate-500">
          {fmtMoney(row.actual, currency)} / {fmtMoney(target, currency)}
        </span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
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
    <div className="rounded-lg border border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900">
      <div className="flex items-center gap-2 border-b border-slate-100 p-3 dark:border-slate-800">
        <input
          value={label}
          onChange={e => setLabel(e.target.value)}
          onKeyDown={e => { if (e.key === "Enter") add(); }}
          placeholder="Add a packing item or errand"
          className="flex-1 rounded border border-slate-200 px-2 py-1 text-sm dark:border-slate-700 dark:bg-slate-900"
        />
        <button onClick={add} className="rounded bg-slate-900 px-3 py-1 text-sm text-white hover:bg-slate-800 dark:bg-slate-100 dark:text-slate-900 dark:hover:bg-white">Add</button>
      </div>
      {data.todos.length === 0 ? (
        <EmptyState message="No todos yet." />
      ) : (
        <ul className="divide-y divide-slate-100 dark:divide-slate-800 text-sm">
          {data.todos.map(t => (
            <li key={t.id} className="flex items-center gap-3 px-3 py-2">
              <button
                onClick={() => toggle(t.id)}
                className={`flex h-5 w-5 items-center justify-center rounded border ${t.done ? "border-emerald-500 bg-emerald-500 text-white" : "border-slate-300 dark:border-slate-600"}`}
              >
                {t.done && <Icon name="check" size={12} />}
              </button>
              <span className={`flex-1 ${t.done ? "text-slate-400 line-through" : ""}`}>{t.label}</span>
              {t.due_at && <span className="text-xs text-slate-500">{fmtDateShort(t.due_at)}</span>}
              <button onClick={() => remove(t.id)} className="text-slate-400 hover:text-red-600">
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
      {err && <p className="text-sm text-red-600">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
        <button onClick={submit} disabled={busy || !name} className="btn-dialog-primary">{busy ? "Creating…" : "Create"}</button>
      </DialogActions>
    </Dialog>
  );
}

function AddItemDialog({ kind, trip, onClose, onCreated }: {
  kind: "transport" | "accommodation" | "activity";
  trip: Trip;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  // Shared
  const [name, setName] = useState("");
  const [cost, setCost] = useState("");
  const [currency, setCurrency] = useState(trip.home_currency);
  const [notes, setNotes] = useState("");

  // Transport
  const [tKind, setTKind] = useState<TransportLeg["kind"]>("flight");
  const [provider, setProvider] = useState("");
  const [reference, setReference] = useState("");
  const [departAt, setDepartAt] = useState(trip.start_at.slice(0, 16));
  const [arriveAt, setArriveAt] = useState(trip.start_at.slice(0, 16));
  const [departLoc, setDepartLoc] = useState("");
  const [arriveLoc, setArriveLoc] = useState("");

  // Accommodation
  const [aKind, setAKind] = useState<Accommodation["kind"]>("hotel");
  const [address, setAddress] = useState("");
  const [checkIn, setCheckIn] = useState(trip.start_at.slice(0, 10));
  const [checkOut, setCheckOut] = useState(trip.end_at.slice(0, 10));

  // Activity
  const [actCategory, setActCategory] = useState<Activity["category"]>("activity");
  const [actStart, setActStart] = useState("");
  const [actLocation, setActLocation] = useState("");

  const submit = async () => {
    setBusy(true); setErr("");
    try {
      const cents = cost.trim() ? parseMoneyDecimal(cost) : undefined;
      if (kind === "transport") {
        await api<TransportLeg>("/transport-legs", {
          method: "POST",
          body: JSON.stringify({
            trip_id: trip.id, kind: tKind,
            depart_at: ensureRfc3339(departAt), arrive_at: ensureRfc3339(arriveAt),
            provider, reference, depart_location: departLoc, arrive_location: arriveLoc,
            cost_estimated: cents, currency, notes,
          }),
        });
      } else if (kind === "accommodation") {
        await api<Accommodation>("/accommodations", {
          method: "POST",
          body: JSON.stringify({
            trip_id: trip.id, name, kind: aKind, address,
            check_in_at: checkIn + "T15:00:00Z", check_out_at: checkOut + "T11:00:00Z",
            cost_estimated: cents, currency, notes,
          }),
        });
      } else {
        await api<Activity>("/activities", {
          method: "POST",
          body: JSON.stringify({
            trip_id: trip.id, name, category: actCategory,
            start_at: actStart ? ensureRfc3339(actStart) : undefined,
            location: actLocation, cost_estimated: cents, currency, notes,
          }),
        });
      }
      onCreated();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Dialog title={kind === "transport" ? "Add transport" : kind === "accommodation" ? "Add accommodation" : "Add activity"} onClose={onClose}>
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
      {err && <p className="text-sm text-red-600">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
        <button onClick={submit} disabled={busy} className="btn-dialog-primary">{busy ? "Saving…" : "Save"}</button>
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
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-5 shadow-xl dark:border-slate-700 dark:bg-slate-900">
        <header className="mb-3 flex items-center justify-between">
          <h3 className="text-base font-semibold">{title}</h3>
          <button onClick={onClose} className="text-slate-400 hover:text-slate-700 dark:hover:text-white"><Icon name="x" size={18} /></button>
        </header>
        <div className="space-y-3">{children}</div>
        <style>{`
          .input { width: 100%; padding: 0.5rem 0.75rem; border-radius: 0.375rem; border: 1px solid rgb(203 213 225); background: white; color: inherit; }
          .dark .input { border-color: rgb(51 65 85); background: rgb(15 23 42); }
          .input:focus { outline: 2px solid rgb(37 99 235); outline-offset: -1px; }
          .btn-dialog-primary { padding: 0.5rem 1rem; border-radius: 0.375rem; background: rgb(15 23 42); color: white; font-size: 0.875rem; }
          .btn-dialog-primary:disabled { opacity: 0.5; }
          .btn-dialog-primary:hover:not(:disabled) { background: rgb(30 41 59); }
          .btn-dialog-secondary { padding: 0.5rem 1rem; border-radius: 0.375rem; background: transparent; color: inherit; font-size: 0.875rem; border: 1px solid rgb(203 213 225); }
          .dark .btn-dialog-secondary { border-color: rgb(51 65 85); }
        `}</style>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs font-medium uppercase tracking-wide text-slate-500">{label}</span>
      {children}
    </label>
  );
}

function DialogActions({ children }: { children: React.ReactNode }) {
  return <div className="mt-4 flex justify-end gap-2">{children}</div>;
}

function EmptyState({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-8 text-center text-sm text-slate-500">
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
