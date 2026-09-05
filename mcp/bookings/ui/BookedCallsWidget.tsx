import { useCallback, useEffect, useMemo, useState } from "react";

interface HostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

interface Booking {
  id: number;
  booking_type_id: number;
  status: string;
  start_at: string;
  end_at: string;
  invitee_name: string;
  invitee_email: string;
  calls_room_id?: number;
  calls_guest_join_url?: string;
  calls_host_join_url?: string;
}

interface BookingType {
  id: number;
  title: string;
}

export interface BookedCallsPreferences {
  horizonDays: number;
  maxItems: number;
  bookingTypeId: number;
}

export function bookedCallsPreferences(settings?: Record<string, unknown>): BookedCallsPreferences {
  const integer = (key: string, fallback: number, min: number, max: number) => {
    const value = Number(settings?.[key] ?? fallback);
    return Number.isFinite(value)
      ? Math.max(min, Math.min(max, Math.round(value)))
      : fallback;
  };
  return {
    horizonDays: integer("horizon_days", 30, 0, 365),
    maxItems: integer("max_items", 6, 1, 20),
    bookingTypeId: integer("booking_type_id", 0, 0, Number.MAX_SAFE_INTEGER),
  };
}

export function upcomingBookedCalls(bookings: Booking[], now: number): Booking[] {
  return bookings
    .filter((booking) =>
      Boolean(booking.calls_room_id)
      && (booking.status === "confirmed" || booking.status === "rescheduled")
      && new Date(booking.end_at).getTime() >= now,
    )
    .sort((left, right) =>
      new Date(left.start_at).getTime() - new Date(right.start_at).getTime()
      || left.id - right.id,
    );
}

function apiPath(props: HostProps, path: string, query: URLSearchParams) {
  if (props.installId) query.set("install_id", String(props.installId));
  if (props.projectId) query.set("project_id", props.projectId);
  return `/api/apps/${encodeURIComponent(props.appName || "bookings")}${path}?${query.toString()}`;
}

async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", signal });
  if (!response.ok) throw new Error((await response.text()).trim() || `Request failed (${response.status})`);
  return response.json() as Promise<T>;
}

function sameLocalDay(left: Date, right: Date) {
  return left.getFullYear() === right.getFullYear()
    && left.getMonth() === right.getMonth()
    && left.getDate() === right.getDate();
}

function dayLabel(value: string, now: number) {
  const date = new Date(value);
  const today = new Date(now);
  const tomorrow = new Date(today);
  tomorrow.setDate(today.getDate() + 1);
  if (sameLocalDay(date, today)) return "Today";
  if (sameLocalDay(date, tomorrow)) return "Tomorrow";
  return new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric" }).format(date);
}

function timeLabel(value: string) {
  return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" }).format(new Date(value));
}

function relativeStart(booking: Booking, now: number) {
  const start = new Date(booking.start_at).getTime();
  const end = new Date(booking.end_at).getTime();
  if (start <= now && end >= now) return "Live now";
  const minutes = Math.max(0, Math.round((start - now) / 60000));
  if (minutes < 60) return `in ${minutes}m`;
  if (minutes < 24 * 60) return `in ${Math.round(minutes / 60)}h`;
  return "";
}

export default function BookedCallsWidget(props: HostProps) {
  const projectId = props.projectId || "";
  const preferences = bookedCallsPreferences(props.widgetSettings);
  const full = props.widgetSize === "full";
  const visibleLimit = Math.min(20, full ? preferences.maxItems * 2 : preferences.maxItems);
  const [bookings, setBookings] = useState<Booking[]>([]);
  const [types, setTypes] = useState<BookingType[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [now, setNow] = useState(() => Date.now());

  const load = useCallback(async () => {
    if (!projectId) return;
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 15000);
    try {
      const current = new Date();
      const from = new Date(current.getTime() - 24 * 60 * 60 * 1000);
      const bookingQuery = new URLSearchParams({
        active: "true",
        calls_only: "true",
        from: from.toISOString(),
        ends_after: current.toISOString(),
        order: "asc",
        limit: "100",
      });
      if (preferences.horizonDays > 0) {
        bookingQuery.set("to", new Date(current.getTime() + preferences.horizonDays * 86400000).toISOString());
      }
      if (preferences.bookingTypeId > 0) {
        bookingQuery.set("booking_type_id", String(preferences.bookingTypeId));
      }
      const [bookingData, typeData] = await Promise.all([
        getJSON<{ bookings?: Booking[] }>(apiPath(props, "/bookings", bookingQuery), controller.signal),
        getJSON<{ booking_types?: BookingType[] }>(apiPath(props, "/booking-types", new URLSearchParams({ active: "false" })), controller.signal),
      ]);
      setBookings(bookingData.bookings || []);
      setTypes(typeData.booking_types || []);
      setError("");
    } catch (reason) {
      setError(reason instanceof Error && reason.name === "AbortError" ? "Request timed out" : reason instanceof Error ? reason.message : String(reason));
    } finally {
      window.clearTimeout(timeout);
      setLoading(false);
    }
  }, [preferences.bookingTypeId, preferences.horizonDays, projectId, props.appName, props.installId]);

  useEffect(() => { void load(); }, [load, props.eventRevision]);
  useEffect(() => {
    const refresh = window.setInterval(() => void load(), 60000);
    return () => window.clearInterval(refresh);
  }, [load]);
  useEffect(() => {
    const clock = window.setInterval(() => setNow(Date.now()), 30000);
    return () => window.clearInterval(clock);
  }, []);

  const typeNames = useMemo(() => new Map(types.map((type) => [type.id, type.title])), [types]);
  const upcoming = useMemo(() => upcomingBookedCalls(bookings, now), [bookings, now]);
  const visible = upcoming.slice(0, visibleLimit);

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-bold text-text">Booked calls</h2>
            {upcoming.length > 0 && (
              <span className="inline-flex min-w-5 items-center justify-center rounded-full bg-accent/15 px-1.5 py-0.5 text-[10px] font-bold tabular-nums text-accent">
                {upcoming.length}
              </span>
            )}
          </div>
          <p className="mt-0.5 text-[11px] text-text-dim">Upcoming client calls and host access</p>
        </div>
        <a href="/apps/bookings/page" className="shrink-0 pt-0.5 text-[11px] text-text-muted hover:text-text">View all →</a>
      </header>

      {error ? (
        <div className="flex min-h-28 flex-1 flex-col items-start justify-center gap-2 p-4">
          <p className="text-xs text-red">{error}</p>
          <button type="button" onClick={() => void load()} className="rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover">Retry</button>
        </div>
      ) : loading ? (
        <p className="flex min-h-28 flex-1 items-center p-4 text-xs text-text-dim">Loading booked calls…</p>
      ) : visible.length === 0 ? (
        <div className="flex min-h-28 flex-1 flex-col justify-center p-4">
          <p className="text-xs font-semibold text-text">No upcoming calls</p>
          <p className="mt-1 text-[11px] text-text-dim">Confirmed Calls bookings will appear here.</p>
        </div>
      ) : (
        <div className="min-h-0 flex-1 divide-y divide-border overflow-y-auto">
          {visible.map((booking) => {
            const timing = relativeStart(booking, now);
            const start = timeLabel(booking.start_at);
            const end = timeLabel(booking.end_at);
            const joinURL = booking.calls_host_join_url || booking.calls_guest_join_url;
            return (
              <article key={booking.id} className="grid min-h-[76px] grid-cols-[5.25rem_minmax(0,1fr)_auto] items-center gap-3 px-4 py-2.5 hover:bg-bg-hover/60">
                <div className="min-w-0">
                  <p className="truncate text-[10px] font-bold uppercase tracking-wide text-text-dim">{dayLabel(booking.start_at, now)}</p>
                  <p className="mt-0.5 whitespace-nowrap text-xs font-semibold tabular-nums text-text">{start}–{end}</p>
                </div>
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <p className="truncate text-xs font-semibold text-text">{booking.invitee_name || booking.invitee_email || "Guest"}</p>
                    {booking.status === "rescheduled" && <span className="shrink-0 rounded border border-blue/30 bg-blue/10 px-1.5 py-0.5 text-[8px] font-bold uppercase text-blue">Moved</span>}
                  </div>
                  <p className="mt-1 truncate text-[10px] text-text-muted">
                    {typeNames.get(booking.booking_type_id) || "Client call"}{timing ? ` · ${timing}` : ""}
                  </p>
                </div>
                {joinURL ? (
                  <a href={joinURL} target="_blank" rel="noreferrer" className={`rounded border px-2.5 py-1.5 text-[10px] font-bold ${timing === "Live now" ? "border-accent bg-accent text-bg" : "border-border text-text-muted hover:border-accent hover:text-accent"}`}>Join</a>
                ) : (
                  <span className="text-[9px] text-yellow">No room</span>
                )}
              </article>
            );
          })}
          {upcoming.length > visible.length && (
            <a href="/apps/bookings/page" className="block px-4 py-2.5 text-center text-[10px] text-text-muted hover:bg-bg-hover hover:text-text">
              {upcoming.length - visible.length} more call{upcoming.length - visible.length === 1 ? "" : "s"} →
            </a>
          )}
        </div>
      )}
    </section>
  );
}
