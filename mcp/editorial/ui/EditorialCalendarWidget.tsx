import { useEffect, useMemo, useState } from "react";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
};

export type CalendarEvent = {
  kind: "item" | "release";
  date: string;
  at: string;
  item_id: number;
  release_id?: number;
  title: string;
  brand_id: string;
  format: string;
  status: string;
  approval: string;
  owner: string;
  channel?: string;
  url?: string;
};

export type View = "month" | "week" | "list";

const setting = <T,>(settings: Record<string, unknown> | undefined, key: string, fallback: T): T => {
  const value = settings?.[key];
  return value === undefined || value === null || value === "" ? fallback : (value as T);
};

export const dateKey = (d: Date) =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

// Plain dates are already calendar days; timestamps become the viewer's local
// day. Same rule the panel uses, so both surfaces bucket an event identically.
export const day = (s: string) => (!s ? "" : /^\d{4}-\d{2}-\d{2}$/.test(s) ? s : dateKey(new Date(s)));

export const eventDay = (e: CalendarEvent) => day(e.at || e.date);

export const label = (s: string) => s.replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase());

// A month grid needs room for seven labelled columns; at half width the host
// gives us one column of a two-column layout, where that is unreadable.
export const resolveView = (preference: string, size: "half" | "full"): View => {
  if (preference === "month" || preference === "week" || preference === "list") return preference;
  return size === "full" ? "month" : "list";
};

export const startOfWeek = (d: Date) => {
  const out = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  out.setDate(out.getDate() - ((out.getDay() + 6) % 7));
  return out;
};

// Six rows of seven always covers a month, but trailing empty weeks look like a
// bug, so the grid is only as tall as the month actually needs.
export const monthGrid = (month: Date): Date[] => {
  const first = startOfWeek(new Date(month.getFullYear(), month.getMonth(), 1));
  const days = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate();
  const offset = (new Date(month.getFullYear(), month.getMonth(), 1).getDay() + 6) % 7;
  const count = Math.ceil((offset + days) / 7) * 7;
  return Array.from({ length: count }, (_, n) => {
    const d = new Date(first);
    d.setDate(d.getDate() + n);
    return d;
  });
};

// The server windows on the stored date; a timestamp can land on a different
// local day, so every request asks for a day either side and re-buckets here.
export const windowFor = (
  view: View,
  anchor: Date,
  horizonDays: number,
): { from: string; to: string } => {
  let start: Date;
  let end: Date;
  if (view === "month") {
    const grid = monthGrid(anchor);
    start = grid[0];
    end = grid[grid.length - 1];
  } else if (view === "week") {
    start = startOfWeek(anchor);
    end = new Date(start);
    end.setDate(end.getDate() + 6);
  } else {
    start = new Date(anchor.getFullYear(), anchor.getMonth(), anchor.getDate());
    end = new Date(start);
    end.setDate(end.getDate() + horizonDays);
  }
  const pad = (d: Date, days: number) => {
    const out = new Date(d);
    out.setDate(out.getDate() + days);
    return dateKey(out);
  };
  return { from: pad(start, -1), to: pad(end, 1) };
};

export const groupByDay = (events: CalendarEvent[]): Record<string, CalendarEvent[]> => {
  const out: Record<string, CalendarEvent[]> = {};
  for (const e of events) {
    const key = eventDay(e);
    if (!key) continue;
    (out[key] ||= []).push(e);
  }
  return out;
};

const css = `
.ec-widget{color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:4px;font-family:inherit;min-width:0;display:flex;flex-direction:column;height:100%;overflow:hidden}
.ec-widget *{box-sizing:border-box}.ec-widget h2,.ec-widget p{margin:0}.ec-widget button{cursor:pointer;font-family:inherit;color:inherit}.ec-widget a{color:inherit;text-decoration:none}.ec-widget :is(button,a):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:-2px}
.ec-widget header{display:flex;align-items:flex-start;gap:10px;padding:14px 16px;border-bottom:1px solid var(--color-border,#303030)}
.ec-widget h2{font-size:14px;font-weight:700}.ec-widget header p{font-size:11px;color:var(--color-text-muted,#aaa);margin-top:4px}
.ec-widget .ec-open{margin-left:auto;font-size:11px;color:var(--color-text-muted,#aaa);text-decoration:none;white-space:nowrap;padding-top:2px}.ec-widget .ec-open:hover{color:var(--color-text,#eceef2)}
.ec-widget .ec-bar{display:flex;align-items:center;gap:6px;padding:9px 16px;border-bottom:1px solid var(--color-border,#303030);flex-wrap:wrap}
.ec-widget .ec-period{font-size:12px;font-weight:600;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ec-widget .ec-nav{display:flex;gap:4px}
.ec-widget .ec-views{margin-left:auto;display:flex;gap:4px}
.ec-widget .ec-btn{border:1px solid var(--color-border,#303030);background:transparent;border-radius:3px;padding:3px 8px;font-size:11px;line-height:18px}
.ec-widget .ec-btn:hover{border-color:var(--color-accent,#ff8c36)}
.ec-widget .ec-btn[aria-pressed=true]{background:var(--color-accent,#ff8c36);border-color:var(--color-accent,#ff8c36);color:var(--color-bg,#141414);font-weight:600}
.ec-widget .ec-body{flex:1;min-height:0;overflow:auto}
.ec-widget .ec-grid{display:grid;grid-template-columns:repeat(7,minmax(0,1fr))}
/* One week is a single row, so it stretches to fill the body instead of
   leaving the rest of the widget empty. */
.ec-widget .ec-grid.week{height:100%;grid-template-rows:auto minmax(0,1fr)}
.ec-widget .ec-weekday{padding:6px 8px;font-size:10px;text-transform:uppercase;letter-spacing:.04em;color:var(--color-text-muted,#999);border-bottom:1px solid var(--color-border,#303030);text-align:right}
.ec-widget .ec-cell{min-height:74px;border-right:1px solid var(--color-border,#303030);border-bottom:1px solid var(--color-border,#303030);padding:5px;min-width:0}
.ec-widget .ec-grid.compact .ec-cell{min-height:40px;padding:4px 3px}
.ec-widget .ec-cell:nth-child(7n){border-right:0}
.ec-widget .ec-cell.outside{background:color-mix(in srgb,var(--color-text-muted,#999) 6%,transparent);color:var(--color-text-dim,#777)}
.ec-widget .ec-cell.picked{outline:1px solid var(--color-accent,#ff8c36);outline-offset:-1px}
.ec-widget .ec-num{display:flex;justify-content:flex-end;font-size:10px;color:var(--color-text-muted,#999);margin-bottom:3px}
.ec-widget .ec-num span{width:18px;height:18px;display:flex;align-items:center;justify-content:center;border-radius:3px;font-variant-numeric:tabular-nums}
.ec-widget .ec-num .today{background:var(--color-accent,#ff8c36);color:var(--color-bg,#141414);font-weight:700}
.ec-widget .ec-chip{display:block;width:100%;text-align:left;border:1px solid var(--color-border,#303030);border-left:2px solid var(--color-accent,#ff8c36);border-radius:3px;background:transparent;padding:3px 5px;margin-bottom:3px;font-size:10px;line-height:14px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ec-widget .ec-chip.release{border-left-color:var(--color-info,#5aa9e6)}
.ec-widget .ec-chip:hover{background:var(--color-bg-hover,#1e1e1e)}
.ec-widget .ec-chip em{font-style:normal;color:var(--color-text-muted,#aaa)}
.ec-widget .ec-dots{display:flex;flex-wrap:wrap;gap:2px;justify-content:flex-end}
.ec-widget .ec-dot{width:5px;height:5px;border-radius:50%;background:var(--color-accent,#ff8c36)}
.ec-widget .ec-dot.release{background:var(--color-info,#5aa9e6)}
.ec-widget .ec-daybtn{display:block;width:100%;height:100%;border:0;background:transparent;padding:0;text-align:right}
.ec-widget .ec-agenda{border-top:1px solid var(--color-border,#303030)}
.ec-widget .ec-daygroup{padding:9px 16px;border-bottom:1px solid var(--color-border,#303030)}
.ec-widget .ec-dayhead{display:flex;align-items:baseline;gap:8px;font-size:11px;color:var(--color-text-muted,#aaa);margin-bottom:6px}
.ec-widget .ec-dayhead strong{font-size:12px;color:var(--color-text,#eceef2);font-weight:700}
.ec-widget .ec-row{display:flex;align-items:baseline;gap:8px;width:100%;text-align:left;border:0;background:transparent;padding:4px 0;min-width:0}
.ec-widget .ec-row:hover .ec-title{color:var(--color-accent,#ff8c36)}
.ec-widget .ec-mark{width:3px;align-self:stretch;border-radius:2px;background:var(--color-accent,#ff8c36);flex:0 0 3px}
.ec-widget .ec-mark.release{background:var(--color-info,#5aa9e6)}
.ec-widget .ec-title{font-size:12px;line-height:18px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0;flex:1}
.ec-widget .ec-meta{font-size:10px;color:var(--color-text-muted,#999);white-space:nowrap}
.ec-widget .ec-empty{padding:26px 16px;text-align:center;font-size:11px;color:var(--color-text-muted,#999)}
.ec-widget .ec-error{padding:12px 16px;font-size:11px;color:var(--color-danger,#f08b8b)}
.ec-widget .ec-note{padding:7px 16px;font-size:10px;color:var(--color-text-muted,#999);border-top:1px solid var(--color-border,#303030)}
@media (max-width:560px){.ec-widget .ec-cell{min-height:52px}}
`;

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const PANEL = "/apps/editorial/page";

export default function EditorialCalendarWidget(props: Props) {
  const size = props.widgetSize === "half" ? "half" : "full";
  const preference = setting<string>(props.widgetSettings, "default_view", "auto");
  const dateField = setting<string>(props.widgetSettings, "date_field", "planned_at");
  const brandId = setting<string>(props.widgetSettings, "brand_id", "");
  const showReleases = setting<boolean>(props.widgetSettings, "show_releases", true);
  const horizonDays = Math.max(1, Math.min(365, Number(setting<number>(props.widgetSettings, "horizon_days", 30)) || 30));

  const [view, setView] = useState<View>(() => resolveView(preference, size));
  const [anchor, setAnchor] = useState(() => new Date());
  const [picked, setPicked] = useState("");
  const [events, setEvents] = useState<CalendarEvent[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  // A resize changes which view is readable, so an automatic choice follows it.
  useEffect(() => setView(resolveView(preference, size)), [preference, size]);

  const range = useMemo(() => windowFor(view, anchor, horizonDays), [view, anchor, horizonDays]);

  useEffect(() => {
    if (!props.projectId) return;
    let alive = true;
    const controller = new AbortController();
    setLoading(true);
    const q = new URLSearchParams({
      project_id: props.projectId,
      from: range.from,
      to: range.to,
      date_field: dateField,
      include_releases: showReleases ? "true" : "false",
    });
    if (props.installId) q.set("install_id", String(props.installId));
    if (brandId) q.set("brand_id", brandId);
    fetch(`/api/apps/${encodeURIComponent(props.appName || "editorial")}/calendar?${q}`, {
      credentials: "same-origin",
      signal: controller.signal,
    })
      .then(async (r) => {
        const data = await r.json();
        if (!r.ok) throw new Error(data?.error || `Calendar unavailable (${r.status})`);
        return data;
      })
      .then((data) => {
        if (!alive) return;
        setEvents(Array.isArray(data?.events) ? data.events : []);
        setTruncated(Boolean(data?.truncated));
        setError("");
      })
      .catch((e) => {
        if (alive && e.name !== "AbortError") setError(e.message);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [
    props.appName,
    props.projectId,
    props.installId,
    props.eventRevision,
    range.from,
    range.to,
    dateField,
    brandId,
    showReleases,
  ]);

  const byDay = useMemo(() => groupByDay(events), [events]);
  const today = dateKey(new Date());
  const shift = (months: number, days: number) => {
    const next = new Date(anchor);
    if (months) next.setMonth(next.getMonth() + months, 1);
    if (days) next.setDate(next.getDate() + days);
    setAnchor(next);
    setPicked("");
  };

  const period =
    view === "month"
      ? anchor.toLocaleDateString(undefined, { month: "long", year: "numeric" })
      : view === "week"
        ? `Week of ${startOfWeek(anchor).toLocaleDateString(undefined, { month: "short", day: "numeric" })}`
        : `Next ${horizonDays} days`;

  const compact = size === "half";
  const fieldLabel = dateField === "deadline" ? "Editorial deadlines" : "Planned publications and releases";

  return (
    <section className="ec-widget" aria-label="Editorial calendar">
      <style>{css}</style>
      <header>
        <div style={{ minWidth: 0 }}>
          <h2>Editorial calendar</h2>
          <p>{fieldLabel}</p>
        </div>
        <a className="ec-open" href={PANEL}>
          Open Editorial →
        </a>
      </header>

      <div className="ec-bar">
        {view !== "list" && (
          <div className="ec-nav">
            <button
              className="ec-btn"
              aria-label={view === "month" ? "Previous month" : "Previous week"}
              onClick={() => (view === "month" ? shift(-1, 0) : shift(0, -7))}
            >
              ‹
            </button>
            <button
              className="ec-btn"
              aria-label={view === "month" ? "Next month" : "Next week"}
              onClick={() => (view === "month" ? shift(1, 0) : shift(0, 7))}
            >
              ›
            </button>
            <button
              className="ec-btn"
              onClick={() => {
                setAnchor(new Date());
                setPicked("");
              }}
            >
              Today
            </button>
          </div>
        )}
        <span className="ec-period">{period}</span>
        <div className="ec-views" role="group" aria-label="Calendar view">
          {(["month", "week", "list"] as View[]).map((v) => (
            <button
              key={v}
              className="ec-btn"
              aria-pressed={view === v}
              onClick={() => {
                setView(v);
                setPicked("");
              }}
            >
              {label(v)}
            </button>
          ))}
        </div>
      </div>

      <div className="ec-body">
        {error ? (
          <p className="ec-error">{error}</p>
        ) : view === "list" ? (
          <Agenda byDay={byDay} from={today} days={horizonDays} loading={loading} />
        ) : (
          <Grid
            days={view === "month" ? monthGrid(anchor) : weekDays(anchor)}
            month={view === "month" ? anchor.getMonth() : -1}
            stretch={view === "week" && !picked}
            byDay={byDay}
            today={today}
            compact={compact}
            picked={picked}
            onPick={setPicked}
            loading={loading}
          />
        )}
        {view !== "list" && picked && (
          <div className="ec-agenda">
            <DayGroup date={picked} events={byDay[picked] || []} />
          </div>
        )}
      </div>

      {truncated && <p className="ec-note">Showing the first events in this range. Open Editorial for the full calendar.</p>}
    </section>
  );
}

const weekDays = (anchor: Date) => {
  const start = startOfWeek(anchor);
  return Array.from({ length: 7 }, (_, n) => {
    const d = new Date(start);
    d.setDate(d.getDate() + n);
    return d;
  });
};

function Grid({
  days,
  month,
  byDay,
  today,
  compact,
  picked,
  onPick,
  loading,
  stretch,
}: {
  days: Date[];
  month: number;
  stretch?: boolean;
  byDay: Record<string, CalendarEvent[]>;
  today: string;
  compact: boolean;
  picked: string;
  onPick: (key: string) => void;
  loading: boolean;
}) {
  return (
    <div className={`ec-grid ${compact ? "compact" : ""} ${stretch ? "week" : ""}`} role="grid" aria-busy={loading}>
      {WEEKDAYS.map((d) => (
        <div className="ec-weekday" key={d}>
          {compact ? d[0] : d}
        </div>
      ))}
      {days.map((d) => {
        const key = dateKey(d);
        const dayEvents = byDay[key] || [];
        const outside = month >= 0 && d.getMonth() !== month;
        return (
          <div
            className={`ec-cell ${outside ? "outside" : ""} ${picked === key ? "picked" : ""}`}
            key={key}
            role="gridcell"
            aria-label={key}
          >
            {compact ? (
              <button className="ec-daybtn" onClick={() => onPick(picked === key ? "" : key)}>
                <span className="ec-num">
                  <span className={key === today ? "today" : ""}>{d.getDate()}</span>
                </span>
                <span className="ec-dots">
                  {dayEvents.slice(0, 6).map((e, n) => (
                    <span className={`ec-dot ${e.kind === "release" ? "release" : ""}`} key={n} />
                  ))}
                </span>
              </button>
            ) : (
              <>
                <div className="ec-num">
                  <span className={key === today ? "today" : ""}>{d.getDate()}</span>
                </div>
                {dayEvents.slice(0, 3).map((e) => (
                  <a className={`ec-chip ${e.kind}`} key={`${e.kind}${e.release_id || e.item_id}`} href={PANEL} title={e.title}>
                    <em>{e.kind === "release" ? e.channel : label(e.format)}</em> {e.title}
                  </a>
                ))}
                {dayEvents.length > 3 && (
                  <button className="ec-chip" onClick={() => onPick(picked === key ? "" : key)}>
                    +{dayEvents.length - 3} more
                  </button>
                )}
              </>
            )}
          </div>
        );
      })}
    </div>
  );
}

function Agenda({
  byDay,
  from,
  days,
  loading,
}: {
  byDay: Record<string, CalendarEvent[]>;
  from: string;
  days: number;
  loading: boolean;
}) {
  const start = new Date(`${from}T12:00:00`);
  const keys = Array.from({ length: days + 1 }, (_, n) => {
    const d = new Date(start);
    d.setDate(d.getDate() + n);
    return dateKey(d);
  }).filter((key) => (byDay[key] || []).length > 0);

  if (!keys.length) {
    return <p className="ec-empty">{loading ? "Loading…" : "Nothing scheduled in this range."}</p>;
  }
  return (
    <div aria-busy={loading}>
      {keys.map((key) => (
        <DayGroup key={key} date={key} events={byDay[key]} />
      ))}
    </div>
  );
}

function DayGroup({ date, events }: { date: string; events: CalendarEvent[] }) {
  const d = new Date(`${date}T12:00:00`);
  return (
    <div className="ec-daygroup">
      <div className="ec-dayhead">
        <strong>{d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" })}</strong>
        <span>
          {events.length} {events.length === 1 ? "entry" : "entries"}
        </span>
      </div>
      {events.length === 0 ? (
        <p className="ec-meta">Nothing scheduled.</p>
      ) : (
        events.map((e) => (
          <a className="ec-row" key={`${e.kind}${e.release_id || e.item_id}`} href={e.url || PANEL} {...(e.url ? { target: "_blank", rel: "noreferrer" } : {})}>
            <span className={`ec-mark ${e.kind === "release" ? "release" : ""}`} />
            <span className="ec-title">{e.title || "Untitled"}</span>
            <span className="ec-meta">{e.kind === "release" ? e.channel : label(e.status)}</span>
          </a>
        ))
      )}
    </div>
  );
}
