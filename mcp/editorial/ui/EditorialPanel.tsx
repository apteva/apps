import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type ButtonHTMLAttributes,
} from "react";
import { AppIcon, Avatar } from "@apteva/ui-kit";

type Props = { appName: string; installId: number; projectId: string };
type Settings = {
  statuses: string[];
  formats: string[];
  channels: string[];
  revision: number;
};
type Item = {
  id: number;
  revision: number;
  title: string;
  body: string;
  format: string;
  status: string;
  owner: string;
  deadline: string;
  planned_at: string;
  approval: string;
  reviewer: string;
  campaign: string;
  sources: string[];
  attachments: string[];
  tags: string[];
  fields: Record<string, unknown>;
  archived: boolean;
};
type Release = {
  id: number;
  revision: number;
  item_id: number;
  channel: string;
  planned_at: string;
  published_at: string;
  url: string;
  status: string;
  notes: string;
  app: string;
  external_id: number;
  results: Record<string, unknown>;
  synced_at: string;
  archived: boolean;
};
type History = {
  id: number;
  action: string;
  created_at: string;
  snapshot: unknown;
};
type API = (path: string, method?: string, body?: unknown) => Promise<any>;
type View = "calendar" | "board" | "content" | "backlog" | "settings";
const approvalStates = [
  "not_required",
  "pending",
  "approved",
  "changes_requested",
];
const itemKeys = [
  "title",
  "body",
  "format",
  "status",
  "owner",
  "deadline",
  "planned_at",
  "approval",
  "reviewer",
  "campaign",
  "sources",
  "attachments",
  "tags",
  "fields",
  "archived",
] as const;
const releaseKeys = [
  "channel",
  "planned_at",
  "published_at",
  "url",
  "status",
  "notes",
  "app",
  "external_id",
  "results",
  "archived",
] as const;
const label = (s: string) =>
  s.replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase());
const dateKey = (d: Date) =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const day = (s: string) =>
  !s ? "" : /^\d{4}-\d{2}-\d{2}$/.test(s) ? s : dateKey(new Date(s));
const shortDate = (s: string) =>
  s
    ? new Date(day(s) + "T12:00:00").toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
      })
    : "—";
const lines = (s: string) =>
  s
    .split("\n")
    .map((v) => v.trim())
    .filter(Boolean);
const split = (s: string) =>
  s
    .split(/[,\n]/)
    .map((v) => v.trim())
    .filter(Boolean);
const blank = (s: Settings): Item => ({
  id: 0,
  revision: 0,
  title: "",
  body: "",
  format: s.formats[0],
  status: s.statuses[0],
  owner: "",
  deadline: "",
  planned_at: "",
  approval: "not_required",
  reviewer: "",
  campaign: "",
  sources: [],
  attachments: [],
  tags: [],
  fields: {},
  archived: false,
});
const blankRelease = (id: number, channel: string): Release => ({
  id: 0,
  revision: 0,
  item_id: id,
  channel,
  planned_at: "",
  published_at: "",
  url: "",
  status: "planned",
  notes: "",
  app: "",
  external_id: 0,
  results: {},
  synced_at: "",
  archived: false,
});
const pick = <T extends object>(value: T, keys: readonly (keyof T)[]) =>
  Object.fromEntries(keys.map((k) => [k, value[k]]));

// Panels are independently bundled. Use the host's runtime tokens directly,
// matching CRM/Social without requiring new Tailwind utilities in the host build.
const styles = `
.editorial{height:100%;min-height:0;position:relative;display:flex;flex-direction:column;background:var(--bg);color:var(--text);font-family:inherit;font-size:12px;line-height:1.5}
.editorial *{box-sizing:border-box}.editorial button,.editorial input,.editorial select,.editorial textarea{font:inherit;color:inherit}.editorial button{cursor:pointer}.editorial button:disabled{opacity:.45;cursor:default}.editorial h1,.editorial h2,.editorial h3,.editorial p{margin:0}.editorial h1,.editorial h2{font-size:14px;font-weight:600}.editorial h3{font-size:12px;font-weight:600}.editorial a{color:var(--accent)}.editorial :focus-visible{outline:2px solid var(--accent);outline-offset:2px}.editorial .muted{color:var(--text-muted)}.editorial .dim{color:var(--text-dim)}.editorial .small{font-size:11px}.editorial .row{display:flex;align-items:center;gap:8px}.editorial .wrap{flex-wrap:wrap}.editorial .between{justify-content:space-between}.editorial .grow{flex:1;min-width:0}.editorial .truncate{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.editorial .shell-header{display:flex;align-items:center;gap:12px;min-height:45px;padding:0 16px;border-bottom:1px solid var(--border);flex-shrink:0}.editorial .identity{display:flex;gap:7px;align-items:center;font-weight:600}.editorial .identity-divider{height:20px;width:1px;background:var(--border)}.editorial .tabs{display:flex;align-self:stretch;gap:4px;align-items:stretch}.editorial .tab{padding:10px 12px 8px;border:0;border-bottom:2px solid transparent;background:transparent;color:var(--text-muted);white-space:nowrap}.editorial .tab:hover{color:var(--text);background:var(--bg-hover)}.editorial .tab.selected{color:var(--accent);border-bottom-color:var(--accent)}.editorial .actions{margin-left:auto;display:flex;align-items:center;gap:8px}.editorial .button{border:1px solid var(--border);border-radius:4px;background:transparent;padding:5px 10px;white-space:nowrap}.editorial .button:hover{background:var(--bg-hover)}.editorial .primary{background:var(--accent);color:var(--bg);border-color:transparent;font-weight:600}.editorial .primary:hover{background:var(--accent-hover)}.editorial .quiet{border-color:transparent;color:var(--text-muted)}.editorial .link-button{padding:0;border:0;background:transparent;color:var(--accent);text-align:left}.editorial .icon-button{width:28px;height:28px;display:inline-flex;align-items:center;justify-content:center;padding:0}.editorial .toolbar{display:flex;gap:8px;align-items:center;flex-wrap:wrap;padding:10px 16px;border-bottom:1px solid var(--border);flex-shrink:0}.editorial .search{width:230px}.editorial input,.editorial select,.editorial textarea{min-width:0;max-width:100%;background:var(--bg-input);border:1px solid var(--border);border-radius:4px;padding:6px 8px;color:var(--text)}.editorial input::placeholder,.editorial textarea::placeholder{color:var(--text-dim)}.editorial textarea{resize:vertical}.editorial .summary{display:flex;gap:16px;align-items:center;padding:7px 16px;border-bottom:1px solid var(--border);color:var(--text-muted);font-size:11px}.editorial .summary strong{color:var(--text);font-weight:500}.editorial .workspace{display:flex;flex:1;min-height:0;overflow:hidden}.editorial .main{flex:1;min-width:0;overflow:auto}.editorial .pad{padding:16px}.editorial .pill{display:inline-flex;align-items:center;border-radius:4px;padding:2px 6px;font-size:10px;white-space:nowrap;background:var(--bg-hover);color:var(--text-muted)}.editorial .pill[data-tone=accent]{background:color-mix(in srgb,var(--accent) 12%,transparent);color:var(--accent)}.editorial .pill[data-tone=success]{background:color-mix(in srgb,var(--success) 12%,transparent);color:var(--success)}.editorial .pill[data-tone=error]{background:color-mix(in srgb,var(--error) 12%,transparent);color:var(--error)}.editorial .pill[data-tone=warn]{background:color-mix(in srgb,var(--warn) 12%,transparent);color:var(--text)}.editorial .error{padding:8px 12px;border:1px solid color-mix(in srgb,var(--error) 40%,var(--border));background:color-mix(in srgb,var(--error) 8%,var(--bg));color:var(--error);margin:10px 16px;border-radius:4px}.editorial .empty{display:flex;align-items:center;justify-content:center;flex-direction:column;gap:10px;text-align:center;padding:64px 24px;color:var(--text-muted)}.editorial .empty h2{color:var(--text)}
.editorial .month-header{padding:12px 16px;display:flex;gap:8px;align-items:center;justify-content:space-between;position:sticky;top:0;z-index:1;background:var(--bg);border-bottom:1px solid var(--border)}.editorial .calendar{display:grid;grid-template-columns:repeat(7,minmax(95px,1fr));min-width:665px}.editorial .weekday{padding:7px 10px;font-size:11px;color:var(--text-muted);border-bottom:1px solid var(--border);text-align:right}.editorial .calendar-day{min-height:100px;border-right:1px solid var(--border);border-bottom:1px solid var(--border);padding:6px}.editorial .calendar-day:nth-child(7n){border-right:0}.editorial .outside{background:var(--bg-card);color:var(--text-dim)}.editorial .day-number{display:flex;justify-content:flex-end;margin-bottom:5px}.editorial .day-number span{width:22px;height:22px;display:flex;align-items:center;justify-content:center;border-radius:4px}.editorial .day-number .today{background:var(--accent);color:var(--bg);font-weight:600}.editorial .event{display:block;width:100%;text-align:left;border:1px solid var(--border);border-left:2px solid var(--accent);border-radius:3px;background:var(--bg-card);padding:5px 6px;margin-bottom:4px;font-size:11px}.editorial .event:hover{background:var(--bg-hover)}.editorial .event.release-event{border-left-color:var(--info)}.editorial .event.chosen{outline:1px solid var(--accent)}.editorial .unscheduled{padding:16px;border-top:1px solid var(--border)}.editorial .unscheduled-list{display:flex;gap:8px;flex-wrap:wrap;margin-top:10px}.editorial .board{display:flex;align-items:flex-start;gap:12px;padding:16px;min-height:100%}.editorial .column{width:240px;min-width:240px}.editorial .column-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:10px;color:var(--text-muted)}.editorial .content-card{border:1px solid var(--border);border-radius:6px;background:var(--bg-card);padding:12px;margin-bottom:8px}.editorial .content-card.chosen{border-color:var(--accent)}.editorial .card-title{display:block;width:100%;text-align:left;border:0;background:transparent;padding:8px 0;color:var(--text);font-weight:500}.editorial .card-footer{display:flex;justify-content:space-between;align-items:center;margin-top:10px;color:var(--text-muted);font-size:11px}.editorial table{width:100%;border-collapse:collapse;white-space:nowrap}.editorial th{text-align:left;font-size:11px;font-weight:500;color:var(--text-muted);background:var(--bg-card);position:sticky;top:0}.editorial th,.editorial td{padding:10px 12px;border-bottom:1px solid var(--border)}.editorial tbody tr:hover{background:var(--bg-hover)}.editorial tr.chosen{background:color-mix(in srgb,var(--accent) 8%,var(--bg))}.editorial .record-title{border:0;background:transparent;text-align:left;padding:0;font-weight:500;max-width:340px;display:block;color:var(--text)}.editorial .backlog-list{max-width:840px;margin:0 auto}.editorial .backlog-row{padding:12px 16px;border-bottom:1px solid var(--border);display:flex;gap:12px;align-items:center}.editorial .backlog-row:hover{background:var(--bg-hover)}
.editorial .inspector{width:440px;max-width:100%;flex-shrink:0;border-left:1px solid var(--border);display:flex;flex-direction:column;min-height:0;background:var(--bg)}.editorial .inspector-header{padding:12px 16px;border-bottom:1px solid var(--border)}.editorial .inspector-title{width:100%;font-size:16px;font-weight:600;margin-top:10px}.editorial .inspector-tabs{display:flex;gap:2px;padding:0 12px;border-bottom:1px solid var(--border)}.editorial .inspector-body{flex:1;overflow:auto;padding:16px}.editorial .inspector-footer{padding:10px 16px;border-top:1px solid var(--border);display:flex;align-items:center;justify-content:space-between;gap:8px;background:var(--bg-card)}.editorial .field{display:flex;flex-direction:column;gap:5px;font-size:11px;color:var(--text-muted);min-width:0}.editorial .field input,.editorial .field select,.editorial .field textarea{color:var(--text);font-size:12px}.editorial .fields{display:grid;grid-template-columns:1fr 1fr;gap:12px}.editorial .wide{grid-column:1/-1}.editorial .stack{display:flex;flex-direction:column;gap:14px}.editorial .section{padding-top:16px;margin-top:16px;border-top:1px solid var(--border)}.editorial .brief{width:100%;min-height:300px;line-height:1.7}.editorial .key-value{display:grid;grid-template-columns:1fr 1.4fr 28px;gap:6px}.editorial .release-card{border:1px solid var(--border);border-radius:5px;margin-top:10px;background:var(--bg-card)}.editorial .release-card summary{padding:10px 12px;cursor:pointer;list-style:none}.editorial .release-form{padding:12px;border-top:1px solid var(--border)}.editorial .history-row{padding:10px 0;border-bottom:1px solid var(--border)}.editorial .history-row summary{cursor:pointer}.editorial pre{font-size:11px;white-space:pre-wrap;word-break:break-word;color:var(--text-muted);padding:8px;background:var(--bg-input);border-radius:4px}.editorial .settings{padding:24px;max-width:900px;margin:auto}.editorial .settings-section{display:grid;grid-template-columns:220px 1fr;gap:24px;padding:20px 0;border-bottom:1px solid var(--border)}.editorial .settings-section textarea{width:100%}.editorial .connection{display:flex;justify-content:space-between;align-items:center;padding:10px 0;border-bottom:1px solid var(--border)}.editorial .date-picker{border:1px solid var(--border);border-radius:4px;padding:8px;margin-top:5px;background:var(--bg-card)}.editorial .date-grid{display:grid;grid-template-columns:repeat(7,1fr);gap:2px;margin-top:6px}.editorial .date-cell{border:0;background:transparent;padding:5px 0;text-align:center;border-radius:3px;font-size:11px}.editorial .date-cell:hover{background:var(--bg-hover)}.editorial .date-cell.selected{background:var(--accent);color:var(--bg)}.editorial .discard{padding:12px;background:var(--bg-card);border-bottom:1px solid var(--border)}
@media(max-width:1000px){.editorial .identity span.app-title{display:none}.editorial .shell-header{gap:6px;padding:0 12px}.editorial .tab{padding-left:8px;padding-right:8px}.editorial .inspector{width:380px}}@media(max-width:760px){.editorial .shell-header{flex-wrap:wrap;padding:8px 12px}.editorial .tabs{order:3;width:100%;overflow:auto}.editorial .identity span.app-title{display:inline}.editorial .identity-divider{display:none}.editorial .inspector{position:absolute;right:0;top:0;bottom:0;z-index:5;width:min(440px,100%);box-shadow:-12px 0 24px var(--bg-overlay)}.editorial .settings-section{grid-template-columns:1fr;gap:10px}.editorial .toolbar .search{width:100%}}
`;
function Button({
  children,
  primary = false,
  quiet = false,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  primary?: boolean;
  quiet?: boolean;
}) {
  return (
    <button
      type="button"
      {...props}
      className={`button ${primary ? "primary" : ""} ${quiet ? "quiet" : ""} ${props.className || ""}`}
    >
      {children}
    </button>
  );
}
function Pill({ value }: { value: string }) {
  const tone = ["approved", "published"].includes(value)
    ? "success"
    : ["failed", "changes_requested"].includes(value)
      ? "error"
      : ["pending", "review"].includes(value)
        ? "warn"
        : ["scheduled", "ready", "in_progress"].includes(value)
          ? "accent"
          : "neutral";
  return (
    <span className="pill" data-tone={tone}>
      {label(value)}
    </span>
  );
}
function Field({
  name,
  children,
  wide = false,
}: {
  name: string;
  children: ReactNode;
  wide?: boolean;
}) {
  return (
    <label className={`field ${wide ? "wide" : ""}`}>
      {name}
      {children}
    </label>
  );
}
function Owner({ name }: { name: string }) {
  return (
    <span className="row small muted">
      {name && <Avatar name={name} size={18} />}
      <span>{name || "Unassigned"}</span>
    </span>
  );
}
function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <h2>{title}</h2>
      {children}
    </div>
  );
}
export default function EditorialPanel(props: Props) {
  return <Planner key={`${props.installId}:${props.projectId}`} {...props} />;
}
function Planner({ appName, installId, projectId }: Props) {
  const [items, setItems] = useState<Item[]>([]),
    [releases, setReleases] = useState<Release[]>([]),
    [settings, setSettings] = useState<Settings | null>(null);
  const [view, setView] = useState<View>("calendar"),
    [selected, setSelected] = useState<Item | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [q, setQ] = useState(""),
    [status, setStatus] = useState(""),
    [format, setFormat] = useState(""),
    [owner, setOwner] = useState(""),
    [approval, setApproval] = useState(""),
    [campaign, setCampaign] = useState(""),
    [archived, setArchived] = useState(false),
    [more, setMore] = useState(false);
  const [month, setMonth] = useState(
      () => new Date(new Date().getFullYear(), new Date().getMonth(), 1),
    ),
    [dateField, setDateField] = useState<"planned_at" | "deadline">(
      "planned_at",
    );
  const [editorReset, setEditorReset] = useState(0);
  const seq = useRef(0),
    dirty = useRef(false),
    [pendingSelection, setPendingSelection] = useState<{
      item: Item | null;
    } | null>(null);
  const api: API = useCallback(
    async (path, method = "GET", body) => {
      if (!projectId) throw new Error("Choose a project first.");
      const params = new URLSearchParams({ project_id: projectId });
      if (installId) params.set("install_id", String(installId));
      const res = await fetch(
        `/api/apps/${encodeURIComponent(appName || "editorial")}${path}${path.includes("?") ? "&" : "?"}${params}`,
        {
          method,
          credentials: "same-origin",
          headers: body ? { "Content-Type": "application/json" } : undefined,
          body: body ? JSON.stringify(body) : undefined,
        },
      );
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || "Request failed");
      return data;
    },
    [appName, installId, projectId],
  );
  const load = useCallback(async () => {
    const current = ++seq.current;
    setLoading(true);
    try {
      const [s, first] = await Promise.all([
        api("/settings"),
        api("/items?archived=all&limit=500"),
      ]);
      const all: Item[] = [...first.items],
        rs: Release[] = [...first.releases];
      for (let offset = all.length; offset < first.total;) {
        const p = await api(`/items?archived=all&limit=500&offset=${offset}`);
        if (!p.items.length) break;
        all.push(...p.items);
        rs.push(...p.releases);
        offset += p.items.length;
      }
      if (seq.current === current) {
        setItems(all);
        setReleases(rs);
        setSettings(s);
        setError("");
      }
    } catch (e) {
      if (seq.current === current) setError((e as Error).message);
    } finally {
      if (seq.current === current) setLoading(false);
    }
  }, [api]);
  useEffect(() => {
    load();
    return () => {
      seq.current++;
    };
  }, [load]);
  const open = (item: Item | null) => {
    if (item?.id && item.id === selected?.id) return;
    if (dirty.current) {
      setPendingSelection({ item });
      return;
    }
    setSelected(item);
  };
  const filtered = useMemo(
    () =>
      items.filter(
        (i) =>
          i.archived === archived &&
          (!q ||
            `${i.title} ${i.body} ${i.tags.join(" ")}`
              .toLowerCase()
              .includes(q.toLowerCase())) &&
          (!status || i.status === status) &&
          (!format || i.format === format) &&
          (!owner || i.owner === owner) &&
          (!approval || i.approval === approval) &&
          (!campaign || i.campaign === campaign),
      ),
    [items, archived, q, status, format, owner, approval, campaign],
  );
  const unscheduled = filtered.filter(
    (i) =>
      !i.planned_at &&
      !releases.some((r) => r.item_id === i.id && !r.archived && r.planned_at),
  );
  const select = (
    name: string,
    value: string,
    set: (v: string) => void,
    values: string[],
  ) => (
    <select
      aria-label={name}
      value={value}
      onChange={(e) => set(e.target.value)}
    >
      <option value="">{name}</option>
      {values.filter(Boolean).map((v) => (
        <option key={v} value={v}>
          {label(v)}
        </option>
      ))}
    </select>
  );
  const iconParams = new URLSearchParams({ project_id: projectId, v: "0.1.1" });
  if (installId) iconParams.set("install_id", String(installId));
  const card = (i: Item) => (
    <article
      key={i.id}
      className={`content-card ${selected?.id === i.id ? "chosen" : ""}`}
    >
      <div className="row between">
        <span className="dim small">{label(i.format)}</span>
        {i.approval !== "not_required" && <Pill value={i.approval} />}
      </div>
      <button className="card-title" onClick={() => open(i)}>
        {i.title}
      </button>
      {i.campaign && <p className="small muted truncate">{i.campaign}</p>}
      <div className="card-footer">
        <Owner name={i.owner} />
        <span title={i.deadline}>Due {shortDate(i.deadline)}</span>
      </div>
    </article>
  );
  return (
    <div className="editorial">
      <style>{styles}</style>
      <header className="shell-header">
        <div className="identity">
          <AppIcon
            name="Editorial"
            src={`/api/apps/${encodeURIComponent(appName || "editorial")}/ui/icon.svg?${iconParams}`}
            iconStyle="monochrome"
            size="sm"
            framed={false}
            className="text-accent"
          />
          <span className="app-title">Editorial</span>
        </div>
        <span className="identity-divider" />
        <nav className="tabs" aria-label="Editorial views">
          {(
            ["calendar", "board", "content", "backlog", "settings"] as View[]
          ).map((v) => (
            <button
              key={v}
              className={`tab ${view === v ? "selected" : ""}`}
              aria-current={view === v ? "page" : undefined}
              onClick={() => setView(v)}
            >
              {label(v)}
            </button>
          ))}
        </nav>
        <div className="actions">
          <Button
            quiet
            onClick={load}
            disabled={loading}
            title="Refresh content"
          >
            Refresh
          </Button>
          <Button
            primary
            disabled={!settings}
            onClick={() => settings && open(blank(settings))}
          >
            + New content
          </Button>
        </div>
      </header>
      {view !== "settings" && (
        <>
          <div className="toolbar">
            <input
              className="search"
              aria-label="Search content"
              placeholder="Search content…"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
            {select(
              "All statuses",
              status,
              setStatus,
              settings?.statuses || [],
            )}
            {select("All formats", format, setFormat, settings?.formats || [])}
            {select("All owners", owner, setOwner, [
              ...new Set(items.map((i) => i.owner)),
            ])}
            <Button quiet onClick={() => setMore(!more)} aria-expanded={more}>
              Filters{approval || campaign || archived ? " •" : ""}
            </Button>
            {more && (
              <>
                {select("All approvals", approval, setApproval, approvalStates)}
                {select("All campaigns", campaign, setCampaign, [
                  ...new Set(items.map((i) => i.campaign)),
                ])}
                <select
                  aria-label="Archive filter"
                  value={String(archived)}
                  onChange={(e) => setArchived(e.target.value === "true")}
                >
                  <option value="false">Active content</option>
                  <option value="true">Archived content</option>
                </select>
              </>
            )}
          </div>
          <div className="summary">
            <span>
              <strong>{filtered.length}</strong> items
            </span>
            <span>
              <strong>
                {filtered.filter((i) => i.approval === "pending").length}
              </strong>{" "}
              awaiting approval
            </span>
            <span>
              <strong>{unscheduled.length}</strong> unscheduled
            </span>
            <span className="grow" />
            <span>{loading ? "Loading…" : "Changes saved per item"}</span>
          </div>
        </>
      )}
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      <div className="workspace">
        <main className="main" aria-busy={loading}>
          {view === "settings" ? (
            settings && (
              <SettingsView settings={settings} api={api} onSaved={load} />
            )
          ) : !items.length && !loading ? (
            <Empty title="Plan your first piece of content">
              <p>Capture an idea, write a brief, and plan its releases.</p>
              <Button
                primary
                disabled={!settings}
                onClick={() => settings && open(blank(settings))}
              >
                + Create content
              </Button>
              <p className="small">No connected apps required.</p>
            </Empty>
          ) : (
            <>
              {view === "calendar" && (
                <>
                  <div className="month-header">
                    <div className="row">
                      <Button
                        className="icon-button"
                        aria-label="Previous month"
                        onClick={() =>
                          setMonth(
                            new Date(
                              month.getFullYear(),
                              month.getMonth() - 1,
                              1,
                            ),
                          )
                        }
                      >
                        ‹
                      </Button>
                      <Button
                        className="icon-button"
                        aria-label="Next month"
                        onClick={() =>
                          setMonth(
                            new Date(
                              month.getFullYear(),
                              month.getMonth() + 1,
                              1,
                            ),
                          )
                        }
                      >
                        ›
                      </Button>
                      <h2>
                        {month.toLocaleDateString(undefined, {
                          month: "long",
                          year: "numeric",
                        })}
                      </h2>
                      <Button
                        quiet
                        onClick={() =>
                          setMonth(
                            new Date(
                              new Date().getFullYear(),
                              new Date().getMonth(),
                              1,
                            ),
                          )
                        }
                      >
                        Today
                      </Button>
                    </div>
                    <select
                      aria-label="Calendar dates"
                      value={dateField}
                      onChange={(e) =>
                        setDateField(e.target.value as typeof dateField)
                      }
                    >
                      <option value="planned_at">Publication dates</option>
                      <option value="deadline">Deadlines</option>
                    </select>
                  </div>
                  <Calendar
                    month={month}
                    items={filtered}
                    releases={releases}
                    field={dateField}
                    selected={selected?.id}
                    onOpen={open}
                  />
                  <section className="unscheduled">
                    <div className="row">
                      <h3>Unscheduled</h3>
                      <span className="pill">{unscheduled.length}</span>
                    </div>
                    {unscheduled.length ? (
                      <div className="unscheduled-list">
                        {unscheduled.map((i) => (
                          <Button key={i.id} onClick={() => open(i)}>
                            {i.title}
                          </Button>
                        ))}
                      </div>
                    ) : (
                      <p className="small muted" style={{ marginTop: 8 }}>
                        All content in this view has a publication date.
                      </p>
                    )}
                  </section>
                </>
              )}
              {view === "board" && (
                <div className="board">
                  {settings?.statuses.map((s) => (
                    <section className="column" key={s}>
                      <div className="column-header">
                        <h3>{label(s)}</h3>
                        <span className="pill">
                          {filtered.filter((i) => i.status === s).length}
                        </span>
                      </div>
                      {filtered.filter((i) => i.status === s).map(card)}
                      <Button
                        quiet
                        onClick={() =>
                          settings && open({ ...blank(settings), status: s })
                        }
                      >
                        + Add content
                      </Button>
                    </section>
                  ))}
                </div>
              )}
              {view === "content" && (
                <table>
                  <thead>
                    <tr>
                      {[
                        "Content",
                        "Status",
                        "Owner",
                        "Deadline",
                        "Publication",
                        "Approval",
                      ].map((h) => (
                        <th key={h}>{h}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((i) => (
                      <tr
                        key={i.id}
                        className={selected?.id === i.id ? "chosen" : ""}
                      >
                        <td>
                          <button
                            className="record-title truncate"
                            onClick={() => open(i)}
                          >
                            {i.title}
                          </button>
                          <span className="small dim">
                            {label(i.format)}
                            {i.campaign && ` · ${i.campaign}`}
                          </span>
                        </td>
                        <td>
                          <Pill value={i.status} />
                        </td>
                        <td>
                          <Owner name={i.owner} />
                        </td>
                        <td>{shortDate(i.deadline)}</td>
                        <td>{shortDate(i.planned_at)}</td>
                        <td>
                          {i.approval === "not_required" ? (
                            <span className="dim">—</span>
                          ) : (
                            <Pill value={i.approval} />
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {view === "backlog" && (
                <div className="backlog-list">
                  <div className="pad">
                    <h2>Backlog</h2>
                    <p className="small muted">
                      Ideas and content without a planned publication or release
                      date.
                    </p>
                  </div>
                  {unscheduled.map((i) => (
                    <div className="backlog-row" key={i.id}>
                      <span className="pill">{label(i.format)}</span>
                      <div className="grow">
                        <button
                          className="record-title"
                          onClick={() => open(i)}
                        >
                          {i.title}
                        </button>
                        {i.campaign && (
                          <span className="small muted">{i.campaign}</span>
                        )}
                      </div>
                      <Owner name={i.owner} />
                      <Pill value={i.status} />
                      <Button quiet onClick={() => open(i)}>
                        Plan →
                      </Button>
                    </div>
                  ))}
                  {!unscheduled.length && (
                    <Empty title="Nothing in the backlog">
                      <p>
                        Add an idea or clear a publication date to plan it
                        later.
                      </p>
                    </Empty>
                  )}
                </div>
              )}
              {!filtered.length &&
                view !== "calendar" &&
                view !== "backlog" && (
                  <Empty title="No matching content">
                    <p>Try changing your filters.</p>
                  </Empty>
                )}
            </>
          )}
        </main>
        {selected && settings && (
          <Inspector
            key={`${selected.id}:${editorReset}`}
            item={selected}
            settings={settings}
            api={api}
            dirty={(v) => {
              dirty.current = v;
            }}
            discard={pendingSelection !== null}
            onKeep={() => setPendingSelection(null)}
            onDiscard={() => {
              dirty.current = false;
              setSelected(pendingSelection?.item || null);
              setEditorReset((n) => n + 1);
              setPendingSelection(null);
            }}
            onClose={() => open(null)}
            onSaved={async (i) => {
              setSelected(i);
              await load();
            }}
            onReleasesChanged={load}
          />
        )}
      </div>
    </div>
  );
}
function Calendar({
  month,
  items,
  releases,
  field,
  selected,
  onOpen,
}: {
  month: Date;
  items: Item[];
  releases: Release[];
  field: "planned_at" | "deadline";
  selected?: number;
  onOpen: (i: Item) => void;
}) {
  const first = new Date(month.getFullYear(), month.getMonth(), 1),
    offset = (first.getDay() + 6) % 7;
  const count =
    Math.ceil(
      (offset +
        new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate()) /
        7,
    ) * 7;
  first.setDate(first.getDate() - offset);
  const byId = new Map(items.map((i) => [i.id, i]));
  return (
    <div style={{ overflowX: "auto" }}>
      <div className="calendar">
        {["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"].map((d) => (
          <div className="weekday" key={d}>
            {d}
          </div>
        ))}
        {Array.from({ length: count }, (_, n) => {
          const d = new Date(first);
          d.setDate(d.getDate() + n);
          const key = dateKey(d);
          return (
            <div
              className={`calendar-day ${d.getMonth() !== month.getMonth() ? "outside" : ""}`}
              key={key}
              aria-label={key}
            >
              <div className="day-number">
                <span className={key === dateKey(new Date()) ? "today" : ""}>
                  {d.getDate()}
                </span>
              </div>
              {items
                .filter((i) => day(i[field]) === key)
                .map((i) => (
                  <button
                    className={`event ${selected === i.id ? "chosen" : ""}`}
                    key={`i${i.id}`}
                    onClick={() => onOpen(i)}
                  >
                    <span className="small dim">{label(i.format)}</span>
                    <div>{i.title}</div>
                  </button>
                ))}
              {field === "planned_at" &&
                releases
                  .filter(
                    (r) =>
                      !r.archived &&
                      byId.has(r.item_id) &&
                      day(r.planned_at) === key,
                  )
                  .map((r) => (
                    <button
                      className={`event release-event ${selected === r.item_id ? "chosen" : ""}`}
                      key={`r${r.id}`}
                      onClick={() => onOpen(byId.get(r.item_id)!)}
                    >
                      <span className="small muted">{r.channel}</span>
                      <div>{byId.get(r.item_id)!.title}</div>
                    </button>
                  ))}
            </div>
          );
        })}
      </div>
    </div>
  );
}
function DateField({
  name,
  value,
  onChange,
}: {
  name: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const [open, setOpen] = useState(false),
    [month, setMonth] = useState(() => {
      const d = value ? new Date(day(value) + "T12:00:00") : new Date();
      return Number.isNaN(d.getTime()) ? new Date() : d;
    });
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  first.setDate(first.getDate() - ((first.getDay() + 6) % 7));
  return (
    <div>
      <Field name={name}>
        <div className="row">
          <input
            className="grow"
            aria-label={name}
            placeholder="YYYY-MM-DD"
            value={value}
            onChange={(e) => onChange(e.target.value)}
          />
          <Button
            className="icon-button"
            aria-label={`Choose ${name.toLowerCase()} date`}
            aria-expanded={open}
            onClick={() => setOpen(!open)}
          >
            ▦
          </Button>
        </div>
      </Field>
      {open && (
        <div className="date-picker">
          <div className="row between">
            <Button
              quiet
              aria-label={`Previous month for ${name}`}
              onClick={() =>
                setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))
              }
            >
              ‹
            </Button>
            <span className="small">
              {month.toLocaleDateString(undefined, {
                month: "short",
                year: "numeric",
              })}
            </span>
            <Button
              quiet
              aria-label={`Next month for ${name}`}
              onClick={() =>
                setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))
              }
            >
              ›
            </Button>
          </div>
          <div className="date-grid">
            {["M", "T", "W", "T", "F", "S", "S"].map((d, n) => (
              <span className="date-cell dim" key={n}>
                {d}
              </span>
            ))}
            {Array.from({ length: 42 }, (_, n) => {
              const d = new Date(first);
              d.setDate(d.getDate() + n);
              return (
                <button
                  key={n}
                  className={`date-cell ${dateKey(d) === day(value) ? "selected" : ""} ${d.getMonth() !== month.getMonth() ? "dim" : ""}`}
                  aria-label={dateKey(d)}
                  onClick={() => {
                    onChange(dateKey(d));
                    setOpen(false);
                  }}
                >
                  {d.getDate()}
                </button>
              );
            })}
          </div>
          <Button
            quiet
            onClick={() => {
              onChange("");
              setOpen(false);
            }}
          >
            Clear date
          </Button>
        </div>
      )}
    </div>
  );
}
function Inspector({
  item,
  settings,
  api,
  dirty,
  discard,
  onKeep,
  onDiscard,
  onClose,
  onSaved,
  onReleasesChanged,
}: {
  item: Item;
  settings: Settings;
  api: API;
  dirty: (v: boolean) => void;
  discard: boolean;
  onKeep: () => void;
  onDiscard: () => void;
  onClose: () => void;
  onSaved: (i: Item) => Promise<void>;
  onReleasesChanged: () => Promise<void>;
}) {
  const [draft, setDraft] = useState(item),
    [tab, setTab] = useState("brief"),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [changed, setChanged] = useState(false),
    [notice, setNotice] = useState("");
  const [sourceText, setSourceText] = useState(item.sources.join("\n")),
    [attachmentText, setAttachmentText] = useState(item.attachments.join("\n")),
    [tagText, setTagText] = useState(item.tags.join(", ")),
    [fields, setFields] = useState(() =>
      Object.entries(item.fields).map(([key, value]) => ({
        key,
        value: JSON.stringify(value),
      })),
    );
  const [detail, setDetail] = useState<{
      releases: Release[];
      history: History[];
      history_truncated: boolean;
    }>({ releases: [], history: [], history_truncated: false }),
    [newRelease, setNewRelease] = useState(false);
  const mark = () => {
    setChanged(true);
    dirty(true);
    setNotice("");
  };
  const set = (k: keyof Item, v: unknown) => {
    mark();
    setDraft((d) => ({ ...d, [k]: v }));
  };
  const read = useCallback(async () => {
    if (!item.id) return;
    try {
      setDetail(await api(`/items/${item.id}`));
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api, item.id]);
  useEffect(() => {
    read();
  }, [read]);
  const save = async (archived?: boolean) => {
    setBusy(true);
    setError("");
    try {
      const custom: Record<string, unknown> = {};
      for (const f of fields) {
        if (!f.key.trim() || Object.hasOwn(custom, f.key.trim()))
          throw new Error("Custom fields need unique, nonempty names.");
        try {
          custom[f.key.trim()] = JSON.parse(f.value);
        } catch {
          custom[f.key.trim()] = f.value;
        }
      }
      const patch = {
        ...pick(draft, itemKeys),
        sources: lines(sourceText),
        attachments: lines(attachmentText),
        tags: split(tagText),
        fields: custom,
        archived: archived ?? draft.archived,
      };
      const saved: Item = await api(
        draft.id ? `/items/${draft.id}` : "/items",
        draft.id ? "PATCH" : "POST",
        draft.id ? { revision: draft.revision, patch } : patch,
      );
      setDraft(saved);
      setChanged(false);
      dirty(false);
      setNotice(
        saved.approval === "pending" && draft.approval === "approved"
          ? "Content changed. Approval reset to pending."
          : "Saved",
      );
      await onSaved(saved);
      await read();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const text = (k: keyof Item, name: string) => (
    <Field name={name}>
      <input
        value={String(draft[k] || "")}
        onChange={(e) => set(k, e.target.value)}
      />
    </Field>
  );
  return (
    <aside className="inspector" aria-label="Content editor">
      {discard && (
        <div className="discard">
          <p>Discard unsaved content changes?</p>
          <div className="row" style={{ marginTop: 8 }}>
            <Button onClick={onKeep}>Keep editing</Button>
            <Button onClick={onDiscard}>Discard changes</Button>
          </div>
        </div>
      )}
      <div className="inspector-header">
        <div className="row between">
          <span className="small dim">
            {draft.id ? `Content #${draft.id}` : "New content"}
          </span>
          <Button
            quiet
            className="icon-button"
            aria-label="Close editor"
            disabled={busy}
            onClick={onClose}
          >
            ×
          </Button>
        </div>
        <input
          className="inspector-title"
          aria-label="Content title"
          placeholder="Untitled content"
          value={draft.title}
          onChange={(e) => set("title", e.target.value)}
        />
        <div className="row" style={{ marginTop: 10 }}>
          <Pill value={draft.status} />
          <Pill value={draft.approval} />
          {draft.archived && <span className="pill">Archived</span>}
        </div>
      </div>
      <nav className="inspector-tabs" aria-label="Content sections">
        {["brief", "details", "releases", "history"].map((v) => (
          <button
            key={v}
            className={`tab ${tab === v ? "selected" : ""}`}
            onClick={() => setTab(v)}
          >
            {label(v)}
            {v === "releases" && detail.releases.length
              ? ` (${detail.releases.length})`
              : ""}
          </button>
        ))}
      </nav>
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      <div className="inspector-body">
        {tab === "brief" && (
          <div className="stack">
            <div className="fields">
              <Field name="Format">
                <select
                  value={draft.format}
                  onChange={(e) => set("format", e.target.value)}
                >
                  {settings.formats.map((v) => (
                    <option key={v} value={v}>
                      {label(v)}
                    </option>
                  ))}
                </select>
              </Field>
              <Field name="Workflow">
                <select
                  value={draft.status}
                  onChange={(e) => set("status", e.target.value)}
                >
                  {settings.statuses.map((v) => (
                    <option key={v} value={v}>
                      {label(v)}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
            <Field name="Brief / draft">
              <textarea
                className="brief"
                placeholder="Audience, angle, key points, draft copy…"
                value={draft.body}
                onChange={(e) => set("body", e.target.value)}
              />
            </Field>
            <p className="small muted">
              Use Details for ownership, dates, approval and source material.
            </p>
          </div>
        )}
        {tab === "details" && (
          <>
            <div className="fields">
              {text("owner", "Owner")}
              {text("campaign", "Campaign / initiative")}
              <DateField
                name="Deadline"
                value={draft.deadline}
                onChange={(v) => set("deadline", v)}
              />
              <DateField
                name="Planned publication"
                value={draft.planned_at}
                onChange={(v) => set("planned_at", v)}
              />
              <Field name="Approval">
                <select
                  value={draft.approval}
                  onChange={(e) => set("approval", e.target.value)}
                >
                  {approvalStates.map((v) => (
                    <option key={v} value={v}>
                      {label(v)}
                    </option>
                  ))}
                </select>
              </Field>
              {text("reviewer", "Reviewer")}
            </div>
            <p className="small muted" style={{ marginTop: 10 }}>
              Editing reviewed content resets approval to pending.
            </p>
            <div className="section stack">
              <Field name="Source links · one per line">
                <textarea
                  rows={3}
                  value={sourceText}
                  onChange={(e) => {
                    mark();
                    setSourceText(e.target.value);
                  }}
                />
              </Field>
              <Field name="Attachment links · one per line">
                <textarea
                  rows={3}
                  value={attachmentText}
                  onChange={(e) => {
                    mark();
                    setAttachmentText(e.target.value);
                  }}
                />
              </Field>
              <Field name="Tags">
                <input
                  value={tagText}
                  placeholder="Comma-separated tags"
                  onChange={(e) => {
                    mark();
                    setTagText(e.target.value);
                  }}
                />
              </Field>
            </div>
            <section className="section stack">
              <div className="row between">
                <h3>Custom fields</h3>
                <Button
                  quiet
                  onClick={() => {
                    mark();
                    setFields((f) => [...f, { key: "", value: "" }]);
                  }}
                >
                  + Add field
                </Button>
              </div>
              {fields.map((f, n) => (
                <div className="key-value" key={n}>
                  <input
                    aria-label={`Field ${n + 1} name`}
                    placeholder="Name"
                    value={f.key}
                    onChange={(e) => {
                      mark();
                      setFields((fs) =>
                        fs.map((x, i) =>
                          i === n ? { ...x, key: e.target.value } : x,
                        ),
                      );
                    }}
                  />
                  <input
                    aria-label={`Field ${n + 1} value`}
                    placeholder="Value"
                    value={f.value}
                    onChange={(e) => {
                      mark();
                      setFields((fs) =>
                        fs.map((x, i) =>
                          i === n ? { ...x, value: e.target.value } : x,
                        ),
                      );
                    }}
                  />
                  <Button
                    quiet
                    className="icon-button"
                    aria-label={`Remove field ${n + 1}`}
                    onClick={() => {
                      mark();
                      setFields((fs) => fs.filter((_, i) => i !== n));
                    }}
                  >
                    ×
                  </Button>
                </div>
              ))}
            </section>
            {draft.id > 0 && (
              <div className="section">
                <Button disabled={busy} onClick={() => save(!draft.archived)}>
                  {draft.archived ? "Restore content" : "Archive content"}
                </Button>
              </div>
            )}
          </>
        )}
        {tab === "releases" && (
          <>
            <div className="row between">
              <h3>Channel releases</h3>
              <Button
                disabled={!draft.id || draft.archived || newRelease}
                onClick={() => setNewRelease(true)}
              >
                + Plan release
              </Button>
            </div>
            <p className="small muted" style={{ marginTop: 8 }}>
              Plan each channel separately. Delivery stays in your publishing
              apps.
            </p>
            {!draft.id && (
              <p className="small muted" style={{ marginTop: 16 }}>
                Save this item to add releases.
              </p>
            )}
            {detail.releases.map((r) => (
              <ReleaseEditor
                key={`${r.id}:${r.revision}`}
                release={r}
                api={api}
                settings={settings}
                disabled={draft.archived}
                onSaved={async () => {
                  await read();
                  await onReleasesChanged();
                }}
              />
            ))}
            {newRelease && (
              <ReleaseEditor
                release={blankRelease(draft.id, settings.channels[0])}
                api={api}
                settings={settings}
                disabled={draft.archived}
                onCancel={() => setNewRelease(false)}
                onSaved={async () => {
                  setNewRelease(false);
                  await read();
                  await onReleasesChanged();
                }}
              />
            )}
          </>
        )}
        {tab === "history" && (
          <>
            <h3>Change history</h3>
            {!detail.history.length && (
              <p className="small muted" style={{ marginTop: 10 }}>
                Saved changes will appear here.
              </p>
            )}
            {detail.history.map((h) => (
              <details className="history-row" key={h.id}>
                <summary>
                  <span>{label(h.action.replaceAll(".", " "))}</span>
                  <div className="small dim">
                    {new Date(h.created_at).toLocaleString()}
                  </div>
                </summary>
                <pre>{JSON.stringify(h.snapshot, null, 2)}</pre>
              </details>
            ))}
            {detail.history_truncated && (
              <p className="small muted">Showing the latest 100 changes.</p>
            )}
          </>
        )}
      </div>
      <footer className="inspector-footer">
        <span className="small muted" role="status">
          {busy
            ? "Saving…"
            : changed
              ? "Unsaved changes"
              : notice || "All changes saved"}
        </span>
        <Button
          primary
          disabled={busy || !draft.title.trim()}
          onClick={() => save()}
        >
          Save content
        </Button>
      </footer>
    </aside>
  );
}
function ReleaseEditor({
  release,
  api,
  settings,
  disabled,
  onSaved,
  onCancel,
}: {
  release: Release;
  api: API;
  settings: Settings;
  disabled: boolean;
  onSaved: () => Promise<void>;
  onCancel?: () => void;
}) {
  const [r, setR] = useState(release),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [results, setResults] = useState(JSON.stringify(release.results, null, 2)),
    [records, setRecords] = useState<any[]>([]);
  const set = (k: keyof Release, v: unknown) => setR((x) => ({ ...x, [k]: v }));
  const run = async (f: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await f();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const save = () =>
    run(async () => {
      let manual = release.results;
      if (!r.app) {
        manual = JSON.parse(results);
        if (!manual || Array.isArray(manual) || typeof manual !== "object")
          throw new Error("Results must be a JSON object.");
      }
      const patch = {
        ...pick(r, releaseKeys),
        external_id: r.app ? r.external_id : 0,
        results: manual,
      };
      await api(
        r.id ? `/releases/${r.id}` : "/releases",
        r.id ? "PATCH" : "POST",
        r.id
          ? { revision: r.revision, patch }
          : { item_id: r.item_id, ...patch },
      );
      await onSaved();
    });
  return (
    <details className="release-card" open={!release.id}>
      <summary>
        <div className="row between">
          <strong>{r.channel || "New release"}</strong>
          <Pill value={r.archived ? "archived" : r.status} />
        </div>
        <div className="small muted">
          {r.planned_at ? shortDate(r.planned_at) : "No date"}
          {r.app && ` · ${label(r.app)}`}
          {typeof release.results.status === "string" &&
            ` · Delivery: ${release.results.status}`}
        </div>
      </summary>
      <div className="release-form stack">
        {error && (
          <p className="error" role="alert" style={{ margin: 0 }}>
            {error}
          </p>
        )}
        <div className="fields">
          <Field name="Channel">
            <input
              list={`editorial-channels-${r.id}`}
              value={r.channel}
              onChange={(e) => set("channel", e.target.value)}
            />
            <datalist id={`editorial-channels-${r.id}`}>
              {settings.channels.map((v) => (
                <option key={v}>{v}</option>
              ))}
            </datalist>
          </Field>
          <Field name="Planning status">
            <select
              value={r.status}
              onChange={(e) => set("status", e.target.value)}
            >
              {["planned", "scheduled", "published", "failed", "cancelled"].map(
                (v) => (
                  <option key={v} value={v}>
                    {label(v)}
                  </option>
                ),
              )}
            </select>
          </Field>
          <DateField
            name="Release date"
            value={r.planned_at}
            onChange={(v) => set("planned_at", v)}
          />
          <DateField
            name="Published date"
            value={r.published_at}
            onChange={(v) => set("published_at", v)}
          />
          <Field name="Published URL" wide>
            <input value={r.url} onChange={(e) => set("url", e.target.value)} />
          </Field>
          <Field name="Notes" wide>
            <textarea
              rows={2}
              value={r.notes}
              onChange={(e) => set("notes", e.target.value)}
            />
          </Field>
          <Field name="Optional app">
            <select
              value={r.app}
              onChange={(e) => {
                set("app", e.target.value);
                set("external_id", 0);
                setRecords([]);
              }}
            >
              <option value="">Standalone</option>
              <option value="social">Social</option>
              <option value="campaigns">Campaigns</option>
            </select>
          </Field>
          {r.app && (
            <Field name="Existing record ID">
              <input
                type="number"
                min={1}
                value={r.external_id || ""}
                onChange={(e) => set("external_id", Number(e.target.value))}
              />
            </Field>
          )}
        </div>
        {r.app ? (
          <>
            <Button
              disabled={busy}
              onClick={() =>
                run(async () => {
                  const out = await api(`/integrations?app=${r.app}`);
                  setRecords(out.posts || out.campaigns || []);
                })
              }
            >
              Browse existing records
            </Button>
            {records.length > 0 && (
              <select
                aria-label="Existing publisher record"
                value={r.external_id || ""}
                onChange={(e) => set("external_id", Number(e.target.value))}
              >
                <option value="">Choose record</option>
                {records.map((x) => (
                  <option key={x.id} value={x.id}>
                    #{x.id} ·{" "}
                    {String(x.name || x.body || x.status).slice(0, 65)}
                  </option>
                ))}
              </select>
            )}
            <p className="small muted">
              Connect the optional app in installation settings. Linking does
              not publish.
            </p>
          </>
        ) : (
          <Field name="Publication results · JSON">
            <textarea
              rows={3}
              value={results}
              onChange={(e) => setResults(e.target.value)}
            />
          </Field>
        )}
        <label className="row small muted">
          <input
            type="checkbox"
            checked={r.archived}
            onChange={(e) => set("archived", e.target.checked)}
          />
          Archived release
        </label>
        <div className="row wrap">
          <Button primary disabled={busy || disabled} onClick={save}>
            Save release
          </Button>
          {onCancel && (
            <Button disabled={busy} onClick={onCancel}>
              Cancel
            </Button>
          )}
          {release.id > 0 && release.app && (
            <Button
              disabled={busy || disabled}
              onClick={() =>
                run(async () => {
                  await api(`/releases/${release.id}/refresh`, "POST", {});
                  await onSaved();
                })
              }
            >
              Refresh results
            </Button>
          )}
        </div>
        {release.url && (
          <a href={release.url} target="_blank" rel="noreferrer">
            Open publication ↗
          </a>
        )}
        {release.synced_at && (
          <p className="small dim">
            Refreshed {new Date(release.synced_at).toLocaleString()}
          </p>
        )}
        {Object.keys(release.results).length > 0 && (
          <details>
            <summary>Saved results</summary>
            <pre>{JSON.stringify(release.results, null, 2)}</pre>
          </details>
        )}
      </div>
    </details>
  );
}
function SettingsView({
  settings,
  api,
  onSaved,
}: {
  settings: Settings;
  api: API;
  onSaved: () => Promise<void>;
}) {
  const [draft, setDraft] = useState({
      statuses: settings.statuses.join("\n"),
      formats: settings.formats.join("\n"),
      channels: settings.channels.join("\n"),
    }),
    [revision, setRevision] = useState(settings.revision),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [message, setMessage] = useState(""),
    [bindings, setBindings] = useState<Record<string, boolean> | null>(null);
  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const s = await api("/settings", "PATCH", {
        revision,
        statuses: split(draft.statuses),
        formats: split(draft.formats),
        channels: split(draft.channels),
      });
      setRevision(s.revision);
      setMessage("Settings saved");
      await onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="settings">
      <div className="row between">
        <div>
          <h2>Editorial settings</h2>
          <p className="muted small">
            Configure your team's planning workflow.
          </p>
        </div>
        <Button primary disabled={busy} onClick={save}>
          Save settings
        </Button>
      </div>
      {error && (
        <div className="error" role="alert" style={{ margin: "12px 0" }}>
          {error}
        </div>
      )}
      {message && (
        <p role="status" className="small muted">
          {message}
        </p>
      )}
      {(["statuses", "formats", "channels"] as const).map((k) => (
        <section className="settings-section" key={k}>
          <div>
            <h3>{k === "statuses" ? "Workflow statuses" : label(k)}</h3>
            <p className="small muted" style={{ marginTop: 5 }}>
              {k === "statuses"
                ? "Board columns in display order. Keep statuses currently used by content."
                : k === "formats"
                  ? "Content types available to your team. The first entry is the default."
                  : "Suggested release channels. Releases can also use a custom channel."}
            </p>
          </div>
          <Field name={`${label(k)} · one per line`}>
            <textarea
              rows={6}
              value={draft[k]}
              onChange={(e) => {
                setMessage("");
                setDraft((d) => ({ ...d, [k]: e.target.value }));
              }}
            />
          </Field>
        </section>
      ))}
      <section className="settings-section">
        <div>
          <h3>Optional connections</h3>
          <p className="small muted" style={{ marginTop: 5 }}>
            Editorial works independently. Connect Social or Campaigns only to
            link delivery records and refresh results.
          </p>
        </div>
        <div>
          {["social", "campaigns"].map((app) => (
            <div className="connection" key={app}>
              <span>{label(app)}</span>
              <span className="small muted">
                {bindings
                  ? bindings[app]
                    ? "Connected"
                    : "Not connected"
                  : "Optional"}
              </span>
            </div>
          ))}
          <Button
            quiet
            style={{ marginTop: 12 }}
            onClick={async () => {
              try {
                setBindings(await api("/integrations"));
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            Check connections
          </Button>
        </div>
      </section>
    </div>
  );
}
