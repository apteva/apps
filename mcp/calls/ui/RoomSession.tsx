import { useEffect, useRef, useState } from "react";
import { CallsSession, type CallsSnapshot } from "../frontend/client";

const controlClass = "px-3 py-1.5 text-xs border border-border rounded disabled:opacity-40";

function Video({ stream, label, muted = false }: { stream: MediaStream; label: string; muted?: boolean }) {
  const video = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    if (video.current) video.current.srcObject = stream;
  }, [stream]);
  const hasVideo = stream.getVideoTracks().length > 0;
  return (
    <div className="rounded border border-border overflow-hidden bg-bg-input">
      <video ref={video} autoPlay playsInline muted={muted} className={hasVideo ? "w-full aspect-video object-contain bg-black" : "sr-only"} />
      <div className="px-2 py-1 text-xs text-text-muted">{label}</div>
    </div>
  );
}

export function RoomSession({ projectId, roomId, onEnd }: { projectId: string; roomId: number; onEnd: () => void }) {
  const session = useRef<CallsSession | null>(null);
  const [snapshot, setSnapshot] = useState<CallsSnapshot>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState("");

  useEffect(() => {
    const controller = new CallsSession({ apiBaseURL: "/api/apps/calls", projectId });
    session.current = controller;
    setSnapshot(controller.getSnapshot());
    const unsubscribe = controller.subscribe(setSnapshot);
    return () => {
      unsubscribe();
      controller.dispose();
      session.current = null;
    };
  }, [projectId, roomId]);

  async function action(run: (controller: CallsSession) => Promise<void>) {
    const controller = session.current;
    if (!controller) return;
    setError("");
    try { await run(controller); }
    catch (err) {
      if (session.current === controller) setError((err as Error).message);
    }
  }

  async function join() {
    setBusy(true);
    try {
      await action(async controller => {
        const params = new URLSearchParams({ project_id: projectId });
        const response = await fetch("/api/apps/calls/admin/rooms/" + roomId + "/join-tokens?" + params, {
          method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            role: "host", display_name: "Host",
            expires_at: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
          }),
        });
        const result = await response.json();
        if (!response.ok) throw new Error(result.error || "Could not create a host invite");
        if (session.current === controller) await controller.join(result.token, "Host");
      });
    } finally { setBusy(false); }
  }

  const joined = snapshot?.phase === "joined";
  const preview = snapshot?.screenStream ?? snapshot?.localStream;
  return (
    <section aria-label="Live call" className="space-y-3 rounded border border-border p-3">
      <div className="flex items-center flex-wrap gap-2">
        <h3 className="text-sm font-semibold">Live call</h3>
        {joined ? (
          <>
            <span className="text-xs text-text-dim">{snapshot.participants.length} in room</span>
            <button type="button" className={controlClass} disabled={!snapshot.capabilities.audio} aria-pressed={snapshot.microphone}
              onClick={() => void action(controller => controller.setMicrophone(!snapshot.microphone))}>
              {snapshot.microphone ? "Mute mic" : "Unmute mic"}
            </button>
            <button type="button" className={controlClass} disabled={!snapshot.capabilities.video} aria-pressed={snapshot.camera}
              onClick={() => void action(controller => controller.setCamera(!snapshot.camera))}>
              {snapshot.camera ? "Turn camera off" : "Turn camera on"}
            </button>
            <button type="button" className={controlClass} disabled={!snapshot.capabilities.screen} aria-pressed={!!snapshot.screenStream}
              onClick={() => void action(controller => controller.setScreenShare(!snapshot.screenStream))}>
              {snapshot.screenStream ? "Stop sharing" : "Share screen"}
            </button>
            <button type="button" className={controlClass} onClick={() => void action(controller => controller.leave())}>Leave</button>
            {snapshot.capabilities.room_control && <button type="button" className={controlClass} onClick={() => {
              if (window.confirm("End this room for everyone?")) void action(async controller => { await controller.end(); onEnd(); });
            }}>End for everyone</button>}
          </>
        ) : (
          <button type="button" className={controlClass} disabled={!snapshot || busy || snapshot.phase === "ended"} onClick={() => void join()}>
            {snapshot?.phase === "ended" ? "Session closed" : busy ? "Joining…" : "Join as host"}
          </button>
        )}
      </div>
      {(error || snapshot?.error) && <p role="alert" className="text-xs text-error">{error || snapshot?.error}</p>}
      {joined && (
        <>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            {preview && <Video stream={preview} label={snapshot.screenStream ? "Your screen" : "You"} muted />}
            {snapshot.remoteStreams.map(({ participant, stream }) =>
              <Video key={participant.id + ":" + stream.id} stream={stream} label={participant.display_name || "Participant " + participant.id} />)}
          </div>
          {snapshot.capabilities.chat && (
            <>
              {snapshot.messages.length > 0 && <ul aria-live="polite" className="max-h-32 overflow-y-auto space-y-1 text-xs">
                {snapshot.messages.map(message => <li key={message.id} className="whitespace-pre-wrap">
                  <strong>{snapshot.participants.find(person => person.id === message.participant_id)?.display_name || "Participant"}</strong>{" "}{message.body}
                </li>)}
              </ul>}
              <form className="flex gap-2" onSubmit={event => {
                event.preventDefault();
                const body = draft.trim();
                if (body) void action(async controller => {
                  await controller.sendMessage(body);
                  setDraft(value => value.trim() === body ? "" : value);
                });
              }}>
                <input value={draft} onChange={event => setDraft(event.target.value)} aria-label="Message the room"
                  placeholder="Message the room" className="flex-1 min-w-0 px-2 py-1 text-xs border border-border rounded bg-bg-input" />
                <button className={controlClass} disabled={!draft.trim()}>Send</button>
              </form>
            </>
          )}
        </>
      )}
    </section>
  );
}
