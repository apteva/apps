import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { widgetAPIURL, widgetPageLink, widgetPreferences, widgetRequest, type WidgetProps } from "./widget";

type Todo = {
  id: number;
  title: string;
  priority: number;
  due_at: string;
  rrule: string;
  list_id: number;
  list_name: string;
};
type Page = {
  title: string;
  items: Todo[];
  total: number;
  lists: { id: number; name: string }[];
  options?: { value: string; label: string }[];
};

const css = `
.tl-widget{box-sizing:border-box;display:flex;flex-direction:column;min-width:0;height:100%;overflow:hidden;border:1px solid var(--color-border,#303030);border-radius:4px;background:var(--color-bg-card,#141414);color:var(--color-text,#eceef2);font-family:inherit}
.tl-widget *{box-sizing:border-box}.tl-widget p,.tl-widget h2{margin:0}.tl-widget button,.tl-widget input,.tl-widget select{font:inherit;color:inherit}.tl-widget button,.tl-widget a{cursor:pointer}.tl-widget a{color:inherit;text-decoration:none}.tl-widget :is(button,a,input,select):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:2px}
.tl-widget header{display:flex;align-items:center;gap:12px;padding:12px 14px;border-bottom:1px solid var(--color-border,#303030)}.tl-widget h2{font-size:14px;overflow-wrap:anywhere}.tl-widget .tl-heading{min-width:0}.tl-widget .tl-count{font-size:11px;color:var(--color-text-muted,#aaa);margin-top:4px}.tl-widget .tl-open{margin-left:auto;font-size:11px;white-space:nowrap;color:var(--color-text-muted,#aaa)}
.tl-widget .tl-tasks{list-style:none;margin:0;padding:0;flex:1;min-height:0;max-height:30rem;overflow:auto}.tl-widget .tl-row{display:flex;align-items:flex-start;gap:10px;padding:11px 14px;border-bottom:1px solid var(--color-border,#303030)}.tl-widget .tl-row:last-child{border-bottom:0}.tl-widget .tl-check{flex-shrink:0;width:24px;height:24px;border:1px solid var(--color-border,#555);background:transparent;border-radius:50%;font-size:12px}.tl-widget .tl-check:hover{border-color:var(--color-accent,#ff8c36);background:var(--color-bg-hover,#222)}.tl-widget .tl-task{min-width:0;flex:1;font-size:12px;line-height:19px;overflow-wrap:anywhere}.tl-widget .tl-meta{display:flex;flex-wrap:wrap;gap:4px 9px;font-size:10px;color:var(--color-text-muted,#aaa);margin-top:2px}.tl-widget .tl-priority{font-weight:600}.tl-widget .tl-priority[data-priority="1"]{color:var(--color-error,#ef6b73)}.tl-widget .tl-priority[data-priority="2"]{color:var(--color-warn,#e9b949)}
.tl-widget .tl-message{padding:18px 14px;font-size:12px;line-height:19px;overflow-wrap:anywhere}.tl-widget .tl-error{color:var(--color-error,#ef6b73)}.tl-widget .tl-notice{padding:4px 14px;font-size:11px;color:var(--color-text-muted,#aaa)}.tl-widget .tl-more{padding:8px 14px;font-size:11px;color:var(--color-text-muted,#aaa);border-top:1px solid var(--color-border,#303030)}
.tl-widget form{padding:10px 14px;border-top:1px solid var(--color-border,#303030);display:flex;flex-wrap:wrap;gap:8px;margin-top:auto}.tl-widget input,.tl-widget select{min-width:0;border:1px solid var(--color-border,#303030);border-radius:3px;background:var(--color-bg-input,#1b1b1b);padding:7px 8px;font-size:12px}.tl-widget input{flex:1;min-width:90px}.tl-widget select{width:100%}.tl-widget .tl-button{border:1px solid var(--color-border,#303030);border-radius:3px;background:transparent;padding:7px 10px;font-size:12px}.tl-widget button:disabled,.tl-widget input:disabled,.tl-widget select:disabled{opacity:.55;cursor:default}
.tl-widget .tl-filters{display:flex;flex-wrap:wrap;gap:6px;padding:8px 14px;border-bottom:1px solid var(--color-border,#303030)}.tl-widget .tl-filters select{width:auto;flex:1;min-width:100px;font-size:11px}
`;

function dueLabel(value: string) {
  if (!value) return "";
  const date = new Date(/^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T12:00:00` : value);
  return Number.isFinite(date.getTime())
    ? date.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) : value;
}

export default function TodoListWidget(props: WidgetProps) {
  const preferences = widgetPreferences(props.widgetSettings);
  const filterScope = JSON.stringify([props.appName, props.projectId, props.installId, preferences]);
  const [filters, setFilters] = useState<{ scope: string; target: string; view: string; sort: string } | null>(null);
  const { target, view, sort } = filters?.scope === filterScope ? filters : preferences;
  const changeFilter = (patch: Partial<{ target: string; view: string; sort: string }>) =>
    setFilters({ scope: filterScope, target, view, sort, ...patch });
  const url = widgetAPIURL(props, "/widget", { target, view, sort, limit: String(preferences.limit) });
  const key = url;
  const activeKey = useRef(key);
  activeKey.current = key;
  const [snapshot, setSnapshot] = useState<{ key: string; page: Page } | null>(null);
  const [failure, setFailure] = useState({ key: "", message: "" });
  const [actionError, setActionError] = useState("");
  const [notice, setNotice] = useState("");
  const [text, setText] = useState("");
  const [destination, setDestination] = useState("");
  const [busy, setBusy] = useState<number | "add" | null>(null);
  const request = useRef<AbortController | null>(null);
  const mutation = useRef<AbortController | null>(null);
  const page = snapshot?.key === key ? snapshot.page : null;
  const error = failure.key === key ? failure.message : "";
  const group = target.startsWith("group:");
  const singleList = target.startsWith("list:");
  const listID = singleList ? target.split(":")[1] : destination || (group ? "" : "0");
  const canAdd = !!page && ((!group && listID === "0") || page.lists.some(list => String(list.id) === listID));
  const link = widgetPageLink(props, target, view);
  const options = page?.options || [];
  const selectedOption = options.some(option => option.value === target);

  const load = useCallback(async () => {
    request.current?.abort();
    if (!props.projectId) return;
    const controller = new AbortController();
    request.current = controller;
    try {
      const data = await widgetRequest<Page>(url, { signal: controller.signal });
      if (controller.signal.aborted || activeKey.current !== key) return;
      setSnapshot({ key, page: data });
      setFailure({ key, message: "" });
    } catch (cause) {
      if (!controller.signal.aborted && activeKey.current === key) {
        setFailure({ key, message: (cause as Error).message });
      }
    }
  }, [url, key, props.projectId, target]);

  useEffect(() => {
    void load();
    return () => request.current?.abort();
  }, [load, props.eventRevision]);

  useEffect(() => {
    setText("");
    setDestination("");
    setActionError("");
    setNotice("");
    setBusy(null);
    return () => { mutation.current?.abort(); mutation.current = null; };
  }, [key]);

  useEffect(() => {
    const timer = window.setInterval(() => void load(), 60000);
    return () => window.clearInterval(timer);
  }, [load]);

  const mutate = async (action: number | "add", path: string, body?: unknown) => {
    if (mutation.current) return;
    const controller = new AbortController();
    mutation.current = controller;
    setBusy(action);
    setActionError("");
    setNotice("");
    try {
      const result = await widgetRequest<{ status: string }>(widgetAPIURL(props, path), {
        method: "POST", signal: controller.signal,
        headers: { "Content-Type": "application/json" },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      if (controller.signal.aborted || activeKey.current !== key) return;
      if (action === "add") setText("");
      setNotice(action === "add" ? (view === "all" ? "Todo added." : "Todo added. Find undated tasks in All open.") : result.status === "open" ? "Completed. Next occurrence scheduled." : "Todo completed.");
      await load();
    } catch (cause) {
      if (!controller.signal.aborted && activeKey.current === key) setActionError((cause as Error).message);
    } finally {
      if (mutation.current === controller) {
        mutation.current = null;
        setBusy(null);
      }
    }
  };

  const add = (event: FormEvent) => {
    event.preventDefault();
    if (!canAdd || !text.trim() || busy !== null || error) return;
    void mutate("add", "/todos", { title: text.trim(), list_id: Number(listID), source: "human" });
  };

  return <section className="tl-widget" aria-label={page?.title ? `${page.title} todos` : "Todo list"}>
    <style>{css}</style>
    <header>
      <div className="tl-heading"><h2>{page?.title || "Todo list"}</h2>
        {page && !error && <p className="tl-count">{page.total} remaining</p>}
      </div>
      <a className="tl-open" href={link}>Open in Todo →</a>
    </header>
    {props.projectId && <div className="tl-filters" aria-label="Todo filters">
      <select aria-label="Filter by list or group" value={target} disabled={busy !== null} onChange={e => changeFilter({ target: e.target.value })}>
        <option value="all">All lists and groups</option>
        {!selectedOption && target !== "all" && <option value={target}>Selected {singleList ? "list" : "group"}</option>}
        {options.filter(option => option.value !== "all").map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
      <select aria-label="Todo view" value={view} disabled={busy !== null} onChange={e => changeFilter({ view: e.target.value })}>
        <option value="today">Today + overdue</option><option value="overdue">Overdue</option><option value="upcoming">Upcoming</option><option value="all">All open</option>
      </select>
      <select aria-label="Sort todos" value={sort} disabled={busy !== null} onChange={e => changeFilter({ sort: e.target.value })}>
        <option value="due">Due date</option><option value="priority">Priority</option>
      </select>
    </div>}
    {!props.projectId ? <p className="tl-message">Select a project to see your todos.</p>
      : error ? <div className="tl-message" role="alert"><p className="tl-error">{error}</p><button className="tl-button" type="button" onClick={() => void load()}>Retry</button></div>
      : !page ? <p className="tl-message" role="status">Loading todos…</p>
      : <>
        {page.items.length === 0 ? <p className="tl-message">No {view === "today" ? "today or overdue" : view === "overdue" ? "overdue" : view === "upcoming" ? "upcoming" : "open"} todos. {group && !page.lists.length ? "Create a list in this group in Todo to get started." : ""}</p>
          : <ul className="tl-tasks">{page.items.map(todo => <li className="tl-row" key={todo.id}>
            <button className="tl-check" type="button" aria-label={`Complete ${todo.title}`} disabled={busy !== null}
              onClick={() => void mutate(todo.id, `/todos/${todo.id}/complete`)}>{busy === todo.id ? "…" : ""}</button>
            <div className="tl-task"><a href={widgetPageLink(props, todo.list_id ? `list:${todo.list_id}` : "all", "all")}>{todo.title}</a>
              <div className="tl-meta">
                {!singleList && <span>{todo.list_name}</span>}
                {todo.due_at && <time dateTime={todo.due_at}>{dueLabel(todo.due_at)}</time>}
                {todo.priority < 4 && <span className="tl-priority" data-priority={todo.priority}>P{todo.priority}</span>}
                {todo.rrule && <span>Repeats</span>}
              </div>
            </div>
          </li>)}</ul>}
        {page.total > page.items.length && <a className="tl-more" href={link}>Showing {page.items.length} of {page.total} · View all →</a>}
        {(!group || page.lists.length > 0) && <form onSubmit={add} aria-label="Add a todo">
          {!singleList && <select aria-label="Add to list" value={canAdd ? listID : ""} disabled={busy !== null} onChange={e => setDestination(e.target.value)}>
            {group ? <option value="">Choose a list to add to…</option> : <option value="0">Inbox</option>}
            {page.lists.map(list => <option key={list.id} value={list.id}>{list.name}</option>)}
          </select>}
          <input aria-label="New todo title" placeholder="Add a todo…" value={text} disabled={busy !== null || !canAdd} onChange={e => setText(e.target.value)} maxLength={2000} />
          <button className="tl-button" type="submit" disabled={busy !== null || !canAdd || !text.trim()}>{busy === "add" ? "Adding…" : "Add"}</button>
        </form>}
      </>}
    {actionError && <p className="tl-message tl-error" role="alert">{actionError}</p>}
    <p className="tl-notice" role="status" aria-live="polite">{notice}</p>
  </section>;
}
