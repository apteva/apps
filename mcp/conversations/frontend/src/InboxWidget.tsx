import { MessageComponents } from "./messageComponents";
import type { Message } from "./types";
import { useConversationLocalization, type ConversationLocalization } from "./i18n";
import { reportSectionsText } from "./messageContent";
// InboxWidget — the conversations app's dashboard.home widget, a
// faithful recreation of the dashboard's AptevaInbox (the operator's
// favorite): tone-coded rows (blue approvals, red/yellow alerts, left
// severity rail), ticking relative timestamps, Review / Open / Dismiss
// actions with detail modals, and the "You're all caught up" empty
// state — backed by this app's own /inbox instead of channel-chat.
//
// House rules: dashboard theme tokens only, inline SVG in
// currentColor, no emojis. Built by
// `bun run scripts/build-panels.ts --app conversations`.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export interface HostProps extends ConversationLocalization {
  agentId?: number;
  conversationsHref?: string;
  appName?: string;
  installId?: number;
  projectId?: string;
  dashboardScope?: "project" | "global";
  slot?: string;
  eventRevision?: number;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

import { useConversationAPI } from "./context";

// flattenPreview: one-line plain-text preview of model-authored
// markdown — raw pipes and heading hashes read as garbage in a row.
function flattenPreview(source: string): string {
  return source
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#*_`>|-]+/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 160);
}

// ─── wire shapes ─────────────────────────────────────────────────────

type InboxMessage = Message;

interface InboxItem {
  priority: number;
  message: InboxMessage;
  project_id?: string;
  project_name?: string;
  agent_name?: string;
}

interface InboxProject {
  id: string;
  name: string;
}

interface AgentInfo {
  id: number;
  name: string;
}

// ─── tones (ported verbatim from the dashboard's AptevaInbox) ────────

interface InboxTone {
  row: string;
  rail: string;
  badge: string;
  action: string;
}

function inboxTone(kind: string, severity: string): InboxTone {
  if (kind === "approval" || kind === "interactive") {
    return {
      row: "border-blue/25 bg-blue/5",
      rail: "bg-blue/80",
      badge: "border-blue/35 bg-blue/10 text-blue",
      action: "border-blue/40 text-blue hover:bg-blue/10",
    };
  }
  if (kind === "alert") {
    const sev = severity.toLowerCase();
    if (sev === "critical" || sev === "error") {
      return {
        row: "border-red/25 bg-red/5",
        rail: "bg-red/80",
        badge: "border-red/35 bg-red/10 text-red",
        action: "border-red/40 text-red hover:bg-red/10",
      };
    }
    if (sev === "warning" || sev === "warn") {
      return {
        row: "border-yellow/25 bg-yellow/5",
        rail: "bg-yellow/80",
        badge: "border-yellow/35 bg-yellow/10 text-yellow",
        action: "border-yellow/40 text-yellow hover:bg-yellow/10",
      };
    }
    return {
      row: "border-border/70 bg-bg-subtle/45",
      rail: "bg-text-muted/70",
      badge: "border-border bg-bg-input text-text-muted",
      action: "border-border text-text-muted hover:text-text hover:border-text-muted",
    };
  }
  return {
    row: "border-accent/25 bg-accent/5",
    rail: "bg-accent/80",
    badge: "border-accent/35 bg-accent/10 text-accent",
    action: "border-accent/35 text-accent hover:bg-accent/10",
  };
}

// ─── time (ticking, exact on hover — donor behavior) ─────────────────

function parseTime(value?: string): Date | null {
  if (!value) return null;
  const normalized = /(?:Z|[+-]\d\d:?\d\d)$/.test(value)
    ? value
    : `${value.replace(" ", "T")}Z`;
  const date = new Date(normalized);
  return Number.isNaN(date.getTime()) ? null : date;
}

// ─── card prop readers ───────────────────────────────────────────────

function card(message: InboxMessage, name: string): Record<string, unknown> {
  return message.components.find((c) => c.name === name)?.props ?? {};
}

function itemTitle(message: InboxMessage): string {
  const kind = message.component_kind;
  if (kind === "interactive") return String(message.components.find(c=>c.interaction)?.props.title ?? message.content);
  if (kind === "approval") return String(card(message, "approval-card").title ?? message.content);
  if (kind === "report") return String(card(message, "report-card").title ?? message.content);
  return String(card(message, "alert-card").text ?? message.content);
}

function itemBody(message: InboxMessage): string {
  const kind = message.component_kind;
  if (kind === "interactive") return String(message.components.find(c=>c.interaction)?.props.body ?? "");
  if (kind === "approval") return String(card(message, "approval-card").body ?? "");
  if (kind === "report") return [String(card(message, "report-card").summary ?? ""),reportSectionsText(card(message,"report-card").sections)].filter(Boolean).join("\n\n");
  return String(card(message, "alert-card").text ?? "");
}

// ─── overlay (lightweight Modal port, same as the panel's) ───────────

function Overlay({
  open,
  onClose,
  ariaLabel,
  children,
}: {
  open: boolean;
  onClose: () => void;
  ariaLabel: string;
  children: React.ReactNode;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        className="relative bg-bg-card border border-border rounded-lg shadow-lg w-full max-w-2xl max-h-full overflow-hidden flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        {children}
      </div>
    </div>
  );
}

// ─── detail modals ───────────────────────────────────────────────────

function ComponentModal({open,message,agentName,onClose,onActed,projectId}:{open:boolean;message:InboxMessage;agentName:string;onClose:()=>void;onActed:()=>void;projectId:string}) {
 const {t}=useConversationLocalization();
 return <Overlay open={open} onClose={onClose} ariaLabel={itemTitle(message)}>
  <div className="border-b border-border px-5 py-3 text-xs text-text-muted">{agentName}</div>
  <div className="overflow-auto px-5 py-4"><MessageComponents message={message} projectId={projectId} onChanged={()=>{onActed();onClose()}}/></div>
  <div className="flex justify-end gap-2 border-t border-border px-5 py-3"><button type="button" className="rounded border border-border px-3 py-1.5 text-xs text-text" onClick={onClose}>{t("common.close")}</button></div>
 </Overlay>;
}

// ─── rows ────────────────────────────────────────────────────────────

function InboxRow({
  item,
  now,
  agentName,
  onChanged,
  projectId,
  projectName,
}: {
  item: InboxItem;
  now: number;
  agentName: string;
  onChanged: () => void;
  projectId: string;
  projectName?: string;
}) {
  const { t, relativeTime, dateTime, statusLabel } = useConversationLocalization();
  const { apiPost } = useConversationAPI();
  const [detailOpen, setDetailOpen] = useState(false);
 const [actionError,setActionError]=useState("");
  const m = item.message;
  const kind = m.component_kind || "report";
  const severity = m.severity || "";
  const tone = inboxTone(kind, severity);
  const label = kind === "interactive" ? t("component.question") : kind === "approval" ? t("card.approval") : kind === "report" ? t("card.report") : t("card.alert");
  const meta =
    kind === "interactive" ? "pending" : kind === "approval"
      ? String(card(m, "approval-card").status ?? "pending")
      : kind === "report"
        ? String(card(m, "report-card").period ?? "") || "report"
        : severity || "alert";
  const createdAt = parseTime(m.created_at);
  const preview = flattenPreview(itemBody(m));

  const dismiss = async () => {
    try {
      await apiPost(`/message-dismiss`, { message_id: m.id }, projectId);
      onChanged();
    } catch(err) {setActionError(String(err));}
  };

  return (
    <>
      <article
        className={`h-[78px] rounded-md border px-3 py-2 flex items-center gap-3 overflow-hidden ${tone.row}`}
      >
        <div className={`w-1.5 h-10 rounded-full shrink-0 ${tone.rail}`} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-[10px] uppercase tracking-wide text-text-dim">
            <span className={`rounded border px-1.5 py-0.5 font-bold ${tone.badge}`}>{label}</span>
            {meta && (
              <span className="text-text-muted normal-case tracking-normal truncate">{kind === "report" && card(m, "report-card").period ? meta : statusLabel(meta)}</span>
            )}
          </div>
          <div className="mt-0.5 text-xs text-text truncate">{itemTitle(m)}</div>
          <div className="mt-0.5 flex items-center gap-2 text-[10px] text-text-dim min-w-0">
            <span className="shrink-0">{agentName}</span>
            {projectName && (
              <>
                <span className="text-text-muted">·</span>
                <span className="shrink-0 rounded border border-border px-1 py-0.5 text-[10px]">{projectName}</span>
              </>
            )}
            {preview && (
              <>
                <span className="text-text-muted">·</span>
                <span className="truncate">{preview}</span>
              </>
            )}
          </div>
        </div>
        <div className="shrink-0 flex items-center gap-3">
          <time
            dateTime={createdAt?.toISOString() || m.created_at}
            title={createdAt ? dateTime(createdAt) : m.created_at}
            className="text-[10px] text-text-dim tabular-nums whitespace-nowrap"
          >
            {relativeTime(createdAt, now)}
          </time>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => setDetailOpen(true)}
              className={`rounded border px-2.5 py-1 text-[11px] ${tone.action}`}
            >
              {kind === "approval" || kind === "interactive" ? t("common.review") : t("common.open")}
            </button>
            {kind !== "approval" && kind !== "interactive" && (
              <button
                type="button"
                onClick={() => void dismiss()}
                className="rounded border border-border px-2.5 py-1 text-[11px] text-text-dim hover:text-text hover:border-text-muted"
              >
                {t("common.dismiss")}
              </button>
            )}
          </div>
        </div>
      </article>
      {actionError&&<p role="alert" className="text-xs text-error">{actionError}</p>}
      <ComponentModal open={detailOpen} message={m} agentName={agentName} onClose={()=>setDetailOpen(false)} onActed={onChanged} projectId={projectId}/>

    </>
  );
}

// ─── widget root ─────────────────────────────────────────────────────

export default function InboxWidget(props: HostProps) {
  const { t, number } = useConversationLocalization();
  const { conversationsClient, apiGet, apiPost, apiPatch, apiDelete } = useConversationAPI();
  const projectId = props.projectId ?? "";
  const global = props.dashboardScope === "global";
  const [items, setItems] = useState<InboxItem[]>([]);
  const [total,setTotal]=useState(0);
  const [projects, setProjects] = useState<InboxProject[]>([]);
  const [selectedProject, setSelectedProject] = useState("");
  const [agents, setAgents] = useState<Map<number, string>>(new Map());
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const [limit,setLimit] = useState(props.widgetSize === "full" ? 12 : 6);
 const [cursor,setCursor] = useState("");
 const loadedCount=useRef(0);loadedCount.current=items.length;
 useEffect(()=>setLimit(props.widgetSize === "full" ? 12 : 6),[props.widgetSize]);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const inbox = await conversationsClient.inbox({
        agent_id: props.agentId,
        limit: 100,
        project_id: global ? selectedProject || undefined : undefined,
      });
      const all=[...inbox.items];let next=inbox.next_cursor;
      while(next&&all.length<loadedCount.current){const page=await conversationsClient.inbox({agent_id:props.agentId,cursor:next,limit:100,project_id:global ? selectedProject || undefined : undefined});all.push(...page.items.filter(row=>!all.some(old=>old.message.id===row.message.id)));if(next===page.next_cursor)break;next=page.next_cursor;}
      setItems(all);setTotal(inbox.total);setCursor(next);
      if(global)setProjects(inbox.projects ?? []);
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
	}, [global,props.agentId,conversationsClient,selectedProject]);

  useEffect(() => {
    load();
    const poll = window.setInterval(load, 15000);
    return () => window.clearInterval(poll);
    // eventRevision: the host bumps it on app events — reload then too.
  }, [load, props.eventRevision]);

  useEffect(() => {
    if(global){setAgents(new Map());return;}
    apiGet<AgentInfo[]>(`/agents`, projectId).then(
      (list) => setAgents(new Map(list.map((a) => [a.id, a.name]))),
      () => {},
    );
	}, [global,projectId,apiGet]);

  const visible = items.slice(0, limit);
  const agentName = (item: InboxItem) =>
    item.agent_name || (item.message.agent_id && agents.get(item.message.agent_id)) || (item.message.agent_id ? t("inbox.agentName", { id: String(item.message.agent_id) }) : t("common.app"));

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <div className="px-4 py-3 border-b border-border flex items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-text text-sm font-bold">{t("inbox.title")}</h2>
            {total > 0 && (
              <span className="inline-flex min-w-5 items-center justify-center rounded-full bg-accent/15 px-1.5 py-0.5 text-[10px] font-bold tabular-nums text-accent">
                {number(total)}
              </span>
            )}
          </div>
          <p className="text-text-dim text-[11px] mt-0.5">
            {t("inbox.description")}
          </p>
        </div>
        <div className="flex items-center gap-3">
          {global && projects.length > 0 && (
            <select aria-label={t("inbox.projectFilter")} value={selectedProject} onChange={(event)=>setSelectedProject(event.target.value)} className="max-w-sm rounded border border-border bg-bg-input px-2 py-1 text-[10px] text-text">
              <option value="">{t("inbox.allProjects")}</option>
              {projects.map(project=><option key={project.id} value={project.id}>{project.name || project.id}</option>)}
            </select>
          )}
          {props.conversationsHref && <a href={props.conversationsHref} className="text-[11px] text-text-muted hover:text-text">
            {t("inbox.openConversations")}
          </a>}
        </div>
      </div>

      <div className="min-h-0 flex-1 p-3 space-y-3 overflow-auto">
        {error && (
          <div className="rounded border border-red/30 bg-red/10 px-3 py-2 text-[11px] text-red">
            {error}
          </div>
        )}
        {!loading && !error && visible.length === 0 && (
          <div className="h-full min-h-[88px] flex items-center px-1 text-xs text-text-muted">
            <div>
              <p className="font-medium">{t("inbox.caughtUp")}</p>
              <p className="mt-1 text-[11px] text-text-dim">
                {t("inbox.emptyHint")}
              </p>
            </div>
          </div>
        )}
        {visible.map((item) => (
          <InboxRow
            key={item.message.id}
            item={item}
            now={now}
            agentName={agentName(item)}
            onChanged={() => void load()}
            projectId={item.project_id || projectId}
            projectName={global ? item.project_name || item.project_id : undefined}
          />
        ))}
        {total > visible.length && (props.conversationsHref ? (
          <a href={props.conversationsHref} className="text-xs text-text-muted">{t("inbox.moreCount", { count: total - visible.length })}</a>
        ) : (
          <button type="button" disabled={loading} className="text-xs text-text-muted" onClick={async()=>{
            setLoading(true);
            try {
              if(limit>=items.length&&cursor){const page=await conversationsClient.inbox({agent_id:props.agentId,cursor,limit:100,project_id:global ? selectedProject || undefined : undefined});setItems(current=>[...current,...page.items.filter(row=>!current.some(old=>old.message.id===row.message.id))]);setCursor(page.next_cursor);}
              setLimit(current=>current+12);
            }catch(err){setError(String(err));}finally{setLoading(false);}
          }}>{t("inbox.showMore")}</button>
        ))}
      </div>
    </section>
  );
}
