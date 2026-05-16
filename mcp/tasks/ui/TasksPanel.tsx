// TasksPanel — mission board for an agent.
//
// Layout: a list grouped by status (open / in_progress / blocked /
// done / cancelled). The agent creates tasks via MCP; the panel adds
// + edits them via REST. Live updates land via /api/app-events when
// the app emits — for now updates only happen via re-fetch (the app
// doesn't emit task.* events yet; v0.2 plumbs them through OnMount).
//
// The agent picker at the top lets the operator browse boards across
// agents. The agent's tasks_list / tasks_create tool calls are scoped
// by agent_id; the panel mirrors that. The platform still passes its
// running-agent handle in as `instanceId` (NativePanelProps contract
// shared by every app), and we use it as the initial agent id.

import { useCallback, useEffect, useState } from "react";

const API = "/api/apps/tasks";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Task {
  id: number;
  agent_id: number;
  title: string;
  notes: string;
  status: string;
  created_at: string;
  updated_at: string;
}

const STATUSES: { key: string; label: string; tone: string }[] = [
  { key: "open",        label: "Open",        tone: "text-text" },
  { key: "planning",    label: "Planning",    tone: "text-accent" },
  { key: "in_progress", label: "In progress", tone: "text-info" },
  { key: "blocked",     label: "Blocked",     tone: "text-warn" },
  { key: "done",        label: "Done",        tone: "text-success" },
  { key: "cancelled",   label: "Cancelled",   tone: "text-text-dim" },
];

export default function TasksPanel({ instanceId }: NativePanelProps) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [pickedAgent, setPickedAgent] = useState<number>(instanceId ?? 0);
  const [status, setStatus] = useState("");
  const [adding, setAdding] = useState(false);
  const [newTitle, setNewTitle] = useState("");
  const [newNotes, setNewNotes] = useState("");
  const [newAskForPlan, setNewAskForPlan] = useState(false);
  const [editing, setEditing] = useState<Task | null>(null);

  const load = useCallback(async () => {
    if (!pickedAgent) {
      setTasks([]);
      return;
    }
    try {
      const res = await fetch(
        `${API}/agents/${pickedAgent}?status=all`,
        { credentials: "same-origin" },
      );
      if (!res.ok) {
        setStatus(`Load: ${res.status}`);
        return;
      }
      const data = await res.json();
      setTasks(data || []);
      setStatus(`${(data || []).length} tasks`);
    } catch (e) {
      setStatus("Load: " + (e as Error).message);
    }
  }, [pickedAgent]);

  useEffect(() => { load(); }, [load]);

  const create = async () => {
    if (!newTitle.trim() || !pickedAgent) return;
    try {
      const res = await fetch(`${API}/agents/${pickedAgent}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          Title: newTitle,
          Notes: newNotes,
          status: newAskForPlan ? "planning" : "open",
        }),
      });
      if (!res.ok) {
        setStatus("Create: " + (await res.text()));
        return;
      }
      setNewTitle(""); setNewNotes(""); setNewAskForPlan(false); setAdding(false);
      load();
    } catch (e) {
      setStatus("Create: " + (e as Error).message);
    }
  };

  const updateStatus = async (id: number, newStatus: string) => {
    try {
      await fetch(`${API}/tasks/${id}`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: newStatus }),
      });
      load();
    } catch (e) {
      setStatus("Update: " + (e as Error).message);
    }
  };

  const remove = async (id: number) => {
    if (!confirm("Delete this task?")) return;
    try {
      await fetch(`${API}/tasks/${id}`, { method: "DELETE", credentials: "same-origin" });
      load();
    } catch (e) {
      setStatus("Delete: " + (e as Error).message);
    }
  };

  const grouped: Record<string, Task[]> = {};
  for (const s of STATUSES) grouped[s.key] = [];
  for (const t of tasks) (grouped[t.status] ?? grouped.open).push(t);

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="text-text font-medium">Mission board</div>
        <input
          type="number"
          placeholder="agent id"
          value={pickedAgent || ""}
          onChange={(e) => setPickedAgent(parseInt(e.target.value) || 0)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm w-32"
        />
        <button
          onClick={() => setAdding(true)}
          disabled={!pickedAgent}
          className="px-3 py-1 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
        >
          + Task
        </button>
        <span className="ml-auto text-text-dim text-xs">{status}</span>
      </header>

      <div className="flex-1 overflow-auto p-4">
        {!pickedAgent ? (
          <div className="py-12 text-center text-text-muted text-sm">
            Pick an agent ID to view its mission board.
          </div>
        ) : tasks.length === 0 ? (
          <div className="py-12 text-center text-text-muted text-sm">
            No tasks yet for agent {pickedAgent}. Add one or have the agent call <code>tasks_create</code>.
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-6 gap-3">
            {STATUSES.map((s) => (
              <div key={s.key} className="border border-border rounded p-2 flex flex-col">
                <div className={`text-xs uppercase font-medium mb-2 ${s.tone}`}>
                  {s.label} <span className="text-text-dim">({grouped[s.key].length})</span>
                </div>
                <div className="flex flex-col gap-2">
                  {grouped[s.key].map((t) => (
                    <TaskCard
                      key={t.id}
                      task={t}
                      onMove={(newStatus) => updateStatus(t.id, newStatus)}
                      onEdit={() => setEditing(t)}
                      onDelete={() => remove(t.id)}
                    />
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {adding && (
        <Dialog onClose={() => setAdding(false)} title="New task">
          <input
            type="text"
            placeholder="Title"
            value={newTitle}
            onChange={(e) => setNewTitle(e.target.value)}
            autoFocus
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          />
          <textarea
            placeholder="Notes (optional)"
            value={newNotes}
            onChange={(e) => setNewNotes(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-[80px]"
          />
          <label className="flex items-center gap-2 text-sm text-text-muted cursor-pointer">
            <input
              type="checkbox"
              checked={newAskForPlan}
              onChange={(e) => setNewAskForPlan(e.target.checked)}
            />
            Ask the agent for a plan first
          </label>
          <div className="flex gap-2 justify-end">
            <button onClick={() => setAdding(false)} className="px-3 py-1.5 text-sm text-text-muted">
              Cancel
            </button>
            <button
              onClick={create}
              disabled={!newTitle.trim()}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              Create
            </button>
          </div>
        </Dialog>
      )}

      {editing && (
        <Dialog onClose={() => setEditing(null)} title="Edit task">
          <EditForm
            task={editing}
            onSaved={() => { setEditing(null); load(); }}
            onCancel={() => setEditing(null)}
          />
        </Dialog>
      )}
    </div>
  );
}

function TaskCard({
  task, onMove, onEdit, onDelete,
}: {
  task: Task;
  onMove: (status: string) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="border border-border rounded p-2 hover:border-text-dim transition-colors group">
      <div className="flex items-start gap-2">
        <button onClick={onEdit} className="text-left flex-1 min-w-0">
          <div className="text-text text-sm truncate">{task.title}</div>
          {task.notes && (
            <div className={
              task.status === "planning"
                ? "text-text-dim text-xs mt-0.5 whitespace-pre-wrap"
                : "text-text-dim text-xs mt-0.5 line-clamp-2"
            }>
              {task.notes}
            </div>
          )}
        </button>
        <button
          onClick={onDelete}
          className="opacity-0 group-hover:opacity-100 text-text-muted hover:text-error text-xs"
          title="Delete"
        >
          ×
        </button>
      </div>
      <div className="flex flex-wrap gap-1 mt-2">
        {STATUSES.filter((s) => s.key !== task.status).map((s) => (
          <button
            key={s.key}
            onClick={() => onMove(s.key)}
            className="text-[10px] px-1.5 py-0.5 border border-border rounded text-text-dim hover:text-text hover:border-accent transition-colors"
          >
            → {s.label}
          </button>
        ))}
      </div>
    </div>
  );
}

function EditForm({
  task, onSaved, onCancel,
}: { task: Task; onSaved: () => void; onCancel: () => void }) {
  const [title, setTitle] = useState(task.title);
  const [notes, setNotes] = useState(task.notes || "");
  const [status, setStatus] = useState(task.status);

  const save = async () => {
    try {
      await fetch(`${API}/tasks/${task.id}`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title, notes, status }),
      });
      onSaved();
    } catch {}
  };

  return (
    <>
      <input
        type="text"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      />
      <textarea
        value={notes}
        onChange={(e) => setNotes(e.target.value)}
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-[80px]"
      />
      <select
        value={status}
        onChange={(e) => setStatus(e.target.value)}
        className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      >
        {STATUSES.map((s) => (
          <option key={s.key} value={s.key}>{s.label}</option>
        ))}
      </select>
      <div className="flex gap-2 justify-end">
        <button onClick={onCancel} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
        <button
          onClick={save}
          disabled={!title.trim()}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
        >
          Save
        </button>
      </div>
    </>
  );
}

function Dialog({ children, onClose, title }: {
  children: React.ReactNode; onClose: () => void; title: string;
}) {
  return (
    <div className="fixed inset-0 bg-black/60 grid place-items-center z-50" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded p-4 w-[480px] max-w-[90vw] flex flex-col gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">{title}</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        {children}
      </div>
    </div>
  );
}
