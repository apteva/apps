// CalendarPanel — week + day + agenda views over the calendar app.
//
// Sidebar lists calendars as toggle chips (click to hide a calendar
// from the grid, edit/delete via the row controls). Main grid renders
// events as colored blocks. Click an event → drawer with edit/delete.
// Click an empty cell → create-event dialog pre-filled with that slot.
//
// Live updates via useAppEvents("calendar") — when calendars/events
// change (from another tab or an agent), the UI refreshes.

import {
  createContext,
  useId,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Calendar {
  id: number;
  name: string;
  color: string;
  kind: string;
  enabled: boolean;
  created_at: string;
}

export interface Occurrence {
  timezone: string;
  rrule: string;
  id: number;
  event_id: number;
  calendar_id: number;
  title: string;
  description: string;
  location: string;
  start_at: string;
  end_at: string;
  all_day: boolean;
  status: string;
  is_recurring: boolean;
  occurrence_start_at: string;
}

type ViewMode = "week" | "day" | "month" | "year" | "agenda";

// --- Inlined SDK app-event subscription -------------------------------
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
    const bridge = (
      window as unknown as {
        __aptevaAppEvents?: {
          subscribe(
            app: string,
            projectId: string,
            fn: (ev: AppEventEnvelope<T>) => void,
          ): () => void;
        };
      }
    ).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

// --- date helpers -----------------------------------------------------

function startOfWeek(d: Date): Date {
  const out = new Date(d);
  out.setHours(0, 0, 0, 0);
  // Monday = 0
  const day = (out.getDay() + 6) % 7;
  out.setDate(out.getDate() - day);
  return out;
}

function addDays(d: Date, n: number): Date {
  const out = new Date(d);
  out.setDate(out.getDate() + n);
  return out;
}

function addMonths(d: Date, n: number): Date {
  // Pin to day 1 first so end-of-month dates don't roll over (e.g. Jan 31 + 1mo).
  const out = new Date(d.getFullYear(), d.getMonth() + n, 1);
  out.setHours(0, 0, 0, 0);
  return out;
}

function startOfMonth(d: Date): Date {
  const out = new Date(d.getFullYear(), d.getMonth(), 1);
  out.setHours(0, 0, 0, 0);
  return out;
}

// Mo-first 6×7 grid origin for any date.
function startOfMonthGrid(d: Date): Date {
  return startOfWeek(startOfMonth(d));
}

function startOfYear(d: Date): Date {
  const out = new Date(d.getFullYear(), 0, 1);
  out.setHours(0, 0, 0, 0);
  return out;
}

function rfc3339(d: Date): string {
  return d.toISOString();
}

function fmtDay(d: Date): string {
  return d.toLocaleDateString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
  });
}

function fmtMonthYear(d: Date): string {
  return d.toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

function fmtTime(d: Date): string {
  return d.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
}

// --- Confirm dialog (replaces window.confirm) ------------------------
//
// Tiny imperative confirm() over a controlled modal: nested components
// call useConfirm()(opts) and await a Promise<boolean>. The provider
// at the panel root owns the state + renders the modal.

interface ConfirmOpts {
  title: string;
  message?: string;
  confirmLabel?: string;
  danger?: boolean;
}
const ConfirmCtx = createContext<
  ((opts: ConfirmOpts) => Promise<boolean>) | null
>(null);
function useConfirm() {
  const c = useContext(ConfirmCtx);
  if (!c) throw new Error("useConfirm must be used inside <ConfirmProvider>");
  return c;
}

function ConfirmProvider({ children }: { children: React.ReactNode }) {
  const [opts, setOpts] = useState<ConfirmOpts | null>(null);
  const resolverRef = useRef<((v: boolean) => void) | null>(null);
  const confirm = useCallback((o: ConfirmOpts) => {
    return new Promise<boolean>((resolve) => {
      resolverRef.current = resolve;
      setOpts(o);
    });
  }, []);
  const close = (result: boolean) => {
    resolverRef.current?.(result);
    resolverRef.current = null;
    setOpts(null);
  };
  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      {opts && (
        <ConfirmModal
          {...opts}
          onConfirm={() => close(true)}
          onCancel={() => close(false)}
        />
      )}
    </ConfirmCtx.Provider>
  );
}

function ConfirmModal({
  title,
  message,
  confirmLabel = "Delete",
  danger = true,
  onConfirm,
  onCancel,
}: ConfirmOpts & { onConfirm: () => void; onCancel: () => void }) {
  return (
    <Dialog onClose={onCancel} title={title}>
      {message && <p className="text-text-muted text-sm">{message}</p>}
      <div className="flex justify-end gap-2">
        <button autoFocus onClick={onCancel}>
          Cancel
        </button>
        <button
          className={danger ? "text-error" : "text-accent"}
          onClick={onConfirm}
        >
          {confirmLabel}
        </button>
      </div>
    </Dialog>
  );
}

// --- Panel -----------------------------------------------------------

type CalendarAPI = (path: string, init?: RequestInit) => Promise<Response>;
const ApiContext = createContext<CalendarAPI>(async () => {
  throw new Error("Calendar API unavailable");
});
function useCalendarApi() {
  return useContext(ApiContext);
}
export default function CalendarPanel(props: NativePanelProps) {
  const api = useMemo<CalendarAPI>(
    () => (path, init) => {
      const url = new URL(
        `/api/apps/calendar/_install/${props.installId}${path}`,
        window.location.origin,
      );
      url.searchParams.set("project_id", props.projectId);
      return fetch(url, { credentials: "same-origin", ...init });
    },
    [props.projectId, props.installId],
  );
  return (
    <ApiContext.Provider value={api}>
      <ConfirmProvider>
        <CalendarPanelInner
          key={`${props.projectId}:${props.installId}`}
          {...props}
        />
      </ConfirmProvider>
    </ApiContext.Provider>
  );
}

function CalendarPanelInner({ projectId }: NativePanelProps) {
  const api = useCalendarApi();
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const calendarsRequest = useRef<AbortController | null>(null);
  const eventsRequest = useRef<AbortController | null>(null);
  const [calendars, setCalendars] = useState<Calendar[]>([]);
  const [events, setEvents] = useState<Occurrence[]>([]);
  const [view, setView] = useState<ViewMode>("week");
  const [anchor, setAnchor] = useState<Date>(() => new Date());
  const [hidden, setHidden] = useState<Set<number>>(new Set());
  const [status, setStatus] = useState("");
  const [addingCalendar, setAddingCalendar] = useState(false);
  const [editingCalendar, setEditingCalendar] = useState<Calendar | null>(null);
  const [creatingEvent, setCreatingEvent] = useState<{
    start: Date;
    calendarId?: number;
  } | null>(null);
  const [editingEvent, setEditingEvent] = useState<Occurrence | null>(null);

  const windowStart = useMemo(() => {
    if (view === "week") return startOfWeek(anchor);
    if (view === "month") return startOfMonthGrid(anchor);
    if (view === "year") return startOfYear(anchor);
    // day + agenda: midnight of anchor day
    const d = new Date(anchor);
    d.setHours(0, 0, 0, 0);
    return d;
  }, [view, anchor]);

  const windowEnd = useMemo(() => {
    if (view === "week") return addDays(windowStart, 7);
    if (view === "day") return addDays(windowStart, 1);
    if (view === "month") return addDays(windowStart, 42); // 6 weeks
    if (view === "year") return new Date(windowStart.getFullYear() + 1, 0, 1); // covers leap years
    return addDays(windowStart, 30); // agenda: 30 days
  }, [view, windowStart]);

  const loadCalendars = useCallback(async () => {
    calendarsRequest.current?.abort();
    const controller = new AbortController();
    calendarsRequest.current = controller;
    try {
      const res = await api(`/calendars`, { signal: controller.signal });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      if (!controller.signal.aborted) setCalendars(data.calendars || []);
    } catch (e) {
      if (!controller.signal.aborted)
        setStatus("Load calendars: " + (e as Error).message);
    }
  }, [api]);

  const loadEvents = useCallback(async () => {
    eventsRequest.current?.abort();
    const controller = new AbortController();
    eventsRequest.current = controller;
    try {
      const res = await api(
        `/items?from=${encodeURIComponent(rfc3339(windowStart))}&to=${encodeURIComponent(rfc3339(windowEnd))}`,
        { signal: controller.signal },
      );
      if (!res.ok) {
        setStatus(`Load events: ${res.status}`);
        return;
      }
      const data = await res.json();
      if (!controller.signal.aborted) {
        setEvents(data.events || []);
        setStatus("");
      }
    } catch (e) {
      if (!controller.signal.aborted)
        setStatus("Load events: " + (e as Error).message);
    }
  }, [windowStart, windowEnd, api]);

  useEffect(() => {
    loadCalendars();
    return () => calendarsRequest.current?.abort();
  }, [loadCalendars]);
  useEffect(() => {
    loadEvents();
    return () => eventsRequest.current?.abort();
  }, [loadEvents, api]);

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
    },
    [],
  );
  useAppEvents("calendar", projectId, () => {
    if (refreshTimer.current) clearTimeout(refreshTimer.current);
    refreshTimer.current = setTimeout(() => {
      loadCalendars();
      loadEvents();
    }, 100);
  });

  const visibleEvents = useMemo(
    () =>
      events.filter(
        (e) =>
          !hidden.has(e.calendar_id) &&
          calendars.some((c) => c.id === e.calendar_id && c.enabled),
      ),
    [events, hidden, calendars],
  );

  const calendarById = useMemo(() => {
    const m = new Map<number, Calendar>();
    for (const c of calendars) m.set(c.id, c);
    return m;
  }, [calendars]);

  const goPrev = () => {
    if (view === "month") setAnchor(addMonths(anchor, -1));
    else if (view === "year")
      setAnchor(new Date(anchor.getFullYear() - 1, 0, 1));
    else
      setAnchor(
        addDays(anchor, view === "week" ? -7 : view === "day" ? -1 : -30),
      );
  };
  const goNext = () => {
    if (view === "month") setAnchor(addMonths(anchor, 1));
    else if (view === "year")
      setAnchor(new Date(anchor.getFullYear() + 1, 0, 1));
    else
      setAnchor(addDays(anchor, view === "week" ? 7 : view === "day" ? 1 : 30));
  };
  const goToday = () => setAnchor(new Date());

  // Used by the new month/year views to dive into a clicked day.
  const jumpToDay = (d: Date) => {
    setAnchor(d);
    setView("day");
  };
  const jumpToMonth = (d: Date) => {
    setAnchor(d);
    setView("month");
  };

  // commitEventTimes is the drag/resize commit path. Updates local
  // state optimistically, PATCHes /items/{event_id} with the new
  // window, and refetches on success to pick up any server-side
  // restructuring (notably the child-row creation for recurring
  // events under scope=this).
  const commitEventTimes = useCallback(
    async (ev: Occurrence, newStart: Date, newEnd: Date) => {
      const key = ev.id + "|" + ev.occurrence_start_at;
      const newStartISO = newStart.toISOString();
      const newEndISO = newEnd.toISOString();
      setEvents((prev) =>
        prev.map((e) =>
          e.id + "|" + e.occurrence_start_at === key
            ? { ...e, start_at: newStartISO, end_at: newEndISO }
            : e,
        ),
      );
      try {
        const body: Record<string, unknown> = {
          start_at: newStartISO,
          end_at: newEndISO,
        };
        if (ev.is_recurring) {
          // scope=this creates a child row at the new time + adds the
          // original date to the master's exdate.
          body.scope = "this";
          body.occurrence_start_at = ev.occurrence_start_at;
        } else {
          body.scope = "all";
        }
        const res = await api(`/items/${ev.event_id}`, {
          method: "PATCH",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
        // Refetch — server may have created a child row (recurring) or
        // applied other side effects we'd miss with a local-only update.
        loadEvents();
      } catch (e) {
        setStatus("Move failed: " + (e as Error).message);
        // Revert the optimistic edit.
        loadEvents();
      }
    },
    [loadEvents, api],
  );

  return (
    <div className="h-full flex relative">
      {/* Sidebar */}
      <aside
        className={`${sidebarOpen ? "flex absolute inset-y-0 left-0 z-20 bg-bg" : "hidden"} md:flex w-56 shrink-0 border-r border-border flex-col`}
      >
        <div className="p-3 border-b border-border flex items-center gap-2">
          <span className="text-text font-medium flex-1">Calendars</span>
          <button
            onClick={() => setAddingCalendar(true)}
            className="text-text-muted hover:text-text text-sm"
            title="Add calendar"
          >
            +
          </button>
        </div>
        <div className="flex-1 overflow-auto p-2 flex flex-col gap-1">
          {calendars.length === 0 ? (
            <div className="px-2 py-4 text-text-dim text-xs text-center">
              No calendars yet. Click + to create one.
            </div>
          ) : (
            calendars.map((c) => (
              <CalendarChip
                key={c.id}
                cal={c}
                hidden={hidden.has(c.id) || !c.enabled}
                onToggle={() => {
                  if (!c.enabled) {
                    api(`/calendars/${c.id}`, {
                      method: "PATCH",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ enabled: true }),
                    })
                      .then(async (res) => {
                        if (!res.ok) throw new Error(await res.text());
                        loadCalendars();
                        loadEvents();
                      })
                      .catch((e) => setStatus(e.message));
                    return;
                  }
                  setHidden((s) => {
                    const n = new Set(s);
                    if (n.has(c.id)) n.delete(c.id);
                    else n.add(c.id);
                    return n;
                  });
                }}
                onEdit={() => setEditingCalendar(c)}
              />
            ))
          )}
        </div>
        <button className="md:hidden p-2" onClick={() => setSidebarOpen(false)}>
          Close calendars
        </button>
      </aside>

      {/* Main */}
      <div className="flex-1 flex flex-col min-w-0">
        <header className="flex flex-wrap items-center gap-2 px-3 py-2 border-b border-border">
          <button
            className="md:hidden"
            aria-label="Toggle calendars"
            onClick={() => setSidebarOpen(!sidebarOpen)}
          >
            Calendars
          </button>
          <button
            className="px-3 py-1 text-sm bg-accent text-bg rounded"
            onClick={() => {
              const c = calendars.find((c) => c.enabled);
              if (c) setCreatingEvent({ start: new Date(), calendarId: c.id });
              else setStatus("Create or enable a calendar first.");
            }}
          >
            New event
          </button>
          <button
            onClick={goToday}
            className="px-3 py-1 text-sm border border-border rounded hover:border-accent"
          >
            Today
          </button>
          <button
            aria-label="Previous date range"
            onClick={goPrev}
            className="px-2 py-1 text-sm text-text-muted hover:text-text"
          >
            ‹
          </button>
          <button
            aria-label="Next date range"
            onClick={goNext}
            className="px-2 py-1 text-sm text-text-muted hover:text-text"
          >
            ›
          </button>
          <span className="text-text font-medium ml-2">
            {view === "week"
              ? `${fmtDay(windowStart)} – ${fmtDay(addDays(windowEnd, -1))}`
              : view === "day"
                ? fmtDay(windowStart)
                : view === "month"
                  ? fmtMonthYear(anchor)
                  : view === "year"
                    ? String(anchor.getFullYear())
                    : `${fmtDay(windowStart)} → 30 days`}
          </span>
          <div className="ml-auto flex items-center gap-1">
            {(["day", "week", "month", "year", "agenda"] as ViewMode[]).map(
              (v) => (
                <button
                  key={v}
                  onClick={() => setView(v)}
                  className={
                    "px-3 py-1 text-sm rounded " +
                    (view === v
                      ? "bg-bg-card text-text"
                      : "text-text-muted hover:text-text")
                  }
                >
                  {v.charAt(0).toUpperCase() + v.slice(1)}
                </button>
              ),
            )}
          </div>
        </header>

        <div aria-live="polite">
          {status && (
            <div role="alert" className="px-3 py-2 text-error text-sm">
              {status}{" "}
              <button
                onClick={() => {
                  loadCalendars();
                  loadEvents();
                }}
              >
                Retry
              </button>
            </div>
          )}
        </div>
        <div className="flex-1 overflow-auto">
          {view === "week" || view === "day" ? (
            <Grid
              start={windowStart}
              days={view === "week" ? 7 : 1}
              events={visibleEvents}
              calendarById={calendarById}
              onEmptyClick={(start) => {
                if (calendars.length === 0) {
                  setStatus("Create a calendar first.");
                  return;
                }
                const firstEnabled = calendars.find((c) => c.enabled);
                if (!firstEnabled) {
                  setStatus("Enable at least one calendar.");
                  return;
                }
                setCreatingEvent({ start, calendarId: firstEnabled.id });
              }}
              onEventClick={setEditingEvent}
              onEventCommit={commitEventTimes}
            />
          ) : view === "month" ? (
            <MonthView
              monthAnchor={anchor}
              gridStart={windowStart}
              events={visibleEvents}
              calendarById={calendarById}
              onEmptyClick={(d) => {
                if (calendars.length === 0) {
                  setStatus("Create a calendar first.");
                  return;
                }
                const firstEnabled = calendars.find((c) => c.enabled);
                if (!firstEnabled) {
                  setStatus("Enable at least one calendar.");
                  return;
                }
                // Default new events created from month cells to noon
                // so they're not stuck at 00:00 if the user just wants
                // a slot to type into.
                const slot = new Date(d);
                slot.setHours(12, 0, 0, 0);
                setCreatingEvent({ start: slot, calendarId: firstEnabled.id });
              }}
              onDayClick={jumpToDay}
              onEventClick={setEditingEvent}
            />
          ) : view === "year" ? (
            <YearView
              year={anchor.getFullYear()}
              events={visibleEvents}
              calendarById={calendarById}
              onDayClick={jumpToDay}
              onMonthClick={jumpToMonth}
            />
          ) : (
            <Agenda
              events={visibleEvents}
              calendarById={calendarById}
              onEventClick={setEditingEvent}
            />
          )}
        </div>
      </div>

      {/* Dialogs */}
      {addingCalendar && (
        <CalendarDialog
          onClose={() => setAddingCalendar(false)}
          onSaved={() => {
            setAddingCalendar(false);
            loadCalendars();
          }}
          setStatus={setStatus}
        />
      )}
      {editingCalendar && (
        <CalendarDialog
          existing={editingCalendar}
          onClose={() => setEditingCalendar(null)}
          onSaved={() => {
            setEditingCalendar(null);
            loadCalendars();
            loadEvents();
          }}
          setStatus={setStatus}
        />
      )}
      {creatingEvent && (
        <EventDialog
          calendars={calendars}
          defaults={creatingEvent}
          onClose={() => setCreatingEvent(null)}
          onSaved={() => {
            setCreatingEvent(null);
            loadEvents();
          }}
          setStatus={setStatus}
        />
      )}
      {editingEvent && (
        <EventDialog
          calendars={calendars}
          existing={editingEvent}
          onClose={() => setEditingEvent(null)}
          onSaved={() => {
            setEditingEvent(null);
            loadEvents();
          }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

// --- Sidebar chip ----------------------------------------------------

function CalendarChip({
  cal,
  hidden,
  onToggle,
  onEdit,
}: {
  cal: Calendar;
  hidden: boolean;
  onToggle: () => void;
  onEdit: () => void;
}) {
  return (
    <div className="flex items-center gap-2 px-2 py-1.5 hover:bg-bg-card rounded group">
      <button
        onClick={onToggle}
        className="w-3 h-3 rounded-full flex-shrink-0 transition-opacity"
        style={{ backgroundColor: cal.color, opacity: hidden ? 0.25 : 1 }}
        title={hidden ? "Show" : "Hide"}
      />
      <button
        onClick={onEdit}
        className={
          "flex-1 text-left text-sm truncate " +
          (hidden ? "text-text-dim" : "text-text")
        }
      >
        {cal.name}
      </button>
      <span className="text-text-dim text-[10px] uppercase opacity-0 group-hover:opacity-100">
        {cal.kind}
      </span>
    </div>
  );
}

// --- Grid (week + day) ----------------------------------------------

const HOUR_HEIGHT = 48; // px per hour

function Grid({
  start,
  days,
  events,
  calendarById,
  onEmptyClick,
  onEventClick,
  onEventCommit,
}: {
  start: Date;
  days: number;
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEmptyClick: (start: Date) => void;
  onEventClick: (e: Occurrence) => void;
  onEventCommit: (e: Occurrence, newStart: Date, newEnd: Date) => void;
}) {
  const [drag, setDrag] = useState<DragState | null>(null);
  const dayDates = Array.from({ length: days }, (_, i) => addDays(start, i));
  const hours = Array.from({ length: 24 }, (_, i) => i);

  // Multi-day / all-day events go to the all-day strip; everything else
  // is a timed block in its day column. This is what stops a 23-day
  // trip from rendering as a giant "02:00 – 01:59" block.
  const allDayEvents = useMemo(
    () =>
      events
        .filter(isMultiDay)
        .sort((a, b) => a.start_at.localeCompare(b.start_at) || a.id - b.id),
    [events],
  );

  return (
    <div className="flex flex-col">
      {/* All-day strip — only when there's something to show. */}
      {allDayEvents.length > 0 && (
        <AllDayStrip
          start={start}
          days={days}
          events={allDayEvents}
          calendarById={calendarById}
          onEventClick={onEventClick}
        />
      )}
      <div className="flex">
        {/* Hour gutter */}
        <div className="w-12 flex-shrink-0">
          <div className="h-8" />
          {hours.map((h) => (
            <div
              key={h}
              data-hour-label={h}
              style={{ height: HOUR_HEIGHT }}
              className="text-text-dim text-[10px] text-right pr-2"
            >
              {h.toString().padStart(2, "0")}:00
            </div>
          ))}
        </div>
        {/* Day columns — timed events only (multi-day go to the strip). */}
        {dayDates.map((d) => (
          <DayColumn
            key={d.toISOString()}
            date={d}
            drag={drag}
            setDrag={setDrag}
            events={events.filter(
              (e) => !isMultiDay(e) && sameDay(new Date(e.start_at), d),
            )}
            calendarById={calendarById}
            onEmptyClick={onEmptyClick}
            onEventClick={onEventClick}
            onEventCommit={onEventCommit}
          />
        ))}
      </div>
    </div>
  );
}

// AllDayStrip — the band above the timed grid that holds multi-day and
// all-day events as horizontal bars spanning their day columns. Mirrors
// the gutter + N-column layout of the grid below so bars line up with
// the right days. Each event gets its own row to avoid overlap; bars
// are clamped to the visible window with arrow affordances when the
// span continues off-screen.
function AllDayStrip({
  start,
  days,
  events,
  calendarById,
  onEventClick,
}: {
  start: Date;
  days: number;
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEventClick: (e: Occurrence) => void;
}) {
  // Keep only events that intersect the visible window at all.
  const rows = events.filter((e) => {
    for (let i = 0; i < days; i++) {
      if (occCoversDay(e, addDays(start, i))) return true;
    }
    return false;
  });
  if (rows.length === 0) return null;

  return (
    <div className="flex border-b border-border">
      {/* Gutter spacer matching the hour gutter width + header row */}
      <div className="w-12 flex-shrink-0 flex items-start justify-end pr-2 pt-1">
        <span className="text-text-dim text-[10px] uppercase">all-day</span>
      </div>
      {/* Span grid: header row height (h-8) reserved by the day columns
          below; here we lay bars over a days-wide grid. */}
      <div
        className="flex-1 min-w-0 py-1"
        style={{
          display: "grid",
          gridTemplateColumns: `repeat(${days}, minmax(0, 1fr))`,
          gridAutoRows: "minmax(20px, auto)",
          rowGap: "2px",
        }}
      >
        {rows.map((ev) => {
          const cal = calendarById.get(ev.calendar_id);
          const color = cal?.color || "#3b82f6";
          // First/last covered column within the window.
          let firstCol = -1;
          let lastCol = -1;
          for (let i = 0; i < days; i++) {
            if (occCoversDay(ev, addDays(start, i))) {
              if (firstCol === -1) firstCol = i;
              lastCol = i;
            }
          }
          if (firstCol === -1) return null;
          const continuesLeft = !sameDay(
            eventDate(ev.start_at, ev.all_day),
            addDays(start, firstCol),
          );
          const endsHere = !occCoversDay(ev, addDays(start, lastCol + 1));
          const continuesRight = !endsHere && lastCol === days - 1;
          return (
            <button
              key={ev.id + "-" + ev.occurrence_start_at}
              type="button"
              onClick={() => onEventClick(ev)}
              className="text-xs text-left text-bg truncate hover:opacity-90 transition-opacity"
              title={ev.title}
              style={{
                gridColumnStart: firstCol + 1,
                gridColumnEnd: lastCol + 2,
                backgroundColor: color,
                paddingTop: "2px",
                paddingBottom: "2px",
                paddingLeft: "6px",
                paddingRight: "6px",
                marginLeft: "1px",
                marginRight: "1px",
                borderTopLeftRadius: continuesLeft ? "0" : "4px",
                borderBottomLeftRadius: continuesLeft ? "0" : "4px",
                borderTopRightRadius: continuesRight ? "0" : "4px",
                borderBottomRightRadius: continuesRight ? "0" : "4px",
              }}
            >
              {continuesLeft ? "‹ " : ""}
              {ev.title}
              {continuesRight ? " ›" : ""}
            </button>
          );
        })}
      </div>
    </div>
  );
}

// SNAP_MIN is the drag/resize quantum — matches the empty-click slot
// resolution above. Aligned to 15 because most calendars do, and
// because anything smaller produces visual jitter at HOUR_HEIGHT=48.
const SNAP_MIN = 15;
// DRAG_THRESHOLD_PX — pointer must move more than this before we treat
// the gesture as a drag. Anything under = click → opens edit drawer.
const DRAG_THRESHOLD_PX = 3;
// RESIZE_HANDLE_PX — bottom strip of an event block that grabs as a
// resize handle. Tuned so 20px-tall (15-min) events still have a
// usable handle without eating the whole block.
const RESIZE_HANDLE_PX = 6;

interface DragState {
  kind: "move" | "resize";
  ev: Occurrence;
  // Pointer position when the drag started.
  anchorClientX: number;
  anchorClientY: number;
  // Pixel-to-time conversion: the rect of the source column at drag
  // start. We use its width for cross-day column detection later.
  origStart: Date;
  origEnd: Date;
  // Current preview state — drives the rendered position while
  // dragging, committed on pointer up.
  currentStart: Date;
  currentEnd: Date;
  moved: boolean;
}

function DayColumn({
  date,
  events,
  calendarById,
  onEmptyClick,
  onEventClick,
  onEventCommit,
  drag,
  setDrag,
}: {
  date: Date;
  drag: DragState | null;
  setDrag: React.Dispatch<React.SetStateAction<DragState | null>>;
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEmptyClick: (start: Date) => void;
  onEventClick: (e: Occurrence) => void;
  onEventCommit: (e: Occurrence, newStart: Date, newEnd: Date) => void;
}) {
  const layout = useMemo(() => layoutTimedEvents(events), [events]);
  // After a pointerup on an event block (whether it was a click, a
  // drag, or a resize), the browser still synthesizes a `click` event
  // that bubbles up to the column's onClick — and that handler treats
  // it as "user clicked an empty slot" and opens the new-event modal.
  // setDrag(null) in pointerup runs synchronously BEFORE that click
  // fires, so the `if (drag) return` guard is already stale. This ref
  // bridges the one-tick gap between pointerup and the click.
  const suppressNextClickRef = useRef(false);
  const dragRef = useRef(drag);
  dragRef.current = drag;
  const sourceDragging = !!drag && sameDay(drag.origStart, date);

  // Document-level pointer listeners while dragging — events that
  // start in an event block continue tracking even when the pointer
  // crosses into another column or off the grid entirely.
  useEffect(() => {
    if (!sourceDragging) return;
    const onMove = (e: PointerEvent) => {
      const drag = dragRef.current;
      if (!drag) return;
      const dx = e.clientX - drag.anchorClientX;
      const dy = e.clientY - drag.anchorClientY;
      const moved = drag.moved || Math.hypot(dx, dy) > DRAG_THRESHOLD_PX;
      const deltaMin =
        Math.round(((dy / HOUR_HEIGHT) * 60) / SNAP_MIN) * SNAP_MIN;

      if (drag.kind === "resize") {
        // Resize only moves end_at. Clamp to a minimum 15-min window
        // so the user can't accidentally collapse the event to zero.
        const newEnd = new Date(drag.origEnd.getTime() + deltaMin * 60_000);
        const minEnd = new Date(drag.origStart.getTime() + SNAP_MIN * 60_000);
        const clamped = newEnd < minEnd ? minEnd : newEnd;
        setDrag({ ...drag, currentEnd: clamped, moved });
        return;
      }

      // Move: cross-column detection via elementFromPoint → data-day-date.
      // Falls back to the original date if the pointer is outside the grid.
      let targetDate = drag.origStart;
      const el = document.elementFromPoint(
        e.clientX,
        e.clientY,
      ) as HTMLElement | null;
      const dayEl = el?.closest("[data-day-date]") as HTMLElement | null;
      if (dayEl?.dataset.dayDate) {
        targetDate = new Date(dayEl.dataset.dayDate);
      }
      const newStart = new Date(targetDate);
      newStart.setHours(
        drag.origStart.getHours(),
        drag.origStart.getMinutes() + deltaMin,
        0,
        0,
      );
      const dur = drag.origEnd.getTime() - drag.origStart.getTime();
      const newEnd = new Date(newStart.getTime() + dur);
      setDrag({ ...drag, currentStart: newStart, currentEnd: newEnd, moved });
    };
    const onUp = () => {
      const d = dragRef.current;
      if (!d) return;
      setDrag(null);
      // Always set the suppress flag — fires for both click-on-event
      // and drag-released-on-column-background. Cleared on the next
      // task tick, well after the synthesized click bubbles.
      suppressNextClickRef.current = true;
      setTimeout(() => {
        suppressNextClickRef.current = false;
      }, 0);
      // If the pointer barely moved, treat this as a click.
      if (!d.moved) {
        onEventClick(d.ev);
        return;
      }
      const sameStart =
        d.kind === "resize" ||
        d.currentStart.getTime() === d.origStart.getTime();
      const sameEnd = d.currentEnd.getTime() === d.origEnd.getTime();
      if (sameStart && sameEnd) return;
      onEventCommit(d.ev, d.currentStart, d.currentEnd);
    };
    const onCancel = () => setDrag(null);
    document.addEventListener("pointercancel", onCancel);
    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
    return () => {
      document.removeEventListener("pointercancel", onCancel);
      document.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerup", onUp);
    };
  }, [sourceDragging, setDrag, onEventClick, onEventCommit]);

  return (
    <div className="flex-1 min-w-0 border-l border-border">
      <div className="h-8 px-2 py-1 text-text text-xs font-medium border-b border-border">
        {date.toLocaleDateString(undefined, {
          weekday: "short",
          day: "numeric",
        })}
      </div>
      <div
        data-day-date={date.toISOString()}
        className="relative"
        style={{ height: HOUR_HEIGHT * 24 }}
        onClick={(e) => {
          // Suppressed while a drag is in flight, AND for the synthesized
          // click that fires right after a pointerup on an event block —
          // see suppressNextClickRef above for why the drag state is
          // already null by the time we get here.
          if (drag || suppressNextClickRef.current) return;
          const rect = (
            e.currentTarget as HTMLDivElement
          ).getBoundingClientRect();
          const y = e.clientY - rect.top;
          const hour = Math.floor(y / HOUR_HEIGHT);
          const minute =
            Math.floor(
              (((y - hour * HOUR_HEIGHT) / HOUR_HEIGHT) * 60) / SNAP_MIN,
            ) * SNAP_MIN;
          const slot = new Date(date);
          slot.setHours(hour, minute, 0, 0);
          onEmptyClick(slot);
        }}
      >
        {/* Hour grid lines */}
        {Array.from({ length: 24 }, (_, h) => (
          <div
            key={h}
            style={{ top: h * HOUR_HEIGHT, height: HOUR_HEIGHT }}
            className="absolute left-0 right-0 border-t border-border/50"
          />
        ))}
        {/* Events */}
        {events.map((ev) => {
          const cal = calendarById.get(ev.calendar_id);
          // When this event is the one being dragged, render at its
          // preview position so the user sees the destination.
          const isDragging =
            drag != null &&
            drag.ev.id === ev.id &&
            drag.ev.occurrence_start_at === ev.occurrence_start_at;
          const start =
            isDragging && drag ? drag.currentStart : new Date(ev.start_at);
          const end =
            isDragging && drag ? drag.currentEnd : new Date(ev.end_at);
          // While dragging across days, hide the original-day block —
          // it's now rendered on the destination column via its own
          // data-day-date match.
          if (
            isDragging &&
            drag &&
            drag.kind === "move" &&
            !sameDay(start, date)
          ) {
            return null;
          }
          const top =
            ((start.getHours() * 60 + start.getMinutes()) / 60) * HOUR_HEIGHT;
          const height = Math.max(
            20,
            ((end.getTime() - start.getTime()) / 1000 / 60 / 60) * HOUR_HEIGHT,
          );
          const draggable = !ev.all_day; // all-day events fall back to dialog-only edits
          return (
            <div
              key={ev.id + "-" + ev.occurrence_start_at}
              role="button"
              tabIndex={0}
              aria-label={`${ev.title}, ${fmtTime(start)} to ${fmtTime(end)}`}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onEventClick(ev);
                }
              }}
              className="absolute left-1 right-1 rounded px-1.5 py-0.5 text-left overflow-hidden text-bg hover:opacity-90 transition-opacity"
              style={{
                top,
                height,
                left: `calc(${((layout.get(eventKey(ev))?.column ?? 0) * 100) / (layout.get(eventKey(ev))?.columns ?? 1)}% + 2px)`,
                right: "auto",
                width: `calc(${100 / (layout.get(eventKey(ev))?.columns ?? 1)}% - 4px)`,
                backgroundColor: cal?.color || "#3b82f6",
                cursor: !draggable
                  ? "pointer"
                  : isDragging
                    ? "grabbing"
                    : "grab",
                opacity: isDragging ? 0.85 : 1,
                userSelect: "none",
                touchAction: "none",
                zIndex: isDragging ? 10 : 1,
              }}
              onPointerDown={(e) => {
                if (e.button !== 0) return;
                if (!draggable) {
                  onEventClick(ev);
                  return;
                }
                // Resize zone: bottom strip.
                const rect = (
                  e.currentTarget as HTMLDivElement
                ).getBoundingClientRect();
                const offsetFromBottom = rect.bottom - e.clientY;
                const kind: "move" | "resize" =
                  offsetFromBottom <= RESIZE_HANDLE_PX ? "resize" : "move";
                (e.currentTarget as HTMLDivElement).setPointerCapture(
                  e.pointerId,
                );
                e.stopPropagation();
                setDrag({
                  kind,
                  ev,
                  anchorClientX: e.clientX,
                  anchorClientY: e.clientY,
                  origStart: new Date(ev.start_at),
                  origEnd: new Date(ev.end_at),
                  currentStart: new Date(ev.start_at),
                  currentEnd: new Date(ev.end_at),
                  moved: false,
                });
              }}
            >
              <div className="text-[11px] font-medium truncate">{ev.title}</div>
              <div className="text-[10px] opacity-80">
                {fmtTime(start)} – {fmtTime(end)}
              </div>
              {draggable && (
                <div
                  className="absolute left-0 right-0 bottom-0"
                  style={{ height: RESIZE_HANDLE_PX, cursor: "ns-resize" }}
                />
              )}
            </div>
          );
        })}

        {/* When a move drag enters THIS column from another, render
            a ghost block so the user sees the destination slot before
            committing. The "isDragging" branch above hides the source-
            column copy, so this preview is the only one visible. */}
        {drag &&
          drag.kind === "move" &&
          sameDay(drag.currentStart, date) &&
          !events.some(
            (e) =>
              e.id === drag.ev.id &&
              e.occurrence_start_at === drag.ev.occurrence_start_at &&
              sameDay(new Date(e.start_at), date),
          ) && (
            <DragGhost
              start={drag.currentStart}
              end={drag.currentEnd}
              color={calendarById.get(drag.ev.calendar_id)?.color || "#3b82f6"}
              title={drag.ev.title}
            />
          )}
      </div>
    </div>
  );
}

function DragGhost({
  start,
  end,
  color,
  title,
}: {
  start: Date;
  end: Date;
  color: string;
  title: string;
}) {
  const top = ((start.getHours() * 60 + start.getMinutes()) / 60) * HOUR_HEIGHT;
  const height = Math.max(
    20,
    ((end.getTime() - start.getTime()) / 1000 / 60 / 60) * HOUR_HEIGHT,
  );
  return (
    <div
      data-testid="drag-preview"
      className="absolute left-1 right-1 rounded px-1.5 py-0.5 text-left overflow-hidden text-bg pointer-events-none"
      style={{ top, height, backgroundColor: color, opacity: 0.85, zIndex: 10 }}
    >
      <div className="text-[11px] font-medium truncate">{title}</div>
      <div className="text-[10px] opacity-80">
        {fmtTime(start)} – {fmtTime(end)}
      </div>
    </div>
  );
}

export function sameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

function ymdKey(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

// Local midnight for a date (strips time-of-day).
export function startOfDay(d: Date): Date {
  const out = new Date(d);
  out.setHours(0, 0, 0, 0);
  return out;
}

// isMultiDay — does this occurrence span more than one calendar day, or
// is it flagged all-day? These get the spanning-bar / all-day-strip
// treatment instead of being drawn as a timed block. We subtract 1ms
// from end_at so an event that ends exactly at midnight (a clean
// single-day block) doesn't count the following day. A genuine
// multi-day trip (Paris: 20 May → 12 Jun) and any all-day event match.
function isMultiDay(ev: Occurrence): boolean {
  if (ev.all_day) return true;
  const s = new Date(ev.start_at);
  const e = new Date(ev.end_at);
  if (e.getTime() <= s.getTime()) return false;
  return !sameDay(s, new Date(e.getTime() - 1));
}

// occCoversDay — true if the occurrence overlaps the calendar day `d`
// (inclusive of the start day, exclusive of a midnight-exact end).
export function occCoversDay(ev: Occurrence, d: Date): boolean {
  const dayStart = startOfDay(d).getTime();
  const dayEnd = addDays(startOfDay(d), 1).getTime();
  const s = eventDate(ev.start_at, ev.all_day).getTime();
  const eRaw = eventDate(ev.end_at, ev.all_day).getTime();
  const e = Math.max(eRaw - 1, s); // half-open end; never before start
  return s < dayEnd && e >= dayStart;
}

// dayIndexInWindow — 0-based column index of date `d` within a window
// that starts at `windowStart`, or -1 if outside [0, days).
function dayIndexInWindow(windowStart: Date, days: number, d: Date): number {
  const a = startOfDay(windowStart).getTime();
  const b = startOfDay(d).getTime();
  const idx = Math.round((b - a) / (24 * 60 * 60 * 1000));
  return idx >= 0 && idx < days ? idx : -1;
}

// --- Month view -----------------------------------------------------

const MONTH_MAX_CHIPS = 3;
// Vertical layout of a month cell: date-number row, then N lanes of
// multi-day spanning bars, then single-day chips below them.
const MONTH_DATE_ROW_H = 24; // px reserved at the top of each cell
const MONTH_LANE_H = 20; // px per multi-day bar lane (incl. gap)

interface WeekSeg {
  ev: Occurrence;
  startCol: number; // 0-6 within the week
  endCol: number; // 0-6 within the week
  lane: number;
  continuesLeft: boolean; // span began before this week
  continuesRight: boolean; // span continues past this week
}

function MonthView({
  monthAnchor,
  gridStart,
  events,
  calendarById,
  onEmptyClick,
  onDayClick,
  onEventClick,
}: {
  monthAnchor: Date;
  gridStart: Date;
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEmptyClick: (d: Date) => void;
  onDayClick: (d: Date) => void;
  onEventClick: (e: Occurrence) => void;
}) {
  const today = new Date();
  const month = monthAnchor.getMonth();

  // Single-day events bucket by their start day -> per-cell chips.
  const singleByDay = useMemo(() => {
    const single = new Map<string, Occurrence[]>();
    for (const e of events) {
      if (isMultiDay(e)) continue;
      const key = ymdKey(new Date(e.start_at));
      if (!single.has(key)) single.set(key, []);
      single.get(key)!.push(e);
    }
    for (const arr of single.values())
      arr.sort((a, b) => a.start_at.localeCompare(b.start_at));
    return single;
  }, [events]);

  // Multi-day events become spanning segments, computed per week so a
  // single <button> spans a whole column range — that's what makes the
  // bar truly continuous (no per-cell gaps). Each segment gets a lane
  // so overlapping trips stack instead of colliding.
  const weeks = useMemo(() => {
    const multi = events.filter(isMultiDay);
    const out: { days: Date[]; segs: WeekSeg[]; laneCount: number }[] = [];
    for (let w = 0; w < 6; w++) {
      const weekStart = addDays(gridStart, w * 7);
      const days = Array.from({ length: 7 }, (_, i) => addDays(weekStart, i));
      const raw = multi
        .map((ev): WeekSeg | null => {
          let startCol = -1;
          let endCol = -1;
          for (let c = 0; c < 7; c++) {
            if (occCoversDay(ev, days[c])) {
              if (startCol === -1) startCol = c;
              endCol = c;
            }
          }
          if (startCol === -1) return null;
          return {
            ev,
            startCol,
            endCol,
            lane: 0,
            continuesLeft: occCoversDay(ev, addDays(weekStart, -1)),
            continuesRight: occCoversDay(ev, addDays(weekStart, 7)),
          };
        })
        .filter((s): s is WeekSeg => s !== null)
        // Longest spans first, then earliest, then id — keeps lane
        // assignment stable so a bar doesn't jump lanes across weeks.
        .sort(
          (a, b) =>
            b.endCol - b.startCol - (a.endCol - a.startCol) ||
            a.startCol - b.startCol ||
            a.ev.id - b.ev.id,
        );
      // Greedy lane packing: first lane whose last-used column is left
      // of this segment's start.
      const laneLastCol: number[] = [];
      for (const seg of raw) {
        let lane = 0;
        while (lane < laneLastCol.length && laneLastCol[lane] >= seg.startCol)
          lane++;
        laneLastCol[lane] = seg.endCol;
        seg.lane = lane;
      }
      out.push({ days, segs: raw, laneCount: laneLastCol.length });
    }
    return out;
  }, [events, gridStart]);

  const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

  return (
    <div className="h-full flex flex-col">
      <div
        className="border-b border-border"
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(7, minmax(0, 1fr))",
        }}
      >
        {weekdays.map((w) => (
          <div key={w} className="px-2 py-1 text-text-dim text-xs uppercase">
            {w}
          </div>
        ))}
      </div>
      <div className="flex-1 min-h-0 flex flex-col">
        {weeks.map((week, wi) => (
          <div
            key={wi}
            className="relative flex-1"
            style={{
              minHeight: MONTH_DATE_ROW_H + week.laneCount * MONTH_LANE_H + 96,
              display: "grid",
              gridTemplateColumns: "repeat(7, minmax(0, 1fr))",
            }}
          >
            {week.days.map((d) => {
              const inMonth = d.getMonth() === month;
              const isToday = sameDay(d, today);
              const single = singleByDay.get(ymdKey(d)) || [];
              const visibleSingle = single.slice(0, MONTH_MAX_CHIPS);
              const extra = single.length - visibleSingle.length;
              return (
                <div
                  key={d.toISOString()}
                  onClick={() => onEmptyClick(d)}
                  className={
                    "border-r border-b border-border overflow-hidden min-h-0 cursor-pointer " +
                    (inMonth
                      ? "bg-bg hover:bg-bg-card"
                      : "bg-bg-card hover:bg-bg-input")
                  }
                  style={{ paddingLeft: "4px", paddingRight: "4px" }}
                >
                  <div
                    className="flex items-center justify-between"
                    style={{ height: MONTH_DATE_ROW_H }}
                  >
                    <button
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        onDayClick(d);
                      }}
                      className={
                        "text-xs leading-none rounded-full w-6 h-6 inline-flex items-center justify-center transition-colors " +
                        (isToday
                          ? "bg-accent text-bg font-medium"
                          : inMonth
                            ? "text-text hover:bg-bg-input"
                            : "text-text-dim hover:bg-bg-input")
                      }
                      title={d.toLocaleDateString()}
                    >
                      {d.getDate()}
                    </button>
                  </div>
                  {/* Reserve room for the multi-day lanes above the chips. */}
                  <div style={{ height: week.laneCount * MONTH_LANE_H }} />
                  <div
                    className="flex flex-col overflow-hidden"
                    style={{ gap: "2px" }}
                  >
                    {visibleSingle.map((ev) => {
                      const cal = calendarById.get(ev.calendar_id);
                      return (
                        <button
                          key={ev.id + "-" + ev.occurrence_start_at}
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation();
                            onEventClick(ev);
                          }}
                          className="flex items-center gap-1 px-1 rounded text-xs text-left hover:bg-bg-input min-w-0"
                          style={{ paddingTop: "2px", paddingBottom: "2px" }}
                          title={ev.title}
                        >
                          <span
                            className="rounded-full flex-shrink-0"
                            style={{
                              width: "6px",
                              height: "6px",
                              backgroundColor: cal?.color || "#3b82f6",
                            }}
                          />
                          {!ev.all_day && (
                            <span className="text-text-dim flex-shrink-0">
                              {fmtTime(new Date(ev.start_at))}
                            </span>
                          )}
                          <span className="text-text truncate">{ev.title}</span>
                        </button>
                      );
                    })}
                    {extra > 0 && (
                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation();
                          onDayClick(d);
                        }}
                        className="text-xs text-text-muted hover:text-text text-left px-1"
                      >
                        +{extra} more
                      </button>
                    )}
                  </div>
                </div>
              );
            })}
            {/* Spanning bars: absolute overlay so each event is one
                element across its column range (truly continuous). */}
            <div className="absolute inset-0 pointer-events-none">
              {week.segs.map((seg) => {
                const cal = calendarById.get(seg.ev.calendar_id);
                const color = cal?.color || "#3b82f6";
                const span = seg.endCol - seg.startCol + 1;
                return (
                  <button
                    key={seg.ev.id + "-" + seg.ev.occurrence_start_at}
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation();
                      onEventClick(seg.ev);
                    }}
                    className="absolute text-xs text-left text-bg truncate hover:opacity-90 transition-opacity"
                    title={seg.ev.title}
                    style={{
                      top: MONTH_DATE_ROW_H + seg.lane * MONTH_LANE_H,
                      left: `calc(100% * ${seg.startCol} / 7)`,
                      width: `calc(100% * ${span} / 7)`,
                      height: MONTH_LANE_H - 2,
                      backgroundColor: color,
                      pointerEvents: "auto",
                      paddingLeft: "6px",
                      paddingRight: "6px",
                      marginLeft: seg.continuesLeft ? "0" : "2px",
                      marginRight: seg.continuesRight ? "0" : "2px",
                      boxSizing: "border-box",
                      display: "flex",
                      alignItems: "center",
                      borderTopLeftRadius: seg.continuesLeft ? "0" : "4px",
                      borderBottomLeftRadius: seg.continuesLeft ? "0" : "4px",
                      borderTopRightRadius: seg.continuesRight ? "0" : "4px",
                      borderBottomRightRadius: seg.continuesRight ? "0" : "4px",
                    }}
                  >
                    {seg.continuesLeft ? "< " : ""}
                    {seg.ev.title}
                  </button>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

// --- Year view ------------------------------------------------------

function YearView({
  year,
  events,
  calendarById,
  onDayClick,
  onMonthClick,
}: {
  year: number;
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onDayClick: (d: Date) => void;
  onMonthClick: (d: Date) => void;
}) {
  const today = new Date();
  // Per-day marker for the mini calendars. Single-day events get a
  // centered dot; multi-day events get a span flag so MiniMonth can
  // draw a connected bottom bar across the days they cover (otherwise
  // a long trip is invisible at mini scale).
  const dayMark = useMemo(() => {
    const map = new Map<string, { color: string; span: boolean }>();
    const sorted = [...events].sort((a, b) =>
      a.start_at.localeCompare(b.start_at),
    );
    for (const e of sorted) {
      const cal = calendarById.get(e.calendar_id);
      const color = cal?.color || "#3b82f6";
      if (isMultiDay(e)) {
        const original = startOfDay(eventDate(e.start_at, e.all_day));
        const s =
          original < new Date(year, 0, 1) ? new Date(year, 0, 1) : original;
        // Cap the walk so a pathological end date can't spin forever.
        for (let i = 0; i < 366; i++) {
          const d = addDays(s, i);
          if (!occCoversDay(e, d)) break;
          const key = ymdKey(d);
          const existing = map.get(key);
          // A span marker wins over a plain dot on the same day.
          if (!existing || !existing.span) map.set(key, { color, span: true });
        }
      } else {
        const key = ymdKey(new Date(e.start_at));
        if (!map.has(key)) map.set(key, { color, span: false });
      }
    }
    return map;
  }, [events, calendarById, year]);

  const months = Array.from({ length: 12 }, (_, i) => new Date(year, i, 1));
  return (
    <div
      className="p-4"
      style={{
        display: "grid",
        gap: "1.25rem",
        // Larger min so we land at ~3-4 mini calendars per row instead
        // of cramming 5-6 tiny ones in.
        gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 280px), 1fr))",
      }}
    >
      {months.map((m) => (
        <MiniMonth
          key={m.getMonth()}
          month={m}
          today={today}
          dayMark={dayMark}
          onDayClick={onDayClick}
          onMonthClick={onMonthClick}
        />
      ))}
    </div>
  );
}

function MiniMonth({
  month,
  today,
  dayMark,
  onDayClick,
  onMonthClick,
}: {
  month: Date;
  today: Date;
  dayMark: Map<string, { color: string; span: boolean }>;
  onDayClick: (d: Date) => void;
  onMonthClick: (d: Date) => void;
}) {
  const start = startOfMonthGrid(month);
  const m = month.getMonth();
  const cells = Array.from({ length: 42 }, (_, i) => addDays(start, i));
  // Single-letter weekday header; second T/S share with their pair.
  const weekdays = ["M", "T", "W", "T", "F", "S", "S"];
  return (
    <div className="border border-border rounded p-4 flex flex-col gap-3">
      <button
        type="button"
        onClick={() => onMonthClick(month)}
        className="text-left text-text font-medium text-base hover:text-accent"
      >
        {month.toLocaleDateString(undefined, { month: "long" })}
      </button>
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(7, minmax(0, 1fr))",
        }}
      >
        {weekdays.map((w, i) => (
          <div
            key={i}
            className="text-center text-text-dim text-xs"
            style={{ paddingTop: "2px", paddingBottom: "2px" }}
          >
            {w}
          </div>
        ))}
        {cells.map((d) => {
          const inMonth = d.getMonth() === m;
          const isToday = sameDay(d, today);
          const mark = inMonth ? dayMark.get(ymdKey(d)) : undefined;
          return (
            <button
              key={d.toISOString()}
              type="button"
              onClick={() => onDayClick(d)}
              className="relative flex flex-col items-center transition-colors"
              style={{
                paddingTop: "3px",
                paddingBottom: "11px",
                minHeight: "2.5rem",
              }}
              title={d.toLocaleDateString()}
            >
              <span
                className={
                  "w-7 h-7 rounded-full text-sm flex items-center justify-center " +
                  (isToday
                    ? "bg-accent text-bg font-medium"
                    : inMonth
                      ? "text-text hover:bg-bg-input"
                      : "text-text-dim hover:bg-bg-input")
                }
              >
                {d.getDate()}
              </span>
              {mark &&
                !isToday &&
                (mark.span ? (
                  // Connected bottom bar: spans the full cell width so
                  // adjacent covered days touch edge-to-edge and a
                  // multi-day span reads as one band.
                  <span
                    className="absolute"
                    style={{
                      bottom: "4px",
                      left: 0,
                      right: 0,
                      height: "3px",
                      backgroundColor: mark.color,
                    }}
                  />
                ) : (
                  <span
                    className="absolute rounded-full"
                    style={{
                      bottom: "4px",
                      left: "50%",
                      transform: "translateX(-50%)",
                      width: "4px",
                      height: "4px",
                      backgroundColor: mark.color,
                    }}
                  />
                ))}
            </button>
          );
        })}
      </div>
    </div>
  );
}

// --- Agenda view ----------------------------------------------------

function Agenda({
  events,
  calendarById,
  onEventClick,
}: {
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEventClick: (e: Occurrence) => void;
}) {
  const grouped = useMemo(() => {
    const byDay = new Map<string, Occurrence[]>();
    for (const e of events) {
      const d = new Date(e.start_at);
      const key = e.all_day ? e.start_at.slice(0, 10) : ymdKey(d);
      if (!byDay.has(key)) byDay.set(key, []);
      byDay.get(key)!.push(e);
    }
    return Array.from(byDay.entries()).sort();
  }, [events]);

  if (grouped.length === 0) {
    return (
      <div className="py-12 text-center text-text-muted text-sm">
        Nothing scheduled in the next 30 days.
      </div>
    );
  }
  return (
    <div className="p-4 flex flex-col gap-3">
      {grouped.map(([dateKey, dayEvents]) => (
        <div key={dateKey}>
          <div className="text-text-muted text-xs uppercase mb-1">
            {fmtDay(new Date(dateKey + "T00:00:00"))}
          </div>
          <div className="flex flex-col gap-1">
            {dayEvents.map((ev) => {
              const cal = calendarById.get(ev.calendar_id);
              return (
                <button
                  key={ev.id + "-" + ev.occurrence_start_at}
                  onClick={() => onEventClick(ev)}
                  className="text-left px-3 py-2 border border-border rounded hover:border-accent flex items-center gap-3"
                >
                  <span
                    className="w-2 h-8 rounded flex-shrink-0"
                    style={{ backgroundColor: cal?.color || "#3b82f6" }}
                  />
                  <div className="flex-1 min-w-0">
                    <div className="text-text text-sm truncate">{ev.title}</div>
                    <div className="text-text-dim text-xs">
                      {ev.all_day
                        ? "All day"
                        : `${fmtTime(new Date(ev.start_at))} – ${fmtTime(new Date(ev.end_at))}`}
                      {ev.location && <span> · {ev.location}</span>}
                    </div>
                  </div>
                  {ev.is_recurring && (
                    <span className="text-text-dim text-[10px] uppercase">
                      recurs
                    </span>
                  )}
                </button>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}

// --- Calendar create/edit dialog -----------------------------------

const PRESET_COLORS = [
  "#3b82f6",
  "#22c55e",
  "#f59e0b",
  "#ec4899",
  "#8b5cf6",
  "#94a3b8",
  "#ef4444",
];

function CalendarDialog({
  existing,
  onClose,
  onSaved,
  setStatus,
}: {
  existing?: Calendar;
  onClose: () => void;
  onSaved: () => void;
  setStatus: (s: string) => void;
}) {
  const confirm = useConfirm();
  const api = useCalendarApi();
  const [error, setError] = useState("");
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [name, setName] = useState(existing?.name || "");
  const [color, setColor] = useState(existing?.color || PRESET_COLORS[0]);
  const [kind, setKind] = useState(existing?.kind || "custom");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      if (existing) {
        const res = await api(`/calendars/${existing.id}`, {
          method: "PATCH",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name, color, kind, enabled }),
        });
        if (!res.ok) {
          setError("Update: " + (await res.text()));
          return;
        }
      } else {
        const res = await api(`/calendars`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name, color, kind, enabled }),
        });
        if (!res.ok) {
          setError("Create: " + (await res.text()));
          return;
        }
      }
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!existing) return;
    if (
      !(await confirm({
        title: `Delete "${existing.name}"?`,
        message: "All its events go with it. This can't be undone.",
        confirmLabel: "Delete calendar",
      }))
    )
      return;
    try {
      setBusy(true);
      const res = await api(`/calendars/${existing.id}`, { method: "DELETE" });
      if (!res.ok) throw new Error(await res.text());
      onSaved();
    } catch (e) {
      setError("Delete: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      onClose={onClose}
      title={existing ? "Edit calendar" : "New calendar"}
    >
      {error && (
        <p role="alert" className="text-error">
          {error}
        </p>
      )}
      <label>
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />{" "}
        Enabled
      </label>
      <input
        type="text"
        value={name}
        onChange={(e) => setName(e.target.value)}
        aria-label="Calendar name"
        placeholder="Name"
        autoFocus
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      />
      <select
        aria-label="Calendar kind"
        value={kind}
        onChange={(e) => setKind(e.target.value)}
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      >
        <option value="personal">Personal</option>
        <option value="work">Work</option>
        <option value="holidays">Holidays</option>
        <option value="blocked">Blocked time</option>
        <option value="custom">Custom</option>
      </select>
      <div className="flex gap-2 flex-wrap">
        {PRESET_COLORS.map((c) => (
          <button
            key={c}
            aria-label={`Color ${c}`}
            onClick={() => setColor(c)}
            className={
              "w-7 h-7 rounded-full border-2 " +
              (color === c ? "border-text" : "border-transparent")
            }
            style={{ backgroundColor: c }}
          />
        ))}
      </div>
      <div className="flex gap-2 justify-end items-center">
        {existing && (
          <button
            onClick={remove}
            className="px-3 py-1.5 text-sm text-error hover:text-error mr-auto"
          >
            Delete
          </button>
        )}
        <button
          onClick={onClose}
          className="px-3 py-1.5 text-sm text-text-muted"
        >
          Cancel
        </button>
        <button
          onClick={save}
          disabled={!name.trim() || busy}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
        >
          {existing ? "Save" : "Create"}
        </button>
      </div>
    </Dialog>
  );
}

// --- Event create/edit dialog --------------------------------------

function EventDialog({
  existing,
  defaults,
  calendars,
  onClose,
  onSaved,
}: {
  existing?: Occurrence;
  defaults?: { start: Date; calendarId?: number };
  calendars: Calendar[];
  onClose: () => void;
  onSaved: () => void;
  setStatus: (s: string) => void;
}) {
  const api = useCalendarApi();
  const confirm = useConfirm();
  const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const [timezone, setTimezone] = useState(existing?.timezone || browserZone);
  const [allDay, setAllDay] = useState(existing?.all_day ?? false);
  const [scope, setScope] = useState<"this" | "this_and_following" | "all">(
    existing?.is_recurring ? "this" : "all",
  );
  const [master, setMaster] = useState<{
    start_at: string;
    end_at: string;
  } | null>(null);
  const initialStart = existing?.start_at ?? defaults!.start.toISOString();
  const initialEnd =
    existing?.end_at ??
    new Date(defaults!.start.getTime() + 30 * 60_000).toISOString();
  const inputTime = (value: string, day = allDay, zone = timezone) =>
    day ? value.slice(0, 10) : toZonedInput(new Date(value), zone);
  const [startStr, setStartStr] = useState(() => inputTime(initialStart));
  const [endStr, setEndStr] = useState(() => inputTime(initialEnd));
  const [calendarId, setCalendarId] = useState(
    existing?.calendar_id ??
      defaults?.calendarId ??
      calendars.find((c) => c.enabled)?.id ??
      0,
  );
  const [title, setTitle] = useState(existing?.title ?? "");
  const [description, setDescription] = useState(existing?.description ?? "");
  const [location, setLocation] = useState(existing?.location ?? "");
  const [status, setEventStatus] = useState(existing?.status ?? "confirmed");
  const [rule, setRule] = useState(existing?.rrule ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!existing?.is_recurring) return;
    const controller = new AbortController();
    api(`/items/${existing.event_id}`, { signal: controller.signal })
      .then(async (res) => {
        if (!res.ok) throw new Error(await res.text());
        const value = await res.json();
        if (!controller.signal.aborted) setMaster(value);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      });
    return () => controller.abort();
  }, [api, existing?.event_id]);
  const changeScope = (next: typeof scope) => {
    setScope(next);
    setStartStr(
      inputTime(next === "all" && master ? master.start_at : initialStart),
    );
    setEndStr(inputTime(next === "all" && master ? master.end_at : initialEnd));
  };
  const toggleAllDay = (next: boolean) => {
    setAllDay(next);
    if (next) {
      const start = startStr.slice(0, 10);
      const end = endStr.slice(0, 10);
      setStartStr(start);
      setEndStr(
        end > start ? end : ymdKey(addDays(new Date(start + "T12:00:00"), 1)),
      );
    } else {
      setStartStr(startStr.slice(0, 10) + "T09:00");
      setEndStr(startStr.slice(0, 10) + "T09:30");
    }
  };
  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const start = allDay
        ? new Date(startStr + "T00:00:00Z").toISOString()
        : fromZonedInput(startStr, timezone).toISOString();
      const end = allDay
        ? new Date(endStr + "T00:00:00Z").toISOString()
        : fromZonedInput(endStr, timezone).toISOString();
      if (end <= start) throw new Error("End must be after start.");
      if (existing?.is_recurring && scope === "all" && !master)
        throw new Error("Wait for the original series to load.");
      const body: Record<string, unknown> = {
        title,
        description,
        location,
        calendar_id: calendarId,
        all_day: allDay,
        timezone,
        status,
      };
      if (!existing || (scope !== "this" && rule !== existing.rrule))
        body.rrule = rule;
      if (existing) {
        Object.assign(
          body,
          eventEditTimes(existing, scope, start, end, master),
        );
      } else {
        body.start_at = start;
        body.end_at = end;
      }
      const res = await api(
        existing ? `/items/${existing.event_id}` : "/items",
        {
          method: existing ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      );
      if (!res.ok) throw new Error(await res.text());
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!existing) return;
    const affected = existing.is_recurring
      ? scope === "all"
        ? "the entire series"
        : scope === "this_and_following"
          ? "this occurrence and all following occurrences"
          : "this occurrence"
      : "this event";
    if (
      !(await confirm({
        title: `Delete ${affected}?`,
        message: existing.title,
        confirmLabel: "Delete",
      }))
    )
      return;
    setBusy(true);
    setError("");
    try {
      const res = await api(`/items/${existing.event_id}`, {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          scope,
          occurrence_start_at: existing.occurrence_start_at,
        }),
      });
      if (!res.ok) throw new Error(await res.text());
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const inputClass =
    "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm";
  return (
    <Dialog onClose={onClose} title={existing ? "Edit event" : "New event"}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        {error && (
          <p role="alert" className="text-error text-sm">
            {error}
          </p>
        )}
        {existing?.is_recurring && (
          <label className="text-sm">
            Apply changes to
            <select
              aria-label="Apply changes to"
              className={inputClass}
              value={scope}
              onChange={(e) => changeScope(e.target.value as typeof scope)}
            >
              <option value="this">This occurrence</option>
              <option value="this_and_following">This and following</option>
              <option value="all" disabled={!master}>
                Entire series
              </option>
            </select>
          </label>
        )}
        <label className="text-sm">
          Title
          <input
            autoFocus
            required
            maxLength={500}
            className={inputClass}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
        </label>
        <label className="text-sm">
          Calendar
          <select
            className={inputClass}
            value={calendarId}
            onChange={(e) => setCalendarId(Number(e.target.value))}
          >
            {calendars.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
                {!c.enabled ? " (disabled)" : ""}
              </option>
            ))}
          </select>
        </label>
        <label className="text-sm">
          <input
            type="checkbox"
            checked={allDay}
            onChange={(e) => toggleAllDay(e.target.checked)}
          />{" "}
          All day
        </label>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          <label className="text-sm">
            Start
            <input
              required
              className={inputClass}
              type={allDay ? "date" : "datetime-local"}
              value={startStr}
              onChange={(e) => setStartStr(e.target.value)}
            />
          </label>
          <label className="text-sm">
            {allDay ? "End (exclusive)" : "End"}
            <input
              required
              className={inputClass}
              type={allDay ? "date" : "datetime-local"}
              value={endStr}
              onChange={(e) => setEndStr(e.target.value)}
            />
          </label>
        </div>
        {!allDay && (
          <label className="text-sm">
            Timezone
            <input
              className={inputClass}
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
              placeholder="Europe/Madrid"
            />
            <span className="text-text-muted text-xs">
              Times above use this timezone. Recurrence keeps local wall time.
            </span>
          </label>
        )}
        {(!existing?.is_recurring || scope !== "this") && (
          <label className="text-sm">
            Repeat
            <select
              aria-label="Repeat preset"
              className={inputClass}
              value={
                [
                  "",
                  "FREQ=DAILY",
                  "FREQ=WEEKLY",
                  "FREQ=MONTHLY",
                  "FREQ=YEARLY",
                ].includes(rule)
                  ? rule
                  : "custom"
              }
              onChange={(e) =>
                setRule(
                  e.target.value === "custom"
                    ? "FREQ=WEEKLY;BYDAY=MO,WE"
                    : e.target.value,
                )
              }
            >
              <option value="">Does not repeat</option>
              <option value="FREQ=DAILY">Daily</option>
              <option value="FREQ=WEEKLY">Weekly</option>
              <option value="FREQ=MONTHLY">Monthly</option>
              <option value="FREQ=YEARLY">Yearly</option>
              <option value="custom">Custom rule</option>
            </select>
            {rule && (
              <input
                aria-label="Recurrence rule"
                className={inputClass}
                value={rule}
                onChange={(e) => setRule(e.target.value)}
              />
            )}
          </label>
        )}
        <label className="text-sm">
          Status
          <select
            className={inputClass}
            value={status}
            onChange={(e) => setEventStatus(e.target.value)}
          >
            <option value="confirmed">Confirmed</option>
            <option value="tentative">Tentative</option>
            <option value="cancelled">
              Cancelled (does not block availability)
            </option>
          </select>
        </label>
        <label className="text-sm">
          Location
          <input
            className={inputClass}
            value={location}
            onChange={(e) => setLocation(e.target.value)}
          />
        </label>
        <label className="text-sm">
          Description
          <textarea
            className={inputClass}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </label>
        <div className="flex gap-2 justify-end">
          {existing && (
            <button
              type="button"
              disabled={busy}
              onClick={remove}
              className="text-error mr-auto"
            >
              Delete
            </button>
          )}
          <button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </button>
          <button
            disabled={busy || !title.trim() || !calendarId}
            className="bg-accent text-bg rounded px-3 py-1.5"
          >
            {busy ? "Saving…" : existing ? "Save" : "Create"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

export function eventEditTimes(
  existing: Occurrence,
  scope: string,
  start: string,
  end: string,
  master: { start_at: string; end_at: string } | null,
): Record<string, unknown> {
  const body: Record<string, unknown> = { scope };
  if (existing.is_recurring && scope !== "all")
    body.occurrence_start_at = existing.occurrence_start_at;
  const base = existing.is_recurring && scope === "all" ? master : existing;
  if (!base) throw new Error("Series must load before editing");
  if (new Date(start).getTime() !== new Date(base.start_at).getTime())
    body.start_at = start;
  if (new Date(end).getTime() !== new Date(base.end_at).getTime())
    body.end_at = end;
  return body;
}
export function eventDate(value: string, allDay: boolean): Date {
  return new Date(allDay ? value.slice(0, 10) + "T00:00:00" : value);
}
export function eventKey(e: Occurrence) {
  return e.id + "|" + e.occurrence_start_at;
}
export function layoutTimedEvents(
  events: Occurrence[],
): Map<string, { column: number; columns: number }> {
  const result = new Map<string, { column: number; columns: number }>();
  const sorted = [...events].sort(
    (a, b) =>
      Date.parse(a.start_at) - Date.parse(b.start_at) ||
      Date.parse(b.end_at) - Date.parse(a.end_at),
  );
  let group: Occurrence[] = [];
  let end = 0;
  const pack = () => {
    const ends: number[] = [];
    for (const e of group) {
      const start = Date.parse(e.start_at);
      let column = ends.findIndex((t) => t <= start);
      if (column < 0) column = ends.length;
      ends[column] = Math.max(Date.parse(e.end_at), start + 25 * 60_000);
      result.set(eventKey(e), { column, columns: 0 });
    }
    for (const e of group) result.get(eventKey(e))!.columns = ends.length;
    group = [];
  };
  for (const e of sorted) {
    if (group.length && Date.parse(e.start_at) >= end) pack();
    group.push(e);
    end = Math.max(
      group.length === 1 ? 0 : end,
      Date.parse(e.end_at),
      Date.parse(e.start_at) + 25 * 60_000,
    );
  }
  pack();
  return result;
}
export function toZonedInput(date: Date, timezone: string): string {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date);
  const p = Object.fromEntries(parts.map((x) => [x.type, x.value]));
  return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}`;
}
export function fromZonedInput(value: string, timezone: string): Date {
  const wall = Date.parse(value + "Z");
  if (!Number.isFinite(wall))
    throw new Error("Enter valid start and end dates.");
  let instant = wall;
  for (let i = 0; i < 3; i++) {
    const actual = Date.parse(toZonedInput(new Date(instant), timezone) + "Z");
    instant += wall - actual;
  }
  if (toZonedInput(new Date(instant), timezone) !== value)
    throw new Error(
      "This local time does not exist during the daylight-saving transition.",
    );
  return new Date(instant);
}

function Dialog({
  children,
  onClose,
  title,
}: {
  children: React.ReactNode;
  onClose: () => void;
  title: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const root = ref.current;
    const focused =
      root?.querySelector<HTMLElement>("input, select, textarea") ??
      root?.querySelector<HTMLElement>("button");
    focused?.focus();
    const key = (e: KeyboardEvent) => {
      if (!root || !root.contains(document.activeElement)) return;
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeRef.current();
      }
      if (e.key === "Tab") {
        const items = Array.from(
          root.querySelectorAll<HTMLElement>(
            'button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex="0"]',
          ),
        );
        const first = items[0],
          last = items[items.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last?.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first?.focus();
        }
      }
    };
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("keydown", key);
      previous?.focus();
    };
  }, []);
  return (
    <div
      className="fixed inset-0 bg-black/60 grid place-items-center z-50"
      onClick={onClose}
    >
      <div
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="bg-bg-card border border-border rounded p-4 w-[480px] max-w-[94vw] max-h-[90vh] overflow-auto flex flex-col gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <h2 id={titleId} className="text-text font-medium">
            {title}
          </h2>
          <button aria-label="Close dialog" onClick={onClose}>
            ×
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
