// TripsPanel — plan + budget trips end-to-end. Calendar mirrored.
//
// Two screens:
//   List view   — chronological trip timeline, "+ New trip" CTA
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
  status: "idea" | "planning" | "booked" | "in_progress" | "done" | "cancelled";
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

interface Settings {
  project_id: string;
  home_airport: string;
  default_passengers: number;
  duffel_connection_id?: number;
  places_connection_id?: number;
  daily_search_budget_cents: number;
}

interface AvailableConnection {
  id: number;
  provider: string;
  name: string;
  status: string;
}

interface PlaceResult {
  place_id: string;
  name: string;
  formatted_address?: string;
  country?: string;
  lat?: number;
  lng?: number;
  rating?: number;
  user_rating_count?: number;
  price_level?: string;
  primary_type?: string;
  google_maps_uri?: string;
}

interface AirportResult {
  id?: string;
  iata_code: string;
  name: string;
  city_name?: string;
  country_code?: string;
  country_name?: string;
}

interface FlightOffer {
  offer_id: string;
  carrier: string;
  carrier_code: string;
  number: string;
  depart_at: string;
  arrive_at: string;
  duration?: string;
  depart_location: string;
  arrive_location: string;
  stops: number;
  cabin?: string;
  total_amount_cents: number;
  currency: string;
}

interface TravelPriceObservation {
  id: number;
  trip_id?: number;
  kind: string;
  provider: string;
  origin_code?: string;
  destination_code?: string;
  depart_date?: string;
  return_date?: string;
  party_size: number;
  cabin_or_class?: string;
  provider_name?: string;
  item_name?: string;
  stops_or_transfers?: number;
  duration?: string;
  amount_cents: number;
  currency: string;
  observed_at: string;
  live_until?: string;
  bookable_ref?: string;
}

interface TravelPriceRouteSummary {
  kind: string;
  origin_code?: string;
  destination_code?: string;
  currency: string;
  party_size: number;
  cabin_or_class?: string;
  observation_count: number;
  lowest_amount_cents: number;
  latest_amount_cents: number;
  cheapest_depart_date?: string;
  cheapest_return_date?: string;
  cheapest_provider_name?: string;
  cheapest_item_name?: string;
  cheapest_observed_at?: string;
  latest_observed_at?: string;
  first_observed_at?: string;
}

interface TravelPriceStats {
  quote_count: number;
  search_count: number;
  route_count: number;
  route_date_count: number;
}

interface TravelPriceRouteDate {
  kind: string;
  origin_code?: string;
  destination_code?: string;
  depart_date?: string;
  return_date?: string;
  currency: string;
  party_size: number;
  cabin_or_class?: string;
  quote_count: number;
  search_count: number;
  lowest_amount_cents: number;
  latest_observed_at?: string;
}

interface TravelPriceResponse {
  observations: TravelPriceObservation[];
  routes: TravelPriceRouteSummary[];
  route_dates?: TravelPriceRouteDate[];
  stats?: TravelPriceStats;
}

interface FlightPriceScanResponse {
  searched: number;
  results: {
    destination: string;
    depart_date: string;
    return_date?: string;
    cached: boolean;
    offers: number;
    error?: string;
  }[];
  prices?: TravelPriceResponse;
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

type Tab = "overview" | "itinerary" | "deals" | "budget" | "todos";
type TripListTab = "upcoming" | "past" | "ideas";

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

function relativeTime(s?: string): string {
  if (!s) return "";
  const t = new Date(s).getTime();
  if (!Number.isFinite(t)) return "";
  const diff = Date.now() - t;
  const minute = 60 * 1000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (diff < minute) return "just now";
  if (diff < hour) return `${Math.floor(diff / minute)}m ago`;
  if (diff < day) return `${Math.floor(diff / hour)}h ago`;
  return `${Math.floor(diff / day)}d ago`;
}

function todayPlus(days: number): string {
  const d = new Date();
  d.setDate(d.getDate() + days);
  return toYMD(d);
}

function daysUntil(startAt: string, endAt: string): { label: string; tone: "future" | "active" | "past" } {
  if (!startAt || !endAt) return { label: "Idea", tone: "future" };
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
  idea: "Idea",
  planning: "Planning",
  booked: "Booked",
  in_progress: "In progress",
  done: "Done",
  cancelled: "Cancelled",
};

function hasDateRange(start?: string, end?: string): boolean {
  return Boolean(start && end);
}

function isTripIdea(trip: Trip): boolean {
  return trip.status === "idea" || !hasDateRange(trip.start_at, trip.end_at);
}

function tripStartMs(trip: Trip): number {
  const t = new Date(trip.start_at).getTime();
  return Number.isFinite(t) ? t : Number.MAX_SAFE_INTEGER;
}

function tripEndMs(trip: Trip): number {
  const t = new Date(trip.end_at).getTime();
  return Number.isFinite(t) ? t : 0;
}

function isPastTrip(trip: Trip): boolean {
  if (isTripIdea(trip)) return false;
  if (trip.status === "done" || trip.status === "cancelled") return true;
  return tripEndMs(trip) < Date.now();
}

function tripDateLabel(trip: Trip): string {
  if (!hasDateRange(trip.start_at, trip.end_at)) return "Dates not set";
  return `${fmtDate(trip.start_at)} – ${fmtDate(trip.end_at)}`;
}

function tripShortDateLabel(trip: Trip): string {
  if (!hasDateRange(trip.start_at, trip.end_at)) return "Idea";
  return `${fmtDateShort(trip.start_at)} – ${fmtDateShort(trip.end_at)}`;
}

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
    case "settings":
      return <svg {...common}><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.01a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>;
    case "search":
      return <svg {...common}><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>;
    case "star":
      return <svg {...common}><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>;
    case "external":
      return <svg {...common}><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>;
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
  const [settings, setSettings] = useState<Settings | null>(null);
  const [mainTab, setMainTab] = useState<"trips" | "deals">("trips");
  const [tripListTab, setTripListTab] = useState<TripListTab>("upcoming");
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [showSettings, setShowSettings] = useState(false);
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

  const refreshSettings = useCallback(async () => {
    try {
      const s = await api<Settings>("/settings");
      setSettings(s);
    } catch {
      setSettings(null);
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);
  useEffect(() => { refreshSettings(); }, [refreshSettings]);
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
          <nav className="flex overflow-hidden rounded-md border border-border text-sm">
            <button
              type="button"
              onClick={() => setMainTab("trips")}
              className={`px-3 py-1.5 ${mainTab === "trips" ? "bg-accent text-bg" : "hover:bg-bg-hover"}`}
            >
              Trips
            </button>
            {settings?.duffel_connection_id && (
              <button
                type="button"
                onClick={() => setMainTab("deals")}
                className={`px-3 py-1.5 ${mainTab === "deals" ? "bg-accent text-bg" : "hover:bg-bg-hover"}`}
              >
                Deals
              </button>
            )}
          </nav>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setShowSettings(true)}
            title="Search providers + defaults"
            className="p-1.5 text-text-muted hover:text-text"
          >
            <Icon name="settings" size={16} />
          </button>
          <button
            onClick={() => setShowNew(true)}
            className="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm text-bg hover:bg-accent-hover"
          >
            <Icon name="plus" size={14} /> New trip
          </button>
        </div>
      </header>

      {error && (
        <div className="rounded-md border border-error/30 bg-error/10 px-3 py-2 text-sm text-error">
          {error}
        </div>
      )}

      <div className="flex-1 overflow-auto">
        <div className="flex flex-col gap-3">
          {mainTab === "deals" && settings?.duffel_connection_id ? (
            <GlobalDealsPanel settings={settings} />
          ) : (
            trips.length === 0 ? (
              <EmptyState message="No trips yet. Click 'New trip' to plan one." />
            ) : (
              <TripTimelineList
                trips={trips}
                activeTab={tripListTab}
                onTabChange={setTripListTab}
                onOpen={setSelectedID}
              />
            )
          )}
        </div>
      </div>

      {showNew && (
        <NewTripDialog
          onClose={() => setShowNew(false)}
          onCreated={async (id) => { setShowNew(false); await refresh(); setSelectedID(id); }}
        />
      )}
      {showSettings && (
        <SettingsDialog onClose={() => setShowSettings(false)} />
      )}
    </div>
  );
}

// ─── List view ───────────────────────────────────────────────────

function TripTimelineList({
  trips,
  activeTab,
  onTabChange,
  onOpen,
}: {
  trips: Trip[];
  activeTab: TripListTab;
  onTabChange: (tab: TripListTab) => void;
  onOpen: (id: number) => void;
}) {
  const buckets = useMemo(() => {
    const upcoming = trips
      .filter(t => !isTripIdea(t) && !isPastTrip(t))
      .sort((a, b) => tripStartMs(a) - tripStartMs(b));
    const past = trips
      .filter(t => !isTripIdea(t) && isPastTrip(t))
      .sort((a, b) => tripEndMs(b) - tripEndMs(a));
    const ideas = trips
      .filter(isTripIdea)
      .sort((a, b) => b.id - a.id);
    return { upcoming, past, ideas };
  }, [trips]);

  const tabs: { id: TripListTab; label: string; count: number }[] = [
    { id: "upcoming", label: "Upcoming", count: buckets.upcoming.length },
    { id: "past", label: "Past", count: buckets.past.length },
    { id: "ideas", label: "Ideas", count: buckets.ideas.length },
  ];
  const visible = buckets[activeTab];
  const empty: Record<TripListTab, string> = {
    upcoming: "No upcoming trips yet.",
    past: "No past trips yet.",
    ideas: "No trip ideas yet.",
  };

  return (
    <section className="flex min-h-0 flex-col rounded-lg border border-border bg-bg-card">
      <div className="flex flex-col gap-3 border-b border-border px-4 py-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div className="text-sm font-semibold">Trips timeline</div>
        </div>
        <div className="flex overflow-hidden rounded-md border border-border text-sm">
          {tabs.map(tab => (
            <button
              key={tab.id}
              type="button"
              onClick={() => onTabChange(tab.id)}
              className={`px-3 py-1.5 ${activeTab === tab.id ? "bg-accent text-bg" : "hover:bg-bg-hover"}`}
            >
              {tab.label} <span className={activeTab === tab.id ? "text-bg/70" : "text-text-dim"}>{tab.count}</span>
            </button>
          ))}
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {visible.length === 0 ? (
          <EmptyState message={empty[activeTab]} />
        ) : (
          <div className="divide-y divide-border">
            {visible.map(trip => (
              <TripTimelineRow
                key={trip.id}
                trip={trip}
                mode={activeTab}
                onOpen={() => onOpen(trip.id)}
              />
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

function TripTimelineRow({ trip, mode, onOpen }: { trip: Trip; mode: TripListTab; onOpen: () => void }) {
  const days = daysUntil(trip.start_at, trip.end_at);
  const planned = trip.total_planned ?? 0;
  const actual = trip.total_actual ?? 0;
  const estimate = planned > 0 ? planned : (trip.total_budget ?? 0);
  const over = planned > 0 && actual > planned;
  const idea = mode === "ideas";
  const statusTone =
    trip.status === "done" ? "bg-success/15 text-success"
    : trip.status === "cancelled" ? "bg-error/15 text-error"
    : idea ? "bg-accent/15 text-accent"
    : days.tone === "active" ? "bg-success/15 text-success"
    : "bg-bg-hover text-text-muted";

  return (
    <button
      type="button"
      onClick={onOpen}
      className="grid w-full grid-cols-[5rem_minmax(0,1fr)] gap-3 px-4 py-3 text-left transition hover:bg-bg-hover md:grid-cols-[7rem_minmax(0,1fr)_12rem]"
    >
      <div className="flex items-start gap-3">
        <span className="mt-1 h-10 w-1 rounded-full" style={{ background: trip.color }} />
        <div className="min-w-0">
          <div className="text-xs font-medium uppercase tracking-wide text-text-muted">
            {idea ? "Idea" : fmtDateShort(trip.start_at)}
          </div>
          {!idea && (
            <div className="mt-1 text-xs text-text-dim">
              {fmtDateShort(trip.end_at)}
            </div>
          )}
        </div>
      </div>

      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h3 className="truncate text-base font-semibold text-text">{trip.name}</h3>
          <span className={`rounded-full px-2 py-0.5 text-xs ${statusTone}`}>{idea ? "Idea" : days.label}</span>
          <span className="text-xs text-text-muted">{STATUS_LABEL[trip.status]}</span>
        </div>
        <div className="mt-1 text-sm text-text-muted">
          {idea ? (trip.notes || "No dates set") : tripDateLabel(trip)}
        </div>
      </div>

      <div className="col-span-2 flex items-center justify-between gap-3 text-sm md:col-span-1 md:block md:text-right">
        {idea ? (
          estimate > 0 ? (
            <>
              <span className="text-xs uppercase tracking-wide text-text-muted md:block">Estimate</span>
              <span className="tabular-nums text-text">{fmtMoney(estimate, trip.home_currency)}</span>
            </>
          ) : (
            <span className="text-text-dim">No estimate</span>
          )
        ) : (
          <>
            <span className="text-xs uppercase tracking-wide text-text-muted md:block">Actual</span>
            <span className={`tabular-nums ${over ? "text-error" : "text-text"}`}>
              {fmtMoney(actual, trip.home_currency)}
              {planned > 0 && <span className="text-text-dim"> / {fmtMoney(planned, trip.home_currency)}</span>}
            </span>
          </>
        )}
      </div>
    </button>
  );
}

function GlobalDealsPanel({ settings }: { settings: Settings }) {
  const ui = useUI();
  const [data, setData] = useState<TravelPriceResponse | null>(null);
  const [origin, setOrigin] = useState(settings.home_airport || "");
  const [destinations, setDestinations] = useState("CDG, LIS, FCO, AMS, LHR");
  const [departFrom, setDepartFrom] = useState(todayPlus(14));
  const [departTo, setDepartTo] = useState(todayPlus(44));
  const [tripLengths, setTripLengths] = useState("3,5,7");
  const [passengers, setPassengers] = useState(settings.default_passengers || 1);
  const [cabin, setCabin] = useState("economy");
  const [maxSearches, setMaxSearches] = useState(18);
  const [busy, setBusy] = useState(false);
  const [scanSummary, setScanSummary] = useState("");
  const [destinationSearch, setDestinationSearch] = useState("");

  const load = useCallback(async () => {
    const params = new URLSearchParams({ kind: "flight", since_days: "180", limit: "800" });
    if (origin) params.set("origin", origin);
    const r = await api<TravelPriceResponse>(`/price-observations?${params}`);
    setData(r);
  }, [origin]);

  useEffect(() => {
    load().catch(() => setData(null));
  }, [load]);

  const scan = async () => {
    if (!origin || !destinations || !departFrom || !departTo) {
      ui.notify("origin, destinations, and date range required");
      return;
    }
    setBusy(true);
    setScanSummary("");
    try {
      const r = await api<FlightPriceScanResponse>("/search/flights/scan", {
        method: "POST",
        body: JSON.stringify({
          origin,
          destinations,
          depart_from: departFrom,
          depart_to: departTo,
          trip_lengths: tripLengths,
          passengers,
          cabin,
          max_searches: maxSearches,
        }),
      });
      if (r.prices) setData(r.prices);
      else await load();
      const failures = r.results.filter(x => x.error).length;
      setScanSummary(`${r.searched} search${r.searched === 1 ? "" : "es"} run${failures ? `, ${failures} failed` : ""}`);
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const routes = data?.routes ?? [];
  const observations = data?.observations ?? [];
  const routeDates = data?.route_dates ?? [];
  const stats = data?.stats;
  const quoteCount = stats?.quote_count ?? observations.length;
  const addDestination = (airport: AirportResult) => {
    const code = airport.iata_code.toUpperCase();
    const codes = destinations
      .split(/[\s,;]+/)
      .map(x => x.trim().toUpperCase())
      .filter(Boolean);
    if (!codes.includes(code)) codes.push(code);
    setDestinations(codes.join(", "));
    setDestinationSearch("");
  };

  return (
    <section className="rounded-lg border border-border bg-bg-card p-4">
      <div className="mb-4 flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <div className="flex items-center gap-2 text-base font-semibold">
            <Icon name="plane" size={18} />
            <span>Flight deals</span>
          </div>
          <div className="mt-1 text-sm text-text-muted">
            {quoteCount} quote{quoteCount === 1 ? "" : "s"} from {stats?.search_count ?? "—"} search{stats?.search_count === 1 ? "" : "es"}, {stats?.route_count ?? routes.length} route{(stats?.route_count ?? routes.length) === 1 ? "" : "s"}, {stats?.route_date_count ?? routeDates.length} date option{(stats?.route_date_count ?? routeDates.length) === 1 ? "" : "s"}.
            {scanSummary && <span className="ml-2 text-text">{scanSummary}.</span>}
          </div>
        </div>
        <button
          type="button"
          onClick={scan}
          disabled={busy}
          className="inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md border border-border bg-accent px-3 py-1.5 text-sm font-medium text-bg hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60"
        >
          <Icon name="search" size={14} /> {busy ? "Scanning…" : "Scan prices"}
        </button>
      </div>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-12">
        <div className="space-y-3 lg:col-span-4">
          <div className="grid grid-cols-2 gap-3">
            <Field label="From">
              <AirportCodeInput value={origin} onChange={setOrigin} placeholder="BCN" />
            </Field>
            <Field label="Max calls">
              <input type="number" min={1} max={60} value={maxSearches} onChange={e => setMaxSearches(Math.max(1, Math.min(60, parseInt(e.target.value || "1", 10))))} className="input" />
            </Field>
          </div>
          <Field label="Destinations">
            <input value={destinations} onChange={e => setDestinations(e.target.value.toUpperCase())} className="input uppercase" placeholder="CDG, LIS, FCO" />
          </Field>
          <Field label="Add destination">
            <AirportCodeInput
              value={destinationSearch}
              onChange={setDestinationSearch}
              onPick={addDestination}
              placeholder="Search city or airport"
            />
          </Field>
          <DateRangeField
            startLabel="Depart from"
            endLabel="Depart to"
            start={departFrom}
            end={departTo}
            onChange={(s, e) => { setDepartFrom(s); setDepartTo(e); }}
            minDate={todayPlus(0)}
          />
          <div className="grid grid-cols-2 gap-3">
            <Field label="Trip lengths (days)">
              <input value={tripLengths} onChange={e => setTripLengths(e.target.value)} className="input" placeholder="3,5,7" />
              <div className="mt-1 text-xs text-text-muted">Return after these many days. Example: 3,5,7 checks weekend, midweek, and week-long trips.</div>
            </Field>
            <Field label="Passengers">
              <input type="number" min={1} value={passengers} onChange={e => setPassengers(Math.max(1, parseInt(e.target.value || "1", 10)))} className="input" />
            </Field>
          </div>
          <Field label="Cabin">
            <select value={cabin} onChange={e => setCabin(e.target.value)} className="input">
              <option value="economy">Economy</option>
              <option value="premium_economy">Premium economy</option>
              <option value="business">Business</option>
              <option value="first">First</option>
            </select>
          </Field>
        </div>

        <div className="space-y-3 lg:col-span-8">
          {routes.length > 0 ? (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
              {routes.slice(0, 4).map(route => (
                <FlightDealCard key={`${route.origin_code}-${route.destination_code}-${route.currency}-${route.party_size}-${route.cabin_or_class}`} route={route} />
              ))}
            </div>
          ) : (
            <div className="rounded-lg border border-border-subtle">
              <EmptyState message="No observed deals yet — run a scan to seed prices." />
            </div>
          )}
          <FlightHeatmap routeDates={routeDates} />
        </div>
      </div>
    </section>
  );
}

function FlightHeatmap({ routeDates }: { routeDates: TravelPriceRouteDate[] }) {
  const byCell = new Map<string, TravelPriceRouteDate>();
  for (const o of routeDates) {
    if (!o.destination_code || !o.depart_date) continue;
    const key = `${o.destination_code}|${o.depart_date}`;
    const prev = byCell.get(key);
    if (!prev || o.lowest_amount_cents < prev.lowest_amount_cents) byCell.set(key, o);
  }
  const destinations = Array.from(new Set(routeDates.map(o => o.destination_code).filter(Boolean) as string[]))
    .sort((a, b) => {
      const mina = Math.min(...routeDates.filter(o => o.destination_code === a).map(o => o.lowest_amount_cents));
      const minb = Math.min(...routeDates.filter(o => o.destination_code === b).map(o => o.lowest_amount_cents));
      return mina - minb;
    })
    .slice(0, 6);
  const dates = Array.from(new Set(routeDates.map(o => o.depart_date).filter(Boolean) as string[]))
    .sort()
    .slice(0, 31);
  const amounts = Array.from(byCell.values()).map(o => o.lowest_amount_cents);
  const min = amounts.length ? Math.min(...amounts) : 0;
  const max = amounts.length ? Math.max(...amounts) : 0;

  if (destinations.length === 0 || dates.length === 0) return null;

  const tone = (amount: number) => {
    if (max <= min) return "bg-success/50";
    const ratio = (amount - min) / (max - min);
    if (ratio < 0.34) return "bg-success/60 text-bg";
    if (ratio < 0.67) return "bg-warn/50";
    return "bg-error/40";
  };

  return (
    <div className="rounded-lg border border-border-subtle p-3">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <div className="text-xs uppercase tracking-wide text-text-muted">When to go</div>
          <div className="mt-1 text-xs text-text-muted">Calendar heatmap from grouped route/date options, not raw offer count.</div>
        </div>
        <div className="text-xs text-text-muted">lowest observed per route/date</div>
      </div>
      <div className="overflow-auto">
        <div className="grid min-w-[680px] gap-1" style={{ gridTemplateColumns: `72px repeat(${dates.length}, minmax(38px, 1fr))` }}>
          <div />
          {dates.map(d => <div key={d} className="truncate text-center text-[11px] text-text-muted">{d.slice(5)}</div>)}
          {destinations.map(dest => (
            <div key={dest} className="contents">
              <div className="flex h-9 items-center text-xs font-medium">{dest}</div>
              {dates.map(date => {
                const o = byCell.get(`${dest}|${date}`);
                return (
                  <div
                    key={`${dest}-${date}`}
                    title={o ? `${dest} ${date}${o.return_date ? ` to ${o.return_date}` : ""}: ${fmtMoney(o.lowest_amount_cents, o.currency)} from ${o.quote_count} quotes` : `${dest} ${date}: no data`}
                    className={`flex h-9 items-center justify-center rounded text-[11px] tabular-nums ${o ? tone(o.lowest_amount_cents) : "bg-bg-hover text-text-dim"}`}
                  >
                    {o ? fmtMoney(o.lowest_amount_cents, o.currency).replace(/\D00$/, "") : "—"}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// OverallBudgetBar aggregates planned + actual across the visible trips,
// grouped by home_currency (no FX in v0.2, so we don't mix currencies).
// Single-currency users see one tidy line; multi-currency users see one
// per currency.
function OverallBudgetBar({ trips }: { trips: Trip[] }) {
  const totals = useMemo(() => {
    const byCcy: Record<string, { planned: number; actual: number; ideaEstimate: number; count: number; ideaCount: number }> = {};
    for (const t of trips) {
      if (t.status === "cancelled") continue;
      const ccy = t.home_currency || "EUR";
      const row = byCcy[ccy] ?? { planned: 0, actual: 0, ideaEstimate: 0, count: 0, ideaCount: 0 };
      if (isTripIdea(t)) {
        row.ideaEstimate += (t.total_planned ?? 0) || (t.total_budget ?? 0);
        row.actual += t.total_actual ?? 0;
        row.ideaCount += 1;
      } else {
        row.planned += t.total_planned ?? 0;
        row.actual += t.total_actual ?? 0;
      }
      row.count += 1;
      byCcy[ccy] = row;
    }
    return Object.entries(byCcy)
      .filter(([, r]) => r.planned > 0 || r.actual > 0 || r.ideaEstimate > 0)
      .sort(([, a], [, b]) => (b.planned + b.ideaEstimate) - (a.planned + a.ideaEstimate));
  }, [trips]);

  if (totals.length === 0) return null;
  return (
    <section className="rounded-lg border border-border bg-bg-card p-4">
      <div className="mb-2 text-xs uppercase tracking-wide text-text-muted">
        Across {trips.filter(t => t.status !== "cancelled").length} trip{trips.length === 1 ? "" : "s"}
      </div>
      <div className="flex flex-col gap-3 sm:flex-row sm:gap-6">
        {totals.map(([ccy, r]) => {
          const hasScheduledBudget = r.planned > 0 || r.actual > 0;
          const target = r.planned > 0 ? r.planned : r.actual;
          const pct = target > 0 ? Math.min(100, (r.actual / target) * 100) : 0;
          const over = r.planned > 0 && r.actual > r.planned;
          const barColor = over ? "bg-error" : "bg-success";
          return (
            <div key={ccy} className="flex-1 min-w-0">
              <div className="flex items-baseline justify-between">
                <span className="text-xs text-text-muted">{hasScheduledBudget ? ccy : `${ccy} estimate`}</span>
                <span className={`text-sm tabular-nums ${over ? "text-error" : "text-text"}`}>
                  {hasScheduledBudget ? fmtMoney(r.actual, ccy) : fmtMoney(r.ideaEstimate, ccy)}
                  {hasScheduledBudget && r.planned > 0 && <span className="text-text-dim"> / {fmtMoney(r.planned, ccy)}</span>}
                </span>
              </div>
              {hasScheduledBudget && r.planned > 0 && (
                <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-bg-hover">
                  <div className={`h-full ${barColor}`} style={{ width: `${pct}%` }} />
                </div>
              )}
              {hasScheduledBudget && r.ideaEstimate > 0 && (
                <div className="mt-1 flex items-center justify-between text-xs text-text-muted">
                  <span>Idea estimate{r.ideaCount > 1 ? "s" : ""}</span>
                  <span className="tabular-nums text-text">{fmtMoney(r.ideaEstimate, ccy)}</span>
                </div>
              )}
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
  const isIdea = trip.status === "idea" || !hasDateRange(trip.start_at, trip.end_at);

  const makeIdea = async () => {
    try {
      await api<Trip>(`/trips/${trip.id}`, {
        method: "PATCH",
        body: JSON.stringify({ start_at: null, end_at: null, status: "idea" }),
      });
      await refresh();
      onChanged();
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div className="flex h-full flex-col gap-3 p-4">
      <header className="flex items-center justify-between">
        <button onClick={onBack} className="flex items-center gap-1 text-sm text-text-muted hover:text-text">
          <Icon name="chevron-left" size={14} /> Trips
        </button>
        <div className="flex items-center gap-2">
          <button
            onClick={() => isIdea ? setShowEdit(true) : makeIdea()}
            className="rounded-md border border-border px-2.5 py-1 text-sm text-text-muted hover:border-accent hover:text-text"
          >
            {isIdea ? "Schedule" : "Make idea"}
          </button>
          <button onClick={() => setShowEdit(true)} title="Edit trip" className="p-1 text-text-muted hover:text-text">
            <Icon name="edit" size={14} />
          </button>
          <button onClick={deleteTrip} title="Delete trip" className="p-1 text-text-muted hover:text-error">
            <Icon name="trash" size={14} />
          </button>
          <nav className="flex rounded-md border border-border overflow-hidden text-sm">
            {(["overview", "itinerary", "deals", "budget", "todos"] as Tab[]).map(t => (
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
              {tripDateLabel(trip)} • {days.label}
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
        {tab === "deals" && <DealsTab trip={trip} />}
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
    if (!l.depart_at) continue;
    items.push({
      kind: "transport",
      when: l.depart_at,
      title: `${l.provider} ${l.reference}`.trim() || l.kind,
      subtitle: `${l.depart_location || "—"} → ${l.arrive_location || "—"}`,
    });
  }
  for (const a of data.accommodations) {
    if (!a.check_in_at) continue;
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

// ─── Deals tab ───────────────────────────────────────────────────

function DealsTab({ trip }: { trip: Trip }) {
  const ui = useUI();
  const [scope, setScope] = useState<"trip" | "all">("trip");
  const [data, setData] = useState<TravelPriceResponse | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const params = new URLSearchParams({ kind: "flight", since_days: "180", limit: "500" });
      if (scope === "trip") params.set("trip_id", String(trip.id));
      const r = await api<TravelPriceResponse>(`/price-observations?${params}`);
      setData(r);
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [scope, trip.id, ui]);

  useEffect(() => { load(); }, [load]);

  const routes = data?.routes ?? [];
  const observations = data?.observations ?? [];
  const best = routes[0];

  return (
    <div className="space-y-3">
      <section className="rounded-lg border border-border bg-bg-card p-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <div className="text-xs uppercase tracking-wide text-text-muted">Flight price intelligence</div>
            <div className="mt-1 text-sm text-text-muted">
              {busy ? "Loading observed prices…" : `${observations.length} observed fare${observations.length === 1 ? "" : "s"} from the last 180 days`}
            </div>
          </div>
          <div className="flex overflow-hidden rounded-md border border-border text-sm">
            <button
              type="button"
              onClick={() => setScope("trip")}
              className={`px-3 py-1.5 ${scope === "trip" ? "bg-accent text-bg" : "hover:bg-bg-hover"}`}
            >
              This trip
            </button>
            <button
              type="button"
              onClick={() => setScope("all")}
              className={`px-3 py-1.5 ${scope === "all" ? "bg-accent text-bg" : "hover:bg-bg-hover"}`}
            >
              All
            </button>
          </div>
        </div>
      </section>

      {best && (
        <section className="rounded-lg border border-border bg-bg-card p-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
            <div>
              <div className="mb-1 text-xs uppercase tracking-wide text-text-muted">Cheapest observed route</div>
              <div className="flex items-center gap-2 text-lg font-semibold">
                <Icon name="plane" size={18} />
                <span>{best.origin_code || "—"} → {best.destination_code || "—"}</span>
              </div>
              <div className="mt-1 text-sm text-text-muted">
                {best.cheapest_provider_name || "Flight"} {best.cheapest_item_name ? `• ${best.cheapest_item_name}` : ""}
                {best.cheapest_depart_date ? ` • ${fmtDateShort(best.cheapest_depart_date)}` : ""}
                {best.cheapest_return_date ? ` – ${fmtDateShort(best.cheapest_return_date)}` : ""}
              </div>
            </div>
            <div className="text-left sm:text-right">
              <div className="text-2xl font-semibold tabular-nums">{fmtMoney(best.lowest_amount_cents, best.currency)}</div>
              <div className="text-xs text-text-muted">
                latest {fmtMoney(best.latest_amount_cents, best.currency)} • seen {relativeTime(best.cheapest_observed_at)}
              </div>
            </div>
          </div>
        </section>
      )}

      {routes.length > 0 ? (
        <section className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          {routes.slice(0, 8).map(route => (
            <FlightDealCard key={`${route.origin_code}-${route.destination_code}-${route.currency}-${route.party_size}-${route.cabin_or_class}`} route={route} />
          ))}
        </section>
      ) : (
        <section className="rounded-lg border border-border bg-bg-card">
          <EmptyState message="No observed flight prices yet." />
        </section>
      )}

      {observations.length > 0 && (
        <section className="rounded-lg border border-border bg-bg-card p-4">
          <div className="mb-3 text-xs uppercase tracking-wide text-text-muted">Recent observations</div>
          <div className="overflow-auto">
            <table className="w-full text-left text-sm">
              <thead className="text-xs uppercase tracking-wide text-text-muted">
                <tr>
                  <th className="pb-2 font-medium">Route</th>
                  <th className="pb-2 font-medium">Date</th>
                  <th className="pb-2 font-medium">Carrier</th>
                  <th className="pb-2 text-right font-medium">Price</th>
                  <th className="pb-2 text-right font-medium">Seen</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-subtle">
                {observations.slice(0, 12).map(o => (
                  <tr key={o.id}>
                    <td className="py-2 tabular-nums">{o.origin_code || "—"} → {o.destination_code || "—"}</td>
                    <td className="py-2 text-text-muted">
                      {o.depart_date ? fmtDateShort(o.depart_date) : "—"}
                      {o.return_date ? ` – ${fmtDateShort(o.return_date)}` : ""}
                    </td>
                    <td className="py-2 text-text-muted">{o.provider_name || o.item_name || o.provider}</td>
                    <td className="py-2 text-right tabular-nums">{fmtMoney(o.amount_cents, o.currency)}</td>
                    <td className="py-2 text-right text-text-muted">{relativeTime(o.observed_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}

function FlightDealCard({ route }: { route: TravelPriceRouteSummary }) {
  const delta = route.latest_amount_cents - route.lowest_amount_cents;
  const liveVsLow = delta === 0 ? "matches low" : `${delta > 0 ? "+" : ""}${fmtMoney(delta, route.currency)} vs low`;
  return (
    <article className="rounded-lg border border-border bg-bg-card p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2 font-medium">
            <Icon name="plane" size={16} />
            <span className="tabular-nums">{route.origin_code || "—"} → {route.destination_code || "—"}</span>
          </div>
          <div className="mt-1 text-xs text-text-muted">
            {route.party_size} pax
            {route.cabin_or_class ? ` • ${route.cabin_or_class.replace("_", " ")}` : ""}
            {route.observation_count ? ` • ${route.observation_count} quotes` : ""}
          </div>
        </div>
        <div className="text-right">
          <div className="text-lg font-semibold tabular-nums">{fmtMoney(route.lowest_amount_cents, route.currency)}</div>
          <div className="text-xs text-text-muted">{liveVsLow}</div>
        </div>
      </div>
      <div className="mt-3 grid grid-cols-2 gap-3 text-xs">
        <div>
          <div className="uppercase tracking-wide text-text-dim">Cheapest</div>
          <div className="mt-1 text-text-muted">
            {route.cheapest_depart_date ? fmtDateShort(route.cheapest_depart_date) : "—"}
            {route.cheapest_return_date ? ` – ${fmtDateShort(route.cheapest_return_date)}` : ""}
          </div>
        </div>
        <div className="text-right">
          <div className="uppercase tracking-wide text-text-dim">Observed</div>
          <div className="mt-1 text-text-muted">{relativeTime(route.cheapest_observed_at || route.latest_observed_at)}</div>
        </div>
      </div>
    </article>
  );
}

// ─── Itinerary tab ───────────────────────────────────────────────

type ItemKind = "transport" | "accommodation" | "activity";
type ItemData = TransportLeg | Accommodation | Activity;

function ItineraryTab({ data, onChanged }: { data: TripDashboard; onChanged: () => void }) {
  const [showAdd, setShowAdd] = useState<ItemKind | null>(null);
  const [editItem, setEditItem] = useState<{ kind: ItemKind; data: ItemData } | null>(null);

  // Build scheduled timeline plus an idea list for undated candidates.
  type Item =
    | { kind: "transport"; data: TransportLeg; when: string }
    | { kind: "accommodation"; data: Accommodation; when: string }
    | { kind: "activity"; data: Activity; when: string };
  const scheduled: Item[] = [];
  const ideas: Item[] = [];
  for (const l of data.transport_legs) (l.depart_at && l.arrive_at ? scheduled : ideas).push({ kind: "transport", data: l, when: l.depart_at });
  for (const a of data.accommodations) (a.check_in_at && a.check_out_at ? scheduled : ideas).push({ kind: "accommodation", data: a, when: a.check_in_at });
  for (const a of data.activities) (a.start_at ? scheduled : ideas).push({ kind: "activity", data: a, when: a.start_at || "" });
  scheduled.sort((a, b) => a.when.localeCompare(b.when));

  return (
    <div className="space-y-3">
      <DestinationsSection data={data} onChanged={onChanged} />

      <div className="flex flex-wrap gap-2">
        <button onClick={() => setShowAdd("transport")} className="btn-secondary"><Icon name="plane" size={14} /> Transport</button>
        <button onClick={() => setShowAdd("accommodation")} className="btn-secondary"><Icon name="bed" size={14} /> Stay</button>
        <button onClick={() => setShowAdd("activity")} className="btn-secondary"><Icon name="compass" size={14} /> Activity</button>
      </div>
      {scheduled.length === 0 && ideas.length === 0 ? (
        <EmptyState message="Empty itinerary — add transport, stays, or activities above." />
      ) : (
        <div className="space-y-4">
          {scheduled.length > 0 && (
            <section>
              <div className="mb-2 text-xs uppercase tracking-wide text-text-muted">Scheduled</div>
              <ol className="space-y-2">
                {scheduled.map((it) => (
                  <ItineraryRow
                    key={`${it.kind}-${it.data.id}`}
                    item={it}
                    trip={data.trip}
                    onEdit={() => setEditItem({ kind: it.kind, data: it.data })}
                    onChanged={onChanged}
                  />
                ))}
              </ol>
            </section>
          )}
          {ideas.length > 0 && (
            <section>
              <div className="mb-2 text-xs uppercase tracking-wide text-text-muted">Ideas</div>
              <ol className="space-y-2">
                {ideas.map((it) => (
                  <ItineraryRow
                    key={`${it.kind}-${it.data.id}`}
                    item={it}
                    trip={data.trip}
                    onEdit={() => setEditItem({ kind: it.kind, data: it.data })}
                    onChanged={onChanged}
                  />
                ))}
              </ol>
            </section>
          )}
        </div>
      )}
      {showAdd && (
        <ItemDialog
          kind={showAdd}
          trip={data.trip}
          destinations={data.destinations}
          onClose={() => setShowAdd(null)}
          onSaved={() => { setShowAdd(null); onChanged(); }}
        />
      )}
      {editItem && (
        <ItemDialog
          kind={editItem.kind}
          trip={data.trip}
          destinations={data.destinations}
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
    when2 = l.depart_at && l.arrive_at ? `${fmtDateShort(l.depart_at)} ${fmtTime(l.depart_at)} – ${fmtTime(l.arrive_at)}` : "Idea";
  } else if (item.kind === "accommodation") {
    const a = item.data as Accommodation;
    icon = "bed";
    title = a.name;
    subtitle = a.address;
    when2 = a.check_in_at && a.check_out_at ? `${fmtDateShort(a.check_in_at)} – ${fmtDateShort(a.check_out_at)}` : "Idea";
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
                  {d.arrive_at && d.depart_at ? `${fmtDateShort(d.arrive_at)} – ${fmtDateShort(d.depart_at)}` : "Idea"}
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
  const [datesKnown, setDatesKnown] = useState(true);
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
          name,
          start_at: datesKnown ? startAt + "T00:00:00Z" : undefined,
          end_at: datesKnown ? endAt + "T23:59:59Z" : undefined,
          status: datesKnown ? "planning" : "idea",
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
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={datesKnown} onChange={e => setDatesKnown(e.target.checked)} />
        Dates known
      </label>
      {datesKnown && (
        <DateRangeField
          start={startAt}
          end={endAt}
          onChange={(s, e) => { setStartAt(s); setEndAt(e); }}
        />
      )}
      <Field label="Home currency"><input value={currency} onChange={e => setCurrency(e.target.value.toUpperCase())} className="input uppercase" maxLength={3} /></Field>
      {err && <p className="text-sm text-error">{err}</p>}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
        <button onClick={submit} disabled={busy || !name} className="btn-dialog-primary">{busy ? "Creating…" : "Create"}</button>
      </DialogActions>
    </Dialog>
  );
}

function ItemDialog({ kind, trip, destinations, existing, onClose, onSaved }: {
  kind: "transport" | "accommodation" | "activity";
  trip: Trip;
  destinations?: Destination[];
  existing?: ItemData;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isEdit = existing != null;
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [showFlightSearch, setShowFlightSearch] = useState(false);
  const [showPlaceSearch, setShowPlaceSearch] = useState(false);

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
  const [transportDatesKnown, setTransportDatesKnown] = useState(t ? hasDateRange(t.depart_at, t.arrive_at) : hasDateRange(trip.start_at, trip.end_at));
  const [departAt, setDepartAt] = useState(t?.depart_at ? t.depart_at.slice(0, 16) : (trip.start_at ? trip.start_at.slice(0, 16) : `${todayPlus(0)}T09:00`));
  const [arriveAt, setArriveAt] = useState(t?.arrive_at ? t.arrive_at.slice(0, 16) : (trip.start_at ? trip.start_at.slice(0, 16) : `${todayPlus(0)}T11:00`));
  const [departLoc, setDepartLoc] = useState(t?.depart_location ?? "");
  const [arriveLoc, setArriveLoc] = useState(t?.arrive_location ?? "");

  const [aKind, setAKind] = useState<Accommodation["kind"]>(a?.kind ?? "hotel");
  const [address, setAddress] = useState(a?.address ?? "");
  const [stayDatesKnown, setStayDatesKnown] = useState(a ? hasDateRange(a.check_in_at, a.check_out_at) : hasDateRange(trip.start_at, trip.end_at));
  const [checkIn, setCheckIn] = useState(a?.check_in_at ? a.check_in_at.slice(0, 10) : (trip.start_at ? trip.start_at.slice(0, 10) : todayPlus(0)));
  const [checkOut, setCheckOut] = useState(a?.check_out_at ? a.check_out_at.slice(0, 10) : (trip.end_at ? trip.end_at.slice(0, 10) : todayPlus(1)));

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
          depart_at: transportDatesKnown && departAt ? ensureRfc3339(departAt) : null,
          arrive_at: transportDatesKnown && arriveAt ? ensureRfc3339(arriveAt) : null,
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
          check_in_at: stayDatesKnown && checkIn ? checkIn + "T15:00:00Z" : null,
          check_out_at: stayDatesKnown && checkOut ? checkOut + "T11:00:00Z" : null,
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

  const onFlightPicked = (offer: FlightOffer) => {
    setTKind("flight");
    setProvider(offer.carrier);
    setReference(`${offer.carrier_code}${offer.number}`);
    setTransportDatesKnown(true);
    setDepartAt(offer.depart_at.slice(0, 16));
    setArriveAt(offer.arrive_at.slice(0, 16));
    setDepartLoc(offer.depart_location);
    setArriveLoc(offer.arrive_location);
    if (offer.total_amount_cents > 0) {
      setCost((offer.total_amount_cents / 100).toFixed(2));
    }
    if (offer.currency) setCurrency(offer.currency);
    setShowFlightSearch(false);
  };

  const onPlacePicked = (p: PlaceResult) => {
    if (kind === "accommodation") {
      setName(p.name);
      if (p.formatted_address) setAddress(p.formatted_address);
      // Hotel-style discovery: open the place in Google Maps in a new
      // tab so the user can book externally and paste the confirmation back.
      if (p.google_maps_uri) window.open(p.google_maps_uri, "_blank", "noopener");
    } else if (kind === "activity") {
      setName(p.name);
      if (p.formatted_address) setActLocation(p.formatted_address);
      // Map Places primary_type → trip activity category.
      if (p.primary_type === "restaurant" || p.primary_type === "cafe" || p.primary_type === "bar") {
        setActCategory("food");
      } else {
        setActCategory("activity");
      }
    }
    setShowPlaceSearch(false);
  };

  // Show search button only when adding (edit mode = user is fixing
  // an existing thing, not researching afresh).
  const canSearchFlights = !isEdit && kind === "transport" && tKind === "flight";
  const canSearchPlaces  = !isEdit && (kind === "accommodation" || kind === "activity");

  return (
    <Dialog title={title} onClose={onClose}>
      {canSearchFlights && (
        <button
          onClick={() => setShowFlightSearch(true)}
          className="mb-1 flex w-full items-center justify-center gap-2 rounded-md border border-border bg-bg-hover px-3 py-2 text-sm text-text hover:border-accent"
        ><Icon name="search" size={14} /> Search flights with Duffel</button>
      )}
      {canSearchPlaces && (
        <button
          onClick={() => setShowPlaceSearch(true)}
          className="mb-1 flex w-full items-center justify-center gap-2 rounded-md border border-border bg-bg-hover px-3 py-2 text-sm text-text hover:border-accent"
        ><Icon name="search" size={14} /> {kind === "accommodation" ? "Search hotels nearby" : "Find places nearby"}</button>
      )}
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
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={transportDatesKnown} onChange={e => setTransportDatesKnown(e.target.checked)} />
            Dates known
          </label>
          {transportDatesKnown && (
            <div className="grid grid-cols-2 gap-3">
              <Field label="Depart"><input type="datetime-local" value={departAt} onChange={e => setDepartAt(e.target.value)} className="input" /></Field>
              <Field label="Arrive"><input type="datetime-local" value={arriveAt} onChange={e => setArriveAt(e.target.value)} className="input" /></Field>
            </div>
          )}
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
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={stayDatesKnown} onChange={e => setStayDatesKnown(e.target.checked)} />
            Dates known
          </label>
          {stayDatesKnown && (
            <DateRangeField
              startLabel="Check-in"
              endLabel="Check-out"
              start={checkIn}
              end={checkOut}
              onChange={(s, e) => { setCheckIn(s); setCheckOut(e); }}
            />
          )}
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
      {showFlightSearch && (
        <SearchFlightsModal
          trip={trip}
          defaultTo=""
          defaultDepartAt={departAt}
          onPick={onFlightPicked}
          onClose={() => setShowFlightSearch(false)}
        />
      )}
      {showPlaceSearch && (
        <SearchPlacesModal
          trip={trip}
          destinations={destinations ?? []}
          defaultKind={kind === "accommodation" ? "lodging" : "attraction"}
          onPick={onPlacePicked}
          onClose={() => setShowPlaceSearch(false)}
        />
      )}
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
  const [datesKnown, setDatesKnown] = useState(existing ? hasDateRange(existing.arrive_at, existing.depart_at) : hasDateRange(trip.start_at, trip.end_at));
  const [arriveAt, setArriveAt] = useState(existing?.arrive_at ? existing.arrive_at.slice(0, 10) : (trip.start_at ? trip.start_at.slice(0, 10) : todayPlus(0)));
  const [departAt, setDepartAt] = useState(existing?.depart_at ? existing.depart_at.slice(0, 10) : (trip.end_at ? trip.end_at.slice(0, 10) : todayPlus(1)));
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
        arrive_at: datesKnown ? arriveAt + "T00:00:00Z" : null,
        depart_at: datesKnown ? departAt + "T23:59:59Z" : null,
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
      <Field label="Place">
        <PlaceAutocomplete
          value={placeName}
          onChange={setPlaceName}
          onPick={(p) => {
            setPlaceName(p.name);
            // Try to extract a 2-letter country code from the
            // secondary text Google returns ("France" etc.). If it
            // isn't a clean 2-letter code we leave it for the user.
            if (p.country && p.country.length === 2) {
              setCountry(p.country.toUpperCase());
            }
          }}
        />
      </Field>
      <Field label="Country (ISO-2)"><input value={country} onChange={e => setCountry(e.target.value.toUpperCase())} className="input uppercase" maxLength={2} placeholder="FR" /></Field>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={datesKnown} onChange={e => setDatesKnown(e.target.checked)} />
        Dates known
      </label>
      {datesKnown && (
        <DateRangeField
          startLabel="Arrive"
          endLabel="Depart"
          start={arriveAt}
          end={departAt}
          onChange={(s, e) => { setArriveAt(s); setDepartAt(e); }}
        />
      )}
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
  const [datesKnown, setDatesKnown] = useState(hasDateRange(trip.start_at, trip.end_at));
  const [startAt, setStartAt] = useState(trip.start_at ? trip.start_at.slice(0, 10) : new Date().toISOString().slice(0, 10));
  const [endAt, setEndAt] = useState(trip.end_at ? trip.end_at.slice(0, 10) : todayPlus(7));
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
          start_at: datesKnown ? startAt + "T00:00:00Z" : null,
          end_at: datesKnown ? endAt + "T23:59:59Z" : null,
          status: datesKnown && status === "idea" ? "planning" : !datesKnown ? "idea" : status,
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
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={datesKnown} onChange={e => setDatesKnown(e.target.checked)} />
        Dates known
      </label>
      {datesKnown && (
        <DateRangeField
          start={startAt}
          end={endAt}
          onChange={(s, e) => { setStartAt(s); setEndAt(e); }}
        />
      )}
      <div className="grid grid-cols-2 gap-3">
        <Field label="Status">
          <select value={status} onChange={e => setStatus(e.target.value as Trip["status"])} className="input">
            <option value="idea">Idea</option>
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

// ─── Search-powered dialogs (v0.4) ───────────────────────────────

// PlaceAutocomplete swaps a free-text input for one wired to the
// Places autocomplete tool. The textbox stays editable (manual entry
// is preserved when no suggestion fits); a dropdown of suggestions
// renders below while focused with results, and Enter / click picks.
function PlaceAutocomplete({ value, onChange, onPick }: {
  value: string;
  onChange: (v: string) => void;
  onPick: (p: PlaceResult) => void;
}) {
  const ui = useUI();
  const [suggestions, setSuggestions] = useState<PlaceResult[]>([]);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const debounceRef = useRef<number | null>(null);

  useEffect(() => {
    if (debounceRef.current) window.clearTimeout(debounceRef.current);
    if (!value || value.length < 2) {
      setSuggestions([]);
      return;
    }
    debounceRef.current = window.setTimeout(async () => {
      setLoading(true);
      try {
        const url = `/search/places?kind=destination&query=${encodeURIComponent(value)}`;
        const r = await api<{ places: PlaceResult[] }>(url);
        setSuggestions(r.places ?? []);
      } catch (e: unknown) {
        // Silent on autocomplete fail — fall back to manual text entry.
        // We do surface the error via the toast only on explicit picks.
        ui.notify(e instanceof Error ? e.message : String(e));
      } finally {
        setLoading(false);
      }
    }, 250);
    return () => {
      if (debounceRef.current) window.clearTimeout(debounceRef.current);
    };
  }, [value, ui]);

  return (
    <div className="relative">
      <input
        value={value}
        onChange={e => { onChange(e.target.value); setOpen(true); }}
        onFocus={() => setOpen(true)}
        onBlur={() => window.setTimeout(() => setOpen(false), 150)}
        className="input"
        autoFocus
        placeholder="Paris"
      />
      {open && suggestions.length > 0 && (
        <ul className="absolute left-0 right-0 top-full z-10 mt-1 max-h-60 overflow-auto rounded-md border border-border bg-bg-card shadow-lg">
          {suggestions.map(s => (
            <li key={s.place_id}>
              <button
                type="button"
                onClick={() => { onPick(s); setOpen(false); }}
                className="flex w-full flex-col items-start px-3 py-2 text-left text-sm hover:bg-bg-hover"
              >
                <span className="text-text">{s.name}</span>
                {s.country && <span className="text-xs text-text-muted">{s.country}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
      {loading && <span className="absolute right-2 top-2 text-xs text-text-dim">…</span>}
    </div>
  );
}

function AirportCodeInput({ value, onChange, onPick, placeholder = "BCN", autoFocus = false }: {
  value: string;
  onChange: (v: string) => void;
  onPick?: (airport: AirportResult) => void;
  placeholder?: string;
  autoFocus?: boolean;
}) {
  const [suggestions, setSuggestions] = useState<AirportResult[]>([]);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const debounceRef = useRef<number | null>(null);

  useEffect(() => {
    if (debounceRef.current) window.clearTimeout(debounceRef.current);
    const query = value.trim();
    if (query.length < 2) {
      setSuggestions([]);
      return;
    }
    debounceRef.current = window.setTimeout(async () => {
      setLoading(true);
      try {
        const r = await api<{ airports: AirportResult[] }>(`/search/airports?query=${encodeURIComponent(query)}&limit=12`);
        setSuggestions(r.airports ?? []);
      } catch {
        setSuggestions([]);
      } finally {
        setLoading(false);
      }
    }, 220);
    return () => {
      if (debounceRef.current) window.clearTimeout(debounceRef.current);
    };
  }, [value]);

  const pick = (airport: AirportResult) => {
    const code = airport.iata_code.toUpperCase();
    onChange(code);
    onPick?.(airport);
    setOpen(false);
  };

  return (
    <div className="relative">
      <input
        value={value}
        onChange={e => { onChange(e.target.value.toUpperCase()); setOpen(true); }}
        onFocus={() => setOpen(true)}
        onBlur={() => window.setTimeout(() => setOpen(false), 150)}
        className="input uppercase"
        placeholder={placeholder}
        autoFocus={autoFocus}
      />
      {open && suggestions.length > 0 && (
        <ul className="absolute left-0 right-0 top-full z-20 mt-1 max-h-64 overflow-auto rounded-md border border-border bg-bg-card shadow-lg">
          {suggestions.map(a => (
            <li key={a.id || a.iata_code}>
              <button
                type="button"
                onMouseDown={e => e.preventDefault()}
                onClick={() => pick(a)}
                className="flex w-full items-start gap-3 px-3 py-2 text-left text-sm hover:bg-bg-hover"
              >
                <span className="min-w-10 font-semibold text-text">{a.iata_code}</span>
                <span className="min-w-0">
                  <span className="block truncate text-text">{a.city_name || a.name}</span>
                  <span className="block truncate text-xs text-text-muted">
                    {[a.name, a.country_name || a.country_code].filter(Boolean).join(" · ")}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {loading && <span className="absolute right-2 top-2 text-xs text-text-dim">…</span>}
    </div>
  );
}

function SearchFlightsModal({ trip, defaultTo, defaultDepartAt, onPick, onClose }: {
  trip: Trip;
  defaultTo: string;
  defaultDepartAt: string;
  onPick: (offer: FlightOffer) => void;
  onClose: () => void;
}) {
  const ui = useUI();
  const [from, setFrom] = useState("");
  const [to, setTo] = useState(defaultTo);
  const [departDate, setDepartDate] = useState(() => (defaultDepartAt || trip.start_at || todayPlus(14)).slice(0, 10));
  const [returnDate, setReturnDate] = useState("");
  const [passengers, setPassengers] = useState(1);
  const [cabin, setCabin] = useState("economy");
  const [offers, setOffers] = useState<FlightOffer[]>([]);
  const [busy, setBusy] = useState(false);

  // Pull settings to default home_airport + passengers.
  useEffect(() => {
    (async () => {
      try {
        const s = await api<Settings>("/settings");
        if (!from && s.home_airport) setFrom(s.home_airport);
        if (s.default_passengers) setPassengers(s.default_passengers);
      } catch { /* keep defaults */ }
    })();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const search = async () => {
    if (!from || !to || !departDate) {
      ui.notify("from / to / depart date all required");
      return;
    }
    setBusy(true);
    try {
      const params = new URLSearchParams({
        from, to, depart_date: departDate,
        passengers: String(passengers),
        cabin,
        trip_id: String(trip.id),
      });
      if (returnDate) params.set("return_date", returnDate);
      const r = await api<{ offers: FlightOffer[] }>(`/search/flights?${params}`);
      setOffers(r.offers ?? []);
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title="Search flights" onClose={onClose}>
      <div className="grid grid-cols-2 gap-3">
        <Field label="From"><AirportCodeInput value={from} onChange={setFrom} placeholder="CDG" /></Field>
        <Field label="To"><AirportCodeInput value={to} onChange={setTo} placeholder="LIN" /></Field>
      </div>
      <DateRangeField
        startLabel="Depart"
        endLabel="Return (optional)"
        start={departDate}
        end={returnDate}
        onChange={(s, e) => { setDepartDate(s); setReturnDate(e); }}
        optionalEnd
      />
      <div className="grid grid-cols-2 gap-3">
        <Field label="Passengers"><input type="number" min={1} value={passengers} onChange={e => setPassengers(Math.max(1, parseInt(e.target.value || "1", 10)))} className="input" /></Field>
        <Field label="Cabin">
          <select value={cabin} onChange={e => setCabin(e.target.value)} className="input">
            <option value="economy">Economy</option>
            <option value="premium_economy">Premium economy</option>
            <option value="business">Business</option>
            <option value="first">First</option>
          </select>
        </Field>
      </div>
      <button onClick={search} disabled={busy} className="btn-dialog-primary w-full">
        {busy ? "Searching…" : "Search"}
      </button>
      {offers.length > 0 && (
        <ul className="-mx-1 max-h-72 overflow-auto">
          {offers.map(o => (
            <li key={o.offer_id}>
              <button
                type="button"
                onClick={() => onPick(o)}
                className="flex w-full items-center justify-between gap-3 rounded-md border border-border-subtle bg-bg-card p-3 text-left text-sm hover:border-accent"
              >
                <div className="flex-1 min-w-0">
                  <div className="font-medium">{o.carrier} {o.carrier_code}{o.number}</div>
                  <div className="text-xs text-text-muted">
                    {o.depart_location} → {o.arrive_location}
                    {o.stops > 0 && <> · {o.stops} stop{o.stops > 1 ? "s" : ""}</>}
                    {o.duration && <> · {humanizeISO8601Duration(o.duration)}</>}
                  </div>
                  <div className="text-xs text-text-dim">
                    {fmtDateShort(o.depart_at)} {fmtTime(o.depart_at)} – {fmtTime(o.arrive_at)}
                  </div>
                </div>
                <div className="text-right tabular-nums text-sm font-medium">
                  {fmtMoney(o.total_amount_cents, o.currency || "EUR")}
                </div>
              </button>
            </li>
          ))}
        </ul>
      )}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Close</button>
      </DialogActions>
    </Dialog>
  );
}

function SearchPlacesModal({ trip, destinations, defaultKind, onPick, onClose }: {
  trip: Trip;
  destinations: Destination[];
  defaultKind: string;
  onPick: (place: PlaceResult) => void;
  onClose: () => void;
}) {
  const ui = useUI();
  const [destID, setDestID] = useState<number | "">(destinations[0]?.id ?? "");
  const [kind, setKind] = useState(defaultKind);
  const [query, setQuery] = useState("");
  const [places, setPlaces] = useState<PlaceResult[]>([]);
  const [busy, setBusy] = useState(false);

  const search = async () => {
    setBusy(true);
    try {
      const params = new URLSearchParams({ kind });
      if (query.trim()) params.set("query", query.trim());
      if (destID) params.set("destination_id", String(destID));
      params.set("limit", "12");
      const r = await api<{ places: PlaceResult[] }>(`/search/places?${params}`);
      setPlaces(r.places ?? []);
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  // Trigger an initial search if we have an anchor.
  useEffect(() => {
    if (destinations.length > 0) search();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Dialog title={`Search ${kindLabel(kind)} near ${trip.name}`} onClose={onClose}>
      <Field label="Near">
        <select value={destID} onChange={e => setDestID(e.target.value ? Number(e.target.value) : "")} className="input">
          <option value="">— No anchor (free text only) —</option>
          {destinations.map(d => <option key={d.id} value={d.id}>{d.place_name}</option>)}
        </select>
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Kind">
          <select value={kind} onChange={e => setKind(e.target.value)} className="input">
            <option value="lodging">Hotels</option>
            <option value="restaurant">Restaurants</option>
            <option value="cafe">Cafés</option>
            <option value="bar">Bars</option>
            <option value="attraction">Attractions</option>
            <option value="museum">Museums</option>
            <option value="shopping">Shopping</option>
          </select>
        </Field>
        <Field label="Free text (optional)"><input value={query} onChange={e => setQuery(e.target.value)} className="input" placeholder="ramen, near louvre, …" /></Field>
      </div>
      <button onClick={search} disabled={busy} className="btn-dialog-primary w-full">
        {busy ? "Searching…" : "Search"}
      </button>
      {places.length > 0 && (
        <ul className="-mx-1 max-h-72 overflow-auto">
          {places.map(p => (
            <li key={p.place_id}>
              <button
                type="button"
                onClick={() => onPick(p)}
                className="flex w-full flex-col items-start gap-1 rounded-md border border-border-subtle bg-bg-card p-3 text-left text-sm hover:border-accent"
              >
                <div className="flex w-full items-center justify-between gap-2">
                  <span className="truncate font-medium">{p.name}</span>
                  {p.rating != null && (
                    <span className="flex items-center gap-1 text-xs text-warn">
                      <Icon name="star" size={10} /> {p.rating.toFixed(1)}
                      {p.user_rating_count != null && <span className="text-text-dim">({p.user_rating_count})</span>}
                    </span>
                  )}
                </div>
                {p.formatted_address && <div className="truncate text-xs text-text-muted">{p.formatted_address}</div>}
              </button>
            </li>
          ))}
        </ul>
      )}
      <DialogActions>
        <button onClick={onClose} className="btn-dialog-secondary">Close</button>
      </DialogActions>
    </Dialog>
  );
}

function kindLabel(kind: string): string {
  switch (kind) {
    case "lodging": return "hotels";
    case "restaurant": return "restaurants";
    case "cafe": return "cafés";
    case "bar": return "bars";
    case "attraction": return "attractions";
    case "museum": return "museums";
    case "shopping": return "shopping";
  }
  return "places";
}

// humanizeISO8601Duration turns "PT1H30M" into "1h 30m". Bare-bones —
// only handles hours + minutes, which covers every flight Duffel returns.
function humanizeISO8601Duration(s: string): string {
  const m = /^PT(?:(\d+)H)?(?:(\d+)M)?$/.exec(s);
  if (!m) return s;
  const parts: string[] = [];
  if (m[1]) parts.push(`${m[1]}h`);
  if (m[2]) parts.push(`${m[2]}m`);
  return parts.join(" ") || s;
}

function SettingsDialog({ onClose }: { onClose: () => void }) {
  const ui = useUI();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [duffelConns, setDuffelConns] = useState<AvailableConnection[]>([]);
  const [placesConns, setPlacesConns] = useState<AvailableConnection[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const s = await api<Settings>("/settings");
        setSettings(s);
        const [d, p] = await Promise.all([
          api<{ connections: AvailableConnection[] }>("/connections?provider=duffel"),
          api<{ connections: AvailableConnection[] }>("/connections?provider=google-places"),
        ]);
        setDuffelConns(d.connections ?? []);
        setPlacesConns(p.connections ?? []);
      } catch (e: unknown) {
        ui.notify(e instanceof Error ? e.message : String(e));
      }
    })();
  }, [ui]);

  const update = (patch: Partial<Settings>) => {
    if (!settings) return;
    setSettings({ ...settings, ...patch });
  };

  const save = async () => {
    if (!settings) return;
    setBusy(true);
    try {
      await api<Settings>("/settings", {
        method: "PATCH",
        body: JSON.stringify({
          home_airport: settings.home_airport,
          default_passengers: settings.default_passengers,
          duffel_connection_id: settings.duffel_connection_id ?? 0,
          places_connection_id: settings.places_connection_id ?? 0,
          daily_search_budget_cents: settings.daily_search_budget_cents,
        }),
      });
      onClose();
    } catch (e: unknown) {
      ui.notify(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Dialog title="Search providers + defaults" onClose={onClose}>
      {!settings ? (
        <p className="text-sm text-text-muted">Loading…</p>
      ) : (
        <>
          <Field label="Duffel (flights)">
            <select
              value={settings.duffel_connection_id ?? ""}
              onChange={e => update({ duffel_connection_id: e.target.value ? Number(e.target.value) : undefined })}
              className="input"
            >
              <option value="">— Not connected —</option>
              {duffelConns.map(c => (
                <option key={c.id} value={c.id}>{c.name} ({c.status})</option>
              ))}
            </select>
            {duffelConns.length === 0 && (
              <p className="mt-1 text-xs text-text-dim">No Duffel connections yet. Add one in the platform Integrations panel, then come back here.</p>
            )}
          </Field>
          <Field label="Google Places (destinations, hotels, restaurants)">
            <select
              value={settings.places_connection_id ?? ""}
              onChange={e => update({ places_connection_id: e.target.value ? Number(e.target.value) : undefined })}
              className="input"
            >
              <option value="">— Not connected —</option>
              {placesConns.map(c => (
                <option key={c.id} value={c.id}>{c.name} ({c.status})</option>
              ))}
            </select>
            {placesConns.length === 0 && (
              <p className="mt-1 text-xs text-text-dim">No Google Places connections yet. Add one in the platform Integrations panel.</p>
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Home airport (IATA)">
              <input
                value={settings.home_airport}
                onChange={e => update({ home_airport: e.target.value.toUpperCase() })}
                className="input uppercase"
                maxLength={3}
                placeholder="CDG"
              />
            </Field>
            <Field label="Default passengers">
              <input
                type="number"
                min={1}
                value={settings.default_passengers}
                onChange={e => update({ default_passengers: Math.max(1, parseInt(e.target.value || "1", 10)) })}
                className="input"
              />
            </Field>
          </div>
          <Field label="Daily Places budget (cents; 0 = unlimited)">
            <input
              type="number"
              min={0}
              value={settings.daily_search_budget_cents}
              onChange={e => update({ daily_search_budget_cents: Math.max(0, parseInt(e.target.value || "0", 10)) })}
              className="input"
            />
          </Field>
          <DialogActions>
            <button onClick={onClose} className="btn-dialog-secondary">Cancel</button>
            <button onClick={save} disabled={busy} className="btn-dialog-primary">{busy ? "Saving…" : "Save"}</button>
          </DialogActions>
        </>
      )}
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

// ─── DateRangeField ──────────────────────────────────────────────
// Airbnb/Skyscanner-style range picker: two paired date fields that
// open a single two-month popover. Range highlights between start and
// end, with hover preview while picking the end. Tokens come from the
// dashboard CSS variables so it inherits the active theme.

function parseYMD(s: string): Date {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y, (m || 1) - 1, d || 1);
}

function toYMD(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

function monthYearLabel(y: number, m: number): string {
  return new Date(y, m, 1).toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

function DateRangeField({
  startLabel = "From",
  endLabel = "To",
  start,
  end,
  onChange,
  optionalEnd = false,
  minDate,
}: {
  startLabel?: string;
  endLabel?: string;
  start: string;
  end: string;
  onChange: (start: string, end: string) => void;
  optionalEnd?: boolean;
  minDate?: string;
}) {
  const [open, setOpen] = useState(false);
  const [picking, setPicking] = useState<"start" | "end">("start");
  const [hovered, setHovered] = useState("");
  const [viewYM, setViewYM] = useState(() => {
    const seed = start ? parseYMD(start) : new Date();
    return { y: seed.getFullYear(), m: seed.getMonth() };
  });
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const openFor = (which: "start" | "end") => {
    setPicking(which);
    setOpen(true);
    setHovered("");
    const anchorYMD = which === "end" && end ? end : (start || toYMD(new Date()));
    const anchor = parseYMD(anchorYMD);
    setViewYM({ y: anchor.getFullYear(), m: anchor.getMonth() });
  };

  const pick = (ymd: string) => {
    if (minDate && ymd < minDate) return;
    if (picking === "start") {
      const keptEnd = end && end >= ymd ? end : "";
      onChange(ymd, keptEnd);
      if (keptEnd) setOpen(false);
      else setPicking("end");
    } else {
      if (start && ymd >= start) {
        onChange(start, ymd);
        setOpen(false);
      } else {
        onChange(ymd, "");
        setPicking("end");
      }
    }
  };

  const shiftMonths = (n: number) => setViewYM(v => {
    const d = new Date(v.y, v.m + n, 1);
    return { y: d.getFullYear(), m: d.getMonth() };
  });

  const month1 = viewYM;
  const month2 = (() => {
    const d = new Date(viewYM.y, viewYM.m + 1, 1);
    return { y: d.getFullYear(), m: d.getMonth() };
  })();

  // While picking the end, hovering shows a preview range.
  const previewEnd = picking === "end" && start && hovered && hovered >= start && !end ? hovered : end;

  return (
    <div ref={rootRef} className="relative">
      <div className="grid grid-cols-2 gap-3">
        <Field label={startLabel}>
          <button
            type="button"
            onClick={() => openFor("start")}
            className={`drf-trigger ${open && picking === "start" ? "drf-trigger-active" : ""}`}
          >
            {start ? fmtDate(start) : <span className="drf-placeholder">Pick a date</span>}
          </button>
        </Field>
        <Field label={endLabel}>
          <button
            type="button"
            onClick={() => openFor("end")}
            className={`drf-trigger ${open && picking === "end" ? "drf-trigger-active" : ""}`}
          >
            {end ? fmtDate(end) : <span className="drf-placeholder">{optionalEnd ? "Optional" : "Pick a date"}</span>}
          </button>
        </Field>
      </div>
      {open && (
        <div className="drf-popover" onMouseLeave={() => setHovered("")}>
          <div className="drf-popover-head">
            <button type="button" onClick={() => shiftMonths(-1)} className="drf-nav" aria-label="Previous month">
              <Icon name="chevron-left" size={14} />
            </button>
            <div className="drf-months-title">
              <span>{monthYearLabel(month1.y, month1.m)}</span>
              <span>{monthYearLabel(month2.y, month2.m)}</span>
            </div>
            <button type="button" onClick={() => shiftMonths(1)} className="drf-nav" aria-label="Next month">
              <Icon name="chevron-right" size={14} />
            </button>
          </div>
          <div className="drf-months">
            <MonthGrid ym={month1} start={start} end={previewEnd} onPick={pick} onHover={setHovered} minDate={minDate} />
            <MonthGrid ym={month2} start={start} end={previewEnd} onPick={pick} onHover={setHovered} minDate={minDate} />
          </div>
          {optionalEnd && (
            <div className="drf-foot">
              <button
                type="button"
                className="drf-foot-btn"
                onClick={() => { onChange(start, ""); setOpen(false); }}
              >
                No return
              </button>
            </div>
          )}
        </div>
      )}
      <style>{drfStyles}</style>
    </div>
  );
}

function MonthGrid({ ym, start, end, onPick, onHover, minDate }: {
  ym: { y: number; m: number };
  start: string;
  end: string;
  onPick: (ymd: string) => void;
  onHover: (ymd: string) => void;
  minDate?: string;
}) {
  const first = new Date(ym.y, ym.m, 1);
  const dayCount = new Date(ym.y, ym.m + 1, 0).getDate();
  // Monday-first weekday index: 0 = Mon, 6 = Sun.
  const leading = (first.getDay() + 6) % 7;
  const cells: (string | null)[] = [];
  for (let i = 0; i < leading; i++) cells.push(null);
  for (let d = 1; d <= dayCount; d++) cells.push(toYMD(new Date(ym.y, ym.m, d)));
  while (cells.length % 7 !== 0) cells.push(null);
  const todayStr = toYMD(new Date());
  return (
    <div className="drf-month">
      <div className="drf-weekdays">
        {["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"].map(w => (
          <div key={w} className="drf-weekday">{w}</div>
        ))}
      </div>
      <div className="drf-grid">
        {cells.map((c, i) => {
          if (!c) return <div key={`e${i}`} className="drf-empty" />;
          const isStart = c === start;
          const isEnd = !!end && c === end;
          const inRange = !!start && !!end && c > start && c < end;
          const hasRange = !!start && !!end && start !== end;
          const disabled = !!minDate && c < minDate;
          const cls = [
            "drf-day",
            inRange ? "drf-day-in-range" : "",
            hasRange && isStart ? "drf-day-range-start" : "",
            hasRange && isEnd ? "drf-day-range-end" : "",
            isStart || isEnd ? "drf-day-active" : "",
            c === todayStr ? "drf-day-today" : "",
            disabled ? "drf-day-disabled" : "",
          ].filter(Boolean).join(" ");
          return (
            <button
              key={c}
              type="button"
              disabled={disabled}
              className={cls}
              onMouseEnter={() => onHover(c)}
              onClick={() => onPick(c)}
            >
              <span className="drf-day-num">{Number(c.slice(8))}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

const drfStyles = `
.drf-trigger {
  width: 100%;
  padding: 0.5rem 0.75rem;
  border-radius: 0.375rem;
  border: 1px solid var(--border);
  background: var(--bg-input);
  color: var(--text);
  text-align: left;
  font: inherit;
  cursor: pointer;
}
.drf-trigger:hover { border-color: var(--border-strong); }
.drf-trigger-active { outline: 2px solid var(--accent); outline-offset: -1px; }
.drf-placeholder { color: var(--text-dim); }

.drf-popover {
  position: absolute;
  top: 100%;
  left: 0;
  margin-top: 0.5rem;
  z-index: 60;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  padding: 0.75rem;
  box-shadow: 0 12px 36px rgba(0, 0, 0, 0.32);
  user-select: none;
}
.drf-popover-head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0 0.25rem 0.5rem;
}
.drf-nav {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 0.375rem;
  color: var(--text-muted);
  background: transparent;
  border: none;
  cursor: pointer;
}
.drf-nav:hover { background: var(--bg-hover); color: var(--text); }
.drf-months-title {
  flex: 1;
  display: grid;
  grid-template-columns: repeat(2, 238px);
  gap: 1.25rem;
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--text);
  text-align: center;
}
.drf-months {
  display: grid;
  grid-template-columns: repeat(2, 238px);
  gap: 1.25rem;
}
.drf-weekdays,
.drf-grid {
  display: grid;
  grid-template-columns: repeat(7, 34px);
}
.drf-weekday {
  height: 26px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.6875rem;
  font-weight: 600;
  letter-spacing: 0.03em;
  text-transform: uppercase;
  color: var(--text-dim);
}
.drf-empty { width: 34px; height: 34px; }
.drf-day {
  position: relative;
  width: 34px;
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  border: none;
  color: var(--text);
  cursor: pointer;
  font: inherit;
  font-size: 0.8125rem;
  padding: 0;
}
/* The number lives in a circle that sits above any range bar. */
.drf-day-num {
  position: relative;
  z-index: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 999px;
}
.drf-day:hover:not(.drf-day-disabled):not(.drf-day-active) .drf-day-num {
  background: var(--bg-hover);
}
.drf-day-today .drf-day-num { box-shadow: inset 0 0 0 1px var(--border-strong); }
/* Range fill sits on the full-width cell so cells join edge-to-edge.
   Endpoints get a half-fill so the bar tucks under their circle. */
.drf-day-in-range { background: var(--bg-hover); }
.drf-day-range-start { background: linear-gradient(to right, transparent 50%, var(--bg-hover) 50%); }
.drf-day-range-end   { background: linear-gradient(to left,  transparent 50%, var(--bg-hover) 50%); }
.drf-day-active .drf-day-num {
  background: var(--accent);
  color: var(--bg);
}
.drf-day-active.drf-day-today .drf-day-num { box-shadow: none; }
.drf-day-active:hover .drf-day-num { background: var(--accent-hover); }
.drf-day-disabled,
.drf-day-disabled:hover {
  color: var(--text-dim);
  opacity: 0.35;
  cursor: not-allowed;
  background: transparent;
}

.drf-foot {
  display: flex;
  justify-content: flex-end;
  margin-top: 0.5rem;
  padding-top: 0.5rem;
  border-top: 1px solid var(--border-subtle, var(--border));
}
.drf-foot-btn {
  font-size: 0.8125rem;
  padding: 0.25rem 0.5rem;
  color: var(--text-muted);
  background: transparent;
  border: none;
  cursor: pointer;
  border-radius: 0.25rem;
}
.drf-foot-btn:hover { background: var(--bg-hover); color: var(--text); }

@media (max-width: 560px) {
  .drf-months { grid-template-columns: 238px; }
  .drf-months > :nth-child(2) { display: none; }
  .drf-months-title { grid-template-columns: 238px; }
  .drf-months-title > :nth-child(2) { display: none; }
}
`;

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
