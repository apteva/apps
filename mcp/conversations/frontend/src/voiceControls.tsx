import { useEffect, useRef, useState } from "react";
import type { ConversationsClient } from "./client";
import { useConversationLocalization } from "./i18n";
import { VoiceAudioClient, type VoiceAudioState } from "./voiceAudio";
import { finalSpeechText, speechRecognitionConstructor, type BrowserSpeechRecognition } from "./voiceDictation";

export function VoiceControls({ client, chatId, disabled = false, onActiveChange, onTranscript }: { client: ConversationsClient; chatId: string; disabled?: boolean; onActiveChange?: (active: boolean) => void; onTranscript?: (text: string) => void }) {
  const { t, locale } = useConversationLocalization();
  const [state, setState] = useState<VoiceAudioState>("closed");
  const [mode, setMode] = useState<"live" | "dictation">("live");
  const [dictating, setDictating] = useState(false);
  const [busy, setBusy] = useState(false);
  const [muted, setMuted] = useState(false);
  const [error, setError] = useState("");
  const audio = useRef<VoiceAudioClient | null>(null);
  const recognition = useRef<BrowserSpeechRecognition | null>(null);
  const transcriptHandler = useRef(onTranscript);
  transcriptHandler.current = onTranscript;
  const active = useRef(false);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    void client.voiceStatus(chatId).then(session => {
      if (mounted.current) setMode(session.status === "active" ? "live" : session.mode ?? "live");
    }).catch(() => {});
    const heartbeat = window.setInterval(() => {
      if (!active.current) return;
      void client.voiceStatus(chatId).then(session => {
        if (session.status === "closed" && active.current) {
          active.current = false;
          audio.current?.close();
          audio.current = null;
          onActiveChange?.(false);
          if (mounted.current) setError(t("voice.endedRemote"));
        }
      }).catch(() => {});
    }, 30_000);
    return () => {
      window.clearInterval(heartbeat);
      mounted.current = false;
      audio.current?.close();
      audio.current = null;
      recognition.current?.abort();
      recognition.current = null;
      if (active.current) void client.endVoice(chatId).catch(() => {});
      active.current = false;
      onActiveChange?.(false);
    };
  }, [client, chatId]);

  const end = async () => {
    if (recognition.current) {
      recognition.current.stop();
      return;
    }
    setBusy(true);
    audio.current?.close();
    audio.current = null;
    try { if (active.current) await client.endVoice(chatId); }
    catch (reason) { if (mounted.current) setError(String(reason)); }
    finally { active.current = false; onActiveChange?.(false); if (mounted.current) { setState("closed"); setBusy(false); setMuted(false); } }
  };

  const startDictation = () => {
    const SpeechRecognition = speechRecognitionConstructor();
    if (!SpeechRecognition) { setError(t("voice.dictationUnsupported")); setState("closed"); onActiveChange?.(false); return; }
    const instance = new SpeechRecognition();
    recognition.current = instance;
    const committed = new Set<number>();
    instance.lang = locale;
    instance.continuous = true;
    instance.interimResults = true;
    instance.onresult = event => {
      const text = finalSpeechText(event, committed);
      if (text && mounted.current) transcriptHandler.current?.(text);
    };
    instance.onerror = event => { if (mounted.current) setError(event.error || t("voice.failed")); };
    instance.onend = () => {
      if (recognition.current !== instance) return;
      recognition.current = null;
      if (mounted.current) { setDictating(false); setState("closed"); onActiveChange?.(false); }
    };
    try { instance.start(); setDictating(true); setState("listening"); onActiveChange?.(true); }
    catch (reason) { recognition.current = null; setState("closed"); setError(String(reason)); onActiveChange?.(false); }
  };

  const start = async () => {
    if (busy || disabled || state !== "closed") return;
    setBusy(true); setError(""); setState("connecting");
    onActiveChange?.(true);
    let previous;
    try { previous = await client.voiceStatus(chatId); }
    catch (reason) { if (mounted.current) { setError(String(reason)); setState("closed"); setBusy(false); onActiveChange?.(false); } return; }
    if (!mounted.current) return;
    if (previous.status !== "active" && (previous.mode === "dictation" || mode === "dictation")) {
      setMode("dictation"); setBusy(false); onActiveChange?.(false); startDictation(); return;
    }
    setMode("live");
    const connection = new VoiceAudioClient(
      next => { if (mounted.current) setState(next); },
      () => {
        // The bridge token is one-use. One renewal is enough to recover a
        // transient network drop; a second failure closes the session.
        if (!active.current || !mounted.current) return;
        void client.renewVoice(chatId).then(async session => {
          if (!session.audio_bridge_url || !audio.current) throw new Error("Audio bridge unavailable.");
          await audio.current.connect(session.audio_bridge_url);
        }).catch(reason => { if (mounted.current) setError(String(reason)); void end(); });
      },
    );
    audio.current = connection;
    let spawnAttempted = false;
    try {
      await connection.prepare(client.voiceWorkletURL());
      if (!mounted.current || audio.current !== connection) { connection.close(); onActiveChange?.(false); return; }
      spawnAttempted = true;
      const session = previous.status === "active" ? await client.renewVoice(chatId) : await client.startVoice(chatId);
      if (!session.audio_bridge_url) throw new Error("Audio bridge unavailable.");
      active.current = true;
      if (!mounted.current) { await client.endVoice(chatId); connection.close(); return; }
      await connection.connect(session.audio_bridge_url);
    } catch (reason) {
      connection.close();
      if (active.current) { active.current = false; void client.endVoice(chatId).catch(() => {}); }
      onActiveChange?.(false);
      if (mounted.current) {
        setError(spawnAttempted ? `${t("voice.liveUnavailable")} ${String(reason)}` : String(reason));
        setState("closed");
        if (spawnAttempted && speechRecognitionConstructor()) setMode("dictation");
      }
    } finally { if (mounted.current) setBusy(false); }
  };

  return <div className="flex flex-wrap items-center gap-2">
    {state === "closed" ? <button type="button" onClick={() => void start()} disabled={busy || disabled}
      className="inline-flex h-9 w-9 items-center justify-center rounded-full text-text-muted hover:bg-bg-hover hover:text-text disabled:opacity-40"
      aria-label={t(mode === "dictation" ? "voice.dictate" : "voice.start")} title={t(mode === "dictation" ? "voice.dictate" : "voice.start")}>
      <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true"><path d="M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3ZM5 10v2a7 7 0 0 0 14 0v-2M12 19v3M8 22h8"/></svg>
    </button> : <>
      <span role="status" className="text-xs text-accent">{t(`voice.${state}` as "voice.connecting" | "voice.listening" | "voice.speaking")}</span>
      {!dictating && <button type="button" onClick={() => { const next = !muted; audio.current?.setMuted(next); setMuted(next); }}
        className="rounded border border-border px-2 py-1 text-xs text-text-muted">{t(muted ? "voice.unmute" : "voice.mute")}</button>}
      <button type="button" onClick={() => void end()} disabled={busy} className="rounded border border-border px-2 py-1 text-xs text-error disabled:opacity-40">{t(dictating ? "voice.stopDictation" : "voice.end")}</button>
      <span className="text-[10px] text-text-muted" title={t(dictating ? "voice.dictationReview" : "voice.transcriptWarning")}>{t(dictating ? "voice.dictationReview" : "voice.transcriptNote")}</span>
    </>}
    {error && <span role="alert" className="max-w-full break-words text-xs text-error">{t("voice.failed")}: {error}</span>}
  </div>;
}
