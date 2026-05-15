// CalendarPanel — week + day + agenda views over the calendar app.
//
// Sidebar lists calendars as toggle chips (click to hide a calendar
// from the grid, edit/delete via the row controls). Main grid renders
// events as colored blocks. Click an event → drawer with edit/delete.
// Click an empty cell → create-event dialog pre-filled with that slot.
//
// Live updates via useAppEvents("calendar") — when calendars/events
// change (from another tab or an agent), the UI refreshes.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/calendar";

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

interface Occurrence {
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

type ViewMode = "week" | "day" | "agenda";

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
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
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

function rfc3339(d: Date): string {
  return d.toISOString();
}

function fmtDay(d: Date): string {
  return d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
}

function fmtTime(d: Date): string {
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

// --- Panel -----------------------------------------------------------

export default function CalendarPanel({ projectId }: NativePanelProps) {
  const [calendars, setCalendars] = useState<Calendar[]>([]);
  const [events, setEvents] = useState<Occurrence[]>([]);
  const [view, setView] = useState<ViewMode>("week");
  const [anchor, setAnchor] = useState<Date>(() => new Date());
  const [hidden, setHidden] = useState<Set<number>>(new Set());
  const [status, setStatus] = useState("");
  const [addingCalendar, setAddingCalendar] = useState(false);
  const [editingCalendar, setEditingCalendar] = useState<Calendar | null>(null);
  const [creatingEvent, setCreatingEvent] = useState<{ start: Date; calendarId?: number } | null>(null);
  const [editingEvent, setEditingEvent] = useState<Occurrence | null>(null);

  const windowStart = useMemo(() => {
    if (view === "week") return startOfWeek(anchor);
    if (view === "day") {
      const d = new Date(anchor);
      d.setHours(0, 0, 0, 0);
      return d;
    }
    // agenda
    const d = new Date(anchor);
    d.setHours(0, 0, 0, 0);
    return d;
  }, [view, anchor]);

  const windowEnd = useMemo(() => {
    if (view === "week") return addDays(windowStart, 7);
    if (view === "day") return addDays(windowStart, 1);
    return addDays(windowStart, 30); // agenda: 30 days
  }, [view, windowStart]);

  const loadCalendars = useCallback(async () => {
    try {
      const res = await fetch(`${API}/calendars`, { credentials: "same-origin" });
      const data = await res.json();
      setCalendars(data.calendars || []);
    } catch (e) {
      setStatus("Load calendars: " + (e as Error).message);
    }
  }, []);

  const loadEvents = useCallback(async () => {
    try {
      const res = await fetch(
        `${API}/items?from=${encodeURIComponent(rfc3339(windowStart))}&to=${encodeURIComponent(rfc3339(windowEnd))}`,
        { credentials: "same-origin" },
      );
      if (!res.ok) {
        setStatus(`Load events: ${res.status}`);
        return;
      }
      const data = await res.json();
      setEvents(data.events || []);
    } catch (e) {
      setStatus("Load events: " + (e as Error).message);
    }
  }, [windowStart, windowEnd]);

  useEffect(() => { loadCalendars(); }, [loadCalendars]);
  useEffect(() => { loadEvents(); }, [loadEvents]);

  useAppEvents("calendar", projectId, (ev) => {
    if (ev.topic.startsWith("calendar.")) loadCalendars();
    if (ev.topic.startsWith("event.")) loadEvents();
  });

  const visibleEvents = useMemo(
    () => events.filter((e) => !hidden.has(e.calendar_id)),
    [events, hidden],
  );

  const calendarById = useMemo(() => {
    const m = new Map<number, Calendar>();
    for (const c of calendars) m.set(c.id, c);
    return m;
  }, [calendars]);

  const goPrev = () => setAnchor(addDays(anchor, view === "week" ? -7 : view === "day" ? -1 : -30));
  const goNext = () => setAnchor(addDays(anchor, view === "week" ? 7 : view === "day" ? 1 : 30));
  const goToday = () => setAnchor(new Date());

  // commitEventTimes is the drag/resize commit path. Updates local
  // state optimistically, PATCHes /items/{event_id} with the new
  // window, and refetches on success to pick up any server-side
  // restructuring (notably the child-row creation for recurring
  // events under scope=this).
  const commitEventTimes = useCallback(async (ev: Occurrence, newStart: Date, newEnd: Date) => {
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
      const body: Record<string, unknown> = { start_at: newStartISO, end_at: newEndISO };
      if (ev.is_recurring) {
        // scope=this creates a child row at the new time + adds the
        // original date to the master's exdate.
        body.scope = "this";
        body.occurrence_start_at = ev.occurrence_start_at;
      } else {
        body.scope = "all";
      }
      const res = await fetch(`${API}/items/${ev.event_id}`, {
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
  }, [loadEvents]);

  return (
    <div className="h-full flex">
      {/* Sidebar */}
      <aside className="w-64 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border flex items-center gap-2">
          <span className="text-text font-medium flex-1">Calendars</span>
          <button
            onClick={() => setAddingCalendar(true)}
            className="text-text-muted hover:text-text text-sm"
            title="Add calendar"
          >+</button>
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
        <div className="p-2 border-t border-border text-text-dim text-[10px]">{status}</div>
      </aside>

      {/* Main */}
      <div className="flex-1 flex flex-col min-w-0">
        <header className="flex items-center gap-2 px-4 py-2 border-b border-border">
          <button onClick={goToday} className="px-3 py-1 text-sm border border-border rounded hover:border-accent">Today</button>
          <button onClick={goPrev} className="px-2 py-1 text-sm text-text-muted hover:text-text">‹</button>
          <button onClick={goNext} className="px-2 py-1 text-sm text-text-muted hover:text-text">›</button>
          <span className="text-text font-medium ml-2">
            {view === "week"
              ? `${fmtDay(windowStart)} – ${fmtDay(addDays(windowEnd, -1))}`
              : view === "day"
                ? fmtDay(windowStart)
                : `${fmtDay(windowStart)} → 30 days`}
          </span>
          <div className="ml-auto flex items-center gap-1">
            {(["week", "day", "agenda"] as ViewMode[]).map((v) => (
              <button
                key={v}
                onClick={() => setView(v)}
                className={
                  "px-3 py-1 text-sm rounded " +
                  (view === v ? "bg-bg-card text-text" : "text-text-muted hover:text-text")
                }
              >
                {v.charAt(0).toUpperCase() + v.slice(1)}
              </button>
            ))}
          </div>
        </header>

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
          onSaved={() => { setAddingCalendar(false); loadCalendars(); }}
          setStatus={setStatus}
        />
      )}
      {editingCalendar && (
        <CalendarDialog
          existing={editingCalendar}
          onClose={() => setEditingCalendar(null)}
          onSaved={() => { setEditingCalendar(null); loadCalendars(); loadEvents(); }}
          setStatus={setStatus}
        />
      )}
      {creatingEvent && (
        <EventDialog
          calendars={calendars}
          defaults={creatingEvent}
          onClose={() => setCreatingEvent(null)}
          onSaved={() => { setCreatingEvent(null); loadEvents(); }}
          setStatus={setStatus}
        />
      )}
      {editingEvent && (
        <EventDialog
          calendars={calendars}
          existing={editingEvent}
          onClose={() => setEditingEvent(null)}
          onSaved={() => { setEditingEvent(null); loadEvents(); }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

// --- Sidebar chip ----------------------------------------------------

function CalendarChip({
  cal, hidden, onToggle, onEdit,
}: { cal: Calendar; hidden: boolean; onToggle: () => void; onEdit: () => void }) {
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
        className={"flex-1 text-left text-sm truncate " + (hidden ? "text-text-dim" : "text-text")}
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
  start, days, events, calendarById, onEmptyClick, onEventClick, onEventCommit,
}: {
  start: Date;
  days: number;
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEmptyClick: (start: Date) => void;
  onEventClick: (e: Occurrence) => void;
  onEventCommit: (e: Occurrence, newStart: Date, newEnd: Date) => void;
}) {
  const dayDates = Array.from({ length: days }, (_, i) => addDays(start, i));
  const hours = Array.from({ length: 24 }, (_, i) => i);

  return (
    <div className="flex">
      {/* Hour gutter */}
      <div className="w-12 flex-shrink-0">
        <div className="h-8" />
        {hours.map((h) => (
          <div key={h} style={{ height: HOUR_HEIGHT }} className="text-text-dim text-[10px] text-right pr-2 -mt-2">
            {h.toString().padStart(2, "0")}:00
          </div>
        ))}
      </div>
      {/* Day columns */}
      {dayDates.map((d) => (
        <DayColumn
          key={d.toISOString()}
          date={d}
          events={events.filter((e) => sameDay(new Date(e.start_at), d))}
          calendarById={calendarById}
          onEmptyClick={onEmptyClick}
          onEventClick={onEventClick}
          onEventCommit={onEventCommit}
        />
      ))}
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
  date, events, calendarById, onEmptyClick, onEventClick, onEventCommit,
}: {
  date: Date;
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEmptyClick: (start: Date) => void;
  onEventClick: (e: Occurrence) => void;
  onEventCommit: (e: Occurrence, newStart: Date, newEnd: Date) => void;
}) {
  const [drag, setDrag] = useState<DragState | null>(null);
  // After a pointerup on an event block (whether it was a click, a
  // drag, or a resize), the browser still synthesizes a `click` event
  // that bubbles up to the column's onClick — and that handler treats
  // it as "user clicked an empty slot" and opens the new-event modal.
  // setDrag(null) in pointerup runs synchronously BEFORE that click
  // fires, so the `if (drag) return` guard is already stale. This ref
  // bridges the one-tick gap between pointerup and the click.
  const suppressNextClickRef = useRef(false);

  // Document-level pointer listeners while dragging — events that
  // start in an event block continue tracking even when the pointer
  // crosses into another column or off the grid entirely.
  useEffect(() => {
    if (!drag) return;
    const onMove = (e: PointerEvent) => {
      const dx = e.clientX - drag.anchorClientX;
      const dy = e.clientY - drag.anchorClientY;
      const moved = drag.moved || Math.hypot(dx, dy) > DRAG_THRESHOLD_PX;
      const deltaMin = Math.round((dy / HOUR_HEIGHT) * 60 / SNAP_MIN) * SNAP_MIN;

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
      const el = document.elementFromPoint(e.clientX, e.clientY) as HTMLElement | null;
      const dayEl = el?.closest("[data-day-date]") as HTMLElement | null;
      if (dayEl?.dataset.dayDate) {
        targetDate = new Date(dayEl.dataset.dayDate);
      }
      const newStart = new Date(targetDate);
      newStart.setHours(
        drag.origStart.getHours(),
        drag.origStart.getMinutes() + deltaMin,
        0, 0,
      );
      const dur = drag.origEnd.getTime() - drag.origStart.getTime();
      const newEnd = new Date(newStart.getTime() + dur);
      setDrag({ ...drag, currentStart: newStart, currentEnd: newEnd, moved });
    };
    const onUp = () => {
      const d = drag;
      setDrag(null);
      // Always set the suppress flag — fires for both click-on-event
      // and drag-released-on-column-background. Cleared on the next
      // task tick, well after the synthesized click bubbles.
      suppressNextClickRef.current = true;
      setTimeout(() => { suppressNextClickRef.current = false; }, 0);
      // If the pointer barely moved, treat this as a click.
      if (!d.moved) {
        onEventClick(d.ev);
        return;
      }
      const sameStart = d.kind === "resize" || d.currentStart.getTime() === d.origStart.getTime();
      const sameEnd = d.currentEnd.getTime() === d.origEnd.getTime();
      if (sameStart && sameEnd) return;
      onEventCommit(d.ev, d.currentStart, d.currentEnd);
    };
    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
    return () => {
      document.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerup", onUp);
    };
  }, [drag, onEventClick, onEventCommit]);

  return (
    <div className="flex-1 min-w-0 border-l border-border">
      <div className="h-8 px-2 py-1 text-text text-xs font-medium border-b border-border">
        {date.toLocaleDateString(undefined, { weekday: "short", day: "numeric" })}
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
          const rect = (e.currentTarget as HTMLDivElement).getBoundingClientRect();
          const y = e.clientY - rect.top;
          const hour = Math.floor(y / HOUR_HEIGHT);
          const minute = Math.floor((y - hour * HOUR_HEIGHT) / HOUR_HEIGHT * 60 / SNAP_MIN) * SNAP_MIN;
          const slot = new Date(date);
          slot.setHours(hour, minute, 0, 0);
          onEmptyClick(slot);
        }}
      >
        {/* Hour grid lines */}
        {Array.from({ length: 24 }, (_, h) => (
          <div key={h} style={{ top: h * HOUR_HEIGHT, height: HOUR_HEIGHT }}
               className="absolute left-0 right-0 border-t border-border/50" />
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
          const start = isDragging && drag ? drag.currentStart : new Date(ev.start_at);
          const end = isDragging && drag ? drag.currentEnd : new Date(ev.end_at);
          // While dragging across days, hide the original-day block —
          // it's now rendered on the destination column via its own
          // data-day-date match.
          if (isDragging && drag && drag.kind === "move" && !sameDay(start, date)) {
            return null;
          }
          const top = (start.getHours() * 60 + start.getMinutes()) / 60 * HOUR_HEIGHT;
          const height = Math.max(20, ((end.getTime() - start.getTime()) / 1000 / 60) / 60 * HOUR_HEIGHT);
          const draggable = !ev.all_day; // all-day events fall back to dialog-only edits
          return (
            <div
              key={ev.id + "-" + ev.occurrence_start_at}
              className="absolute left-1 right-1 rounded px-1.5 py-0.5 text-left overflow-hidden text-bg hover:opacity-90 transition-opacity"
              style={{
                top,
                height,
                backgroundColor: cal?.color || "#3b82f6",
                cursor: !draggable ? "pointer" : isDragging ? "grabbing" : "grab",
                opacity: isDragging ? 0.85 : 1,
                userSelect: "none",
                touchAction: "none",
                zIndex: isDragging ? 10 : 1,
              }}
              onPointerDown={(e) => {
                if (e.button !== 0) return;
                if (!draggable) { onEventClick(ev); return; }
                // Resize zone: bottom strip.
                const rect = (e.currentTarget as HTMLDivElement).getBoundingClientRect();
                const offsetFromBottom = rect.bottom - e.clientY;
                const kind: "move" | "resize" = offsetFromBottom <= RESIZE_HANDLE_PX ? "resize" : "move";
                (e.currentTarget as HTMLDivElement).setPointerCapture(e.pointerId);
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
              <div className="text-[10px] opacity-80">{fmtTime(start)} – {fmtTime(end)}</div>
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
        {drag && drag.kind === "move" && sameDay(drag.currentStart, date) &&
          !events.some(e => e.id === drag.ev.id && e.occurrence_start_at === drag.ev.occurrence_start_at && sameDay(new Date(e.start_at), date)) && (
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

function DragGhost({ start, end, color, title }: { start: Date; end: Date; color: string; title: string }) {
  const top = (start.getHours() * 60 + start.getMinutes()) / 60 * HOUR_HEIGHT;
  const height = Math.max(20, ((end.getTime() - start.getTime()) / 1000 / 60) / 60 * HOUR_HEIGHT);
  return (
    <div
      className="absolute left-1 right-1 rounded px-1.5 py-0.5 text-left overflow-hidden text-bg pointer-events-none"
      style={{ top, height, backgroundColor: color, opacity: 0.85, zIndex: 10 }}
    >
      <div className="text-[11px] font-medium truncate">{title}</div>
      <div className="text-[10px] opacity-80">{fmtTime(start)} – {fmtTime(end)}</div>
    </div>
  );
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

// --- Agenda view ----------------------------------------------------

function Agenda({
  events, calendarById, onEventClick,
}: {
  events: Occurrence[];
  calendarById: Map<number, Calendar>;
  onEventClick: (e: Occurrence) => void;
}) {
  const grouped = useMemo(() => {
    const byDay = new Map<string, Occurrence[]>();
    for (const e of events) {
      const d = new Date(e.start_at);
      const key = d.toISOString().slice(0, 10);
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
                      {fmtTime(new Date(ev.start_at))} – {fmtTime(new Date(ev.end_at))}
                      {ev.location && <span> · {ev.location}</span>}
                    </div>
                  </div>
                  {ev.is_recurring && (
                    <span className="text-text-dim text-[10px] uppercase">recurs</span>
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

const PRESET_COLORS = ["#3b82f6", "#22c55e", "#f59e0b", "#ec4899", "#8b5cf6", "#94a3b8", "#ef4444"];

function CalendarDialog({
  existing, onClose, onSaved, setStatus,
}: {
  existing?: Calendar;
  onClose: () => void;
  onSaved: () => void;
  setStatus: (s: string) => void;
}) {
  const [name, setName] = useState(existing?.name || "");
  const [color, setColor] = useState(existing?.color || PRESET_COLORS[0]);
  const [kind, setKind] = useState(existing?.kind || "custom");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      if (existing) {
        const res = await fetch(`${API}/calendars/${existing.id}`, {
          method: "PATCH",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name, color, kind }),
        });
        if (!res.ok) { setStatus("Update: " + (await res.text())); return; }
      } else {
        const res = await fetch(`${API}/calendars`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name, color, kind }),
        });
        if (!res.ok) { setStatus("Create: " + (await res.text())); return; }
      }
      onSaved();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!existing || !confirm(`Delete "${existing.name}" and all its events?`)) return;
    try {
      await fetch(`${API}/calendars/${existing.id}`, { method: "DELETE", credentials: "same-origin" });
      onSaved();
    } catch (e) {
      setStatus("Delete: " + (e as Error).message);
    }
  };

  return (
    <Dialog onClose={onClose} title={existing ? "Edit calendar" : "New calendar"}>
      <input
        type="text"
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="Name"
        autoFocus
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      />
      <select
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
            onClick={() => setColor(c)}
            className={"w-7 h-7 rounded-full border-2 " + (color === c ? "border-text" : "border-transparent")}
            style={{ backgroundColor: c }}
          />
        ))}
      </div>
      <div className="flex gap-2 justify-end items-center">
        {existing && (
          <button onClick={remove} className="px-3 py-1.5 text-sm text-error hover:text-error mr-auto">
            Delete
          </button>
        )}
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
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
  existing, defaults, calendars, onClose, onSaved, setStatus,
}: {
  existing?: Occurrence;
  defaults?: { start: Date; calendarId?: number };
  calendars: Calendar[];
  onClose: () => void;
  onSaved: () => void;
  setStatus: (s: string) => void;
}) {
  const initialStart = existing ? new Date(existing.start_at) : defaults!.start;
  const initialEnd = existing ? new Date(existing.end_at) : new Date(initialStart.getTime() + 30 * 60 * 1000);

  const [calendarId, setCalendarId] = useState<number>(
    existing?.calendar_id ?? defaults?.calendarId ?? calendars[0]?.id ?? 0,
  );
  const [title, setTitle] = useState(existing?.title || "");
  const [description, setDescription] = useState(existing?.description || "");
  const [location, setLocation] = useState(existing?.location || "");
  const [startStr, setStartStr] = useState(toLocalInput(initialStart));
  const [endStr, setEndStr] = useState(toLocalInput(initialEnd));
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!title.trim() || !calendarId) return;
    setBusy(true);
    try {
      const startISO = new Date(startStr).toISOString();
      const endISO = new Date(endStr).toISOString();
      if (existing) {
        const res = await fetch(`${API}/items/${existing.event_id}`, {
          method: "PATCH",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            scope: "all",
            title, description, location,
            start_at: startISO, end_at: endISO,
          }),
        });
        if (!res.ok) { setStatus("Update: " + (await res.text())); return; }
      } else {
        const res = await fetch(`${API}/items`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            calendar_id: calendarId,
            title, description, location,
            start_at: startISO, end_at: endISO,
          }),
        });
        if (!res.ok) { setStatus("Create: " + (await res.text())); return; }
      }
      onSaved();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!existing || !confirm(`Delete "${existing.title}"?`)) return;
    try {
      await fetch(`${API}/items/${existing.event_id}`, {
        method: "DELETE",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ scope: "all" }),
      });
      onSaved();
    } catch (e) {
      setStatus("Delete: " + (e as Error).message);
    }
  };

  return (
    <Dialog onClose={onClose} title={existing ? "Edit event" : "New event"}>
      <input
        type="text"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Title"
        autoFocus
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      />
      <select
        value={calendarId}
        onChange={(e) => setCalendarId(Number(e.target.value))}
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      >
        {calendars.map((c) => (
          <option key={c.id} value={c.id}>{c.name}</option>
        ))}
      </select>
      <div className="flex gap-2">
        <input
          type="datetime-local"
          value={startStr}
          onChange={(e) => setStartStr(e.target.value)}
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
        <input
          type="datetime-local"
          value={endStr}
          onChange={(e) => setEndStr(e.target.value)}
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
      </div>
      <input
        type="text"
        value={location}
        onChange={(e) => setLocation(e.target.value)}
        placeholder="Location (optional)"
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      />
      <textarea
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder="Description (optional)"
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-[60px]"
      />
      <div className="flex gap-2 justify-end items-center">
        {existing && (
          <button onClick={remove} className="px-3 py-1.5 text-sm text-error hover:text-error mr-auto">
            Delete
          </button>
        )}
        <button onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
        <button
          onClick={save}
          disabled={!title.trim() || !calendarId || busy}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
        >
          {existing ? "Save" : "Create"}
        </button>
      </div>
    </Dialog>
  );
}

function toLocalInput(d: Date): string {
  // datetime-local wants "YYYY-MM-DDTHH:MM" in local time (no Z).
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// --- Dialog shell ---------------------------------------------------

function Dialog({ children, onClose, title }: {
  children: React.ReactNode; onClose: () => void; title: string;
}) {
  return (
    <div className="fixed inset-0 bg-black/60 grid place-items-center z-50" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded p-4 w-[480px] max-w-[90vw] flex flex-col gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">{title}</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        {children}
      </div>
    </div>
  );
}
