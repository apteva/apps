import { useMemo } from "react";
import { NoProject, WidgetFrame } from "./WidgetFrame";
import {
  PHASES, PHASE_COLOR, PHASE_LABEL, PRIORITY_TONE, ago, brandSetting, callTool, human, intSetting, pageLink, setting, slaLate, useWidgetData,
  type Work, type WidgetProps,
} from "./widget";

const LISTS = ["attention", "active", "recent"] as const;
const LIST_TITLE: Record<(typeof LISTS)[number], string> = { attention: "Needs attention", active: "In progress", recent: "Recently updated" };
const LIST_EMPTY: Record<(typeof LISTS)[number], string> = {
  attention: "Nothing is waiting on you.", active: "Nothing is in progress right now.", recent: "No open work.",
};

function reason(w: Work): string {
  if (w.status === "needs_owner") return "decision needed";
  if (slaLate(w)) return "past its deadline";
  if (w.status === "on_hold") return "on hold";
  if (w.priority === "p1") return "urgent";
  return human(w.status).toLowerCase();
}

function pick(work: Work[], list: (typeof LISTS)[number]): Work[] {
  if (list === "attention") return work.filter((w) => w.status === "needs_owner" || slaLate(w) || w.priority === "p1" || w.status === "on_hold");
  if (list === "active") return work.filter((w) => w.status === "in_progress" || w.phase === "execute");
  return [...work].sort((a, b) => b.updated_at.localeCompare(a.updated_at));
}

export default function OperationsWorkWidget(props: WidgetProps) {
  const brand = brandSetting(props.widgetSettings);
  const list = setting(props.widgetSettings, "list", LISTS, "attention");
  const max = intSetting(props.widgetSettings, "max_items", 8, 3, 20);
  const full = props.widgetSize === "full";
  const state = useWidgetData(props, `${props.projectId}|${props.installId}|${brand}`, (signal) =>
    callTool<{ work: Work[] }>(props, "work_search", { phase: "open", limit: 500, ...(brand ? { brand } : {}) }, signal).then((r) => r.work ?? []));

  const work = state.data;
  const byPhase = useMemo(() => {
    const g: Record<string, Work[]> = {};
    for (const p of PHASES) g[p] = [];
    for (const w of work ?? []) (g[w.phase] ??= []).push(w);
    return g;
  }, [work]);
  if (!props.projectId) return <NoProject title="Work in progress" />;

  const rows = pick(work ?? [], list);
  const open = (n: string) => pageLink(props, { work: n });
  const attention = (work ?? []).filter((w) => w.status === "needs_owner" || slaLate(w)).length;

  return (
    <WidgetFrame title="Work in progress" link={pageLink(props, { tab: "work" })} error={state.error} loading={!work && !state.error}
      sub={work ? `${work.length} open${attention ? ` · ${attention} waiting on you` : ""}${brand ? ` · ${brand}` : ""}` : undefined}>
      <div className="flex gap-2 overflow-x-auto border-b border-border px-4 py-3">
        {PHASES.map((p) => (
          <a key={p} href={pageLink(props, { tab: "work", phase: p })} className="min-w-0 flex-1 rounded border border-border px-2 py-1.5 hover:bg-bg-hover" style={{ minWidth: 72 }}>
            <div className="flex items-center gap-1.5 text-xs text-text-muted">
              <span style={{ width: 7, height: 7, borderRadius: 999, background: PHASE_COLOR[p], flexShrink: 0 }} />
              <span className="truncate">{PHASE_LABEL[p]}</span>
            </div>
            <div className="mt-0.5 text-lg font-semibold">{byPhase[p]?.length ?? 0}</div>
          </a>
        ))}
      </div>

      {full ? (
        <div className="grid gap-3 p-3" style={{ gridTemplateColumns: `repeat(${PHASES.length}, minmax(150px, 1fr))`, overflowX: "auto" }}>
          {PHASES.map((p) => (
            <div key={p} className="min-w-0 space-y-2">
              {(byPhase[p] ?? []).slice(0, max).map((w) => (
                <a key={w.id} href={open(w.number)} className="block rounded border border-border bg-bg p-2 hover:border-accent">
                  <div className="flex items-center gap-1.5">
                    <span className="font-mono text-xs text-text-muted">{w.number}</span>
                    {w.priority !== "p3" && <span className={`rounded px-1 text-xs ${PRIORITY_TONE[w.priority] ?? ""}`}>{w.priority.toUpperCase()}</span>}
                  </div>
                  <div className="mt-0.5 truncate text-xs">{w.customer_name || human(w.work_type)}</div>
                  <div className={`truncate text-xs ${w.status === "needs_owner" || slaLate(w) ? "text-red" : "text-text-dim"}`}>{reason(w)}</div>
                </a>
              ))}
              {(byPhase[p] ?? []).length > max && <div className="text-center text-xs text-text-dim">+{(byPhase[p] ?? []).length - max} more</div>}
              {(byPhase[p] ?? []).length === 0 && <div className="py-3 text-center text-xs text-text-dim">Empty</div>}
            </div>
          ))}
        </div>
      ) : (
        <>
          <h3 className="px-4 pt-3 text-xs uppercase tracking-wide text-text-dim">{LIST_TITLE[list]}</h3>
          {rows.length === 0 ? <p className="px-4 py-4 text-xs text-text-dim">{LIST_EMPTY[list]}</p> : (
            <ul>
              {rows.slice(0, max).map((w) => (
                <li key={w.id}>
                  <a href={open(w.number)} className="flex items-center gap-3 border-b border-border px-4 py-2 hover:bg-bg-hover">
                    <span style={{ width: 7, height: 7, borderRadius: 999, background: PHASE_COLOR[w.phase] ?? "var(--color-border)", flexShrink: 0 }} />
                    <span className="w-20 shrink-0 font-mono text-xs text-text-muted">{w.number}</span>
                    <span className="min-w-0 flex-1 truncate text-sm">{w.customer_name || human(w.work_type)}
                      <span className="text-text-dim"> · {w.summary || human(w.work_type)}</span></span>
                    <span className={`shrink-0 text-xs ${w.status === "needs_owner" || slaLate(w) ? "text-red" : "text-text-dim"}`}>{list === "recent" ? ago(w.updated_at) : reason(w)}</span>
                  </a>
                </li>
              ))}
            </ul>
          )}
          {rows.length > max && <a href={pageLink(props, { tab: "work" })} className="block px-4 py-2 text-xs text-text-muted hover:text-accent">{rows.length - max} more</a>}
        </>
      )}
    </WidgetFrame>
  );
}
