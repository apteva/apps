import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface VoiceNote {
  id: number;
  title: string;
  status: string;
  storage_file_id?: string;
  playback_url?: string;
  file_name?: string;
  content_type?: string;
  duration_ms?: number;
  transcript_status: string;
  transcript_text?: string;
  transcript_language?: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
}

const API = "/api/apps/voice-notes";

export default function VoiceNotesPanel({ projectId }: NativePanelProps) {
  const [notes, setNotes] = useState<VoiceNote[]>([]);
  const [selectedId, setSelectedId] = useState(0);
  const [status, setStatus] = useState("");
  const [query, setQuery] = useState("");
  const [recording, setRecording] = useState(false);
  const [elapsed, setElapsed] = useState(0);
  const [title, setTitle] = useState("");
  const [saving, setSaving] = useState(false);
  const recorderRef = useRef<MediaRecorder | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  const startedRef = useRef<number>(0);
  const timerRef = useRef<number | null>(null);

  const withProject = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const load = useCallback(async () => {
    try {
      const params = query ? `?q=${encodeURIComponent(query)}` : "";
      const path = params ? `/notes${params}` : "/notes";
      const res = await fetch(withProject(path), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const list = data.notes ?? [];
      setNotes(list);
      setSelectedId((current) => current && list.some((n: VoiceNote) => n.id === current) ? current : list[0]?.id ?? 0);
      setStatus(`${list.length} note${list.length === 1 ? "" : "s"}`);
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [query, withProject]);

  useEffect(() => { load(); }, [load]);

  const selected = useMemo(
    () => notes.find((n) => n.id === selectedId) ?? notes[0] ?? null,
    [notes, selectedId],
  );

  const startRecording = async () => {
    setStatus("");
    if (!navigator.mediaDevices?.getUserMedia) {
      setStatus("Browser microphone recording is unavailable.");
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      const mime = pickMimeType();
      const recorder = new MediaRecorder(stream, mime ? { mimeType: mime } : undefined);
      chunksRef.current = [];
      recorder.ondataavailable = (event) => {
        if (event.data.size > 0) chunksRef.current.push(event.data);
      };
      recorder.onstop = () => {
        stream.getTracks().forEach((track) => track.stop());
      };
      recorderRef.current = recorder;
      startedRef.current = Date.now();
      setElapsed(0);
      timerRef.current = window.setInterval(() => {
        setElapsed(Date.now() - startedRef.current);
      }, 250);
      recorder.start();
      setRecording(true);
    } catch (e) {
      setStatus((e as Error).message);
    }
  };

  const stopRecording = async () => {
    const recorder = recorderRef.current;
    if (!recorder || recorder.state === "inactive") return;
    setSaving(true);
    const stopped = new Promise<void>((resolve) => {
      const prev = recorder.onstop;
      recorder.onstop = (event) => {
        if (typeof prev === "function") prev.call(recorder, event);
        resolve();
      };
    });
    recorder.stop();
    if (timerRef.current) window.clearInterval(timerRef.current);
    setRecording(false);
    await stopped;
    try {
      const type = recorder.mimeType || "audio/webm";
      const blob = new Blob(chunksRef.current, { type });
      const content = await blobToBase64(blob);
      const ext = type.includes("mp4") ? "m4a" : type.includes("ogg") ? "ogg" : "webm";
      const name = `voice-note-${new Date().toISOString().replace(/[:.]/g, "-")}.${ext}`;
      const res = await fetch(withProject("/notes"), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title,
          name,
          content_type: type,
          content_base64: content,
          duration_ms: Math.max(elapsed, Date.now() - startedRef.current),
          transcribe: true,
        }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setTitle("");
      setSelectedId(data.note?.id ?? 0);
      await load();
      setStatus(data.note?.transcript_status === "failed" ? "Saved. Transcription unavailable or failed." : "Saved.");
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setSaving(false);
      recorderRef.current = null;
      chunksRef.current = [];
    }
  };

  const transcribe = async (note: VoiceNote) => {
    setSaving(true);
    try {
      const res = await fetch(withProject(`/notes/${note.id}/transcribe?force=1`), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      await load();
      setStatus("Transcription updated.");
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">Voice Notes</h1>
          <p className="text-xs text-text-muted">Record, store, and optionally transcribe audio notes.</p>
        </div>
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search notes"
          className="ml-auto w-56 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
        <button type="button" onClick={load} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
          Refresh
        </button>
      </header>

      <main className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[360px_minmax(0,1fr)]">
        <aside className="border-r border-border min-h-0 flex flex-col">
          <section className="p-4 border-b border-border space-y-3">
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Optional title"
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              disabled={recording || saving}
            />
            <div className="rounded border border-border bg-bg-input/30 p-4 text-center">
              <div className="text-2xl font-mono tabular-nums">{formatDuration(elapsed)}</div>
              <div className="mt-3 flex justify-center gap-2">
                {!recording ? (
                  <button
                    type="button"
                    onClick={startRecording}
                    disabled={saving}
                    className="px-4 py-2 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50"
                  >
                    Record
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={stopRecording}
                    className="px-4 py-2 text-sm bg-red text-bg rounded font-medium"
                  >
                    Stop & Save
                  </button>
                )}
              </div>
              {saving && <div className="text-xs text-text-muted mt-2">Saving...</div>}
            </div>
            {status && <div className="text-xs text-text-muted">{status}</div>}
          </section>

          <div className="flex-1 overflow-y-auto">
            {notes.length === 0 ? (
              <div className="p-4 text-sm text-text-muted">No voice notes yet.</div>
            ) : (
              <ul>
                {notes.map((note) => (
                  <li key={note.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(note.id)}
                      className={`w-full text-left px-4 py-3 border-b border-border hover:bg-bg-input/40 ${selected?.id === note.id ? "bg-bg-input/60" : ""}`}
                    >
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium truncate">{note.title || "Untitled"}</span>
                        <StatusPill value={note.transcript_status || note.status} />
                      </div>
                      <div className="text-[11px] text-text-dim mt-1">
                        {formatDate(note.created_at)} {note.duration_ms ? `· ${formatDuration(note.duration_ms)}` : ""}
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </aside>

        <section className="min-h-0 overflow-y-auto">
          {!selected ? (
            <div className="p-8 text-sm text-text-muted">Select or record a note.</div>
          ) : (
            <div className="p-6 max-w-4xl space-y-5">
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <h2 className="text-xl font-semibold truncate">{selected.title || "Untitled"}</h2>
                  <div className="text-xs text-text-muted mt-1">
                    {formatDate(selected.created_at)} · {selected.file_name || "manual note"}
                  </div>
                </div>
                <StatusPill value={selected.transcript_status || selected.status} />
              </div>

              {selected.playback_url && (
                <div className="border border-border rounded p-4 bg-bg-input/30">
                  <audio src={sameOriginStorageURL(selected.playback_url)} controls preload="metadata" className="w-full" />
                </div>
              )}

              {selected.error_message && (
                <div className="border border-red/40 bg-red/10 text-red rounded p-3 text-sm">
                  {selected.error_message}
                </div>
              )}

              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => transcribe(selected)}
                  disabled={saving || !selected.storage_file_id}
                  className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
                >
                  Transcribe
                </button>
              </div>

              <section>
                <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Transcript</h3>
                {selected.transcript_text ? (
                  <div className="whitespace-pre-wrap text-sm leading-6 border border-border rounded p-4 bg-bg-card">
                    {selected.transcript_text}
                  </div>
                ) : (
                  <div className="border border-border rounded p-4 text-sm text-text-muted">
                    No transcript yet.
                  </div>
                )}
              </section>
            </div>
          )}
        </section>
      </main>
    </div>
  );
}

function pickMimeType(): string {
  const candidates = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4", "audio/ogg;codecs=opus"];
  return candidates.find((type) => MediaRecorder.isTypeSupported(type)) || "";
}

async function blobToBase64(blob: Blob): Promise<string> {
  const buffer = await blob.arrayBuffer();
  let binary = "";
  const bytes = new Uint8Array(buffer);
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

function formatDuration(ms?: number): string {
  const total = Math.max(0, Math.floor((ms || 0) / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

function formatDate(value: string): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value || "";
  return d.toLocaleString();
}

function sameOriginStorageURL(value: string): string {
  if (!value) return "";
  try {
    const url = new URL(value, window.location.origin);
    if (url.pathname.startsWith("/api/apps/storage/")) {
      return `${url.pathname}${url.search}${url.hash}`;
    }
  } catch {
    return value;
  }
  return value;
}

function StatusPill({ value }: { value: string }) {
  const cls =
    value === "ok" || value === "ready"
      ? "bg-green/15 text-green"
      : value === "running" || value === "transcribing"
        ? "bg-accent/15 text-accent"
        : value === "failed"
          ? "bg-red/15 text-red"
          : "bg-border text-text-muted";
  return <span className={`text-[10px] px-1.5 py-0.5 rounded shrink-0 ${cls}`}>{value || "none"}</span>;
}
