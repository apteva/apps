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

import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/todo";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
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
}

interface Tag {
  id: number;
  name: string;
  count: number;
}

type View = "inbox" | "today" | "upcoming" | "overdue" | "all" | "done";

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

export default function TodoPanel({}: NativePanelProps) {
  const [view, setView] = useState<View>("today");
  const [pickedList, setPickedList] = useState<number | null>(null);
  const [pickedTag, setPickedTag] = useState<string | null>(null);
  const [todos, setTodos] = useState<Todo[]>([]);
  const [lists, setLists] = useState<List[]>([]);
  const [tags, setTags] = useState<Tag[]>([]);
  const [quick, setQuick] = useState("");
  const [statusMsg, setStatusMsg] = useState("");
  const [editing, setEditing] = useState<Todo | null>(null);
  const [creating, setCreating] = useState(false);
  const [newListOpen, setNewListOpen] = useState(false);

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

  const loadTags = useCallback(async () => {
    try {
      const res = await fetch(`${API}/tags`, { credentials: "same-origin" });
      if (res.ok) setTags(await res.json() || []);
    } catch {}
  }, []);

  useEffect(() => { loadTodos(); }, [loadTodos]);
  useEffect(() => { loadLists(); loadTags(); }, [loadLists, loadTags]);

  const refreshAll = useCallback(() => {
    loadTodos(); loadLists(); loadTags();
  }, [loadTodos, loadLists, loadTags]);

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
    const path = t.status === "done" ? "uncomplete" : "complete";
    await fetch(`${API}/todos/${t.id}/${path}`, {
      method: "POST",
      credentials: "same-origin",
    });
    loadTodos();
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

  const headerLabel = useMemo(() => {
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
  }, [view, pickedList, pickedTag, lists]);

  return (
    <div className="h-full flex w-full overflow-hidden">
      <aside className="w-56 border-r border-border flex flex-col py-3 px-2 gap-1 text-sm overflow-y-auto">
        {VIEWS.map((v) => (
          <button
            key={v.key}
            onClick={() => { setView(v.key); setPickedList(null); }}
            className={`text-left px-2 py-1 rounded ${
              view === v.key && !pickedList
                ? "bg-bg-card text-text"
                : "text-text-muted hover:text-text"
            }`}
          >
            {v.label}
          </button>
        ))}

        <div className="flex items-center justify-between px-2 mt-3 mb-1">
          <span className="text-xs uppercase text-text-dim">Lists</span>
          <button
            onClick={() => setNewListOpen((o) => !o)}
            className="text-text-muted hover:text-text text-base leading-none px-1"
            title="New list"
          >
            +
          </button>
        </div>
        {newListOpen && (
          <NewListForm
            onSave={createList}
            onCancel={() => setNewListOpen(false)}
          />
        )}
        {lists.filter((l) => !l.archived).map((l) => (
          <ListRow
            key={l.id}
            list={l}
            active={pickedList === l.id}
            onClick={() => { setPickedList(l.id); setView("today"); }}
            onUpdate={(fields) => updateList(l.id, fields)}
            onDelete={() => deleteList(l.id)}
          />
        ))}

        {tags.length > 0 && (
          <>
            <div className="text-xs uppercase text-text-dim px-2 mt-3 mb-1">Tags</div>
            {tags.map((t) => (
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
        <header className="flex items-center gap-3 border-b border-border px-4 py-2">
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
          {todos.length === 0 ? (
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

function TodoRow({
  t, list, onToggle, onSnooze, onEdit, onDelete, onTagClick,
}: {
  t: Todo;
  list?: List;
  onToggle: () => void;
  onSnooze: (k: string) => void;
  onEdit: () => void;
  onDelete: () => void;
  onTagClick: (tag: string) => void;
}) {
  const due = t.due_at ? new Date(t.due_at) : null;
  const overdue = due && t.status === "open" && due < new Date();
  return (
    <li className="flex items-start gap-2 py-1.5 px-2 border-b border-border/50 hover:bg-bg-card/50 group">
      <button
        onClick={onToggle}
        className={`shrink-0 mt-0.5 w-4 h-4 rounded-full border ${
          t.status === "done" ? "bg-success border-success" : "border-text-dim"
        }`}
      />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          {t.priority < 4 && (
            <span className={`shrink-0 text-xs ${PRIORITY_TONE[t.priority]}`}>P{t.priority}</span>
          )}
          <button
            onClick={onEdit}
            className="flex-1 min-w-0 text-left text-text text-sm truncate"
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
              key={tag}
              onClick={(e) => { e.stopPropagation(); onTagClick(tag); }}
              className="hover:text-text"
              title={`Filter by @${tag}`}
            >
              @{tag}
            </button>
          ))}
        </div>
      </div>
      <div className="shrink-0 opacity-30 group-hover:opacity-100 flex items-center gap-1 text-xs">
        <button onClick={() => onSnooze("tomorrow")} className="text-text-muted hover:text-text px-1">tmrw</button>
        <button onClick={() => onSnooze("next_week")} className="text-text-muted hover:text-text px-1">+1w</button>
        <button onClick={onDelete} className="text-text-muted hover:text-error px-1">×</button>
      </div>
    </li>
  );
}

function ListRow({
  list, active, onClick, onUpdate, onDelete,
}: {
  list: List;
  active: boolean;
  onClick: () => void;
  onUpdate: (fields: Record<string, unknown>) => void;
  onDelete: () => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    <div className="relative group">
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
          <div className="absolute right-0 top-full z-20 bg-bg-card border border-border rounded shadow text-sm flex flex-col w-32 py-1">
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
            <button
              onClick={() => {
                onUpdate({ archived: true });
                setMenuOpen(false);
              }}
              className="text-left px-3 py-1 hover:bg-bg-input"
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
