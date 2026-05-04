// TodoPanel — personal todo list, sibling of TasksPanel.
//
// Layout:
//   ┌─ sidebar ─┐ ┌──────────── main ─────────────────────────────┐
//   │ Inbox     │ │ quick add bar                                │
//   │ Today     │ │ ──────────────────────────────────────────── │
//   │ Upcoming  │ │ todo rows (checkbox · title · due · tags)    │
//   │ Overdue   │ │                                              │
//   │ Done      │ │                                              │
//   │ ── projects ─                                              │
//   │ #home     │ │                                              │
//   │ #work     │ │                                              │
//   └───────────┘ └────────────────────────────────────────────-─┘
//
// Quick-add box accepts the same NL grammar as the MCP tool:
//   "call the plumber tomorrow p1 #home @errand"

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
  project_ref: number | null;
  tags: string[];
  created_at: string;
}

interface Project {
  id: number;
  name: string;
  color: string;
  archived: boolean;
}

type View = "inbox" | "today" | "upcoming" | "overdue" | "done";

const VIEWS: { key: View; label: string }[] = [
  { key: "inbox",    label: "Inbox" },
  { key: "today",    label: "Today" },
  { key: "upcoming", label: "Upcoming" },
  { key: "overdue",  label: "Overdue" },
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
  const [pickedProject, setPickedProject] = useState<number | null>(null);
  const [todos, setTodos] = useState<Todo[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [quick, setQuick] = useState("");
  const [status, setStatus] = useState("");
  const [editing, setEditing] = useState<Todo | null>(null);

  const params = useMemo(() => {
    const p = new URLSearchParams();
    p.set("view", view);
    if (pickedProject) p.set("project_id", String(pickedProject));
    return p.toString();
  }, [view, pickedProject]);

  const loadTodos = useCallback(async () => {
    try {
      const res = await fetch(`${API}/todos?${params}`, { credentials: "same-origin" });
      if (!res.ok) { setStatus(`Load: ${res.status}`); return; }
      const data: Todo[] = await res.json();
      setTodos(data || []);
      setStatus(`${(data || []).length} todos`);
    } catch (e) {
      setStatus("Load: " + (e as Error).message);
    }
  }, [params]);

  const loadProjects = useCallback(async () => {
    try {
      const res = await fetch(`${API}/projects`, { credentials: "same-origin" });
      if (res.ok) setProjects(await res.json() || []);
    } catch {}
  }, []);

  useEffect(() => { loadTodos(); }, [loadTodos]);
  useEffect(() => { loadProjects(); }, [loadProjects]);

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
      if (!res.ok) { setStatus("Add: " + (await res.text())); return; }
      setQuick("");
      loadTodos();
      loadProjects();
    } catch (e) {
      setStatus("Add: " + (e as Error).message);
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
    loadTodos();
  };

  return (
    <div className="h-full flex">
      <aside className="w-48 border-r border-border flex flex-col py-3 px-2 gap-1 text-sm">
        {VIEWS.map((v) => (
          <button
            key={v.key}
            onClick={() => { setView(v.key); setPickedProject(null); }}
            className={`text-left px-2 py-1 rounded ${
              view === v.key && !pickedProject
                ? "bg-bg-card text-text"
                : "text-text-muted hover:text-text"
            }`}
          >
            {v.label}
          </button>
        ))}
        <div className="text-xs uppercase text-text-dim px-2 mt-3 mb-1">Projects</div>
        {projects.filter((p) => !p.archived).map((p) => (
          <button
            key={p.id}
            onClick={() => { setPickedProject(p.id); setView("today"); }}
            className={`text-left px-2 py-1 rounded flex items-center gap-2 ${
              pickedProject === p.id
                ? "bg-bg-card text-text"
                : "text-text-muted hover:text-text"
            }`}
          >
            <span className="w-2 h-2 rounded-full" style={{ background: p.color }} />
            {p.name}
          </button>
        ))}
      </aside>

      <main className="flex-1 flex flex-col">
        <header className="flex items-center gap-3 border-b border-border px-4 py-2">
          <div className="text-text font-medium">
            {pickedProject
              ? projects.find((p) => p.id === pickedProject)?.name ?? "Project"
              : VIEWS.find((v) => v.key === view)?.label}
          </div>
          <span className="ml-auto text-text-dim text-xs">{status}</span>
        </header>

        <form onSubmit={submitQuick} className="px-4 py-3 border-b border-border">
          <input
            type="text"
            value={quick}
            onChange={(e) => setQuick(e.target.value)}
            placeholder="Add todo… (e.g. 'call plumber tomorrow p1 #home @errand')"
            className="w-full bg-bg-input border border-border rounded px-3 py-1.5 text-sm"
          />
        </form>

        <div className="flex-1 overflow-auto px-2 py-2">
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
                  project={projects.find((p) => p.id === t.project_ref)}
                  onToggle={() => toggle(t)}
                  onSnooze={(k) => snooze(t, k)}
                  onEdit={() => setEditing(t)}
                  onDelete={() => remove(t)}
                />
              ))}
            </ul>
          )}
        </div>
      </main>

      {editing && (
        <EditDialog
          todo={editing}
          projects={projects}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); loadTodos(); }}
        />
      )}
    </div>
  );
}

function TodoRow({
  t, project, onToggle, onSnooze, onEdit, onDelete,
}: {
  t: Todo;
  project?: Project;
  onToggle: () => void;
  onSnooze: (k: string) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const due = t.due_at ? new Date(t.due_at) : null;
  const overdue = due && t.status === "open" && due < new Date();
  return (
    <li className="flex items-start gap-2 py-1.5 px-2 border-b border-border/50 hover:bg-bg-card/50 group">
      <button
        onClick={onToggle}
        className={`mt-0.5 w-4 h-4 rounded-full border ${
          t.status === "done" ? "bg-success border-success" : "border-text-dim"
        }`}
      />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className={`text-xs ${PRIORITY_TONE[t.priority]}`}>P{t.priority}</span>
          <button onClick={onEdit} className="text-left text-text text-sm truncate">
            {t.title}
          </button>
          {t.rrule && <span className="text-[10px] text-text-dim">↻</span>}
          {t.source === "agent" && (
            <span className="text-[10px] text-info border border-info/40 rounded px-1">agent</span>
          )}
        </div>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {due && (
            <span className={overdue ? "text-error" : ""}>
              {formatDue(due)}
            </span>
          )}
          {project && (
            <span className="flex items-center gap-1">
              <span className="w-1.5 h-1.5 rounded-full" style={{ background: project.color }} />
              {project.name}
            </span>
          )}
          {t.tags.map((tag) => (
            <span key={tag}>@{tag}</span>
          ))}
        </div>
      </div>
      <div className="opacity-0 group-hover:opacity-100 flex items-center gap-1 text-xs">
        <button onClick={() => onSnooze("tomorrow")} className="text-text-muted hover:text-text px-1">tmrw</button>
        <button onClick={() => onSnooze("next_week")} className="text-text-muted hover:text-text px-1">+1w</button>
        <button onClick={onDelete} className="text-text-muted hover:text-error px-1">×</button>
      </div>
    </li>
  );
}

function EditDialog({
  todo, projects, onClose, onSaved,
}: {
  todo: Todo;
  projects: Project[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [title, setTitle] = useState(todo.title);
  const [notes, setNotes] = useState(todo.notes);
  const [priority, setPriority] = useState(todo.priority);
  const [dueAt, setDueAt] = useState(todo.due_at?.slice(0, 16) ?? "");
  const [projectRef, setProjectRef] = useState<number | "">(todo.project_ref ?? "");
  const [rrule, setRRule] = useState(todo.rrule ?? "");
  const [tags, setTags] = useState(todo.tags.join(" "));

  const save = async () => {
    const body: Record<string, unknown> = {
      title, notes, priority, rrule,
      tags: tags.split(/\s+/).filter(Boolean).map((s) => s.replace(/^@/, "")),
    };
    body.due_at = dueAt ? new Date(dueAt).toISOString() : "";
    body.project_id = projectRef === "" ? 0 : projectRef;
    await fetch(`${API}/todos/${todo.id}`, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
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
          <div className="text-text font-medium">Edit todo</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        <input
          value={title}
          onChange={(e) => setTitle(e.target.value)}
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
            Project
            <select
              value={projectRef}
              onChange={(e) =>
                setProjectRef(e.target.value === "" ? "" : parseInt(e.target.value))
              }
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value="">— inbox —</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>{p.name}</option>
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
            Save
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
