import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/calls";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Room {
  id: number;
  slug: string;
  title: string;
  status: string;
  created_at: string;
}

interface Participant {
  id: number;
  kind: "human" | "agent" | "service";
  role: "host" | "guest" | "observer";
  display_name?: string;
  status: string;
}

interface Message {
  id: number;
  participant_id?: number;
  kind: string;
  visibility: string;
  body: string;
}

interface TranscriptItem {
  id: number;
  participant_id?: number;
  speaker_name?: string;
  text: string;
}

type TokenForm = {
  participant_kind: "human" | "agent" | "service";
  role: "host" | "guest" | "observer";
  display_name: string;
  max_uses: number;
  chat: boolean;
  audio: boolean;
  video: boolean;
  screen: boolean;
  transcript_read: boolean;
};

export default function CallsPanel({ projectId }: NativePanelProps) {
  const [rooms, setRooms] = useState<Room[]>([]);
  const [selectedId, setSelectedId] = useState(0);
  const [participants, setParticipants] = useState<Participant[]>([]);
  const [messages, setMessages] = useState<Message[]>([]);
  const [transcript, setTranscript] = useState<TranscriptItem[]>([]);
  const [newTitle, setNewTitle] = useState("");
  const [status, setStatus] = useState("");
  const [joinURL, setJoinURL] = useState("");
  const [tokenForm, setTokenForm] = useState<TokenForm>({
    participant_kind: "human",
    role: "guest",
    display_name: "",
    max_uses: 1,
    chat: true,
    audio: true,
    video: true,
    screen: true,
    transcript_read: false,
  });

  const withProject = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const selected = useMemo(
    () => rooms.find((r) => r.id === selectedId) ?? rooms[0] ?? null,
    [rooms, selectedId],
  );

  const loadRooms = useCallback(async () => {
    try {
      const res = await fetch(withProject("/admin/rooms"), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const list = data.rooms ?? [];
      setRooms(list);
      if (!selectedId && list.length) setSelectedId(list[0].id);
      setStatus(`${list.length} rooms`);
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [selectedId, withProject]);

  const loadRoomDetail = useCallback(async () => {
    if (!selected) {
      setParticipants([]);
      setMessages([]);
      setTranscript([]);
      return;
    }
    try {
      const [roomRes, messageRes, transcriptRes] = await Promise.all([
        fetch(withProject(`/admin/rooms/${selected.id}`), { credentials: "same-origin" }),
        fetch(withProject(`/admin/rooms/${selected.id}/messages`), { credentials: "same-origin" }),
        fetch(withProject(`/admin/rooms/${selected.id}/transcript`), { credentials: "same-origin" }),
      ]);
      if (!roomRes.ok) throw new Error(await roomRes.text());
      const roomData = await roomRes.json();
      const messageData = messageRes.ok ? await messageRes.json() : { messages: [] };
      const transcriptData = transcriptRes.ok ? await transcriptRes.json() : { transcript_items: [] };
      setParticipants(roomData.participants ?? []);
      setMessages(messageData.messages ?? []);
      setTranscript(transcriptData.transcript_items ?? []);
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [selected, withProject]);

  useEffect(() => { loadRooms(); }, [loadRooms]);
  useEffect(() => { loadRoomDetail(); }, [loadRoomDetail]);

  const createRoom = async () => {
    if (!newTitle.trim()) return;
    const res = await fetch(withProject("/admin/rooms"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: newTitle.trim() }),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    const data = await res.json();
    setNewTitle("");
    setJoinURL(data.host_join_url ?? "");
    setSelectedId(data.room?.id ?? 0);
    await loadRooms();
  };

  const endRoom = async () => {
    if (!selected) return;
    const res = await fetch(withProject(`/admin/rooms/${selected.id}/end`), {
      method: "POST",
      credentials: "same-origin",
    });
    if (!res.ok) setStatus(await res.text());
    await loadRooms();
    await loadRoomDetail();
  };

  const mintToken = async () => {
    if (!selected) return;
    const res = await fetch(withProject(`/admin/rooms/${selected.id}/join-tokens`), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        participant_kind: tokenForm.participant_kind,
        role: tokenForm.role,
        display_name: tokenForm.display_name,
        max_uses: tokenForm.max_uses,
        capabilities: {
          chat: tokenForm.chat,
          audio: tokenForm.audio,
          video: tokenForm.video,
          screen: tokenForm.screen,
          transcript_read: tokenForm.transcript_read,
        },
      }),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    const data = await res.json();
    setJoinURL(data.join_url ?? "");
  };

  const removeParticipant = async (p: Participant) => {
    if (!selected) return;
    const res = await fetch(withProject(`/admin/rooms/${selected.id}/participants/${p.id}/remove`), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason: "removed from panel" }),
    });
    if (!res.ok) setStatus(await res.text());
    await loadRoomDetail();
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="px-4 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-sm font-semibold">Calls</h1>
        <input
          value={newTitle}
          onChange={(e) => setNewTitle(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") createRoom(); }}
          placeholder="New room title"
          className="w-56 bg-bg-input border border-border rounded px-2 py-1 text-sm"
        />
        <button
          type="button"
          onClick={createRoom}
          disabled={!newTitle.trim()}
          className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-50"
        >
          Create
        </button>
        <button
          type="button"
          onClick={() => { loadRooms(); loadRoomDetail(); }}
          className="px-2 py-1.5 text-xs border border-border rounded text-text-muted hover:text-text"
        >
          Refresh
        </button>
        <span className="ml-auto text-xs text-text-dim">{status}</span>
      </header>

      <div className="flex-1 min-h-0 flex">
        <aside className="w-72 border-r border-border overflow-y-auto">
          {rooms.length === 0 ? (
            <div className="p-4 text-xs text-text-muted">No rooms yet.</div>
          ) : (
            <ul>
              {rooms.map((room) => (
                <li key={room.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedId(room.id)}
                    className={`w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input/40 ${selected?.id === room.id ? "bg-bg-input/60" : ""}`}
                  >
                    <div className="flex items-center gap-2">
                      <StatusDot status={room.status} />
                      <span className="text-sm truncate">{room.title}</span>
                    </div>
                    <div className="text-[11px] text-text-dim mt-0.5 truncate">{room.slug}</div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <main className="flex-1 min-w-0 overflow-y-auto">
          {!selected ? (
            <div className="p-8 text-sm text-text-muted">Create a room to begin.</div>
          ) : (
            <div className="p-4 space-y-4">
              <section className="flex items-start gap-4 border-b border-border pb-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h2 className="text-lg font-semibold truncate">{selected.title}</h2>
                    <StatusPill status={selected.status} />
                  </div>
                  <div className="text-xs text-text-dim mt-1">Room #{selected.id} · {selected.slug}</div>
                </div>
                <button
                  type="button"
                  onClick={endRoom}
                  disabled={selected.status === "ended"}
                  className="ml-auto px-3 py-1.5 text-xs border border-border rounded text-text-muted hover:text-text disabled:opacity-40"
                >
                  End
                </button>
              </section>

              <section className="grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_360px] gap-4">
                <div className="space-y-4">
                  <PanelSection title="Participants">
                    {participants.length === 0 ? (
                      <Empty text="No active participants yet." />
                    ) : (
                      <div className="overflow-x-auto">
                        <table className="w-full text-sm">
                          <thead className="text-xs text-text-dim">
                            <tr className="border-b border-border">
                              <th className="text-left py-2 font-medium">Name</th>
                              <th className="text-left py-2 font-medium">Kind</th>
                              <th className="text-left py-2 font-medium">Role</th>
                              <th className="text-left py-2 font-medium">Status</th>
                              <th className="w-16"></th>
                            </tr>
                          </thead>
                          <tbody>
                            {participants.map((p) => (
                              <tr key={p.id} className="border-b border-border/70">
                                <td className="py-2">{p.display_name || `Participant ${p.id}`}</td>
                                <td className="py-2 text-text-muted">{p.kind}</td>
                                <td className="py-2 text-text-muted">{p.role}</td>
                                <td className="py-2"><StatusPill status={p.status} /></td>
                                <td className="py-2 text-right">
                                  <button
                                    type="button"
                                    onClick={() => removeParticipant(p)}
                                    disabled={p.status === "removed" || p.status === "left"}
                                    className="text-xs text-text-dim hover:text-error disabled:opacity-30"
                                  >
                                    Remove
                                  </button>
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )}
                  </PanelSection>

                  <PanelSection title="Messages">
                    {messages.length === 0 ? (
                      <Empty text="No room messages yet." />
                    ) : (
                      <div className="space-y-2">
                        {messages.map((m) => (
                          <div key={m.id} className="border border-border rounded p-2">
                            <div className="text-[11px] text-text-dim mb-1">
                              #{m.participant_id ?? "-"} · {m.kind} · {m.visibility}
                            </div>
                            <div className="text-sm whitespace-pre-wrap">{m.body}</div>
                          </div>
                        ))}
                      </div>
                    )}
                  </PanelSection>

                  <PanelSection title="Transcript">
                    {transcript.length === 0 ? (
                      <Empty text="No transcript items yet." />
                    ) : (
                      <div className="space-y-2">
                        {transcript.map((item) => (
                          <div key={item.id} className="border-l-2 border-accent pl-3 py-1">
                            <div className="text-[11px] text-text-dim">{item.speaker_name || `Participant ${item.participant_id}`}</div>
                            <div className="text-sm">{item.text}</div>
                          </div>
                        ))}
                      </div>
                    )}
                  </PanelSection>
                </div>

                <PanelSection title="Join Token">
                  <div className="space-y-3">
                    <div className="grid grid-cols-2 gap-2">
                      <SelectField
                        label="Kind"
                        value={tokenForm.participant_kind}
                        onChange={(value) => setTokenForm((f) => ({ ...f, participant_kind: value as TokenForm["participant_kind"] }))}
                        options={["human", "agent", "service"]}
                      />
                      <SelectField
                        label="Role"
                        value={tokenForm.role}
                        onChange={(value) => setTokenForm((f) => ({ ...f, role: value as TokenForm["role"] }))}
                        options={["guest", "host", "observer"]}
                      />
                    </div>
                    <label className="block text-xs text-text-muted">
                      Display name
                      <input
                        value={tokenForm.display_name}
                        onChange={(e) => setTokenForm((f) => ({ ...f, display_name: e.target.value }))}
                        className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm text-text"
                      />
                    </label>
                    <label className="block text-xs text-text-muted">
                      Max uses
                      <input
                        type="number"
                        min={1}
                        value={tokenForm.max_uses}
                        onChange={(e) => setTokenForm((f) => ({ ...f, max_uses: Math.max(1, parseInt(e.target.value) || 1) }))}
                        className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm text-text"
                      />
                    </label>
                    <div className="grid grid-cols-2 gap-2 text-xs text-text-muted">
                      {(["chat", "audio", "video", "screen", "transcript_read"] as const).map((key) => (
                        <label key={key} className="flex items-center gap-2">
                          <input
                            type="checkbox"
                            checked={Boolean(tokenForm[key])}
                            onChange={(e) => setTokenForm((f) => ({ ...f, [key]: e.target.checked }))}
                          />
                          {key}
                        </label>
                      ))}
                    </div>
                    <button
                      type="button"
                      onClick={mintToken}
                      className="w-full px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium"
                    >
                      Mint token
                    </button>
                    {joinURL && (
                      <div className="space-y-2">
                        <div className="text-xs text-text-dim break-all border border-border rounded p-2 bg-bg-input/40">{joinURL}</div>
                        <button
                          type="button"
                          onClick={() => navigator.clipboard?.writeText(joinURL)}
                          className="w-full px-3 py-1.5 text-xs border border-border rounded text-text-muted hover:text-text"
                        >
                          Copy link
                        </button>
                      </div>
                    )}
                  </div>
                </PanelSection>
              </section>
            </div>
          )}
        </main>
      </div>
    </div>
  );
}

function PanelSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h3 className="text-xs uppercase text-text-dim font-medium mb-2">{title}</h3>
      {children}
    </section>
  );
}

function Empty({ text }: { text: string }) {
  return <div className="border border-border rounded p-3 text-sm text-text-muted">{text}</div>;
}

function SelectField({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: string[];
  onChange: (value: string) => void;
}) {
  return (
    <label className="text-xs text-text-muted">
      {label}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm text-text"
      >
        {options.map((option) => <option key={option} value={option}>{option}</option>)}
      </select>
    </label>
  );
}

function StatusDot({ status }: { status: string }) {
  const cls = status === "open" || status === "active" ? "bg-success" : status === "ended" ? "bg-text-dim" : "bg-warn";
  return <span className={`w-2 h-2 rounded-full ${cls}`} />;
}

function StatusPill({ status }: { status: string }) {
  const cls =
    status === "open" || status === "active"
      ? "border-success text-success"
      : status === "ended" || status === "left" || status === "removed"
        ? "border-border text-text-dim"
        : "border-warn text-warn";
  return <span className={`inline-flex px-1.5 py-0.5 rounded border text-[11px] ${cls}`}>{status}</span>;
}
