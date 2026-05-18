// StatusPanel — short-horizon status line for an agent.
//
// One row per agent in the status_status table (upsert semantics).
// The panel reads via GET /api/apps/status/agents/{id}, writes via
// POST to the same path, and clears via DELETE. The agent itself
// writes the same data via the status_set MCP tool — same store,
// two callers.
//
// The platform passes its running-agent handle in as `instanceId`
// (NativePanelProps contract shared by every app); the panel
// internally uses "agent" terminology for everything user-facing.

import { useCallback, useEffect, useState } from "react";

const API = "/api/apps/status";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface StatusRow {
  agent_id: number;
  message: string;
  emoji: string;
  tone: string;
  set_by_thread: string;
  updated_at: string;
}

const TONES = [
  { key: "info",    label: "Info",    cls: "text-info" },
  { key: "working", label: "Working", cls: "text-accent" },
  { key: "warn",    label: "Warn",    cls: "text-warn" },
  { key: "error",   label: "Error",   cls: "text-error" },
  { key: "success", label: "Success", cls: "text-success" },
  { key: "idle",    label: "Idle",    cls: "text-text-dim" },
];

function toneClass(tone: string): string {
  return TONES.find((t) => t.key === tone)?.cls ?? "text-text";
}

function relTime(iso: string): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (!t) return iso;
  const dt = Date.now() - t;
  const s = Math.max(0, Math.floor(dt / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export default function StatusPanel({ instanceId }: NativePanelProps) {
  const [pickedAgent, setPickedAgent] = useState<number>(instanceId ?? 0);
  const [row, setRow] = useState<StatusRow | null>(null);
  const [loading, setLoading] = useState(false);
  const [status, setStatus] = useState("");

  const [draftMessage, setDraftMessage] = useState("");
  const [draftEmoji, setDraftEmoji] = useState("");
  const [draftTone, setDraftTone] = useState("info");
  const [draftThread, setDraftThread] = useState("");

  const load = useCallback(async () => {
    if (!pickedAgent) {
      setRow(null);
      return;
    }
    setLoading(true);
    try {
      const res = await fetch(`${API}/agents/${pickedAgent}`, { credentials: "same-origin" });
      if (res.status === 204) {
        setRow(null);
        setStatus("no status set");
      } else if (!res.ok) {
        setStatus(`Load: ${res.status}`);
      } else {
        const data: StatusRow = await res.json();
        setRow(data);
        setDraftMessage(data.message ?? "");
        setDraftEmoji(data.emoji ?? "");
        setDraftTone(data.tone || "info");
        setDraftThread(data.set_by_thread ?? "");
        setStatus(`updated ${relTime(data.updated_at)}`);
      }
    } catch (e) {
      setStatus("Load: " + (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [pickedAgent]);

  useEffect(() => { load(); }, [load]);

  const save = async () => {
    if (!pickedAgent || !draftMessage.trim()) return;
    try {
      const res = await fetch(`${API}/agents/${pickedAgent}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          message: draftMessage,
          emoji: draftEmoji,
          tone: draftTone,
          thread_id: draftThread,
        }),
      });
      if (!res.ok) {
        setStatus("Save: " + (await res.text()));
        return;
      }
      load();
    } catch (e) {
      setStatus("Save: " + (e as Error).message);
    }
  };

  const clear = async () => {
    if (!pickedAgent) return;
    if (!confirm(`Clear status for agent ${pickedAgent}?`)) return;
    try {
      await fetch(`${API}/agents/${pickedAgent}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      setDraftMessage("");
      setDraftEmoji("");
      setDraftTone("info");
      setDraftThread("");
      load();
    } catch (e) {
      setStatus("Clear: " + (e as Error).message);
    }
  };

  const dirty =
    !!row &&
    (draftMessage !== row.message ||
      draftEmoji !== row.emoji ||
      draftTone !== row.tone ||
      draftThread !== (row.set_by_thread ?? ""));

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="text-text font-medium">Status line</div>
        <input
          type="number"
          placeholder="agent id"
          value={pickedAgent || ""}
          onChange={(e) => setPickedAgent(parseInt(e.target.value) || 0)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm w-32"
        />
        <button
          onClick={load}
          disabled={!pickedAgent || loading}
          className="px-3 py-1 text-sm border border-border rounded text-text-muted hover:text-text disabled:opacity-50"
        >
          Refresh
        </button>
        <span className="ml-auto text-text-dim text-xs">{status}</span>
      </header>

      <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
        {!pickedAgent ? (
          <div className="py-12 text-center text-text-muted text-sm">
            Pick an agent ID to view its status line.
          </div>
        ) : (
          <>
            <section className="border border-border rounded p-4 flex flex-col gap-2">
              <div className="text-text-dim text-xs uppercase">Now</div>
              {row ? (
                <>
                  <div className="flex items-baseline gap-3">
                    {row.emoji && (
                      <span style={{ fontSize: "28px", lineHeight: 1 }}>{row.emoji}</span>
                    )}
                    <div
                      className={`${toneClass(row.tone)} font-medium`}
                      style={{ fontSize: "18px" }}
                    >
                      {row.message}
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-3 text-text-dim text-xs">
                    <span className={toneClass(row.tone)}>{row.tone}</span>
                    <span>updated {relTime(row.updated_at)}</span>
                    {row.set_by_thread && <span>thread {row.set_by_thread}</span>}
                  </div>
                </>
              ) : (
                <div className="text-text-muted text-sm py-2">
                  No status set for agent {pickedAgent}. The agent can publish one
                  via <code>status_set</code>, or you can write the first line below.
                </div>
              )}
            </section>

            <section className="border border-border rounded p-4 flex flex-col gap-3">
              <div className="text-text-dim text-xs uppercase">
                {row ? "Edit" : "Set"}
              </div>
              <textarea
                placeholder="Message"
                value={draftMessage}
                onChange={(e) => setDraftMessage(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                style={{ minHeight: "80px" }}
              />
              <div className="flex gap-2 flex-wrap items-center">
                <input
                  type="text"
                  placeholder="Emoji"
                  value={draftEmoji}
                  onChange={(e) => setDraftEmoji(e.target.value)}
                  className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-24"
                />
                <select
                  value={draftTone}
                  onChange={(e) => setDraftTone(e.target.value)}
                  className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                >
                  {TONES.map((t) => (
                    <option key={t.key} value={t.key}>
                      {t.label}
                    </option>
                  ))}
                </select>
                <input
                  type="text"
                  placeholder="thread_id (optional)"
                  value={draftThread}
                  onChange={(e) => setDraftThread(e.target.value)}
                  className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                  style={{ minWidth: "180px" }}
                />
              </div>
              <div className="flex items-center gap-2 justify-end">
                {dirty && <span className="text-text-dim text-xs mr-auto">unsaved</span>}
                <button
                  onClick={clear}
                  disabled={!row}
                  className="px-3 py-1.5 text-sm text-text-muted disabled:opacity-50"
                >
                  Clear
                </button>
                <button
                  onClick={save}
                  disabled={!draftMessage.trim()}
                  className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
                >
                  Save
                </button>
              </div>
            </section>
          </>
        )}
      </div>
    </div>
  );
}
