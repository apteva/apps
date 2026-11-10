import { useEffect, useMemo, useRef, useState } from "react";
import { badgeStyles, DestinationBadges, destinationNames, StatusBadge, statusLabel } from "./EditorialBadges";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
  widgetId?: string;
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
  destinations?: string[];
  url?: string;
};

export type View = "month" | "week" | "list";

export type Brand = { id: string; name: string; color: string };

// Sentinels the picker and the endpoint agree on: "" is every brand, and
// "unassigned" is content that deliberately has none.
export const ALL_BRANDS = "";
export const UNASSIGNED = "unassigned";

// Brand colour wins over the kind colour, because on an all-brands calendar
// "whose is this" is the question the operator is actually asking. Projects
// with no brands configured keep the kind colours and never see a picker.
export const brandColor = (brands: Brand[], id: string): string => {
  if (!id) return "";
  const brand = brands.find((b) => b.id === id);
  return brand?.color || "";
};
export const brandName = (brands: Brand[], id: string): string =>
  brands.find((b) => b.id === id)?.name || "";

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

// The configured size sets the default view. Grid density follows the actual width.
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
.ec-widget{container:editorial-calendar / inline-size;box-sizing:border-box;color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:4px;font-family:inherit;min-width:0;overflow:clip}
.ec-widget *{box-sizing:border-box}.ec-widget h2,.ec-widget p{margin:0}.ec-widget button{cursor:pointer;font-family:inherit;color:inherit}.ec-widget a{color:inherit;text-decoration:none}.ec-widget :is(button,a):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:-2px}
.ec-widget header{display:flex;align-items:flex-start;gap:10px;padding:10px 14px;border-bottom:1px solid var(--color-border,#303030)}
.ec-widget h2{font-size:14px;font-weight:700}.ec-widget header p{font-size:11px;color:var(--color-text-muted,#aaa);margin-top:4px}
.ec-widget .ec-open{margin-left:auto;font-size:11px;color:var(--color-text-muted,#aaa);text-decoration:none;white-space:nowrap;padding-top:2px}.ec-widget .ec-open:hover{color:var(--color-text,#eceef2)}
.ec-widget .ec-bar{display:flex;align-items:center;gap:6px;padding:6px 14px;border-bottom:1px solid var(--color-border,#303030);flex-wrap:wrap}
.ec-widget .ec-period{font-size:12px;font-weight:600;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ec-widget .ec-nav{display:flex;gap:4px}
.ec-widget .ec-views{margin-left:auto;display:flex;gap:4px}
.ec-widget .ec-btn{border:1px solid var(--color-border,#303030);background:transparent;border-radius:3px;padding:3px 8px;font-size:11px;line-height:18px}
.ec-widget .ec-btn:hover{border-color:var(--color-accent,#ff8c36)}
.ec-widget .ec-btn[aria-pressed=true]{background:var(--color-accent,#ff8c36);border-color:var(--color-accent,#ff8c36);color:var(--color-bg,#141414);font-weight:600}
.ec-widget .ec-calendar-body{display:flex;flex-direction:column;min-height:14rem}
.ec-widget .ec-grid{display:grid;flex:1;grid-template-columns:repeat(7,minmax(0,1fr));grid-template-rows:min-content}
.ec-widget .ec-weekday{padding:6px 8px;font-size:10px;text-transform:uppercase;letter-spacing:.04em;color:var(--color-text-muted,#999);border-bottom:1px solid var(--color-border,#303030);text-align:right}
.ec-widget .ec-cell{min-height:42px;border-right:1px solid var(--color-border,#303030);border-bottom:1px solid var(--color-border,#303030);padding:4px;min-width:0}
.ec-widget .ec-cell:nth-child(7n){border-right:0}
.ec-widget .ec-cell.outside{background:color-mix(in srgb,var(--color-text-muted,#999) 6%,transparent);color:var(--color-text-dim,#777)}
.ec-widget .ec-cell.picked{outline:1px solid var(--color-accent,#ff8c36);outline-offset:-1px}
.ec-widget .ec-num{display:flex;justify-content:flex-end;font-size:10px;color:var(--color-text-muted,#999);margin-bottom:3px}
.ec-widget .ec-num span{width:18px;height:18px;display:flex;align-items:center;justify-content:center;border-radius:3px;font-variant-numeric:tabular-nums}
.ec-widget .ec-num .today{background:var(--color-accent,#ff8c36);color:var(--color-bg,#141414);font-weight:700}
.ec-widget .ec-daybtn{width:100%;border:0;background:transparent;padding:0}
.ec-widget .ec-daybtn:hover span{background:var(--color-bg-hover,#1e1e1e)}
.ec-widget .ec-daybtn:hover .today{background:var(--color-accent,#ff8c36)}
.ec-widget .ec-chip{display:block;width:100%;text-align:left;border:1px solid var(--color-border,#303030);border-left:2px solid var(--color-accent,#ff8c36);border-radius:3px;background:transparent;padding:3px 5px;margin-bottom:3px;font-size:10px;line-height:14px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ec-widget .ec-chip.release{border-left-color:var(--color-info,#5aa9e6)}
.ec-widget .ec-chip:hover{background:var(--color-bg-hover,#1e1e1e)}
.ec-widget .ec-chip-title{display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:2;white-space:normal;overflow:hidden;overflow-wrap:anywhere;font-size:11px;line-height:15px}
.ec-widget .ec-chip-meta{display:flex;flex-wrap:wrap;align-items:center;gap:3px;margin-top:3px}
.ec-widget .ec-chip-meta .ed-destination{border:0;padding:1px}
.ec-widget .ec-chip-meta .ed-status{max-width:100%;overflow:hidden}
.ec-widget .ec-chip-meta .ed-status-name{overflow:hidden;text-overflow:ellipsis}
.ec-widget .ec-chip-meta .multiple .ed-destination-name{position:absolute;clip-path:inset(50%);width:1px;height:1px}
.ec-widget .ec-select{border:1px solid var(--color-border,#303030);background:var(--color-bg-card,#141414);color:inherit;border-radius:3px;padding:3px 6px;font-size:11px;line-height:18px;font-family:inherit;max-width:150px}
.ec-widget .ec-select:hover{border-color:var(--color-accent,#ff8c36)}
.ec-widget .ec-swatch{width:7px;height:7px;border-radius:2px;flex:0 0 7px;display:inline-block}
.ec-widget .ec-daygroup{padding:9px 16px;border-bottom:1px solid var(--color-border,#303030)}
.ec-widget .ec-dayhead{display:flex;align-items:baseline;gap:8px;font-size:11px;color:var(--color-text-muted,#aaa);margin-bottom:6px}
.ec-widget .ec-dayhead strong{font-size:12px;color:var(--color-text,#eceef2);font-weight:700}
.ec-widget .ec-dayhead button{margin-left:auto}
.ec-widget .ec-row{display:grid;grid-template-columns:3px minmax(0,1fr);gap:9px;text-align:left;padding:5px 0;min-width:0}
.ec-widget .ec-row:hover .ec-title{color:var(--color-accent,#ff8c36)}
.ec-widget .ec-mark{width:3px;align-self:stretch;border-radius:2px;background:var(--color-accent,#ff8c36)}
.ec-widget .ec-mark.release{background:var(--color-info,#5aa9e6)}
.ec-widget .ec-title{display:block;font-size:12px;font-weight:600;line-height:18px;overflow-wrap:anywhere}
.ec-widget .ec-row-meta{display:flex;flex-wrap:wrap;align-items:center;gap:5px 8px;margin-top:4px}
.ec-widget .ec-row :is(.ed-destination,.ed-status){font-size:11px}
.ec-widget .ec-meta{font-size:10px;color:var(--color-text-muted,#999);overflow-wrap:anywhere}
.ec-widget .ec-list{padding:3px 0}
.ec-widget .ec-list .ec-row{gap:8px;padding:6px 14px}
.ec-widget .ec-list .ec-row + .ec-row{border-top:1px solid var(--color-border,#303030)}
.ec-widget .ec-list .ec-row:hover{background:var(--color-bg-hover,#1e1e1e)}
.ec-widget .ec-list .ec-row-content{display:grid;grid-template-columns:5rem minmax(0,1fr);column-gap:8px;row-gap:2px;align-items:center}
.ec-widget .ec-list-date{color:var(--color-text-muted,#999);font-size:10px}
.ec-widget .ec-list :is(.ec-title,.ec-list-date,.ec-meta,.ed-status-name){overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ec-widget .ec-list .ec-row-meta{grid-column:2;flex-wrap:nowrap;gap:5px;margin-top:0;overflow:clip}
.ec-widget .ec-list :is(.ed-status,.ed-destination){font-size:10px;padding:1px 4px}
.ec-widget .ec-list .ed-status{min-width:0;max-width:35%}
.ec-widget .ec-list .ed-destinations{flex-wrap:nowrap;gap:4px}
.ec-widget .ec-list .ed-destination svg{width:11px;height:11px}
.ec-widget .ec-list .ec-meta{min-width:0}
.ec-widget .ec-pager{display:flex;align-items:center;justify-content:flex-end;gap:8px;padding:0 12px 8px;color:var(--color-text-muted,#999);font-size:11px}
.ec-widget .ec-pager button:disabled{opacity:.45;cursor:default}
.ec-widget .ec-empty{padding:26px 16px;text-align:center;font-size:11px;color:var(--color-text-muted,#999)}
.ec-widget .ec-error{padding:12px 16px;font-size:11px;color:var(--color-danger,#f08b8b)}
.ec-widget .ec-note{padding:7px 16px;font-size:10px;color:var(--color-text-muted,#999);border-top:1px solid var(--color-border,#303030)}
@container editorial-calendar (width < 560px){
  .ec-widget .ec-weekday{padding-inline:4px}
  .ec-widget .ec-cell{min-height:40px;padding:4px 3px}
  .ec-widget .ec-chip{border-block:0;border-right:0;padding:2px 3px}
  .ec-widget .ec-chip-title{display:block;white-space:nowrap;text-overflow:ellipsis;font-size:10px;line-height:14px}
  .ec-widget .ec-chip-meta .ed-destination-name,.ec-widget .ec-chip-meta .ed-status-name{position:absolute;clip-path:inset(50%);width:1px;height:1px}
  .ec-widget .ec-chip-meta .ed-destination svg{width:10px;height:10px}
  .ec-widget .ec-chip-meta .ed-status{background:none;padding:1px}
  .ec-widget .ec-list .ec-row-meta{grid-column:1/-1}
  .ec-widget .ec-list .multiple [data-known-icon]{flex:none}
  .ec-widget .ec-list .multiple [data-known-icon] .ed-destination-name{position:absolute;clip-path:inset(50%);width:1px;height:1px}
}
.ec-widget .ec-chip:focus-visible .ec-chip-title{display:block;white-space:normal}
`;

// Colour is not a label: screen readers and colour-blind viewers get the brand
// name through the accessible title instead.
const eventDestinations = (e: CalendarEvent) =>
  e.kind === "release" ? (e.channel ? [e.channel] : []) : e.destinations || [];
const titleOf = (brands: Brand[], e: CalendarEvent) =>
  [e.title, brandName(brands, e.brand_id), destinationNames(eventDestinations(e)).join(", "), statusLabel(e.status)]
    .filter(Boolean).join(" · ");

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const PANEL = "/apps/editorial/page";

export default function EditorialCalendarWidget(props: Props) {
  const size = props.widgetSize === "half" ? "half" : "full";
  const preference = setting<string>(props.widgetSettings, "default_view", "auto");
  const dateField = setting<string>(props.widgetSettings, "date_field", "planned_at");
  const brandDefault = setting<string>(props.widgetSettings, "brand_id", ALL_BRANDS);
  const showReleases = setting<boolean>(props.widgetSettings, "show_releases", true);
  const horizonDays = Math.max(1, Math.min(365, Number(setting<number>(props.widgetSettings, "horizon_days", 30)) || 30));

  const [brands, setBrands] = useState<Brand[]>([]);
  // The operator setting supplies the starting selection; the viewer's own
  // choice overrides it and persists per widget instance.
  const brandKey = `apteva:editorial:brand:${props.widgetId || `${props.projectId || "global"}:${props.installId || 0}`}`;
  const [brand, setBrand] = useState<string>(() => {
    try {
      const stored = window.localStorage.getItem(brandKey);
      if (stored !== null) return stored;
    } catch {
      // Filtering stays usable when browser storage is unavailable.
    }
    return brandDefault;
  });
  useEffect(() => {
    try {
      window.localStorage.setItem(brandKey, brand);
    } catch {
      // As above: a failed write only costs the preference, not the render.
    }
  }, [brandKey, brand]);

  const [view, setView] = useState<View>(() => resolveView(preference, size));
  const [anchor, setAnchor] = useState(() => new Date());
  const [picked, setPicked] = useState("");
  const [events, setEvents] = useState<CalendarEvent[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  // A resize changes which view is readable, so an automatic choice follows it.
  useEffect(() => setView(resolveView(preference, size)), [preference, size]);

  useEffect(() => {
    if (!props.projectId) return;
    let alive = true;
    const controller = new AbortController();
    const q = new URLSearchParams({ project_id: props.projectId });
    if (props.installId) q.set("install_id", String(props.installId));
    fetch(`/api/apps/${encodeURIComponent(props.appName || "editorial")}/settings?${q}`, {
      credentials: "same-origin",
      signal: controller.signal,
    })
      .then((r) => (r.ok ? r.json() : { brands: [] }))
      .then((data) => {
        if (alive) setBrands(Array.isArray(data?.brands) ? data.brands : []);
      })
      .catch(() => {
        // Brands are decoration and a filter; the calendar renders without them.
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [props.appName, props.projectId, props.installId, props.eventRevision]);

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
    if (brand) q.set("brand_id", brand);
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
    brand,
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

  const fieldLabel = dateField === "deadline" ? "Editorial deadlines" : "Planned publications and releases";

  return (
    <section className="ec-widget" aria-label="Editorial calendar">
      <style>{badgeStyles + css}</style>
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
        {brands.length > 0 && (
          <select
            className="ec-select"
            aria-label="Filter by brand"
            value={brand}
            onChange={(e) => {
              setBrand(e.target.value);
              setPicked("");
            }}
          >
            <option value={ALL_BRANDS}>All brands</option>
            {brands.map((b) => (
              <option key={b.id} value={b.id}>
                {b.name}
              </option>
            ))}
            <option value={UNASSIGNED}>Unassigned</option>
          </select>
        )}
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

      <div className={view === "list" ? undefined : "ec-calendar-body"}>
        {error ? (
          <p className="ec-error">{error}</p>
        ) : view === "list" ? (
          <Agenda key={`${props.projectId}:${props.installId}:${brand}:${dateField}:${showReleases}:${range.to}`}
            brands={brands} byDay={byDay} from={today} days={horizonDays} loading={loading} />
        ) : (
          <>
            {picked && (
              <DayGroup brands={brands} date={picked} events={byDay[picked] || []} onClose={() => setPicked("")} />
            )}
            <Grid
              brands={brands}
              days={view === "month" ? monthGrid(anchor) : weekDays(anchor)}
              month={view === "month" ? anchor.getMonth() : -1}
              byDay={byDay}
              today={today}
              picked={picked}
              onPick={(key) => setPicked(picked === key ? "" : key)}
              loading={loading}
            />
          </>
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
  brands,
  days,
  month,
  byDay,
  today,
  picked,
  onPick,
  loading,
}: {
  brands: Brand[];
  days: Date[];
  month: number;
  byDay: Record<string, CalendarEvent[]>;
  today: string;
  picked: string;
  onPick: (key: string) => void;
  loading: boolean;
}) {
  return (
    <div className="ec-grid" role="grid" aria-busy={loading}>
      {WEEKDAYS.map((d) => (
        <div className="ec-weekday" key={d}>
          {d}
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
            <button className="ec-num ec-daybtn" aria-label={`Agenda for ${key}`} aria-expanded={picked === key} onClick={() => onPick(key)}>
              <span className={key === today ? "today" : ""}>{d.getDate()}</span>
            </button>
            {dayEvents.map((e) => (
              <a
                className={`ec-chip ${e.kind}`}
                key={`${e.kind}${e.release_id || e.item_id}`}
                href={PANEL}
                title={titleOf(brands, e)}
                style={{ borderLeftColor: brandColor(brands, e.brand_id) || undefined }}
              >
                <span className="ec-chip-title">{e.title}</span>
                <span className="ec-chip-meta">
                  <DestinationBadges channels={eventDestinations(e)} />
                  <StatusBadge value={e.status} />
                </span>
              </a>
            ))}
          </div>
        );
      })}
    </div>
  );
}

function Agenda({
  brands,
  byDay,
  from,
  days,
  loading,
}: {
  brands: Brand[];
  byDay: Record<string, CalendarEvent[]>;
  from: string;
  days: number;
  loading: boolean;
}) {
  const [requestedPage, setPage] = useState(0);

  const start = new Date(`${from}T12:00:00`);
  const entries = Array.from({ length: days + 1 }, (_, n) => {
    const d = new Date(start);
    d.setDate(d.getDate() + n);
    return dateKey(d);
  }).flatMap((key) => byDay[key] || []);
  const pageSize = 6;
  const lastPage = Math.max(0, Math.ceil(entries.length / pageSize) - 1);
  const page = Math.min(requestedPage, lastPage);
  const first = page * pageSize;
  const last = Math.min(first + pageSize, entries.length);
  return (
    <div aria-busy={loading}>
      {entries.length ? (
        <div className="ec-list">
          {entries.slice(first, last).map((e) => (
            <AgendaEntry key={`${e.kind}${e.release_id || e.item_id}`} brands={brands} event={e} date={eventDay(e)} />
          ))}
        </div>
      ) : (
        <p className="ec-empty">{loading ? "Loading…" : "Nothing scheduled in this range."}</p>
      )}
      {lastPage > 0 && (
        <div className="ec-pager" role="group" aria-label="List pages">
          <span aria-live="polite">{first + 1}–{last} of {entries.length}</span>
          <button className="ec-btn" type="button" disabled={page === 0}
            aria-label="Previous entries" onClick={() => setPage(page - 1)}>‹</button>
          <button className="ec-btn" type="button" disabled={page === lastPage}
            aria-label="Next entries" onClick={() => setPage(page + 1)}>›</button>
        </div>
      )}
    </div>
  );
}

const agendaDate = (date: string) =>
  new Date(`${date}T12:00:00`).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });

function AgendaEntry({ brands, event: e, date }: { brands: Brand[]; event: CalendarEvent; date?: string }) {
  return (
    <a
      className="ec-row"
      href={e.url || PANEL}
      title={titleOf(brands, e)}
      {...(e.url ? { target: "_blank", rel: "noreferrer" } : {})}
    >
      <span
        className={`ec-mark ${e.kind === "release" ? "release" : ""}`}
        style={{ background: brandColor(brands, e.brand_id) || undefined }}
      />
      <span className="ec-row-content">
        {date && <time className="ec-list-date" dateTime={date} title={agendaDate(date)}>{agendaDate(date)}</time>}
        <span className="ec-title">{e.title || "Untitled"}</span>
        <span className="ec-row-meta">
          <StatusBadge value={e.status} />
          <DestinationBadges channels={eventDestinations(e)} />
          <span className="ec-meta">
            {[e.kind === "release" ? "Release" : "", brandName(brands, e.brand_id)].filter(Boolean).join(" · ")}
          </span>
        </span>
      </span>
    </a>
  );
}

function DayGroup({ brands, date, events, onClose }: { brands: Brand[]; date: string; events: CalendarEvent[]; onClose: () => void }) {
  const agendaRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    agendaRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [date]);
  return (
    <div className="ec-daygroup" ref={agendaRef}>
      <div className="ec-dayhead">
        <strong>{agendaDate(date)}</strong>
        <span>
          {events.length} {events.length === 1 ? "entry" : "entries"}
        </span>
        <button className="ec-btn" onClick={onClose}>Close agenda</button>
      </div>
      {events.length === 0 ? (
        <p className="ec-meta">Nothing scheduled.</p>
      ) : (
        events.map((e) => (
          <AgendaEntry key={`${e.kind}${e.release_id || e.item_id}`} brands={brands} event={e} />
        ))
      )}
    </div>
  );
}
