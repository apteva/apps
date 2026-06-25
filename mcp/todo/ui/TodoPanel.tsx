// TodoPanel — personal todo list, sibling of TasksPanel.
//
// Layout:
//   ┌─ sidebar ─┐ ┌──────────── main ─────────────────────────────┐
//   │ Inbox     │ │ quick add bar  [+]                           │
//   │ Today     │ │ ──────────────────────────────────────────── │
//   │ Upcoming  │ │ todo rows (checkbox · title · due · tags)    │
//   │ Overdue   │ │                                              │
//   │ All       │ │                                              │
//   │ Done      │ │                                              │
//   │ ── lists  + ─                                              │
//   │ #home  ⋯  │ │                                              │
//   │ #work  ⋯  │ │                                              │
//   │ ── tags ─                                                  │
//   │ @errand 4 │ │                                              │
//   │ @waiting 2│ │                                              │
//   └───────────┘ └────────────────────────────────────────────-─┘
//
// Quick-add box accepts the same NL grammar as the MCP tool:
//   "call the plumber tomorrow p1 #home @errand"
// where #name resolves to a list (created if missing) and @name is a tag.
// For a structured form (specific time, recurrence, free-form notes)
// hit the "+" button beside quick-add to open a full create dialog.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/todo";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface AppEventEnvelope<T = unknown> {
  seq: number;
  topic: string;
  data?: T;
}

function useAppEvents<T = unknown>(
  app: string,
  projectId: string,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;

  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const shared = (window as typeof window & {
      __aptevaAppEvents?: {
        subscribe: (
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ) => () => void;
      };
    }).__aptevaAppEvents;
    if (shared) return shared.subscribe(app, projectId, handler);

    let since = 0;
    let es: EventSource | null = null;
    let stopped = false;
    let retry: number | null = null;
    const connect = () => {
      if (stopped) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (since > 0 ? `&since=${since}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= since) return;
          since = ev.seq;
          handler(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (retry) window.clearTimeout(retry);
          retry = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      stopped = true;
      if (retry) window.clearTimeout(retry);
      if (es) es.close();
    };
  }, [app, projectId]);
}

interface Todo {
  id: number;
  title: string;
  notes: string;
  priority: number;
  due_at?: string;
  snoozed_until?: string;
  rrule?: string;
  status: string;
  source: string;
  list_id: number | null;
  tags: string[];
  created_at: string;
}

interface List {
  id: number;
  name: string;
  color: string;
  archived: boolean;
  group_id: number | null;
}

interface ListGroup {
  id: number;
  name: string;
  color: string;
  archived: boolean;
}

interface Tag {
  id: number;
  name: string;
  count: number;
}

interface WorkSummary {
  overdue: number;
  today: number;
  future: number;
}

type View = "inbox" | "today" | "upcoming" | "overdue" | "all" | "done";
type PanelMode = "tasks" | "calendar";
type CalendarTab = "heatmap" | "month" | "day";

const VIEWS: { key: View; label: string }[] = [
  { key: "inbox",    label: "Inbox" },
  { key: "today",    label: "Today" },
  { key: "upcoming", label: "Upcoming" },
  { key: "overdue",  label: "Overdue" },
  { key: "all",      label: "All" },
  { key: "done",     label: "Done" },
];

const PRIORITY_TONE: Record<number, string> = {
  1: "text-error",
  2: "text-warn",
  3: "text-info",
  4: "text-text-dim",
};

const SOFT_BORDER = "rgba(148, 163, 184, 0.10)";

export default function TodoPanel({ projectId }: NativePanelProps) {
  const [view, setView] = useState<View>("today");
  const [mode, setMode] = useState<PanelMode>("tasks");
  const [pickedList, setPickedList] = useState<number | null>(null);
  const [pickedTag, setPickedTag] = useState<string | null>(null);
  const [todos, setTodos] = useState<Todo[]>([]);
  const [allOpenTodos, setAllOpenTodos] = useState<Todo[]>([]);
  const [doneTodos, setDoneTodos] = useState<Todo[]>([]);
  const [lists, setLists] = useState<List[]>([]);
  const [groups, setGroups] = useState<ListGroup[]>([]);
  const [tags, setTags] = useState<Tag[]>([]);
  const [summary, setSummary] = useState<WorkSummary>({ overdue: 0, today: 0, future: 0 });
  const [calendarMonth, setCalendarMonth] = useState(() => dateKey(startOfMonth(new Date())));
  const [calendarSelectedDay, setCalendarSelectedDay] = useState(() => dateKey(new Date()));
  const [calendarGroup, setCalendarGroup] = useState<number | "all">("all");
  const [calendarList, setCalendarList] = useState<number | "all">("all");
  const [calendarIncludeDone, setCalendarIncludeDone] = useState(false);
  const [calendarTab, setCalendarTab] = useState<CalendarTab>("heatmap");
  const [quick, setQuick] = useState("");
  const [statusMsg, setStatusMsg] = useState("");
  const [editing, setEditing] = useState<Todo | null>(null);
  const [creating, setCreating] = useState(false);
  const [newListOpen, setNewListOpen] = useState(false);
  const [newGroupOpen, setNewGroupOpen] = useState(false);
  const [collapsedGroups, setCollapsedGroups] = useState<Record<number, boolean>>({});
  const [settlingTodos, setSettlingTodos] = useState<Record<number, "complete" | "uncomplete">>({});

  const params = useMemo(() => {
    const p = new URLSearchParams();
    p.set("view", view);
    if (pickedList) p.set("list_id", String(pickedList));
    if (pickedTag) p.set("tag", pickedTag);
    return p.toString();
  }, [view, pickedList, pickedTag]);

  const loadTodos = useCallback(async () => {
    try {
      const res = await fetch(`${API}/todos?${params}`, { credentials: "same-origin" });
      if (!res.ok) { setStatusMsg(`Load: ${res.status}`); return; }
      const data: Todo[] = await res.json();
      setTodos(data || []);
      setStatusMsg(`${(data || []).length} todos`);
    } catch (e) {
      setStatusMsg("Load: " + (e as Error).message);
    }
  }, [params]);

  const loadLists = useCallback(async () => {
    try {
      const res = await fetch(`${API}/lists`, { credentials: "same-origin" });
      if (res.ok) setLists(await res.json() || []);
    } catch {}
  }, []);

  const loadGroups = useCallback(async () => {
    try {
      const res = await fetch(`${API}/list_groups`, { credentials: "same-origin" });
      if (res.ok) setGroups(await res.json() || []);
    } catch {}
  }, []);

  const loadTags = useCallback(async () => {
    try {
      const res = await fetch(`${API}/tags`, { credentials: "same-origin" });
      if (res.ok) setTags(await res.json() || []);
    } catch {}
  }, []);

  const loadSummary = useCallback(async () => {
    try {
      const res = await fetch(`${API}/todos?view=all`, { credentials: "same-origin" });
      if (!res.ok) return;
      const data: Todo[] = await res.json();
      const items = data || [];
      setAllOpenTodos(items);
      setSummary(summarizeWork(items));
    } catch {}
  }, []);

  const loadDoneTodos = useCallback(async () => {
    try {
      const res = await fetch(`${API}/todos?view=done`, { credentials: "same-origin" });
      if (res.ok) setDoneTodos(await res.json() || []);
    } catch {}
  }, []);

  useEffect(() => { loadTodos(); }, [loadTodos]);
  useEffect(() => { loadLists(); loadGroups(); loadTags(); loadSummary(); }, [loadLists, loadGroups, loadTags, loadSummary]);
  useEffect(() => { if (calendarIncludeDone) loadDoneTodos(); }, [calendarIncludeDone, loadDoneTodos]);

  const refreshAll = useCallback(() => {
    loadTodos(); loadLists(); loadGroups(); loadTags(); loadSummary();
    if (calendarIncludeDone) loadDoneTodos();
  }, [loadTodos, loadLists, loadGroups, loadTags, loadSummary, loadDoneTodos, calendarIncludeDone]);

  useAppEvents("todo", projectId, (ev) => {
    switch (ev.topic) {
      case "todo.created":
      case "todo.updated":
      case "todo.completed":
      case "todo.uncompleted":
      case "todo.snoozed":
      case "todo.deleted":
      case "todo.list.created":
      case "todo.list.updated":
      case "todo.list.deleted":
      case "todo.list_group.created":
      case "todo.list_group.updated":
      case "todo.list_group.deleted":
      case "todo.tags.changed":
        refreshAll();
        break;
    }
  });

  const submitQuick = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!quick.trim()) return;
    try {
      const res = await fetch(`${API}/quick_add`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: quick, source: "human" }),
      });
      if (!res.ok) { setStatusMsg("Add: " + (await res.text())); return; }
      setQuick("");
      refreshAll();
    } catch (e) {
      setStatusMsg("Add: " + (e as Error).message);
    }
  };

  const toggle = async (t: Todo) => {
    const action = t.status === "done" ? "uncomplete" : "complete";
    setSettlingTodos((s) => ({ ...s, [t.id]: action }));
    try {
      const res = await fetch(`${API}/todos/${t.id}/${action}`, {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) setStatusMsg(`Update: ${res.status}`);
    } catch (e) {
      setStatusMsg("Update: " + (e as Error).message);
    } finally {
      window.setTimeout(() => {
        setSettlingTodos((s) => {
          const next = { ...s };
          delete next[t.id];
          return next;
        });
        refreshAll();
      }, action === "complete" ? 220 : 120);
    }
  };

  const snooze = async (t: Todo, forKey: string) => {
    await fetch(`${API}/todos/${t.id}/snooze`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ for: forKey }),
    });
    loadTodos();
  };

  const remove = async (t: Todo) => {
    if (!confirm(`Delete "${t.title}"?`)) return;
    await fetch(`${API}/todos/${t.id}`, { method: "DELETE", credentials: "same-origin" });
    refreshAll();
  };

  const createList = async (name: string, color: string) => {
    const res = await fetch(`${API}/lists`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, color }),
    });
    if (!res.ok) { setStatusMsg("New list: " + (await res.text())); return; }
    setNewListOpen(false);
    loadLists();
  };

  const updateList = async (id: number, fields: Record<string, unknown>) => {
    await fetch(`${API}/lists/${id}`, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(fields),
    });
    loadLists();
  };

  const deleteList = async (id: number) => {
    await fetch(`${API}/lists/${id}`, { method: "DELETE", credentials: "same-origin" });
    if (pickedList === id) setPickedList(null);
    refreshAll();
  };

  const createGroup = async (name: string, color: string) => {
    const res = await fetch(`${API}/list_groups`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, color }),
    });
    if (!res.ok) { setStatusMsg("New group: " + (await res.text())); return; }
    setNewGroupOpen(false);
    loadGroups();
  };

  const updateGroup = async (id: number, fields: Record<string, unknown>) => {
    await fetch(`${API}/list_groups/${id}`, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(fields),
    });
    loadGroups();
  };

  const deleteGroup = async (id: number) => {
    await fetch(`${API}/list_groups/${id}`, { method: "DELETE", credentials: "same-origin" });
    refreshAll();
  };

  const toggleGroup = (id: number) =>
    setCollapsedGroups((s) => ({ ...s, [id]: !s[id] }));

  const headerLabel = useMemo(() => {
    if (mode === "calendar") return "Calendar";
    const parts: string[] = [];
    if (pickedList) {
      const l = lists.find((x) => x.id === pickedList);
      if (l) parts.push(l.name);
    } else {
      const v = VIEWS.find((x) => x.key === view);
      if (v) parts.push(v.label);
    }
    if (pickedTag) parts.push(`@${pickedTag}`);
    return parts.join(" · ") || "Today";
  }, [mode, view, pickedList, pickedTag, lists]);

  return (
    <div className="h-full flex w-full overflow-hidden">
      <aside className="w-56 border-r border-border flex flex-col py-3 px-2 gap-1 text-sm overflow-y-auto">
        {VIEWS.map((v) => (
          <button
            key={v.key}
            onClick={() => { setMode("tasks"); setView(v.key); setPickedList(null); }}
            className={`text-left px-2 py-1 rounded ${
              mode === "tasks" && view === v.key && !pickedList
                ? "bg-bg-card text-text"
                : "text-text-muted hover:text-text"
            }`}
          >
            {v.label}
          </button>
        ))}
        <button
          onClick={() => { setMode("calendar"); setPickedList(null); }}
          className={`text-left px-2 py-1 rounded ${
            mode === "calendar"
              ? "bg-bg-card text-text"
              : "text-text-muted hover:text-text"
          }`}
        >
          Calendar
        </button>

        <div className="flex items-center justify-between px-2 mt-3 mb-1">
          <span className="text-xs uppercase text-text-dim">Lists</span>
          <div className="flex items-center gap-1">
            <button
              onClick={() => { setNewListOpen((o) => !o); setNewGroupOpen(false); }}
              className="text-text-muted hover:text-text text-base leading-none px-1"
              title="New list"
            >
              +
            </button>
            <button
              onClick={() => { setNewGroupOpen((o) => !o); setNewListOpen(false); }}
              className="text-text-muted hover:text-text text-xs leading-none px-1 border border-border rounded"
              title="New group"
            >
              +grp
            </button>
          </div>
        </div>
        {newListOpen && (
          <NewListForm
            onSave={createList}
            onCancel={() => setNewListOpen(false)}
          />
        )}
        {newGroupOpen && (
          <NewGroupForm
            onSave={createGroup}
            onCancel={() => setNewGroupOpen(false)}
          />
        )}

        {/* Ungrouped lists first */}
        {lists.filter((l) => !l.archived && !l.group_id).map((l) => (
          <ListRow
            key={l.id}
            list={l}
            groups={groups}
            active={mode === "tasks" && pickedList === l.id}
            onClick={() => { setMode("tasks"); setPickedList(l.id); setView("today"); }}
            onUpdate={(fields) => updateList(l.id, fields)}
            onDelete={() => deleteList(l.id)}
          />
        ))}

        {/* Groups, each containing its member lists */}
        {groups.filter((g) => !g.archived).map((g) => {
          const collapsed = !!collapsedGroups[g.id];
          const members = lists.filter((l) => !l.archived && l.group_id === g.id);
          return (
            <div key={`g-${g.id}`} className="mt-1">
              <GroupRow
                group={g}
                collapsed={collapsed}
                memberCount={members.length}
                onToggle={() => toggleGroup(g.id)}
                onUpdate={(fields) => updateGroup(g.id, fields)}
                onDelete={() => deleteGroup(g.id)}
              />
              {!collapsed && members.map((l) => (
                <ListRow
                  key={l.id}
                  list={l}
                  groups={groups}
                  nested
                  active={mode === "tasks" && pickedList === l.id}
                  onClick={() => { setMode("tasks"); setPickedList(l.id); setView("today"); }}
                  onUpdate={(fields) => updateList(l.id, fields)}
                  onDelete={() => deleteList(l.id)}
                />
              ))}
            </div>
          );
        })}

        {tags.filter((t) => t.count > 0).length > 0 && (
          <>
            <div className="text-xs uppercase text-text-dim px-2 mt-3 mb-1">Tags</div>
            {tags.filter((t) => t.count > 0).map((t) => (
              <button
                key={t.id}
                onClick={() => setPickedTag(pickedTag === t.name ? null : t.name)}
                className={`text-left px-2 py-1 rounded flex items-center justify-between ${
                  pickedTag === t.name
                    ? "bg-bg-card text-text"
                    : "text-text-muted hover:text-text"
                }`}
              >
                <span className="truncate">@{t.name}</span>
                <span className="text-text-dim text-xs ml-2">{t.count}</span>
              </button>
            ))}
          </>
        )}
      </aside>

      <main className="flex-1 flex flex-col min-w-0">
        <header className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2">
          <div className="text-text font-medium">{headerLabel}</div>
          {pickedTag && (
            <button
              onClick={() => setPickedTag(null)}
              className="text-text-dim hover:text-text text-xs"
              title="Clear tag filter"
            >
              clear ×
            </button>
          )}
          <SummaryPills
            summary={summary}
            activeView={mode === "tasks" ? view : undefined}
            onSelect={(next) => { setMode("tasks"); setView(next); setPickedList(null); }}
          />
          <span className="ml-auto text-text-dim text-xs">{statusMsg}</span>
        </header>

        <form onSubmit={submitQuick} className="px-4 py-3 border-b border-border flex gap-2">
          <input
            type="text"
            value={quick}
            onChange={(e) => setQuick(e.target.value)}
            placeholder="Add todo… (e.g. 'call plumber tomorrow p1 #home @errand')"
            className="flex-1 bg-bg-input border border-border rounded px-3 py-1.5 text-sm"
          />
          <button
            type="button"
            onClick={() => setCreating(true)}
            className="px-3 text-text-muted hover:text-text border border-border rounded text-sm"
            title="New todo with full options"
          >
            +
          </button>
        </form>

        <div className="flex-1 overflow-y-auto overflow-x-hidden px-2 py-2">
          {mode === "calendar" ? (
            <CalendarView
              openTodos={allOpenTodos}
              doneTodos={doneTodos}
              lists={lists}
              groups={groups}
              month={parseDateKey(calendarMonth)}
              selectedDay={calendarSelectedDay}
              groupFilter={calendarGroup}
              listFilter={calendarList}
              includeDone={calendarIncludeDone}
              activeTab={calendarTab}
              onMonthChange={(next) => {
                setCalendarMonth(dateKey(startOfMonth(next)));
                setCalendarSelectedDay(dateKey(next));
              }}
              onSelectedDayChange={setCalendarSelectedDay}
              onActiveTabChange={setCalendarTab}
              onGroupFilterChange={(next) => {
                setCalendarGroup(next);
                setCalendarList("all");
              }}
              onListFilterChange={setCalendarList}
              onIncludeDoneChange={setCalendarIncludeDone}
              onEditTodo={setEditing}
            />
          ) : todos.length === 0 ? (
            <div className="py-12 text-center text-text-muted text-sm">
              Nothing here.
            </div>
          ) : (
            <ul className="flex flex-col">
              {todos.map((t) => (
                <TodoRow
                  key={t.id}
                  t={t}
                  list={lists.find((l) => l.id === t.list_id)}
                  settling={settlingTodos[t.id]}
                  onToggle={() => toggle(t)}
                  onSnooze={(k) => snooze(t, k)}
                  onEdit={() => setEditing(t)}
                  onDelete={() => remove(t)}
                  onTagClick={(tag) => setPickedTag(tag)}
                />
              ))}
            </ul>
          )}
        </div>
      </main>

      {(editing || creating) && (
        <TodoDialog
          todo={editing ?? undefined}
          lists={lists}
          defaultListID={pickedList ?? undefined}
          onClose={() => { setEditing(null); setCreating(false); }}
          onSaved={() => {
            setEditing(null);
            setCreating(false);
            refreshAll();
          }}
        />
      )}
    </div>
  );
}

function summarizeWork(todos: Todo[]): WorkSummary {
  const now = new Date();
  const tomorrow = new Date(now);
  tomorrow.setHours(24, 0, 0, 0);
  const out: WorkSummary = { overdue: 0, today: 0, future: 0 };
  for (const t of todos) {
    if (t.status !== "open" || !t.due_at) continue;
    const due = new Date(t.due_at);
    if (Number.isNaN(due.getTime())) continue;
    if (due < now) out.overdue++;
    else if (due < tomorrow) out.today++;
    else out.future++;
  }
  return out;
}

function SummaryPills({
  summary, activeView, onSelect,
}: {
  summary: WorkSummary;
  activeView?: View;
  onSelect: (view: View) => void;
}) {
  const items: { key: View; label: string; value: number; tone: string }[] = [
    { key: "overdue", label: "Overdue", value: summary.overdue, tone: "text-error border-error/40 bg-error/10" },
    { key: "today", label: "Today", value: summary.today, tone: "text-warn border-warn/40 bg-warn/10" },
    { key: "upcoming", label: "Future", value: summary.future, tone: "text-info border-info/40 bg-info/10" },
  ];
  return (
    <div className="flex shrink-0 items-center gap-1.5 min-w-0">
      {items.map((item) => (
        <button
          key={item.key}
          type="button"
          onClick={() => onSelect(item.key)}
          className={`h-6 px-2 rounded border text-xs font-medium whitespace-nowrap ${
            activeView === item.key
              ? item.tone
              : "border-border text-text-muted hover:text-text hover:bg-bg-card"
          }`}
          title={`${item.label}: ${item.value}`}
        >
          {item.label} {item.value}
        </button>
      ))}
    </div>
  );
}

function CalendarView({
  openTodos,
  doneTodos,
  lists,
  groups,
  month,
  selectedDay,
  groupFilter,
  listFilter,
  includeDone,
  activeTab,
  onMonthChange,
  onSelectedDayChange,
  onActiveTabChange,
  onGroupFilterChange,
  onListFilterChange,
  onIncludeDoneChange,
  onEditTodo,
}: {
  openTodos: Todo[];
  doneTodos: Todo[];
  lists: List[];
  groups: ListGroup[];
  month: Date;
  selectedDay: string;
  groupFilter: number | "all";
  listFilter: number | "all";
  includeDone: boolean;
  activeTab: CalendarTab;
  onMonthChange: (month: Date) => void;
  onSelectedDayChange: (day: string) => void;
  onActiveTabChange: (tab: CalendarTab) => void;
  onGroupFilterChange: (group: number | "all") => void;
  onListFilterChange: (list: number | "all") => void;
  onIncludeDoneChange: (include: boolean) => void;
  onEditTodo: (todo: Todo) => void;
}) {
  const listByID = useMemo(() => new Map(lists.map((l) => [l.id, l])), [lists]);
  const activeGroups = useMemo(
    () => groups.filter((g) => !g.archived),
    [groups],
  );
  const activeLists = useMemo(
    () => lists.filter((l) => !l.archived),
    [lists],
  );
  const filteredLists = useMemo(() => {
    if (groupFilter === "all") return activeLists;
    return activeLists.filter((l) => l.group_id === groupFilter);
  }, [activeLists, groupFilter]);

  const calendarTodos = useMemo(() => {
    const source = includeDone ? [...openTodos, ...doneTodos] : openTodos;
    return source
      .filter((t) => {
        if (!t.due_at) return false;
        if (listFilter !== "all") return t.list_id === listFilter;
        if (groupFilter !== "all") {
          const list = t.list_id ? listByID.get(t.list_id) : undefined;
          return list?.group_id === groupFilter;
        }
        return true;
      })
      .sort(compareTodosByDue);
  }, [doneTodos, groupFilter, includeDone, listByID, listFilter, openTodos]);

  const buckets = useMemo(() => {
    const out = new Map<string, Todo[]>();
    for (const todo of calendarTodos) {
      const due = todo.due_at ? new Date(todo.due_at) : null;
      if (!due || Number.isNaN(due.getTime())) continue;
      const key = dateKey(due);
      const existing = out.get(key);
      if (existing) existing.push(todo);
      else out.set(key, [todo]);
    }
    return out;
  }, [calendarTodos]);

  const gridDays = useMemo(() => monthGrid(month), [month]);
  const heatMonths = useMemo(
    () => [0, 1, 2].map((offset) => addMonths(month, offset)),
    [month],
  );
  const selectedTodos = buckets.get(selectedDay) || [];
  const selectedListGroups = groupTodosByList(selectedTodos, listByID);
  const monthLabel = month.toLocaleDateString(undefined, { month: "long", year: "numeric" });

  const changeMonth = (delta: number) => {
    const next = new Date(month);
    next.setMonth(next.getMonth() + delta);
    onMonthChange(next);
  };

  const pickDay = (key: string) => {
    onSelectedDayChange(key);
    onActiveTabChange("day");
  };

  return (
    <section className="flex flex-col gap-3 p-2">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => changeMonth(-1)}
          className="h-8 w-8 border border-border rounded text-text-muted hover:text-text hover:bg-bg-card"
          title="Previous month"
        >
          ‹
        </button>
        <button
          type="button"
          onClick={() => onMonthChange(new Date())}
          className="h-8 px-3 border border-border rounded text-sm text-text-muted hover:text-text hover:bg-bg-card"
        >
          Today
        </button>
        <button
          type="button"
          onClick={() => changeMonth(1)}
          className="h-8 w-8 border border-border rounded text-text-muted hover:text-text hover:bg-bg-card"
          title="Next month"
        >
          ›
        </button>
        <div className="text-text font-medium min-w-[9rem]">{monthLabel}</div>
        <select
          value={groupFilter}
          onChange={(e) => onGroupFilterChange(e.target.value === "all" ? "all" : Number(e.target.value))}
          className="h-8 bg-bg-input border border-border rounded px-2 text-sm text-text"
          title="Filter by group"
        >
          <option value="all">All groups</option>
          {activeGroups.map((g) => (
            <option key={g.id} value={g.id}>{g.name}</option>
          ))}
        </select>
        <select
          value={listFilter}
          onChange={(e) => onListFilterChange(e.target.value === "all" ? "all" : Number(e.target.value))}
          className="h-8 bg-bg-input border border-border rounded px-2 text-sm text-text"
          title="Filter by list"
        >
          <option value="all">All lists</option>
          {filteredLists.map((l) => (
            <option key={l.id} value={l.id}>{l.name}</option>
          ))}
        </select>
        <label className="ml-auto h-8 flex items-center gap-2 text-sm text-text-muted">
          <input
            type="checkbox"
            checked={includeDone}
            onChange={(e) => onIncludeDoneChange(e.target.checked)}
          />
          Done
        </label>
      </div>

      <div className="flex items-center gap-1 border-b border-border">
        {[
          { key: "heatmap" as const, label: "Heatmap" },
          { key: "month" as const, label: "Month" },
          { key: "day" as const, label: "Day" },
        ].map((tab) => (
          <button
            key={tab.key}
            type="button"
            onClick={() => onActiveTabChange(tab.key)}
            className={`px-3 py-2 text-sm border-b-2 ${
              activeTab === tab.key
                ? "border-accent text-text"
                : "border-transparent text-text-muted hover:text-text"
            }`}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {activeTab === "heatmap" && (
        <div className="grid gap-4 xl:grid-cols-3 lg:grid-cols-2">
          {heatMonths.map((heatMonth) => {
            const heatMonthDays = miniMonthGrid(heatMonth);
            const scheduled = daysInMonth(heatMonth)
              .reduce((sum, day) => sum + (buckets.get(dateKey(day))?.length || 0), 0);
            return (
              <div
                key={dateKey(heatMonth)}
                className="rounded bg-bg-input/20 p-4"
                style={{ border: `1px solid ${SOFT_BORDER}` }}
              >
                <div className="mb-4 flex items-baseline justify-between gap-3">
                  <div className="text-lg font-medium text-text">
                    {heatMonth.toLocaleDateString(undefined, { month: "long" })}
                  </div>
                  <div className="text-xs text-text-dim">{scheduled} tasks</div>
                </div>
                <div
                  className="grid gap-x-2 gap-y-2"
                  style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))" }}
                >
                  {["M", "T", "W", "T", "F", "S", "S"].map((label, index) => (
                    <div key={`${label}-${index}`} className="pb-1 text-center text-[11px] font-medium text-text-dim">
                      {label}
                    </div>
                  ))}
                  {heatMonthDays.map((day) => {
                    const key = dateKey(day);
                    const count = buckets.get(key)?.length || 0;
                    const inMonth = day.getMonth() === heatMonth.getMonth();
                    return (
                      <button
                        key={key}
                        type="button"
                        onClick={() => pickDay(key)}
                        className={`relative h-9 rounded text-center text-sm font-medium transition hover:bg-bg-card ${
                          selectedDay === key ? "ring-1 ring-accent" : ""
                        } ${inMonth ? "text-text" : "text-text-dim opacity-35"}`}
                        style={heatCellStyle(count)}
                        title={`${formatShortDate(day)}: ${count} task${count === 1 ? "" : "s"}`}
                      >
                        <span>{day.getDate()}</span>
                        {count > 0 && (
                          <span className="absolute bottom-0.5 right-1 text-[9px] opacity-80">{count}</span>
                        )}
                      </button>
                    );
                  })}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {activeTab === "month" && (
        <div className="overflow-hidden rounded" style={{ border: `1px solid ${SOFT_BORDER}` }}>
        <div
          className="grid bg-bg-input/40"
          style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))" }}
        >
          {["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].map((label) => (
            <div
              key={label}
              className="px-2 py-1.5 text-[10px] uppercase text-text-dim"
              style={{ borderRight: `1px solid ${SOFT_BORDER}`, borderBottom: `1px solid ${SOFT_BORDER}` }}
            >
              {label}
            </div>
          ))}
        </div>
        <div
          className="grid"
          style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))" }}
        >
          {gridDays.map((day) => {
            const key = dateKey(day);
            const items = buckets.get(key) || [];
            const inMonth = day.getMonth() === month.getMonth();
            const uniqueLists = new Set(items.map((t) => t.list_id).filter(Boolean)).size;
            return (
              <button
                key={key}
                type="button"
                onClick={() => pickDay(key)}
                className={`min-h-[7.5rem] p-2 text-left align-top hover:bg-bg-card/60 ${
                  selectedDay === key ? "bg-bg-card" : ""
                } ${inMonth ? "" : "bg-bg-input/20 opacity-45"}`}
                style={{ borderRight: `1px solid ${SOFT_BORDER}`, borderBottom: `1px solid ${SOFT_BORDER}` }}
              >
                <div className="flex items-start justify-between gap-2">
                  <span className="text-xs font-medium text-text">{day.getDate()}</span>
                  {items.length > 0 && (
                    <span className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${heatBadgeClass(items.length)}`}>
                      {items.length}
                    </span>
                  )}
                </div>
                {uniqueLists > 1 && (
                  <div className="mt-1 text-[10px] text-text-dim">{uniqueLists} lists</div>
                )}
                <div className="mt-2 flex flex-col gap-1">
                  {items.slice(0, 3).map((todo) => {
                    const list = todo.list_id ? listByID.get(todo.list_id) : undefined;
                    return (
                      <div key={todo.id} className="flex items-center gap-1 min-w-0 text-[11px] text-text-muted">
                        <span
                          className="h-1.5 w-1.5 rounded-full shrink-0"
                          style={{ background: list?.color || "#6b7280" }}
                        />
                        <span className={todo.status === "done" ? "truncate line-through" : "truncate"}>
                          {todo.title}
                        </span>
                      </div>
                    );
                  })}
                  {items.length > 3 && (
                    <div className="text-[10px] text-text-dim">+{items.length - 3} more</div>
                  )}
                </div>
              </button>
            );
          })}
        </div>
      </div>
      )}

      {activeTab === "day" && (
      <div className="rounded bg-bg-card/30" style={{ border: `1px solid ${SOFT_BORDER}` }}>
        <div className="flex items-center justify-between px-3 py-2" style={{ borderBottom: `1px solid ${SOFT_BORDER}` }}>
          <div>
            <div className="text-sm font-medium text-text">{formatLongDate(parseDateKey(selectedDay))}</div>
            <div className="text-xs text-text-dim">{selectedTodos.length} task{selectedTodos.length === 1 ? "" : "s"}</div>
          </div>
          <div className={`rounded px-2 py-1 text-xs ${heatBadgeClass(selectedTodos.length)}`}>
            {heatLabel(selectedTodos.length)}
          </div>
        </div>
        {selectedTodos.length === 0 ? (
          <div className="px-3 py-6 text-center text-sm text-text-muted">No scheduled tasks.</div>
        ) : (
          <div className="flex flex-col">
            {selectedListGroups.map(({ list, todos: dayTodos }) => (
              <div
                key={list?.id ?? "inbox"}
                className="px-3 py-2"
                style={{ borderBottom: `1px solid ${SOFT_BORDER}` }}
              >
                <div className="mb-1 flex items-center gap-2 text-xs uppercase text-text-dim">
                  <span
                    className="h-2 w-2 rounded-full"
                    style={{ background: list?.color || "#6b7280" }}
                  />
                  {list?.name || "Inbox"}
                  <span className="normal-case">{dayTodos.length}</span>
                </div>
                <div className="flex flex-col">
                  {dayTodos.map((todo) => (
                    <button
                      key={todo.id}
                      type="button"
                      onClick={() => onEditTodo(todo)}
                      className="flex items-center gap-2 rounded px-2 py-1 text-left text-sm hover:bg-bg-card"
                    >
                      <span className="w-11 shrink-0 text-xs text-text-dim">{formatTime(todo.due_at)}</span>
                      <span className={`min-w-0 flex-1 truncate text-text ${todo.status === "done" ? "line-through text-text-dim" : ""}`}>
                        {todo.title}
                      </span>
                      {todo.priority < 4 && (
                        <span className={`shrink-0 text-xs ${PRIORITY_TONE[todo.priority]}`}>P{todo.priority}</span>
                      )}
                    </button>
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
      )}
    </section>
  );
}

function compareTodosByDue(a: Todo, b: Todo): number {
  const ad = a.due_at ? new Date(a.due_at).getTime() : Number.MAX_SAFE_INTEGER;
  const bd = b.due_at ? new Date(b.due_at).getTime() : Number.MAX_SAFE_INTEGER;
  if (ad !== bd) return ad - bd;
  return a.id - b.id;
}

function groupTodosByList(todos: Todo[], listByID: Map<number, List>) {
  const grouped = new Map<number | "inbox", { list?: List; todos: Todo[] }>();
  for (const todo of todos) {
    const key = todo.list_id ?? "inbox";
    const existing = grouped.get(key);
    if (existing) existing.todos.push(todo);
    else grouped.set(key, { list: todo.list_id ? listByID.get(todo.list_id) : undefined, todos: [todo] });
  }
  return Array.from(grouped.values());
}

function startOfMonth(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), 1);
}

function addMonths(d: Date, count: number): Date {
  return new Date(d.getFullYear(), d.getMonth() + count, 1);
}

function parseDateKey(key: string): Date {
  const [year, month, day] = key.split("-").map(Number);
  return new Date(year, month - 1, day);
}

function dateKey(d: Date): string {
  const year = d.getFullYear();
  const month = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function monthGrid(month: Date): Date[] {
  const first = startOfMonth(month);
  const start = new Date(first);
  start.setDate(first.getDate() - first.getDay());
  const last = new Date(month.getFullYear(), month.getMonth() + 1, 0);
  const end = new Date(last);
  end.setDate(last.getDate() + (6 - last.getDay()));
  const out: Date[] = [];
  for (const d = new Date(start); d <= end; d.setDate(d.getDate() + 1)) {
    out.push(new Date(d));
  }
  return out;
}

function miniMonthGrid(month: Date): Date[] {
  const first = startOfMonth(month);
  const start = new Date(first);
  const mondayIndex = (first.getDay() + 6) % 7;
  start.setDate(first.getDate() - mondayIndex);
  const out: Date[] = [];
  for (let i = 0; i < 42; i++) {
    const day = new Date(start);
    day.setDate(start.getDate() + i);
    out.push(day);
  }
  return out;
}

function daysInMonth(month: Date): Date[] {
  const out: Date[] = [];
  const lastDay = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate();
  for (let day = 1; day <= lastDay; day++) {
    out.push(new Date(month.getFullYear(), month.getMonth(), day));
  }
  return out;
}

function heatCellStyle(count: number): React.CSSProperties {
  if (count === 0) {
    return { background: "transparent", boxShadow: `inset 0 0 0 1px ${SOFT_BORDER}` };
  }
  if (count <= 3) {
    return { background: "rgba(59, 130, 246, 0.16)", color: "#bfdbfe" };
  }
  if (count <= 7) {
    return { background: "rgba(34, 197, 94, 0.18)", color: "#bbf7d0" };
  }
  if (count <= 12) {
    return { background: "rgba(245, 158, 11, 0.22)", color: "#fde68a" };
  }
  return { background: "rgba(239, 68, 68, 0.26)", color: "#fecaca" };
}

function heatBadgeClass(count: number): string {
  if (count === 0) return "bg-bg-input text-text-dim";
  if (count <= 3) return "bg-info/10 text-info";
  if (count <= 7) return "bg-success/15 text-success";
  if (count <= 12) return "bg-warn/20 text-warn";
  return "bg-error/25 text-error";
}

function heatLabel(count: number): string {
  if (count === 0) return "Empty";
  if (count <= 3) return "Light";
  if (count <= 7) return "Medium";
  if (count <= 12) return "Heavy";
  return "Hot";
}

function formatShortDate(d: Date): string {
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function formatLongDate(d: Date): string {
  return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" });
}

function formatTime(dueAt?: string): string {
  if (!dueAt) return "";
  const d = new Date(dueAt);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getHours().toString().padStart(2, "0")}:${d.getMinutes().toString().padStart(2, "0")}`;
}

function TodoRow({
  t, list, settling, onToggle, onSnooze, onEdit, onDelete, onTagClick,
}: {
  t: Todo;
  list?: List;
  settling?: "complete" | "uncomplete";
  onToggle: () => void;
  onSnooze: (k: string) => void;
  onEdit: () => void;
  onDelete: () => void;
  onTagClick: (tag: string) => void;
}) {
  const due = t.due_at ? new Date(t.due_at) : null;
  const overdue = due && t.status === "open" && due < new Date();
  const visuallyDone = t.status === "done" || settling === "complete";
  const isSettling = !!settling;
  return (
    <li
      className={`group relative flex items-start gap-2 rounded-md border-b border-border/50 px-2 py-1.5 transition-all duration-150 ease-out hover:-translate-y-px hover:bg-bg-card/70 hover:shadow-sm focus-within:bg-bg-card/70 ${
        isSettling ? "translate-x-1 bg-success/5 opacity-70" : ""
      }`}
    >
      <button
        type="button"
        onClick={onToggle}
        className={`relative mt-0.5 flex h-5 w-5 shrink-0 cursor-pointer items-center justify-center rounded-full border transition-all duration-150 ease-out hover:scale-110 hover:border-accent focus:outline-none focus:ring-2 focus:ring-accent/40 ${
          visuallyDone ? "border-success bg-success text-bg" : "border-text-dim bg-transparent text-transparent group-hover:border-text-muted"
        }`}
        aria-label={visuallyDone ? "Mark todo open" : "Mark todo complete"}
        title={visuallyDone ? "Mark open" : "Complete todo"}
      >
        {settling === "complete" && (
          <span className="absolute inset-[-3px] rounded-full border border-success/50 animate-ping" />
        )}
        <span className={`text-[11px] leading-none transition-transform duration-150 ${visuallyDone ? "scale-100" : "scale-0"}`}>
          ✓
        </span>
      </button>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          {t.priority < 4 && (
            <span className={`shrink-0 text-xs ${PRIORITY_TONE[t.priority]}`}>P{t.priority}</span>
          )}
          <button
            type="button"
            onClick={onEdit}
            className={`flex-1 min-w-0 cursor-pointer truncate text-left text-sm text-text transition-colors group-hover:text-accent ${
              visuallyDone ? "text-text-muted line-through decoration-text-dim/70" : ""
            }`}
            title={t.title}
          >
            {t.title}
          </button>
          {t.rrule && <span className="shrink-0 text-[10px] text-text-dim">↻</span>}
          {t.source === "agent" && (
            <span className="shrink-0 text-[10px] text-info border border-info/40 rounded px-1">agent</span>
          )}
        </div>
        <div className="flex items-center gap-2 text-xs text-text-dim flex-wrap">
          {due && (
            <span className={overdue ? "text-error" : ""}>
              {formatDue(due)}
            </span>
          )}
          {list && (
            <span className="flex items-center gap-1 min-w-0">
              <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: list.color }} />
              <span className="truncate">{list.name}</span>
            </span>
          )}
          {t.tags.map((tag) => (
            <button
              type="button"
              key={tag}
              onClick={(e) => { e.stopPropagation(); onTagClick(tag); }}
              className="cursor-pointer transition-colors hover:text-text"
              title={`Filter by @${tag}`}
            >
              @{tag}
            </button>
          ))}
        </div>
      </div>
      <div className="shrink-0 flex items-center gap-1 text-xs opacity-25 transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100">
        <button type="button" onClick={() => onSnooze("tomorrow")} className="cursor-pointer rounded px-1 text-text-muted transition-colors hover:bg-bg-input hover:text-text">tmrw</button>
        <button type="button" onClick={() => onSnooze("next_week")} className="cursor-pointer rounded px-1 text-text-muted transition-colors hover:bg-bg-input hover:text-text">+1w</button>
        <button type="button" onClick={onDelete} className="cursor-pointer rounded px-1 text-text-muted transition-colors hover:bg-bg-input hover:text-error">×</button>
      </div>
    </li>
  );
}

function ListRow({
  list, groups, nested, active, onClick, onUpdate, onDelete,
}: {
  list: List;
  groups: ListGroup[];
  nested?: boolean;
  active: boolean;
  onClick: () => void;
  onUpdate: (fields: Record<string, unknown>) => void;
  onDelete: () => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    <div className={`relative group ${nested ? "pl-4" : ""}`}>
      <button
        onClick={onClick}
        className={`w-full text-left px-2 py-1 rounded flex items-center gap-2 pr-7 ${
          active
            ? "bg-bg-card text-text"
            : "text-text-muted hover:text-text"
        }`}
      >
        <span className="w-2 h-2 rounded-full shrink-0" style={{ background: list.color }} />
        <span className="truncate">{list.name}</span>
      </button>
      <button
        onClick={(e) => { e.stopPropagation(); setMenuOpen((o) => !o); }}
        className="absolute right-1 top-1 opacity-0 group-hover:opacity-100 text-text-muted hover:text-text px-1 leading-none"
        title="List options"
      >
        ⋯
      </button>
      {menuOpen && (
        <>
          <div
            className="fixed inset-0 z-10"
            onClick={() => setMenuOpen(false)}
          />
          <div className="absolute right-0 top-full z-20 bg-bg-card border border-border rounded shadow text-sm flex flex-col w-40 py-1">
            <button
              onClick={() => {
                const name = prompt("Rename list", list.name);
                if (name && name.trim() && name !== list.name) onUpdate({ name: name.trim() });
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input"
            >
              Rename
            </button>
            <button
              onClick={() => {
                const color = prompt("Color (hex, e.g. #3b82f6)", list.color);
                if (color && color.trim()) onUpdate({ color: color.trim() });
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input"
            >
              Recolor
            </button>
            {groups.length > 0 && (
              <>
                <div className="px-3 pt-1 pb-0.5 text-[10px] uppercase text-text-dim">Move to</div>
                <button
                  onClick={() => { onUpdate({ group_id: 0 }); setMenuOpen(false); }}
                  className={`text-left px-3 py-0.5 hover:bg-bg-input ${list.group_id ? "" : "text-accent"}`}
                >
                  — ungrouped —
                </button>
                {groups.filter((g) => !g.archived).map((g) => (
                  <button
                    key={g.id}
                    onClick={() => { onUpdate({ group_id: g.id }); setMenuOpen(false); }}
                    className={`text-left px-3 py-0.5 hover:bg-bg-input ${list.group_id === g.id ? "text-accent" : ""}`}
                  >
                    {g.name}
                  </button>
                ))}
              </>
            )}
            <button
              onClick={() => {
                onUpdate({ archived: true });
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input mt-1 border-t border-border"
            >
              Archive
            </button>
            <button
              onClick={() => {
                if (confirm(`Delete list "${list.name}"? Todos in it move to inbox.`)) {
                  onDelete();
                }
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input text-error"
            >
              Delete
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function GroupRow({
  group, collapsed, memberCount, onToggle, onUpdate, onDelete,
}: {
  group: ListGroup;
  collapsed: boolean;
  memberCount: number;
  onToggle: () => void;
  onUpdate: (fields: Record<string, unknown>) => void;
  onDelete: () => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    <div className="relative group mt-2">
      <button
        onClick={onToggle}
        className="w-full text-left px-2 py-1 rounded flex items-center gap-2 pr-7 text-text"
      >
        <span className="text-text-dim text-xs w-3 inline-block">{collapsed ? "▸" : "▾"}</span>
        <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: group.color }} />
        <span className="truncate text-xs uppercase tracking-wider">{group.name}</span>
        <span className="ml-auto text-text-dim text-[10px]">{memberCount}</span>
      </button>
      <button
        onClick={(e) => { e.stopPropagation(); setMenuOpen((o) => !o); }}
        className="absolute right-0 top-1 opacity-0 group-hover:opacity-100 text-text-muted hover:text-text px-1 leading-none"
        title="Group options"
      >
        ⋯
      </button>
      {menuOpen && (
        <>
          <div className="fixed inset-0 z-10" onClick={() => setMenuOpen(false)} />
          <div className="absolute right-0 top-full z-20 bg-bg-card border border-border rounded shadow text-sm flex flex-col w-32 py-1">
            <button
              onClick={() => {
                const name = prompt("Rename group", group.name);
                if (name && name.trim() && name !== group.name) onUpdate({ name: name.trim() });
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input"
            >
              Rename
            </button>
            <button
              onClick={() => {
                const color = prompt("Color (hex)", group.color);
                if (color && color.trim()) onUpdate({ color: color.trim() });
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input"
            >
              Recolor
            </button>
            <button
              onClick={() => {
                if (confirm(`Delete group "${group.name}"? Its ${memberCount} list(s) become ungrouped.`)) {
                  onDelete();
                }
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input text-error"
            >
              Delete
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function NewGroupForm({
  onSave, onCancel,
}: {
  onSave: (name: string, color: string) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [color, setColor] = useState("#6b7280");
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) onSave(name.trim(), color);
      }}
      className="flex items-center gap-1 px-2 py-1"
    >
      <input
        type="color"
        value={color}
        onChange={(e) => setColor(e.target.value)}
        className="w-5 h-5 rounded border border-border bg-transparent shrink-0 cursor-pointer"
        title="Group color"
      />
      <input
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="Group name"
        onKeyDown={(e) => { if (e.key === "Escape") onCancel(); }}
        className="flex-1 bg-bg-input border border-border rounded px-2 py-0.5 text-sm min-w-0"
      />
    </form>
  );
}

function NewListForm({
  onSave, onCancel,
}: {
  onSave: (name: string, color: string) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [color, setColor] = useState("#3b82f6");
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) onSave(name.trim(), color);
      }}
      className="flex items-center gap-1 px-2 py-1"
    >
      <input
        type="color"
        value={color}
        onChange={(e) => setColor(e.target.value)}
        className="w-5 h-5 rounded border border-border bg-transparent shrink-0 cursor-pointer"
        title="List color"
      />
      <input
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="List name"
        onKeyDown={(e) => { if (e.key === "Escape") onCancel(); }}
        className="flex-1 bg-bg-input border border-border rounded px-2 py-0.5 text-sm min-w-0"
      />
    </form>
  );
}

function TodoDialog({
  todo, lists, defaultListID, onClose, onSaved,
}: {
  todo?: Todo;
  lists: List[];
  defaultListID?: number;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isCreate = !todo;
  const [title, setTitle] = useState(todo?.title ?? "");
  const [notes, setNotes] = useState(todo?.notes ?? "");
  const [priority, setPriority] = useState(todo?.priority ?? 4);
  const [dueAt, setDueAt] = useState(todo?.due_at?.slice(0, 16) ?? "");
  const [listID, setListID] = useState<number | "">(
    todo?.list_id ?? (defaultListID ?? "")
  );
  const [rrule, setRRule] = useState(todo?.rrule ?? "");
  const [tags, setTags] = useState((todo?.tags ?? []).join(" "));

  const save = async () => {
    const tagList = tags.split(/\s+/).filter(Boolean).map((s) => s.replace(/^@/, ""));
    const body: Record<string, unknown> = {
      title,
      notes,
      priority,
      rrule,
      tags: tagList,
      due_at: dueAt ? new Date(dueAt).toISOString() : "",
      list_id: listID === "" ? 0 : listID,
    };

    if (isCreate) {
      await fetch(`${API}/todos`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...body, source: "human" }),
      });
    } else {
      await fetch(`${API}/todos/${todo!.id}`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
    }
    onSaved();
  };

  return (
    <div
      className="fixed inset-0 bg-black/60 grid place-items-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-bg-card border border-border rounded p-4 w-[480px] max-w-[90vw] flex flex-col gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">
            {isCreate ? "New todo" : "Edit todo"}
          </div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        <input
          autoFocus={isCreate}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Title"
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
        <textarea
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          placeholder="Notes"
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-[80px]"
        />
        <div className="grid grid-cols-2 gap-2">
          <label className="text-xs text-text-dim flex flex-col gap-1">
            Priority
            <select
              value={priority}
              onChange={(e) => setPriority(parseInt(e.target.value))}
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value={1}>P1 — urgent</option>
              <option value={2}>P2 — high</option>
              <option value={3}>P3 — normal</option>
              <option value={4}>P4 — low</option>
            </select>
          </label>
          <label className="text-xs text-text-dim flex flex-col gap-1">
            List
            <select
              value={listID}
              onChange={(e) =>
                setListID(e.target.value === "" ? "" : parseInt(e.target.value))
              }
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value="">— inbox —</option>
              {lists.filter((l) => !l.archived).map((l) => (
                <option key={l.id} value={l.id}>{l.name}</option>
              ))}
            </select>
          </label>
          <label className="text-xs text-text-dim flex flex-col gap-1">
            Due
            <input
              type="datetime-local"
              value={dueAt}
              onChange={(e) => setDueAt(e.target.value)}
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </label>
          <label className="text-xs text-text-dim flex flex-col gap-1">
            Recurrence
            <input
              value={rrule}
              onChange={(e) => setRRule(e.target.value)}
              placeholder="FREQ=DAILY"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </label>
        </div>
        <label className="text-xs text-text-dim flex flex-col gap-1">
          Tags
          <input
            value={tags}
            onChange={(e) => setTags(e.target.value)}
            placeholder="errand home"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
        </label>
        <div className="flex gap-2 justify-end">
          <button onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
          <button
            onClick={save}
            disabled={!title.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {isCreate ? "Create" : "Save"}
          </button>
        </div>
      </div>
    </div>
  );
}

function formatDue(d: Date): string {
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  if (sameDay) {
    return `today ${d.getHours().toString().padStart(2, "0")}:${d
      .getMinutes()
      .toString()
      .padStart(2, "0")}`;
  }
  const opts: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" };
  return d.toLocaleDateString(undefined, opts);
}
