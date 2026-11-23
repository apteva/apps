import { NoProject, WidgetFrame } from "./WidgetFrame";
import { PRIORITY_TONE, brandSetting, callTool, clock, day, human, intSetting, pageLink, setting, useWidgetData, type Slot, type WidgetProps } from "./widget";

const RANGES = ["today", "week"] as const;
const STATUS_TONE: Record<string, string> = { in_progress: "text-green", missed: "text-red", done: "text-text-dim", booked: "text-text-muted" };

function label(s: Slot): string {
  return s.work?.customer_name || s.work?.number || "Booking";
}
function detail(s: Slot): string {
  const parts = [human(s.work?.work_type ?? "")];
  if (s.work?.party_size) parts.push(`${s.work.party_size} guests`);
  if (s.resources.length) parts.push(s.resources.map((r) => r.name).join(", "));
  return parts.filter(Boolean).join(" · ");
}

function Timeline({ slots, props }: { slots: Slot[]; props: WidgetProps }) {
  const lanes = new Map<number, { name: string; slots: Slot[] }>();
  for (const s of slots) {
    for (const r of s.resources.length ? s.resources : [{ id: 0, name: "Unassigned", kind: "" }]) {
      if (!lanes.has(r.id)) lanes.set(r.id, { name: r.name, slots: [] });
      lanes.get(r.id)!.slots.push(s);
    }
  }
  const hours = slots.flatMap((s) => [new Date(s.starts_at).getHours(), new Date(s.ends_at).getHours() + (new Date(s.ends_at).getMinutes() ? 1 : 0)]);
  const start = Math.min(8, ...hours);
  const end = Math.max(18, ...hours);
  const span = (end - start) * 60;
  const pos = (iso: string) => {
    const d = new Date(iso);
    return Math.max(0, Math.min(100, ((d.getHours() - start) * 60 + d.getMinutes()) / span * 100));
  };
  const now = new Date();
  const nowPos = pos(now.toISOString());
  const ticks = Array.from({ length: end - start + 1 }, (_, i) => start + i).filter((h) => (end - start > 12 ? h % 2 === 0 : true));
  return (
    <div className="p-4">
      <div className="relative h-4 text-xs text-text-dim" style={{ marginLeft: 112 }}>
        {ticks.map((h) => <span key={h} style={{ position: "absolute", left: `${((h - start) * 60) / span * 100}%`, transform: "translateX(-50%)" }}>{String(h).padStart(2, "0")}</span>)}
      </div>
      <div className="mt-1 space-y-1.5">
        {[...lanes.entries()].map(([id, lane]) => (
          <div key={id} className="flex items-center gap-2">
            <span className="shrink-0 truncate text-xs" style={{ width: 104 }}>{lane.name}</span>
            <div className="relative h-8 flex-1 rounded bg-bg-input">
              {nowPos > 0 && nowPos < 100 && <div style={{ position: "absolute", left: `${nowPos}%`, top: 0, bottom: 0, width: 1, background: "var(--color-red)" }} />}
              {lane.slots.map((s) => {
                const l = pos(s.starts_at);
                const w = Math.max(1.5, pos(s.ends_at) - l);
                return (
                  <a key={s.id} href={pageLink(props, { work: s.work?.number ?? "" })} title={`${clock(s.starts_at)}–${clock(s.ends_at)} ${label(s)}`}
                    className="absolute overflow-hidden rounded border border-border bg-bg-card px-1 text-xs hover:border-accent"
                    style={{ left: `${l}%`, width: `${w}%`, top: 2, bottom: 2, borderLeft: `3px solid ${s.status === "in_progress" ? "var(--color-green)" : s.status === "missed" ? "var(--color-red)" : "var(--color-accent)"}`, opacity: s.status === "done" ? 0.55 : 1, whiteSpace: "nowrap", lineHeight: "26px" }}>
                    {label(s)}
                  </a>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

export default function OperationsAgendaWidget(props: WidgetProps) {
  const brand = brandSetting(props.widgetSettings);
  const range = setting(props.widgetSettings, "range", RANGES, "today");
  const resource = intSetting(props.widgetSettings, "resource_id", 0, 0, Number.MAX_SAFE_INTEGER);
  const max = intSetting(props.widgetSettings, "max_items", 8, 3, 30);
  const full = props.widgetSize === "full";
  const state = useWidgetData(props, `${props.projectId}|${props.installId}|${brand}|${range}|${resource}|${new Date().toDateString()}`, (signal) => {
    const from = new Date(); from.setHours(0, 0, 0, 0);
    const to = new Date(from); to.setDate(from.getDate() + (range === "week" ? 7 : 1));
    return callTool<{ slots: Slot[] }>(props, "slots_list", {
      from: from.toISOString(), to: to.toISOString(), ...(brand ? { brand } : {}), ...(resource ? { resource_id: resource } : {}),
    }, signal).then((r) => (r.slots ?? []).filter((s) => s.status !== "cancelled").sort((a, b) => a.starts_at.localeCompare(b.starts_at)));
  });
  if (!props.projectId) return <NoProject title="Agenda" />;

  const slots = state.data;
  const now = Date.now();
  const upcoming = (slots ?? []).filter((s) => s.status === "in_progress" || new Date(s.ends_at).getTime() > now);
  const next = upcoming.find((s) => s.status === "booked");
  const days = new Map<string, Slot[]>();
  for (const s of upcoming.slice(0, max)) {
    const k = day(s.starts_at);
    if (!days.has(k)) days.set(k, []);
    days.get(k)!.push(s);
  }

  return (
    <WidgetFrame title="Agenda" link={pageLink(props, { tab: "agenda" })} error={state.error} loading={!slots && !state.error}
      sub={slots ? `${slots.length} booking${slots.length === 1 ? "" : "s"} ${range === "week" ? "this week" : "today"}${next ? ` · next ${day(next.starts_at) === "Today" ? "" : `${day(next.starts_at)} `}${clock(next.starts_at)}` : ""}` : undefined}
      empty={slots && slots.length === 0 ? (range === "week" ? "Nothing booked in the next 7 days." : "Nothing booked today.") : undefined}>
      {slots && slots.length > 0 && (full && range === "today" ? <Timeline slots={slots} props={props} /> : (
        upcoming.length === 0 ? <p className="px-4 py-5 text-xs text-text-dim">Everything booked {range === "week" ? "this week" : "today"} is done.</p> : (
          <div>
            {[...days.entries()].map(([d, list]) => (
              <div key={d}>
                {range === "week" && <h3 className="px-4 pt-3 text-xs uppercase tracking-wide text-text-dim">{d}</h3>}
                <ul>
                  {list.map((s) => (
                    <li key={s.id}>
                      <a href={pageLink(props, { work: s.work?.number ?? "" })} className="flex items-center gap-3 border-b border-border px-4 py-2 hover:bg-bg-hover">
                        <span className="w-12 shrink-0 font-mono text-xs text-text-muted">{clock(s.starts_at)}</span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm">{label(s)}
                            {s.work?.priority && s.work.priority !== "p3" && <span className={`ml-1.5 rounded px-1 text-xs ${PRIORITY_TONE[s.work.priority] ?? ""}`}>{s.work.priority.toUpperCase()}</span>}</span>
                          <span className="block truncate text-xs text-text-dim">{detail(s)}</span>
                        </span>
                        <span className={`shrink-0 text-xs ${STATUS_TONE[s.status] ?? "text-text-muted"}`}>
                          {s.status === "booked" ? (s.eta ? `ETA ${clock(s.eta)}` : "") : human(s.status)}
                        </span>
                      </a>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
            {upcoming.length > max && <a href={pageLink(props, { tab: "agenda" })} className="block px-4 py-2 text-xs text-text-muted hover:text-accent">{upcoming.length - max} more</a>}
          </div>
        )
      ))}
    </WidgetFrame>
  );
}
