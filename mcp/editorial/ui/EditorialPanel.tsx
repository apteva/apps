import { useCallback, useEffect, useMemo, useRef, useState } from "react";

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
  updated_at: string;
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
  results: Record<string, any>;
  synced_at: string;
  archived: boolean;
};
type API = (path: string, method?: string, body?: unknown) => Promise<any>;
const label = (s: string) => s.replaceAll("_", " ");
const approvals = ["not_required", "pending", "approved", "changes_requested"];
const dateKey = (d: Date) =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const split = (s: string) =>
  s
    .split(/[,\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
const day = (s: string) =>
  s ? (/^\d{4}-\d{2}-\d{2}$/.test(s) ? s : dateKey(new Date(s))) : "";
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
  updated_at: "",
});
const freshRelease = (id: number, channel: string): Release => ({
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
const styles = `
.ed{height:100%;min-height:500px;display:flex;flex-direction:column;background:var(--color-bg,#101315);color:var(--color-text,#e7e9e8);font:13px/1.5 system-ui,sans-serif;--ed-line:var(--color-border,#303638);--ed-panel:var(--color-bg-input,#191e20);--ed-muted:var(--color-text-muted,#97a3a3);--ed-accent:#c2e7b4}
.ed *{box-sizing:border-box}.ed button,.ed input,.ed select,.ed textarea{font:inherit;color:inherit}.ed button{cursor:pointer;border:1px solid var(--ed-line);background:transparent;border-radius:7px;padding:7px 11px}.ed button:hover{background:var(--ed-panel)}.ed button:disabled{opacity:.45;cursor:wait}.ed .primary,.ed button.active{background:var(--ed-accent);color:#17351d;border-color:transparent}.ed input,.ed select,.ed textarea{background:var(--ed-panel);border:1px solid var(--ed-line);border-radius:6px;padding:8px;min-width:0;max-width:100%}.ed textarea{resize:vertical}.ed input:focus,.ed textarea:focus,.ed select:focus{outline:2px solid #83b59e;outline-offset:1px}.ed h1{font-size:25px;letter-spacing:-.7px;margin:0}.ed h2{font-size:18px;margin:0}.ed h3{font-size:14px;margin:0}.ed p{margin:4px 0}.ed .muted{color:var(--ed-muted)}.ed .small{font-size:11px}.ed .top{padding:24px 28px 18px;border-bottom:1px solid var(--ed-line)}.ed .row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.ed .between{justify-content:space-between}.ed .toolbar{padding:14px 28px;display:flex;gap:8px;flex-wrap:wrap;border-bottom:1px solid var(--ed-line)}.ed .toolbar input{flex:1;min-width:140px}.ed .content{padding:22px 28px;overflow:auto;flex:1}.ed .badge{display:inline-block;border:1px solid var(--ed-line);padding:2px 7px;border-radius:5px;font-size:10px;white-space:nowrap;text-transform:capitalize}.ed .card{background:var(--ed-panel);border:1px solid var(--ed-line);border-radius:9px;padding:13px;margin-bottom:9px;cursor:pointer;text-align:left;width:100%}.ed .card strong{display:block;margin:7px 0;font-size:13px}.ed .board{display:flex;gap:16px;align-items:flex-start;min-height:350px}.ed .column{min-width:235px;width:260px;flex-shrink:0}.ed .column h3{margin-bottom:14px;text-transform:capitalize}.ed table{width:100%;border-collapse:collapse;white-space:nowrap}.ed td,.ed th{text-align:left;padding:12px;border-bottom:1px solid var(--ed-line)}.ed th{font-size:11px;color:var(--ed-muted);font-weight:500}.ed tbody tr:hover{background:var(--ed-panel)}.ed .textbutton{border:0;padding:0;text-align:left}.ed .calendar{display:grid;grid-template-columns:repeat(7,minmax(120px,1fr));min-width:840px;border-top:1px solid var(--ed-line);border-left:1px solid var(--ed-line);margin-top:16px}.ed .day{border-right:1px solid var(--ed-line);border-bottom:1px solid var(--ed-line);padding:8px;min-height:120px}.ed .weekday{min-height:30px;font-size:11px;color:var(--ed-muted);text-align:center}.ed .outside{opacity:.45}.ed .today{color:var(--ed-accent);font-weight:700}.ed .event{display:block;width:100%;margin-top:5px;padding:5px 7px;text-align:left;font-size:11px;border-left:3px solid #9abae6;border-radius:3px;white-space:normal}.ed .release-event{border-left-color:#c2e7b4}.ed .empty{border:1px dashed var(--ed-line);border-radius:10px;padding:42px;text-align:center;color:var(--ed-muted)}.ed .error{background:#462a27;border:1px solid #9d554b;color:#ffdbd2;padding:10px 15px;border-radius:7px;margin:10px 28px}.ed .overlay{position:fixed;inset:0;background:#0008;z-index:60;display:flex;justify-content:flex-end}.ed .drawer{width:min(800px,100%);height:100%;overflow:auto;background:var(--color-bg,#101315);box-shadow:-15px 0 50px #0005;padding:25px}.ed .formgrid{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin:18px 0}.ed label{display:flex;flex-direction:column;gap:5px;color:var(--ed-muted);font-size:12px}.ed label>input,.ed label>select,.ed label>textarea{color:var(--color-text,#e7e9e8)}.ed .wide{grid-column:1/-1}.ed .section{border-top:1px solid var(--ed-line);padding-top:20px;margin-top:22px}.ed .release{border:1px solid var(--ed-line);border-radius:9px;padding:14px;margin:12px 0}.ed .release summary{cursor:pointer;font-weight:600}.ed a{color:#b1d1ed}.ed pre{white-space:pre-wrap;word-break:break-word;font-size:11px}.ed .tiles{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:12px}.ed .fieldrow{display:grid;grid-template-columns:1fr 1.5fr auto;gap:8px;margin-top:8px}.ed .notice{padding:10px 14px;border:1px solid var(--ed-line);border-radius:7px;margin:15px 0}.ed .stats{gap:24px;margin-top:16px}.ed .stats b{font-size:19px;margin-right:5px}.ed .title-input{font-size:23px;width:100%;margin:18px 0 0}.ed .sticky{position:sticky;top:-25px;z-index:2;background:var(--color-bg,#101315);padding:12px 0}.ed .empty p{margin-bottom:14px}@media(max-width:650px){.ed .top,.ed .toolbar,.ed .content{padding:14px}.ed .formgrid{grid-template-columns:1fr}.ed .wide{grid-column:auto}.ed .drawer{padding:15px}}
`;

export default function EditorialPanel(props: Props) {
  return <Planner key={`${props.installId}:${props.projectId}`} {...props} />;
}
function Planner({ projectId, appName }: Props) {
  const [items, setItems] = useState<Item[]>([]),
    [releases, setReleases] = useState<Release[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null),
    [view, setView] = useState("calendar");
  const [query, setQuery] = useState(""),
    [status, setStatus] = useState(""),
    [owner, setOwner] = useState(""),
    [format, setFormat] = useState(""),
    [approval, setApproval] = useState(""),
    [campaign, setCampaign] = useState(""),
    [archived, setArchived] = useState(false);
  const [month, setMonth] = useState(
    () => new Date(new Date().getFullYear(), new Date().getMonth(), 1),
  );
  const [dateField, setDateField] = useState("planned_at"),
    [editing, setEditing] = useState<Item | null>(null),
    [showSettings, setShowSettings] = useState(false);
  const [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const sequence = useRef(0);
  const api: API = useCallback(
    async (path, method = "GET", body) => {
      if (!projectId) throw new Error("Choose a project to plan content.");
      const sep = path.includes("?") ? "&" : "?";
      const res = await fetch(
        `/api/apps/${encodeURIComponent(appName || "editorial")}${path}${sep}project_id=${encodeURIComponent(projectId)}`,
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
    [appName, projectId],
  );
  const load = useCallback(async () => {
    const seq = ++sequence.current;
    setLoading(true);
    try {
      const [s, first] = await Promise.all([
        api("/settings"),
        api("/items?archived=all&limit=500"),
      ]);
      const allItems: Item[] = [...first.items],
        allReleases: Release[] = [...first.releases];
      for (let offset = first.items.length; offset < first.total;) {
        const page = await api(
          `/items?archived=all&limit=500&offset=${offset}`,
        );
        if (!page.items.length) break;
        allItems.push(...page.items);
        allReleases.push(...page.releases);
        offset += page.items.length;
      }
      if (seq === sequence.current) {
        setSettings(s);
        setItems(allItems);
        setReleases(allReleases);
        setError("");
      }
    } catch (e) {
      if (seq === sequence.current) setError((e as Error).message);
    } finally {
      if (seq === sequence.current) setLoading(false);
    }
  }, [api]);
  useEffect(() => {
    load();
    return () => {
      sequence.current++;
    };
  }, [load]);
  const filtered = useMemo(
    () =>
      items.filter(
        (i) =>
          i.archived === archived &&
          (!query ||
            `${i.title} ${i.body} ${i.tags.join(" ")}`
              .toLowerCase()
              .includes(query.toLowerCase())) &&
          (!status || i.status === status) &&
          (!owner || i.owner === owner) &&
          (!format || i.format === format) &&
          (!approval || i.approval === approval) &&
          (!campaign || i.campaign === campaign),
      ),
    [items, archived, query, status, owner, format, approval, campaign],
  );
  const visible =
    view === "backlog"
      ? filtered.filter(
          (i) =>
            !i.planned_at &&
            !releases.some(
              (r) => r.item_id === i.id && r.planned_at && !r.archived,
            ),
        )
      : filtered;
  const overdue = items.filter(
    (i) =>
      !i.archived &&
      i.deadline &&
      day(i.deadline) < dateKey(new Date()) &&
      i.status !== "published",
  ).length;
  const card = (i: Item) => (
    <button className="card" key={i.id} onClick={() => setEditing(i)}>
      <span className="badge">{label(i.format)}</span>
      <strong>{i.title}</strong>
      <p className="small muted">
        {i.owner || "Unassigned"}
        {i.deadline && ` · Due ${day(i.deadline)}`}
      </p>
      <div className="row" style={{ marginTop: 8 }}>
        <span className="badge">{label(i.approval)}</span>
        {i.campaign && <span className="small muted">{i.campaign}</span>}
      </div>
    </button>
  );
  const selectFilter = (
    name: string,
    value: string,
    set: (s: string) => void,
    values: string[],
  ) => (
    <select
      aria-label={name}
      value={value}
      onChange={(e) => set(e.target.value)}
    >
      <option value="">{name}</option>
      {values.filter(Boolean).map((v) => (
        <option key={v}>{v}</option>
      ))}
    </select>
  );
  return (
    <div className="ed">
      <style>{styles}</style>
      <header className="top">
        <div className="row between">
          <div>
            <div className="small muted" style={{ letterSpacing: 2 }}>
              CONTENT OPERATIONS
            </div>
            <h1>Editorial</h1>
            <p className="muted">
              From first idea to final release. Plan every channel in one place.
            </p>
          </div>
          <div className="row">
            <button onClick={() => setShowSettings(true)} disabled={!settings}>
              Settings
            </button>
            <button onClick={load} disabled={loading}>
              Refresh
            </button>
            <button
              className="primary"
              disabled={!settings}
              onClick={() => settings && setEditing(blank(settings))}
            >
              ＋ New content
            </button>
          </div>
        </div>
        <div className="row stats">
          <span>
            <b>{items.filter((i) => !i.archived).length}</b>
            <span className="muted">content items</span>
          </span>
          <span>
            <b>
              {
                items.filter((i) => !i.archived && i.approval === "pending")
                  .length
              }
            </b>
            <span className="muted">awaiting approval</span>
          </span>
          <span>
            <b>{overdue}</b>
            <span className="muted">past deadline</span>
          </span>
          <span className="badge">Standalone planning</span>
        </div>
      </header>
      <nav className="toolbar">
        {["calendar", "board", "table", "backlog"].map((v) => (
          <button
            className={view === v ? "active" : ""}
            key={v}
            onClick={() => setView(v)}
          >
            {label(v)}
          </button>
        ))}
        <input
          aria-label="Search content"
          placeholder="Search titles, briefs, tags…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {selectFilter(
          "All statuses",
          status,
          setStatus,
          settings?.statuses || [],
        )}
        {selectFilter(
          "All formats",
          format,
          setFormat,
          settings?.formats || [],
        )}
        {selectFilter("All owners", owner, setOwner, [
          ...new Set(items.map((i) => i.owner)),
        ])}
        {selectFilter("All approvals", approval, setApproval, approvals)}
        {selectFilter("All campaigns", campaign, setCampaign, [
          ...new Set(items.map((i) => i.campaign)),
        ])}
        <select
          aria-label="Archived filter"
          value={String(archived)}
          onChange={(e) => setArchived(e.target.value === "true")}
        >
          <option value="false">Active</option>
          <option value="true">Archived</option>
        </select>
      </nav>
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      <main className="content" aria-busy={loading}>
        {loading && <p className="muted">Loading the editorial calendar…</p>}
        {!loading && !items.length && settings ? (
          <div className="empty">
            <h2>Your next story starts here</h2>
            <p>
              Capture an idea, assign an owner, or plan a release. No connected
              apps needed.
            </p>
            <button
              className="primary"
              onClick={() => setEditing(blank(settings))}
            >
              Create your first idea
            </button>
          </div>
        ) : (
          <>
            {view === "calendar" && (
              <>
                <div className="row between">
                  <div className="row">
                    <button
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
                      ←
                    </button>
                    <h2>
                      {month.toLocaleDateString(undefined, {
                        month: "long",
                        year: "numeric",
                      })}
                    </h2>
                    <button
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
                      →
                    </button>
                    <button
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
                    </button>
                  </div>
                  <select
                    aria-label="Calendar date field"
                    value={dateField}
                    onChange={(e) => setDateField(e.target.value)}
                  >
                    <option value="planned_at">
                      Publication dates + releases
                    </option>
                    <option value="deadline">Editorial deadlines</option>
                  </select>
                </div>
                <Calendar
                  month={month}
                  items={filtered}
                  releases={releases}
                  field={dateField}
                  onOpen={setEditing}
                />
                <div className="section">
                  <h3>
                    Without{" "}
                    {dateField === "deadline"
                      ? "a deadline"
                      : "a publication date"}
                  </h3>
                  <p className="small muted">
                    Add a date in the item or plan a channel-specific release.
                  </p>
                  <div className="tiles" style={{ marginTop: 12 }}>
                    {filtered
                      .filter(
                        (i) =>
                          !(i as any)[dateField] &&
                          (dateField === "deadline" ||
                            !releases.some(
                              (r) =>
                                r.item_id === i.id &&
                                r.planned_at &&
                                !r.archived,
                            )),
                      )
                      .map(card)}
                  </div>
                </div>
              </>
            )}
            {view === "board" && (
              <div className="board">
                {settings?.statuses.map((s) => (
                  <section className="column" key={s}>
                    <h3>
                      {label(s)}{" "}
                      <span className="muted">
                        {filtered.filter((i) => i.status === s).length}
                      </span>
                    </h3>
                    {filtered.filter((i) => i.status === s).map(card)}
                  </section>
                ))}
              </div>
            )}
            {view === "table" && (
              <table>
                <thead>
                  <tr>
                    {[
                      "Content",
                      "Format",
                      "Workflow",
                      "Owner",
                      "Deadline",
                      "Publication",
                      "Approval",
                      "Campaign",
                    ].map((h) => (
                      <th key={h}>{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((i) => (
                    <tr key={i.id}>
                      <td>
                        <button
                          className="textbutton"
                          onClick={() => setEditing(i)}
                        >
                          {i.title}
                        </button>
                      </td>
                      <td>{label(i.format)}</td>
                      <td>
                        <span className="badge">{label(i.status)}</span>
                      </td>
                      <td>{i.owner || "—"}</td>
                      <td>{day(i.deadline) || "—"}</td>
                      <td>{day(i.planned_at) || "—"}</td>
                      <td>{label(i.approval)}</td>
                      <td>{i.campaign || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {view === "backlog" && (
              <>
                <h2>Idea backlog</h2>
                <p className="muted" style={{ marginBottom: 18 }}>
                  Content without a planned publication or channel release date.
                </p>
                <div className="tiles">{visible.map(card)}</div>
              </>
            )}
            {!visible.length && view !== "calendar" && (
              <div className="empty">No content matches this view.</div>
            )}
          </>
        )}
      </main>
      {editing && settings && (
        <ItemEditor
          key={editing.id}
          item={editing}
          settings={settings}
          api={api}
          onClose={() => setEditing(null)}
          onSaved={async (i) => {
            setEditing(i);
            await load();
          }}
        />
      )}
      {showSettings && settings && (
        <SettingsEditor
          settings={settings}
          api={api}
          onClose={() => setShowSettings(false)}
          onSaved={async () => {
            setShowSettings(false);
            await load();
          }}
        />
      )}
    </div>
  );
}

function Calendar({
  month,
  items,
  releases,
  field,
  onOpen,
}: {
  month: Date;
  items: Item[];
  releases: Release[];
  field: string;
  onOpen: (i: Item) => void;
}) {
  const start = new Date(month.getFullYear(), month.getMonth(), 1);
  start.setDate(start.getDate() - ((start.getDay() + 6) % 7));
  const cells = Array.from({ length: 42 }, (_, n) => {
    const d = new Date(start);
    d.setDate(d.getDate() + n);
    return d;
  });
  const byId = new Map(items.map((i) => [i.id, i]));
  return (
    <div style={{ overflowX: "auto" }}>
      <div className="calendar">
        {["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"].map((v) => (
          <div className="day weekday" key={v}>
            {v}
          </div>
        ))}
        {cells.map((d) => (
          <div
            className={`day ${d.getMonth() !== month.getMonth() ? "outside" : ""}`}
            key={dateKey(d)}
          >
            <span
              className={dateKey(d) === dateKey(new Date()) ? "today" : "muted"}
            >
              {d.getDate()}
            </span>
            {items
              .filter((i) => day((i as any)[field]) === dateKey(d))
              .map((i) => (
                <button
                  key={`i${i.id}`}
                  className="event"
                  onClick={() => onOpen(i)}
                >
                  {i.title}
                </button>
              ))}
            {field === "planned_at" &&
              releases
                .filter(
                  (r) =>
                    !r.archived &&
                    byId.has(r.item_id) &&
                    day(r.planned_at) === dateKey(d),
                )
                .map((r) => (
                  <button
                    key={`r${r.id}`}
                    className="event release-event"
                    onClick={() => onOpen(byId.get(r.item_id)!)}
                  >
                    <b>{r.channel}</b> · {byId.get(r.item_id)!.title}
                    <br />
                    <span className="muted">
                      {r.results.status || r.status}
                    </span>
                  </button>
                ))}
          </div>
        ))}
      </div>
    </div>
  );
}

function ItemEditor({
  item,
  settings,
  api,
  onClose,
  onSaved,
}: {
  item: Item;
  settings: Settings;
  api: API;
  onClose: () => void;
  onSaved: (i: Item) => Promise<void>;
}) {
  const [draft, setDraft] = useState(item),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const [detail, setDetail] = useState<{
    releases: Release[];
    history: any[];
    history_truncated: boolean;
  }>({ releases: [], history: [], history_truncated: false });
  const [newRelease, setNewRelease] = useState(false),
    [fields, setFields] = useState(() =>
      Object.entries(item.fields).map(([k, v]) => [
        k,
        typeof v === "string" ? v : JSON.stringify(v),
      ]),
    );
  const [sources, setSources] = useState(item.sources.join("\n")),
    [attachments, setAttachments] = useState(item.attachments.join("\n")),
    [tags, setTags] = useState(item.tags.join(", "));
  const set = (key: keyof Item, value: any) =>
    setDraft((d) => ({ ...d, [key]: value }));
  const loadDetail = useCallback(async () => {
    if (item.id) {
      try {
        setDetail(await api(`/items/${item.id}`));
      } catch (e) {
        setError((e as Error).message);
      }
    }
  }, [api, item.id]);
  useEffect(() => {
    loadDetail();
  }, [loadDetail]);
  const save = async (archive?: boolean) => {
    setBusy(true);
    setError("");
    try {
      const custom: Record<string, unknown> = {};
      for (const [k, v] of fields) {
        if (!k.trim())
          throw new Error("Name every custom field or remove its row.");
        if (k.trim() in custom)
          throw new Error("Custom field names must be unique.");
        try {
          custom[k.trim()] = JSON.parse(v);
        } catch {
          custom[k.trim()] = v;
        }
      }
      const { id, revision, updated_at, ...data } = draft;
      // The server returns created_at too; send only editable properties.
      const patch = {
        title: data.title,
        body: data.body,
        format: data.format,
        status: data.status,
        owner: data.owner,
        deadline: data.deadline,
        planned_at: data.planned_at,
        approval: data.approval,
        reviewer: data.reviewer,
        campaign: data.campaign,
        sources: sources
          .split("\n")
          .map((x) => x.trim())
          .filter(Boolean),
        attachments: attachments
          .split("\n")
          .map((x) => x.trim())
          .filter(Boolean),
        tags: split(tags),
        fields: custom,
        archived: archive ?? data.archived,
      };
      const saved = await api(
        id ? `/items/${id}` : "/items",
        id ? "PATCH" : "POST",
        id ? { revision, patch } : patch,
      );
      setDraft(saved);
      await onSaved(saved);
      await loadDetail();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const input = (key: keyof Item, name: string, type = "text") =>
    type === "date" ? (
      <DateField
        name={name}
        value={String(draft[key] || "")}
        onChange={(v) => set(key, v)}
      />
    ) : (
      <label>
        {name}
        <input
          value={String(draft[key] || "")}
          onChange={(e) => set(key, e.target.value)}
        />
      </label>
    );

  return (
    <div className="overlay">
      <section
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-label="Content editor"
      >
        <div className="row between sticky">
          <span className="muted">
            {item.id ? `Content #${item.id}` : "New content"}
          </span>
          <div className="row">
            <button disabled={busy} onClick={onClose}>
              Close
            </button>
            <button className="primary" disabled={busy} onClick={() => save()}>
              {busy ? "Saving…" : "Save content"}
            </button>
          </div>
        </div>
        {error && (
          <div className="error" role="alert" style={{ margin: "10px 0" }}>
            {error}
          </div>
        )}
        <input
          className="title-input"
          aria-label="Content title"
          placeholder="What are we creating?"
          value={draft.title}
          onChange={(e) => set("title", e.target.value)}
        />
        <div className="formgrid">
          <label>
            Format
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
          </label>
          <label>
            Workflow
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
          </label>
          {input("owner", "Owner")}
          {input("campaign", "Campaign / initiative")}
          {input("deadline", "Deadline", "date")}
          {input("planned_at", "Planned publication", "date")}
          <label>
            Approval
            <select
              value={draft.approval}
              onChange={(e) => set("approval", e.target.value)}
            >
              {approvals.map((v) => (
                <option key={v} value={v}>
                  {label(v)}
                </option>
              ))}
            </select>
          </label>
          {input("reviewer", "Reviewer")}
          <label className="wide">
            Brief / draft
            <textarea
              rows={10}
              placeholder="Audience, angle, key points, draft copy…"
              value={draft.body}
              onChange={(e) => set("body", e.target.value)}
            />
          </label>
          <label>
            Source links (one per line)
            <textarea
              rows={3}
              value={sources}
              onChange={(e) => setSources(e.target.value)}
            />
          </label>
          <label>
            Attachment links (one per line)
            <textarea
              rows={3}
              value={attachments}
              onChange={(e) => setAttachments(e.target.value)}
            />
          </label>
          <label className="wide">
            Tags
            <input
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="Comma-separated tags"
            />
          </label>
        </div>
        <p className="small muted">
          Dates accept YYYY-MM-DD or a timestamp with timezone. Changing
          approved content resets its approval to pending.
        </p>
        <div className="section">
          <div className="row between">
            <h3>Custom fields</h3>
            <button onClick={() => setFields((f) => [...f, ["", ""]])}>
              ＋ Add field
            </button>
          </div>
          {fields.map(([k, v], n) => (
            <div className="fieldrow" key={n}>
              <input
                aria-label={`Field ${n + 1} name`}
                placeholder="Field name"
                value={k}
                onChange={(e) =>
                  setFields((f) =>
                    f.map((r, i) => (i === n ? [e.target.value, r[1]] : r)),
                  )
                }
              />
              <input
                aria-label={`Field ${n + 1} value`}
                placeholder="Value"
                value={v}
                onChange={(e) =>
                  setFields((f) =>
                    f.map((r, i) => (i === n ? [r[0], e.target.value] : r)),
                  )
                }
              />
              <button
                aria-label={`Remove field ${n + 1}`}
                onClick={() => setFields((f) => f.filter((_, i) => i !== n))}
              >
                ×
              </button>
            </div>
          ))}
        </div>
        <div className="section">
          <div className="row between">
            <h3>Channel releases</h3>
            <button
              disabled={!item.id || draft.archived}
              onClick={() => setNewRelease(true)}
            >
              ＋ Plan release
            </button>
          </div>
          <p className="small muted">
            Each channel has its own date and results. Planning here does not
            schedule or send anything externally.
          </p>
          {!item.id && (
            <p className="notice muted">
              Save the content first to add releases.
            </p>
          )}
          {detail.releases.map((r) => (
            <ReleaseEditor
              key={`${r.id}:${r.revision}`}
              release={r}
              settings={settings}
              api={api}
              parentArchived={draft.archived}
              onSaved={async () => {
                await loadDetail();
                await onSaved(draft);
              }}
            />
          ))}
          {newRelease && (
            <ReleaseEditor
              key="new"
              release={freshRelease(item.id, settings.channels[0])}
              settings={settings}
              api={api}
              parentArchived={draft.archived}
              onCancel={() => setNewRelease(false)}
              onSaved={async () => {
                setNewRelease(false);
                await loadDetail();
                await onSaved(draft);
              }}
            />
          )}
        </div>
        {item.id > 0 && (
          <>
            <div className="section">
              <h3>Change history</h3>
              {detail.history.slice(0, 20).map((h) => (
                <details key={h.id}>
                  <summary className="small" style={{ padding: "7px 0" }}>
                    {new Date(h.created_at).toLocaleString()} ·{" "}
                    {label(h.action.replaceAll(".", " "))}
                  </summary>
                  <pre>{JSON.stringify(h.snapshot, null, 2)}</pre>
                </details>
              ))}
              {detail.history.length > 20 && (
                <p className="small muted">
                  Showing the latest 20 changes. The item API includes up to
                  100.
                </p>
              )}
            </div>
            <div className="section">
              <button disabled={busy} onClick={() => save(!draft.archived)}>
                {draft.archived ? "Restore content" : "Archive content"}
              </button>
            </div>
          </>
        )}
      </section>
    </div>
  );
}

function ReleaseEditor({
  release,
  settings,
  api,
  parentArchived,
  onSaved,
  onCancel,
}: {
  release: Release;
  settings: Settings;
  api: API;
  parentArchived: boolean;
  onSaved: () => Promise<void>;
  onCancel?: () => void;
}) {
  const [results, setResults] = useState(
    JSON.stringify(release.results, null, 2),
  );
  const [r, setR] = useState(release),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [records, setRecords] = useState<any[]>([]);
  const set = (k: keyof Release, v: any) => setR((s) => ({ ...s, [k]: v }));
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const save = () =>
    run(async () => {
      const manualResults = JSON.parse(results);
      if (
        !manualResults ||
        Array.isArray(manualResults) ||
        typeof manualResults !== "object"
      )
        throw new Error("Results must be a JSON object.");
      const patch = {
        channel: r.channel,
        planned_at: r.planned_at,
        published_at: r.published_at,
        url: r.url,
        status: r.status,
        notes: r.notes,
        app: r.app,
        external_id: r.app ? r.external_id : 0,
        archived: r.archived,
        ...(!r.app ? { results: manualResults } : {}),
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
  const browse = () =>
    run(async () => {
      const data = await api(`/integrations?app=${r.app}`);
      setRecords(data.posts || data.campaigns || []);
    });
  const text = (k: keyof Release, name: string) =>
    k.endsWith("_at") ? (
      <DateField
        name={name}
        value={String(r[k] || "")}
        onChange={(v) => set(k, v)}
      />
    ) : (
      <label>
        {name}
        <input
          value={String(r[k] || "")}
          onChange={(e) => set(k, e.target.value)}
        />
      </label>
    );

  return (
    <details className="release" open={!release.id}>
      <summary>
        {r.channel || "New release"} · {day(r.planned_at) || "No date"}{" "}
        <span className="badge">{r.archived ? "archived" : r.status}</span>
        {r.results.status && (
          <span className="badge">Delivery: {r.results.status}</span>
        )}
      </summary>
      {error && (
        <p role="alert" className="error" style={{ margin: "10px 0" }}>
          {error}
        </p>
      )}
      <div className="formgrid">
        <label>
          Channel
          <input
            list={`channels-${r.id}`}
            value={r.channel}
            onChange={(e) => set("channel", e.target.value)}
          />
          <datalist id={`channels-${r.id}`}>
            {settings.channels.map((v) => (
              <option key={v}>{v}</option>
            ))}
          </datalist>
        </label>
        <label>
          Planning status
          <select
            value={r.status}
            onChange={(e) => set("status", e.target.value)}
          >
            {["planned", "scheduled", "published", "failed", "cancelled"].map(
              (v) => (
                <option key={v}>{v}</option>
              ),
            )}
          </select>
        </label>
        {text("planned_at", "Planned publication")}
        {text("published_at", "Actual publication")}
        {text("url", "Published URL")}
        {text("notes", "Release notes")}
        <label>
          Optional app link
          <select
            value={r.app}
            onChange={(e) => {
              set("app", e.target.value);
              set("external_id", 0);
              setRecords([]);
            }}
          >
            <option value="">Standalone / manual</option>
            <option value="social">Social</option>
            <option value="campaigns">Campaigns</option>
          </select>
        </label>
        {r.app && (
          <label>
            Existing record ID
            <input
              type="number"
              min={1}
              value={r.external_id || ""}
              onChange={(e) => set("external_id", Number(e.target.value))}
            />
          </label>
        )}
      </div>
      {r.app && (
        <>
          <div className="row">
            <button disabled={busy} onClick={browse}>
              Browse existing {r.app === "social" ? "posts" : "campaigns"}
            </button>
            {records.length > 0 && (
              <select
                aria-label="Choose existing record"
                value={r.external_id || ""}
                onChange={(e) => set("external_id", Number(e.target.value))}
              >
                <option value="">Choose a record</option>
                {records.map((x) => (
                  <option key={x.id} value={x.id}>
                    #{x.id} ·{" "}
                    {String(x.name || x.body || x.status).slice(0, 70)}
                  </option>
                ))}
              </select>
            )}
          </div>
          <p className="small muted">
            Connect this optional app in installation settings to browse or
            refresh. Linking never publishes.
          </p>
        </>
      )}
      {!r.app && (
        <label>
          Publication results (JSON, for example views or clicks)
          <textarea
            rows={3}
            value={results}
            onChange={(e) => setResults(e.target.value)}
          />
        </label>
      )}
      <div className="row" style={{ marginTop: 15 }}>
        <button
          className="primary"
          disabled={busy || parentArchived}
          onClick={save}
        >
          Save release
        </button>
        {onCancel && (
          <button disabled={busy} onClick={onCancel}>
            Cancel
          </button>
        )}
        {release.id > 0 && release.app && (
          <button
            disabled={busy || parentArchived}
            onClick={() =>
              run(async () => {
                await api(`/releases/${release.id}/refresh`, "POST", {});
                await onSaved();
              })
            }
          >
            Refresh saved link
          </button>
        )}
        <label style={{ flexDirection: "row", alignItems: "center" }}>
          <input
            type="checkbox"
            checked={r.archived}
            onChange={(e) => set("archived", e.target.checked)}
          />
          Archived
        </label>
      </div>
      {release.url && (
        <p>
          <a href={release.url} target="_blank" rel="noreferrer">
            Open publication ↗
          </a>
        </p>
      )}
      {release.synced_at && (
        <p className="small muted">
          Results refreshed {new Date(release.synced_at).toLocaleString()}.
          Delivery status is separate from your planning status.
        </p>
      )}
      {Object.keys(release.results).length > 0 && (
        <details>
          <summary>Publication results</summary>
          <pre>{JSON.stringify(release.results, null, 2)}</pre>
        </details>
      )}
    </details>
  );
}

function SettingsEditor({
  settings,
  api,
  onClose,
  onSaved,
}: {
  settings: Settings;
  api: API;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [values, setValues] = useState({
      statuses: settings.statuses.join("\n"),
      formats: settings.formats.join("\n"),
      channels: settings.channels.join("\n"),
    }),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [bindings, setBindings] = useState<Record<string, boolean> | null>(null);
  const save = async () => {
    setBusy(true);
    setError("");
    try {
      await api("/settings", "PATCH", {
        revision: settings.revision,
        statuses: split(values.statuses),
        formats: split(values.formats),
        channels: split(values.channels),
      });
      await onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="overlay">
      <section
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-label="Editorial settings"
      >
        <div className="row between">
          <h2>Editorial settings</h2>
          <button disabled={busy} onClick={onClose}>
            Close
          </button>
        </div>
        <p className="notice">
          Editorial works with no other apps installed. Configure the workflow
          for your team; connect publishing apps only when useful.
        </p>
        {error && (
          <p className="error" role="alert" style={{ margin: "10px 0" }}>
            {error}
          </p>
        )}
        <div className="formgrid">
          {(["statuses", "formats", "channels"] as const).map((k) => (
            <label key={k}>
              {label(k)} · one per line
              <textarea
                rows={9}
                value={values[k]}
                onChange={(e) =>
                  setValues((v) => ({ ...v, [k]: e.target.value }))
                }
              />
            </label>
          ))}
        </div>
        <p className="small muted">
          Statuses and formats used by existing items must remain available. The
          first status and format are defaults for new items. Channels are
          suggestions; releases can use any channel name.
        </p>
        <button className="primary" disabled={busy} onClick={save}>
          Save settings
        </button>
        <div className="section">
          <h3>Optional connections</h3>
          <p className="muted">
            Social and Campaigns supply existing delivery records and results.
            Their own dependencies apply only if you choose to install them.
          </p>
          <button
            onClick={async () => {
              try {
                setBindings(await api("/integrations"));
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            Check connections
          </button>
          {bindings &&
            Object.entries(bindings).map(([k, v]) => (
              <p key={k}>
                {label(k)} · {v ? "Connected" : "Not connected (optional)"}
              </p>
            ))}
        </div>
      </section>
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
      const d = value ? new Date(value) : new Date();
      return Number.isNaN(d.getTime()) ? new Date() : d;
    });
  const start = new Date(month.getFullYear(), month.getMonth(), 1);
  start.setDate(start.getDate() - ((start.getDay() + 6) % 7));
  return (
    <div>
      <label>
        {name}
        <input
          aria-label={name}
          placeholder="YYYY-MM-DD"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      </label>
      <button
        className="small"
        style={{ marginTop: 4 }}
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        aria-label={`Choose ${name.toLowerCase()} date`}
      >
        Choose date
      </button>
      {open && (
        <div className="release">
          <div className="row between">
            <button
              aria-label={`Previous month for ${name}`}
              onClick={() =>
                setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))
              }
            >
              ←
            </button>
            <span className="small">
              {month.toLocaleDateString(undefined, {
                month: "short",
                year: "numeric",
              })}
            </span>
            <button
              aria-label={`Next month for ${name}`}
              onClick={() =>
                setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))
              }
            >
              →
            </button>
          </div>
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(7,1fr)",
              gap: 2,
              marginTop: 8,
            }}
          >
            {["M", "T", "W", "T", "F", "S", "S"].map((v, i) => (
              <span
                className="small muted"
                style={{ textAlign: "center" }}
                key={i}
              >
                {v}
              </span>
            ))}
            {Array.from({ length: 42 }, (_, n) => {
              const d = new Date(start);
              d.setDate(d.getDate() + n);
              return (
                <button
                  key={n}
                  className={dateKey(d) === day(value) ? "active" : ""}
                  style={{
                    padding: "4px",
                    opacity: d.getMonth() === month.getMonth() ? 1 : 0.4,
                  }}
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
          <button
            className="small"
            style={{ marginTop: 8 }}
            onClick={() => {
              onChange("");
              setOpen(false);
            }}
          >
            Clear date
          </button>
        </div>
      )}
    </div>
  );
}
