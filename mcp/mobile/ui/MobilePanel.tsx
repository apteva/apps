import { useCallback, useEffect, useRef, useState, type PointerEvent } from "react";
import { StatusDot } from "@apteva/ui-kit";

interface Props { projectId: string; installId?: number; appName?: string }
interface Device { id: string; name: string; platform: string; physical: boolean; driver: string; state: string; hint?: string; capabilities: string[] }
interface Session { id: string; device_id: string; device: Device; status: string; mode: string; expires_at: number; error?: string }
interface Result { index: number; action: string; label?: string; status: string; error?: string; artifact_id?: string }
interface Run { id: string; name: string; device_id: string; build_ref?: string; status: string; failure_kind?: string; error?: string; created_at: number; results: Result[]; recording_id?: string }
interface Marker { name: string; at_ms: number }
interface Check { name: string; ok: boolean; detail: string }
interface Upload { status: string; storage_file_id?: number; error?: string }
interface Recording { id: string; kind: "capture" | "export"; parent_id?: string; session_id?: string; run_id?: string; device_id?: string; platform?: string; method?: string; preset?: string; audio: boolean; status: string; file_name?: string; size_bytes: number; duration_ms: number; width: number; height: number; fps: number; video_codec?: string; audio_codec?: string; markers: Marker[]; checks: Check[]; valid?: boolean; error?: string; created_at: number; started_at?: number; file_url?: string; upload?: Upload }
interface Preset { name: string; description?: string; sizes?: Record<string, { w: number; h: number }>; default_variant?: string; min_seconds?: number; max_seconds?: number; orientations?: string[] }
interface IOSSetup { state: string; message: string; team_id?: string; teams: {id: string; name: string; personal: boolean}[] }

const API = "/api/apps/mobile/api";
const sample = JSON.stringify([
  { action: "navigate", label: "Open website", params: { url: "https://example.com" } },
  { action: "wait_for_text", label: "Check page loaded", params: { text: "Example Domain", timeout_ms: 10000 } },
  { action: "screenshot", label: "Capture result" },
], null, 2);

export default function MobilePanel({ projectId }: Props) {
  const [tab, setTab] = useState<"devices" | "tests" | "recordings" | "setup">("devices");
  const [devices, setDevices] = useState<Device[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [presets, setPresets] = useState<Preset[]>([]);
  const [selectedRecording, setSelectedRecording] = useState("");
  const [quality, setQuality] = useState("auto");
  const [recordAudio, setRecordAudio] = useState(true);
  const [markName, setMarkName] = useState("");
  const [installSource, setInstallSource] = useState("artifact");
  const [appName, setAppName] = useState("");
  const [storeID, setStoreID] = useState("");
  const [recordRun, setRecordRun] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [diagnostics, setDiagnostics] = useState<string[]>([]);
  const [selected, setSelected] = useState("");
  const [sessionID, setSessionID] = useState("");
  const [mode, setMode] = useState("screen");
  const [image, setImage] = useState("");
  const [live, setLive] = useState(false);
  const [source, setSource] = useState("");
  const [url, setURL] = useState("");
  const [text, setText] = useState("");
  const [appID, setAppID] = useState("");
  const [artifact, setArtifact] = useState("");
  const [testName, setTestName] = useState("Website smoke test");
  const [buildRef, setBuildRef] = useState("");
  const [plan, setPlan] = useState(sample);
  const [selectedRun, setSelectedRun] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [iosSetup, setIOSSetup] = useState<IOSSetup | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [pendingConnection,setPendingConnection] = useState(false);
  const connection = useRef<AbortController | null>(null);
  const pointer = useRef<{ x: number; y: number } | null>(null);

  const call = useCallback(async (tool: string, args: Record<string, unknown> = {}, signal?: AbortSignal) => {
    const response = await fetch(`${API}/call?project_id=${encodeURIComponent(projectId)}`, {
      method: "POST", credentials: "same-origin", signal,
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ tool, args }),
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw Object.assign(new Error(body.error || `Request failed (${response.status})`), { code: body.code });
    return body;
  }, [projectId]);

  useEffect(() => {
    if (tab !== "setup") return;
    const abort = new AbortController(); let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try { const setup = await call("mobile_setup", {action: "status"}, abort.signal); if (!abort.signal.aborted) setIOSSetup(setup); }
      catch (e) { if (!abort.signal.aborted) setIOSSetup({state:"unavailable", message:(e as Error).message, teams:[]}); }
      if (!abort.signal.aborted) timer = setTimeout(poll, 5000);
    };
    void poll();
    return () => { abort.abort(); clearTimeout(timer); };
  }, [tab,call]);
  useEffect(() => () => { connection.current?.abort(); }, [projectId]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const [inventory, active, history, videos] = await Promise.all([
      call("mobile_devices", {}, signal), call("mobile_session", { action: "list" }, signal), call("mobile_test_get", {}, signal), call("mobile_record", { action: "list", limit: 100 }, signal),
    ]);
    if (signal?.aborted) return;
    setDevices(inventory.devices || []); setDiagnostics(inventory.diagnostics || []);
    setSessions(active.sessions || []); setRuns(history.runs || []); setRecordings(videos.recordings || []);
    setSelected(current => current || inventory.devices?.[0]?.id || "");
    setLoading(false);
  }, [call]);

  useEffect(() => {
    setSessionID(""); setImage(""); setSource(""); setSelected(""); setSelectedRun(""); setLive(false); setLoading(true);setPendingConnection(false);
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try { await refresh(abort.signal); }
      catch (e) { if (!abort.signal.aborted) { setError((e as Error).message); setLoading(false); } }
      if (!abort.signal.aborted) timer = setTimeout(poll, 6000);
    };
    void poll();
    void call("mobile_record", { action: "presets" }, abort.signal).then(r => { if (!abort.signal.aborted) setPresets(r.presets || []); }).catch(() => {});
    return () => { abort.abort(); clearTimeout(timer); };
  }, [refresh, call]);

  const session = sessions.find(s => s.id === sessionID);
  const active = session?.status === "active" && session.expires_at > Date.now() / 1000;
  const device = devices.find(d => d.id === selected);
  const report = runs.find(r => r.id === selectedRun);
  const capturing = recordings.find(r => r.session_id === sessionID && r.status === "recording" && r.kind === "capture");
  const working = recordings.some(r => r.status === "recording" || r.status === "processing");
  const video = recordings.find(r => r.id === selectedRecording);
  const caps = device?.capabilities || session?.device.capabilities || [];

  useEffect(() => {
    if (!capturing) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [capturing]);

  // Poll faster while a capture or export is in progress.
  useEffect(() => {
    if (!working) return;
    const timer = setInterval(() => { void call("mobile_record", { action: "list", limit: 100 }).then(r => setRecordings(r.recordings || [])).catch(() => {}); }, 2000);
    return () => clearInterval(timer);
  }, [working, call]);

  const observe = useCallback(async (includeSource = false, signal?: AbortSignal) => {
    const obs = await call("mobile_observe", { session_id: sessionID, source: includeSource }, signal);
    if (signal?.aborted) return;
    setImage(`data:${obs.mime_type};base64,${obs.image_base64}`);
    if (includeSource) setSource(obs.source || "No UI source returned.");
  }, [call, sessionID]);

  useEffect(() => {
    if (!live || !active || busy || tab !== "devices") return;
    const abort = new AbortController(); let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try { await observe(false, abort.signal); }
      catch (e) { if (!abort.signal.aborted) { setError((e as Error).message); setLive(false); } }
      if (!abort.signal.aborted) timer = setTimeout(poll, 1500);
    };
    void poll();
    return () => { abort.abort(); clearTimeout(timer); };
  }, [live, active, busy, tab, observe]);

  async function perform(fn: () => Promise<void>) {
    if (busy) return;
    setBusy(true); setError(""); setNotice("");
    try { await fn(); await refresh(); } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  async function act(action: string, args: Record<string, unknown> = {}) {
    await call("mobile_action", { ...args, session_id: sessionID, action });
    await observe();
  }
  async function open() {
    if (device?.driver === "xctest" && device.physical) {
      const setup = await call("mobile_setup",{action:"status"}); setIOSSetup(setup);
      if (setup.state !== "ready") {setPendingConnection(true);setTab("setup"); return;}
    }
    const abort = new AbortController(); connection.current = abort; setConnecting(true);
    const deadline = Date.now() + 20 * 60 * 1000;
    try {
      while (!abort.signal.aborted) {
        try {
          const result = await call("mobile_session", { action: "open", device_id: selected, mode }, abort.signal);
          if (abort.signal.aborted) return;
          setSessionID(result.session.id); setSessions(current => [result.session, ...current]); setLive(true); setImage(""); setSource(""); setNotice(""); return;
        } catch(e) {
          if (abort.signal.aborted) return;
          if ((e as Error & {code?:string}).code !== "setup_in_progress") throw e;
          if (Date.now() >= deadline) throw new Error("The iPhone runner is taking longer than expected. Check Xcode and the phone for Apple approval prompts, then try again.");
          setNotice("Preparing your iPhone: Mobile is building, signing and installing its runner. This can take a few minutes. Connection resumes automatically.");
          await new Promise<void>(resolve => { const done = () => { clearTimeout(timer); abort.signal.removeEventListener("abort",done); resolve(); }; const timer = setTimeout(done,3000); abort.signal.addEventListener("abort",done,{once:true}); });
        }
      }
    } finally { if (connection.current === abort) connection.current = null; setConnecting(false); }
  }
  function selectDevice(id: string) {
    setPendingConnection(false);
    setSelected(id);
    const existing = sessions.find(s => s.device_id === id && ["active", "cleanup_failed"].includes(s.status));
    setSessionID(existing?.id || ""); setImage(""); setSource(""); setLive(existing?.status === "active");
    if (devices.find(d => d.id === id)?.driver !== "appium") setMode("screen");
  }
  function position(e: PointerEvent<HTMLImageElement>) {
    const r = e.currentTarget.getBoundingClientRect();
    return { x: Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)), y: Math.max(0, Math.min(1, (e.clientY - r.top) / r.height)) };
  }
  async function startTest() {
    const steps = JSON.parse(plan);
    if (!Array.isArray(steps) || steps.length < 1 || steps.length > 50) throw new Error("A test needs 1–50 steps.");
    const result = await call("mobile_test_run", { device_id: selected, mode, name: testName, build_ref: buildRef, steps, ...(recordRun ? { record: { quality, audio: recordAudio } } : {}) });
    setSelectedRun(result.run_id); setTab("tests");
  }
  const evidenceURL = (id: string) => `${API}/artifacts/${encodeURIComponent(id)}?project_id=${encodeURIComponent(projectId)}`;

  useEffect(() => {
    if (!pendingConnection || busy || iosSetup?.state !== "ready") return;
    setPendingConnection(false);setTab("devices");void perform(open);
  },[pendingConnection,busy,iosSetup?.state]);

  return <div className="h-full min-h-0 flex flex-col bg-bg text-text">
    <nav className="shrink-0 flex items-center gap-1 border-b border-border px-3 pt-2 text-xs" aria-label="Mobile views">
      {(["devices", "tests", "recordings", "setup"] as const).map(t => <button
        type="button"
        key={t}
        aria-current={tab === t ? "page" : undefined}
        onClick={() => setTab(t)}
        className={`px-3 py-1.5 rounded-t border-b-2 ${tab === t ? "border-accent text-accent" : "border-transparent text-text-muted hover:text-text"}`}
      >{t === "devices" ? "Devices" : t === "tests" ? "Test runs" : t === "recordings" ? "Recordings" : "Setup"}</button>)}
      <button type="button" className={`${buttonClass} ml-auto mb-1`} disabled={busy || loading} onClick={() => void perform(async () => {})}>Refresh</button>
    </nav>

    {error && <div role="alert" className="shrink-0 flex items-start justify-between gap-3 border-b border-error/30 bg-error/10 px-4 py-2 text-xs text-error">
      <span>{error}</span>
      <button type="button" aria-label="Dismiss error" className="shrink-0 hover:underline" onClick={() => setError("")}>Dismiss</button>
    </div>}

    {notice && <div role="status" className="shrink-0 flex items-start justify-between gap-3 border-b border-border bg-bg-input px-4 py-2 text-xs text-text-muted">
      <span>{notice}</span>
      {connecting && <button type="button" className="shrink-0 hover:underline" onClick={() => { connection.current?.abort(); setNotice("Connection cancelled. Runner preparation may finish in the background."); }}>Cancel connection</button>}
      <button type="button" aria-label="Dismiss notice" className="shrink-0 hover:underline" onClick={() => setNotice("")}>Dismiss</button>
    </div>}

    {tab === "devices" && <div className="flex-1 min-h-0 flex overflow-hidden">
      <aside className="w-72 shrink-0 border-r border-border flex flex-col" style={{ maxWidth: "40%" }}>
        <div className="p-3 border-b border-border flex items-center justify-between gap-2">
          <span className={sectionTitleClass}>Devices</span>
          <span className="text-xs text-text-dim">{devices.length}</span>
        </div>
        <div className="flex-1 min-h-0 overflow-auto">
          {loading && <p className="p-3 text-xs text-text-muted">Discovering devices…</p>}
          {!loading && devices.length === 0 && <div className="p-3 space-y-3 text-xs text-text-muted">
            <p>No devices connected.</p>
            <p>Connect an Android phone or register an iPhone to get started.</p>
            <button type="button" className={buttonClass} onClick={() => setTab("setup")}>View setup</button>
          </div>}
          {devices.map(d => <button
            type="button"
            key={d.id}
            aria-pressed={selected === d.id}
            className={`w-full px-3 py-2 text-left border-b border-border hover:bg-bg-input/50 ${selected === d.id ? "bg-bg-input" : ""}`}
            onClick={() => selectDevice(d.id)}
          >
            <div className="flex items-center gap-2">
              <span className="text-sm text-text font-medium truncate flex-1">{d.name}</span>
              <Status value={d.state} />
            </div>
            <div className="text-xs text-text-muted mt-1">{d.platform === "ios" ? "iOS" : "Android"} · {d.physical ? "Physical" : "Virtual"} · {d.driver === "adb" ? "ADB" : d.driver === "xctest" ? "Native iOS" : "Appium"}</div>
            {d.hint && <p className="text-xs text-warn mt-1">{d.hint}</p>}
          </button>)}

          {sessions.some(s => s.status === "active" || s.status === "cleanup_failed") && <>
            <div className={`${sectionTitleClass} p-3 border-b border-border`}>Sessions</div>
            {sessions.filter(s => s.status === "active" || s.status === "cleanup_failed").map(s => <button
              type="button"
              key={s.id}
              aria-pressed={sessionID === s.id}
              className={`w-full px-3 py-2 text-left border-b border-border hover:bg-bg-input/50 ${sessionID === s.id ? "bg-bg-input" : ""}`}
              onClick={() => { setSessionID(s.id); setSelected(s.device_id); setImage(""); setSource(""); setLive(s.status === "active"); }}
            >
              <div className="text-sm text-text font-medium truncate">{s.device.name}</div>
              <div className="text-xs text-text-muted mt-1">{s.mode} · {s.status === "cleanup_failed" ? "Needs cleanup" : `Expires ${new Date(s.expires_at * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`}</div>
            </button>)}
          </>}
        </div>
        <div className="shrink-0 p-2 text-xs text-text-dim border-t border-border">{sessions.filter(s => s.status === "active").length} active sessions</div>
      </aside>

      <main className="flex-1 min-w-0 min-h-0 flex flex-col overflow-hidden">
        {device || session ? <>
          <header className="shrink-0 p-4 border-b border-border flex items-center gap-3 flex-wrap">
            <div className="flex-1 min-w-0">
              <h1 className="text-lg text-text font-semibold truncate">{session ? session.device.name : device?.name}</h1>
              <p className="text-xs text-text-dim mt-1">{active ? `${session?.mode === "structured" ? "Structured" : "Screen"} session · Click to tap, drag to swipe` : "Open an exclusive session to control this device."}</p>
            </div>
            <Status value={active ? "active" : session?.status || device?.state || "offline"} />
            {active && <>
              <button type="button" className={buttonClass} disabled={busy} onClick={() => void perform(async () => { await call("mobile_session", { action: "renew", session_id: sessionID }); })}>Extend session</button>
              <button type="button" className={dangerButtonClass} disabled={busy} onClick={() => void perform(async () => { await call("mobile_session", { action: "close", session_id: sessionID }); setSessionID(""); setImage(""); setLive(false); })}>Close session</button>
            </>}
          </header>

          {!active ? <div className="flex-1 overflow-auto p-6">
            <div className="max-w-lg space-y-4">
              <div>
                <h2 className="text-sm font-medium text-text">{session?.status === "cleanup_failed" ? "Device needs cleanup" : "Start a session"}</h2>
                <p className="text-sm text-text-muted mt-2">{session?.error || "Reserve this device to navigate websites, interact with apps, and inspect its screen."}</p>
              </div>
              <label className={labelClass}>Interaction mode
                <select className={inputClass} value={mode} onChange={e => setMode(e.target.value)}>
                  <option value="screen">Screen — taps and gestures</option>
                  <option value="structured" disabled={device?.driver !== "appium"}>Structured — browser elements</option>
                </select>
              </label>
              <button type="button" className={primaryButtonClass} disabled={busy || !device || !["ready", "configured"].includes(device.state)} onClick={() => void perform(open)}>{busy ? "Opening…" : "Open session"}</button>
              <p className="text-xs text-text-dim">Sessions are exclusive. Closing a session preserves app data.</p>
            </div>
          </div> : <>
            <form className="shrink-0 px-4 py-3 border-b border-border flex items-center gap-2" onSubmit={e => { e.preventDefault(); void perform(() => act("navigate", { url })); }}>
              <input className={`${inputClass} flex-1 min-w-0 mt-0`} aria-label="Website URL" placeholder="https://example.com" value={url} onChange={e => setURL(e.target.value)} />
              <button type="submit" className={primaryButtonClass} disabled={busy || !url}>Open URL</button>
            </form>
            <div className="flex-1 min-h-0 overflow-auto flex flex-col lg:flex-row">
              <section className="flex-1 min-w-0 p-4 bg-bg-input flex flex-col" aria-label="Device screen">
                <div className="flex items-center gap-2 text-xs mb-4">
                  <h2 className={sectionTitleClass}>Screen</h2>
                  <StatusDot variant={live ? "live" : "muted"}>{live ? "Live" : "Paused"}</StatusDot>
                  <span className="text-text-dim">Screenshot polling</span>
                  <button type="button" className={`${buttonClass} ml-auto`} onClick={() => setLive(!live)}>{live ? "Pause" : "Resume"}</button>
                </div>
                <div className="flex-1 flex items-center justify-center">
                  {image ? <img
                    src={image}
                    alt={`Screen of ${session?.device.name}`}
                    className="block border border-border rounded select-none"
                    style={{ maxHeight: "min(55vh, 640px)", maxWidth: "100%", width: "auto", height: "auto", touchAction: "none", cursor: "crosshair" }}
                    draggable={false}
                    onPointerDown={e => { if (busy) return; e.currentTarget.setPointerCapture(e.pointerId); pointer.current = position(e); }}
                    onPointerUp={e => { const start = pointer.current; pointer.current = null; if (!start || busy) return; const end = position(e); void perform(() => Math.hypot(end.x - start.x, end.y - start.y) > 0.04 ? act("swipe", { ...start, to_x: end.x, to_y: end.y }) : act("tap", end)); }}
                    onPointerCancel={() => { pointer.current = null; }}
                  /> : <p className="py-12 text-sm text-text-muted">Waiting for the first frame…</p>}
                </div>
                <div className="shrink-0 flex justify-center gap-2 mt-4">
                  {["back", "home", "enter"].map(key => <button type="button" className={buttonClass} key={key} disabled={busy} onClick={() => void perform(() => act("key", { key }))}>{key === "back" ? "Back" : key === "home" ? "Home" : "Enter"}</button>)}
                </div>
              </section>

              <aside className="shrink-0 w-full lg:w-72 border-t lg:border-t-0 lg:border-l border-border p-4 space-y-5" aria-label="Session controls">
                <section>
                  <h2 className={`${sectionTitleClass} mb-3`}>Input</h2>
                  <form className="space-y-2" onSubmit={e => { e.preventDefault(); void perform(async () => { await act("text", { text }); setText(""); }); }}>
                    <label className={labelClass}>Focused field
                      <input className={inputClass} aria-label="Text to type" type="password" autoComplete="off" value={text} onChange={e => setText(e.target.value)} placeholder="Text to enter" />
                    </label>
                    <button type="submit" className={buttonClass} disabled={busy || !text}>Send text</button>
                  </form>
                </section>
                <section className="border-t border-border pt-4 space-y-3">
                  <div className="flex items-center gap-2">
                    <h2 className={`${sectionTitleClass} flex-1`}>Recording</h2>
                    {capturing && <StatusDot variant="live">{clock(now - (capturing.started_at || capturing.created_at) * 1000)}</StatusDot>}
                  </div>
                  {capturing ? <>
                    <form className="flex gap-2" onSubmit={e => { e.preventDefault(); void perform(async () => { await call("mobile_record", { action: "mark", recording_id: capturing.id, name: markName || `Mark ${capturing.markers.length + 1}` }); setMarkName(""); }); }}>
                      <input className={`${inputClass} mt-0`} aria-label="Marker name" placeholder={`Mark ${capturing.markers.length + 1}`} value={markName} onChange={e => setMarkName(e.target.value)} />
                      <button type="submit" className={buttonClass} disabled={busy}>Mark</button>
                    </form>
                    {capturing.markers.length > 0 && <p className="text-xs text-text-dim">{capturing.markers.length} marker{capturing.markers.length === 1 ? "" : "s"}</p>}
                    <button type="button" className={dangerButtonClass} disabled={busy} onClick={() => void perform(async () => { const r = await call("mobile_record", { action: "stop", recording_id: capturing.id }); setSelectedRecording(r.recording.id); })}>Stop recording</button>
                  </> : <>
                    <label className={labelClass}>Quality
                      <select className={inputClass} value={quality} onChange={e => setQuality(e.target.value)}>
                        <option value="auto">Best available</option>
                        <option value="standard">Standard</option>
                        <option value="high" disabled={!caps.includes("record_high")}>High, full resolution</option>
                      </select>
                    </label>
                    <label className="flex items-center gap-2 text-xs text-text-muted"><input type="checkbox" checked={recordAudio} disabled={!caps.includes("record_audio")} onChange={e => setRecordAudio(e.target.checked)} />Record audio</label>
                    <button type="button" className={buttonClass} disabled={busy || !caps.includes("record")} onClick={() => void perform(async () => { await call("mobile_record", { action: "start", session_id: sessionID, quality, ...(caps.includes("record_audio") ? { audio: recordAudio } : {}) }); })}>Start recording</button>
                    {!caps.includes("record") && <p className="text-xs text-text-dim">Recording needs adb, scrcpy, Appium or USB capture on the runner. See Setup.</p>}
                  </>}
                </section>
                <section className="border-t border-border pt-4 space-y-3">
                  <h2 className={sectionTitleClass}>Applications</h2>
                  <label className={labelClass}>App identifier<input className={inputClass} placeholder="com.example.app" value={appID} onChange={e => setAppID(e.target.value)} /></label>
                  <div className="flex gap-2 flex-wrap">{["launch", "stop", "status"].map(action => <button type="button" key={action} className={buttonClass} disabled={busy || !appID} onClick={() => void perform(async () => { const r = await call("mobile_app", { session_id: sessionID, action, app_id: appID }); if (action === "status") setNotice(r.installed ? `${appID} is installed.` : `${appID} is not installed yet.`); else await observe(); })}>{action === "launch" ? "Launch" : action === "stop" ? "Stop" : "Check installed"}</button>)}</div>
                  <label className={labelClass}>Install from
                    <select className={inputClass} value={installSource} onChange={e => setInstallSource(e.target.value)}>
                      <option value="artifact">Runner file</option>
                      <option value="store">{session?.device.platform === "ios" ? "App Store" : "Play Store"}</option>
                      <option value="beta">{session?.device.platform === "ios" ? "TestFlight" : "Play testing track"}</option>
                    </select>
                  </label>
                  {installSource === "artifact" && <label className={labelClass}>Install artifact<input className={inputClass} placeholder="app.apk or app.ipa" value={artifact} onChange={e => setArtifact(e.target.value)} /></label>}
                  {installSource !== "artifact" && session?.device.platform === "ios" && <>
                    {installSource === "beta" && <label className={labelClass}>Name in TestFlight<input className={inputClass} placeholder="My Game" value={appName} onChange={e => setAppName(e.target.value)} /></label>}
                    <label className={labelClass}>Apple ID of the app {installSource === "beta" && <span className="text-text-dim">(optional)</span>}<input className={inputClass} inputMode="numeric" placeholder="1234567890" value={storeID} onChange={e => setStoreID(e.target.value)} /></label>
                  </>}
                  <button type="button" className={buttonClass} disabled={busy || (installSource === "artifact" ? !artifact : session?.device.platform === "ios" ? (installSource === "store" ? !storeID : !appName && !storeID) : !appID)} onClick={() => void perform(async () => {
                    const args: Record<string, unknown> = installSource === "artifact" ? { artifact } : { source: installSource, app_id: appID || undefined, app_name: appName || undefined, store_id: storeID || undefined };
                    const r = await call("mobile_app", { session_id: sessionID, action: "install", ...args });
                    if (r.result) setNotice(r.result === "already_installed" ? "Already installed and up to date." : "Install started. Use Check installed to follow it.");
                    await observe();
                  })}>Install on device</button>
                  <p className="text-xs text-text-dim">{installSource === "artifact" ? "File must be in the runner’s artifact directory." : "The phone must be signed in to the store account. Store installs tap Install or Update for you."}</p>
                </section>
                <section className="border-t border-border pt-4 space-y-3">
                  <h2 className={sectionTitleClass}>Inspect</h2>
                  <div className="flex gap-2 flex-wrap">
                    <button type="button" className={buttonClass} disabled={busy} onClick={() => void perform(() => observe())}>Screenshot</button>
                    <button type="button" className={buttonClass} disabled={busy} onClick={() => void perform(() => observe(true))}>UI source</button>
                  </div>
                  {source && <pre className="text-xs text-text-muted bg-bg-input border border-border rounded p-2 max-h-48 overflow-auto whitespace-pre-wrap break-all font-mono">{source}</pre>}
                </section>
              </aside>
            </div>
          </>}
        </> : <div className="p-8 text-text-muted text-sm text-center mt-12">{loading ? "Discovering devices…" : "Connect a device, then select it on the left."}</div>}
      </main>
    </div>}

    {tab === "tests" && <div className="flex-1 min-h-0 flex overflow-hidden">
      <aside className="w-72 shrink-0 border-r border-border flex flex-col" style={{ maxWidth: "40%" }}>
        <div className="p-3 border-b border-border flex items-center gap-2">
          <h2 className={`${sectionTitleClass} flex-1`}>Test runs</h2>
          <button type="button" className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg" onClick={() => setSelectedRun("")}>+ New</button>
        </div>
        <div className="flex-1 min-h-0 overflow-auto">
          {runs.length === 0 && <p className="p-3 text-xs text-text-muted">No test runs yet.</p>}
          {runs.map(r => <button
            type="button"
            key={r.id}
            aria-pressed={selectedRun === r.id}
            className={`w-full px-3 py-2 text-left border-b border-border hover:bg-bg-input/50 ${selectedRun === r.id ? "bg-bg-input" : ""}`}
            onClick={() => setSelectedRun(r.id)}
          >
            <div className="flex items-center gap-2"><span className="text-sm font-medium truncate flex-1">{r.name || "Smoke test"}</span><Status value={r.status} /></div>
            <div className="text-xs text-text-muted mt-1 truncate">{devices.find(d => d.id === r.device_id)?.name || r.device_id}</div>
            <div className="text-xs text-text-dim mt-1">{new Date(r.created_at * 1000).toLocaleString()}</div>
          </button>)}
        </div>
        <div className="shrink-0 p-2 text-xs text-text-dim border-t border-border">{runs.length} recent runs</div>
      </aside>

      <main className="flex-1 min-w-0 overflow-auto">
        {report ? <>
          <header className="p-4 border-b border-border flex items-center gap-3 flex-wrap">
            <div className="flex-1 min-w-0"><h1 className="text-lg font-semibold truncate">{report.name || "Smoke test"}</h1><p className="text-xs text-text-dim mt-1 break-all font-mono">{report.id}</p></div>
            <Status value={report.status} />
            {["queued", "running"].includes(report.status) && <button type="button" className={dangerButtonClass} disabled={busy} onClick={() => void perform(async () => { await call("mobile_test_cancel", { run_id: report.id }); })}>Cancel test</button>}
          </header>
          <div className="p-6 max-w-4xl space-y-5">
            {report.build_ref && <p className="text-xs text-text-muted">Build <span className="font-mono text-text">{report.build_ref}</span></p>}
            {report.recording_id && <button type="button" className={buttonClass} onClick={() => { setSelectedRecording(report.recording_id!); setTab("recordings"); }}>Open run recording</button>}
            {report.error && <p className="border border-error/30 bg-error/10 rounded p-3 text-xs text-error">{report.failure_kind}: {report.error}</p>}
            <h2 className={sectionTitleClass}>Steps</h2>
            {report.results.length === 0 && <p className="text-sm text-text-muted">Waiting for the first step…</p>}
            {report.results.map(step => <section className="border-b border-border pb-4" key={step.index}>
              <div className="flex items-center gap-3"><h3 className="text-sm font-medium flex-1">{step.index + 1}. {step.label || step.action}</h3><Status value={step.status} /></div>
              {step.error && <p className="text-xs text-error mt-2">{step.error}</p>}
              {step.artifact_id && <a href={evidenceURL(step.artifact_id)} target="_blank" rel="noreferrer" className="inline-block mt-3"><img className="max-h-48 max-w-full border border-border rounded" loading="lazy" src={evidenceURL(step.artifact_id)} alt={`Evidence for step ${step.index + 1}`} /></a>}
            </section>)}
          </div>
        </> : selectedRun ? <p className="p-6 text-sm text-text-muted">Loading test run…</p> : <>
          <header className="p-4 border-b border-border"><h1 className="text-lg font-semibold">New smoke test</h1><p className="text-xs text-text-dim mt-1">Run a sequence on one device and save evidence for each step.</p></header>
          <form className="p-6 max-w-3xl space-y-4" onSubmit={e => { e.preventDefault(); void perform(startTest); }}>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <label className={labelClass}>Device<select className={inputClass} value={selected} onChange={e => selectDevice(e.target.value)}><option value="">Choose a device</option>{devices.map(d => <option key={d.id} value={d.id}>{d.name} · {d.state}</option>)}</select></label>
              <label className={labelClass}>Interaction mode<select className={inputClass} value={mode} onChange={e => setMode(e.target.value)}><option value="screen">Screen</option><option value="structured" disabled={device?.driver !== "appium"}>Structured browser</option></select></label>
              <label className={labelClass}>Test name<input className={inputClass} value={testName} onChange={e => setTestName(e.target.value)} /></label>
              <label className={labelClass}>Build reference <span className="text-text-dim">(optional)</span><input className={inputClass} value={buildRef} onChange={e => setBuildRef(e.target.value)} placeholder="Build ID or commit" /></label>
            </div>
            <label className={labelClass}>Test steps<textarea className={`${inputClass} font-mono text-xs resize-y`} aria-label="Test steps JSON" spellCheck={false} rows={15} value={plan} onChange={e => setPlan(e.target.value)} /></label>
            <label className="flex items-center gap-2 text-xs text-text-muted"><input type="checkbox" checked={recordRun} onChange={e => setRecordRun(e.target.checked)} />Record a video of the run, with a marker at each step</label>
            <p className="text-xs text-text-dim">Use navigate, tap, swipe, text, launch, install, wait, wait_for_app, wait_for_text, assert_text, record_mark or screenshot. Close the device’s interactive session first.</p>
            <button type="submit" className={primaryButtonClass} disabled={busy || !selected}>Run test</button>
          </form>
        </>}
      </main>
    </div>}

    {tab === "recordings" && <div className="flex-1 min-h-0 flex overflow-hidden">
      <aside className="w-72 shrink-0 border-r border-border flex flex-col" style={{ maxWidth: "40%" }}>
        <div className="p-3 border-b border-border flex items-center justify-between gap-2">
          <h2 className={sectionTitleClass}>Recordings</h2>
          <span className="text-xs text-text-dim">{recordings.filter(r => r.kind === "capture").length}</span>
        </div>
        <div className="flex-1 min-h-0 overflow-auto">
          {recordings.filter(r => r.kind === "capture").length === 0 && <p className="p-3 text-xs text-text-muted">No recordings yet. Start one from an open device session, or record a test run.</p>}
          {recordings.filter(r => r.kind === "capture").map(r => <button
            type="button"
            key={r.id}
            aria-pressed={selectedRecording === r.id}
            className={`w-full px-3 py-2 text-left border-b border-border hover:bg-bg-input/50 ${selectedRecording === r.id ? "bg-bg-input" : ""}`}
            onClick={() => setSelectedRecording(r.id)}
          >
            <div className="flex items-center gap-2"><span className="text-sm font-medium truncate flex-1">{devices.find(d => d.id === r.device_id)?.name || r.device_id || "Recording"}</span><Status value={r.status} /></div>
            <div className="text-xs text-text-muted mt-1">{r.duration_ms ? clock(r.duration_ms) : "—"} · {r.method?.replaceAll("_", " ")}{r.run_id ? " · test run" : ""}</div>
            <div className="text-xs text-text-dim mt-1">{new Date(r.created_at * 1000).toLocaleString()}</div>
          </button>)}
        </div>
      </aside>
      <main className="flex-1 min-w-0 overflow-auto">
        {video ? <RecordingDetail
          recording={video.kind === "export" ? recordings.find(r => r.id === video.parent_id) || video : video}
          exports={recordings.filter(r => r.parent_id === (video.kind === "export" ? video.parent_id : video.id))}
          presets={presets}
          busy={busy}
          onExport={args => perform(async () => { await call("mobile_record_export", args); })}
          onDelete={id => perform(async () => { await call("mobile_record", { action: "delete", recording_id: id, confirm: true }); if (id === selectedRecording) setSelectedRecording(""); })}
          onUpload={id => perform(async () => { await call("mobile_record", { action: "upload", recording_id: id }); })}
        /> : <div className="p-8 text-text-muted text-sm text-center mt-12">Select a recording to play it and export clips.</div>}
      </main>
    </div>}

    {tab === "setup" && <main className="flex-1 min-h-0 overflow-auto p-6">
      <div className="max-w-4xl space-y-6">
        <header><h1 className="text-xl text-text font-semibold">Device setup</h1><p className="text-sm text-text-muted mt-2">Run Mobile beside your phones or connect it to a dedicated runner.</p></header>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <section className="space-y-3">
            <h2 className={sectionTitleClass}>Android</h2>
            <ol className="list-decimal pl-5 text-sm text-text-muted space-y-2"><li>Install Android Platform Tools on the runner.</li><li>Enable Developer options and USB debugging.</li><li>Connect the phone and accept its debugging prompt.</li><li>Refresh Devices and open a session.</li></ol>
            <p className="text-xs text-text-dim">Paired Wi-Fi debugging also works. Appium adds element-based automation.</p>
          </section>
          <section className="space-y-3">
            <h2 className={sectionTitleClass}>iPhone</h2>
            <p className="text-sm text-text-muted">Mobile handles certificates, provisioning profiles, signing and runner installation. Complete only Apple's account and device approvals.</p>
            <div role="status" className="text-sm text-text-muted border border-border rounded p-3">{iosSetup?.message || "Checking Xcode and Apple signing teams…"}</div>
            {iosSetup && ["needs_account","needs_xcode"].includes(iosSetup.state) && <button type="button" className={primaryButtonClass} disabled={busy} onClick={() => void perform(async () => { setIOSSetup(await call("mobile_setup",{action:"open_xcode"})); })}>Open Xcode to sign in</button>}
            {iosSetup?.teams && iosSetup.teams.length > 1 && <label className={labelClass}>Apple signing team
              <select className={inputClass} value={iosSetup.team_id || ""} disabled={busy} onChange={e => {const team_id=e.target.value; if(team_id) void perform(async () => { setIOSSetup(await call("mobile_setup",{action:"select_team",team_id})); });}}>
                <option value="">Choose your team</option>
                {iosSetup.teams.map(t => <option key={t.id} value={t.id}>{t.name || t.id}{t.personal ? " (Personal Team)" : ""}</option>)}
              </select>
            </label>}
            <ol className="list-decimal pl-5 text-sm text-text-muted space-y-2"><li>Sign into Xcode › Settings › Accounts once, including any two-factor authentication. A free Apple Account works; no manual certificate creation is needed.</li><li>Connect your iPhone by USB, unlock it and tap Trust.</li><li>Enable Developer Mode under Settings › Privacy & Security and confirm after restarting.</li><li>If iOS asks, trust the developer under Settings › General › VPN & Device Management.</li></ol>
            <p className="text-xs text-text-dim">Requires iOS 17 or newer. Setup refreshes automatically after sign-in. Open a Screen session from Devices; Mobile connects automatically when its first build finishes. Booted Simulators need no Apple Account.</p>
          </section>
        </div>
        <section className="border-t border-border pt-5 space-y-3">
          <h2 className={sectionTitleClass}>Screen recording</h2>
          <ul className="list-disc pl-5 text-sm text-text-muted space-y-2">
            <li>Android standard quality uses adb. Install scrcpy 2.1+ for full-resolution recording with audio (Android 11+).</li>
            <li>iPhone recording captures over USB on the Mac running Mobile, with audio. Allow Camera access for that app the first time. Appium setups can also record standard quality.</li>
            <li>iOS Simulators record at full resolution without audio, handy for store previews at exact device sizes.</li>
            <li>Install ffmpeg on the runner to export clips and read video details. Runner copies are kept for 7 days; Storage copies in /.mobile/ stay.</li>
          </ul>
        </section>
        <section className="border-t border-border pt-5 space-y-3">
          <h2 className={sectionTitleClass}>Runner diagnostics</h2>
          {diagnostics.map((hint, i) => <p className="text-sm text-text-muted" key={i}>{hint}</p>)}
          <p className="text-xs text-text-dim">Configure runner_url, runner_token, ios_team_id, appium_url, devices_json and the artifact directory in the app’s settings. Runner commands and examples are in the app README.</p>
        </section>
        <p className="border-t border-border pt-4 text-xs text-text-dim">Use dedicated test devices and accounts. Phones retain application state. Live view uses screenshot polling.</p>
      </div>
    </main>}
  </div>;
}

function clock(ms: number) {
  const total = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}
function megabytes(bytes: number) { return `${(bytes / (1 << 20)).toFixed(1)} MB`; }

interface DetailProps {
  recording: Recording; exports: Recording[]; presets: Preset[]; busy: boolean;
  onExport: (args: Record<string, unknown>) => Promise<void>; onDelete: (id: string) => Promise<void>; onUpload: (id: string) => Promise<void>;
}

function RecordingDetail({ recording, exports, presets, busy, onExport, onDelete, onUpload }: DetailProps) {
  const player = useRef<HTMLVideoElement>(null);
  const [preset, setPreset] = useState("app_store_preview");
  const [variant, setVariant] = useState("");
  const [orientation, setOrientation] = useState("");
  const [fit, setFit] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const chosen = presets.find(p => p.name === preset);
  const variants = Object.keys(chosen?.sizes || {});
  const selection = (Number(to || recording.duration_ms / 1000) - Number(from || 0));
  const tooLong = !!chosen?.max_seconds && selection > chosen.max_seconds;
  const tooShort = !!chosen?.min_seconds && selection < chosen.min_seconds;
  useEffect(() => { setVariant(""); }, [preset]);
  const position = () => (player.current ? player.current.currentTime.toFixed(1) : "0");
  return <>
    <header className="p-4 border-b border-border flex items-center gap-3 flex-wrap">
      <div className="flex-1 min-w-0"><h1 className="text-lg font-semibold truncate">{recording.file_name || recording.id}</h1><p className="text-xs text-text-dim mt-1">{new Date(recording.created_at * 1000).toLocaleString()} · {recording.method?.replaceAll("_", " ")}</p></div>
      <Status value={recording.status} />
      {recording.file_url && <a className={buttonClass} href={`${recording.file_url}&download=1`}>Download</a>}
      {!["recording", "processing", "deleted"].includes(recording.status) && <button type="button" className={dangerButtonClass} disabled={busy} onClick={() => void onDelete(recording.id)}>Delete</button>}
    </header>
    <div className="p-6 max-w-5xl space-y-6">
      {recording.error && <p className={`border rounded p-3 text-xs ${recording.status === "failed" ? "border-error/30 bg-error/10 text-error" : "border-warn/30 bg-warn/10 text-warn"}`}>{recording.error}</p>}
      {recording.status === "recording" && <p className="text-sm text-text-muted">Recording in progress. Stop it from the device session.</p>}
      {recording.status === "processing" && <p className="text-sm text-text-muted">Finishing the video…</p>}
      {recording.file_url && <div className="flex flex-col lg:flex-row gap-6">
        <div className="shrink-0 bg-bg-input border border-border rounded flex items-center justify-center p-2" style={{ maxWidth: "100%" }}>
          <video ref={player} src={recording.file_url} controls preload="metadata" className="block rounded" style={{ maxHeight: "min(60vh, 640px)", maxWidth: "100%" }} />
        </div>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs content-start" style={{ minWidth: 220 }}>
          <dt className="text-text-dim">Duration</dt><dd className="text-text">{clock(recording.duration_ms)}</dd>
          <dt className="text-text-dim">Resolution</dt><dd className="text-text">{recording.width ? `${recording.width}×${recording.height}` : "—"}</dd>
          <dt className="text-text-dim">Frame rate</dt><dd className="text-text">{recording.fps ? `${recording.fps} fps` : "—"}</dd>
          <dt className="text-text-dim">Video</dt><dd className="text-text">{recording.video_codec || "—"}</dd>
          <dt className="text-text-dim">Audio</dt><dd className="text-text">{recording.audio ? recording.audio_codec || "yes" : "none"}</dd>
          <dt className="text-text-dim">Size</dt><dd className="text-text">{megabytes(recording.size_bytes)}</dd>
          <dt className="text-text-dim">Storage</dt><dd className="text-text">{recording.upload?.status === "uploaded" ? `File #${recording.upload.storage_file_id}` : recording.upload?.status || "pending"}{recording.upload?.status === "failed" && <button type="button" className="ml-2 text-accent hover:underline" onClick={() => void onUpload(recording.id)}>Retry</button>}</dd>
        </dl>
      </div>}
      {recording.upload?.status === "failed" && recording.upload.error && <p className="text-xs text-text-dim">{recording.upload.error}</p>}

      {recording.markers.length > 0 && <section className="space-y-2">
        <h2 className={sectionTitleClass}>Markers</h2>
        <div className="flex flex-wrap gap-2">{recording.markers.map((m, i) => <button type="button" key={i} className={buttonClass} title="Jump to marker" onClick={() => { if (player.current) { player.current.currentTime = m.at_ms / 1000; void player.current.play().catch(() => {}); } }}>{clock(m.at_ms)} · {m.name}</button>)}</div>
      </section>}

      {recording.status === "ready" && <section className="border-t border-border pt-5 space-y-4">
        <h2 className={sectionTitleClass}>Export</h2>
        <form className="space-y-4" onSubmit={e => { e.preventDefault(); void onExport({ recording_id: recording.id, preset, ...(variant ? { variant } : {}), ...(orientation ? { orientation } : {}), ...(fit ? { fit } : {}), ...(from !== "" ? { from: Number(from) } : {}), ...(to !== "" ? { to: Number(to) } : {}) }); }}>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            <label className={labelClass}>Preset<select className={inputClass} value={preset} onChange={e => setPreset(e.target.value)}>{presets.map(p => <option key={p.name} value={p.name}>{p.name.replaceAll("_", " ")}</option>)}</select></label>
            {variants.length > 1 && <label className={labelClass}>Size<select className={inputClass} value={variant || chosen?.default_variant || ""} onChange={e => setVariant(e.target.value)}>{variants.map(v => <option key={v} value={v}>{v.replaceAll("_", " ")} · {chosen!.sizes![v].w}×{chosen!.sizes![v].h}</option>)}</select></label>}
            {preset !== "raw" && <label className={labelClass}>Orientation<select className={inputClass} value={orientation} onChange={e => setOrientation(e.target.value)}><option value="">Match recording</option><option value="portrait">Portrait</option><option value="landscape">Landscape</option></select></label>}
            {preset !== "raw" && <label className={labelClass}>Fit<select className={inputClass} value={fit} onChange={e => setFit(e.target.value)}><option value="">Preset default</option><option value="pad">Pad with black</option><option value="crop">Crop to fill</option><option value="stretch">Stretch</option></select></label>}
            <label className={labelClass}>From (seconds)
              <div className="flex gap-1"><input className={inputClass} inputMode="decimal" placeholder="0" value={from} onChange={e => setFrom(e.target.value)} /><button type="button" className={`${buttonClass} mt-1`} onClick={() => setFrom(position())}>Now</button></div>
            </label>
            <label className={labelClass}>To (seconds)
              <div className="flex gap-1"><input className={inputClass} inputMode="decimal" placeholder={(recording.duration_ms / 1000).toFixed(1)} value={to} onChange={e => setTo(e.target.value)} /><button type="button" className={`${buttonClass} mt-1`} onClick={() => setTo(position())}>Now</button></div>
            </label>
          </div>
          {chosen?.description && <p className="text-xs text-text-dim">{chosen.description}</p>}
          <p className={`text-xs ${tooLong || tooShort ? "text-warn" : "text-text-dim"}`}>Selection {selection.toFixed(1)} s{chosen?.max_seconds ? ` · allowed ${chosen.min_seconds || 0}–${chosen.max_seconds} s` : ""}</p>
          <button type="submit" className={primaryButtonClass} disabled={busy || tooLong || tooShort || selection <= 0}>Export</button>
        </form>
      </section>}

      {exports.length > 0 && <section className="border-t border-border pt-5 space-y-3">
        <h2 className={sectionTitleClass}>Exports</h2>
        {exports.map(x => <div key={x.id} className="border-b border-border pb-3 space-y-2">
          <div className="flex items-center gap-3 flex-wrap">
            <span className="text-sm font-medium flex-1 truncate">{x.preset?.replaceAll("_", " ").replace("-", " · ")}</span>
            {x.valid !== undefined && <Status value={x.valid ? "passed" : "failed"} />}
            <Status value={x.status} />
            {x.file_url && <a className={buttonClass} href={x.file_url} target="_blank" rel="noreferrer">Play</a>}
            {x.file_url && <a className={buttonClass} href={`${x.file_url}&download=1`}>Download</a>}
            {!["processing"].includes(x.status) && <button type="button" className={dangerButtonClass} disabled={busy} onClick={() => void onDelete(x.id)}>Delete</button>}
          </div>
          <p className="text-xs text-text-muted">{x.width ? `${x.width}×${x.height} · ${x.fps} fps · ${clock(x.duration_ms)} · ${megabytes(x.size_bytes)}` : x.status === "processing" ? "Encoding…" : ""}{x.upload?.status === "uploaded" ? ` · Storage file #${x.upload.storage_file_id}` : ""}</p>
          {x.checks.length > 0 && <ul className="text-xs space-y-1">{x.checks.map(c => <li key={c.name} className={c.ok ? "text-text-dim" : "text-error"}>{c.ok ? "Pass" : "Fail"} · {c.name.replaceAll("_", " ")}: {c.detail}</li>)}</ul>}
          {x.error && <p className="text-xs text-error">{x.error}</p>}
        </div>)}
      </section>}
    </div>
  </>;
}

const buttonClass = "px-2 py-1 text-xs border border-border rounded text-text-muted hover:text-text hover:bg-bg-input disabled:opacity-40 disabled:cursor-not-allowed whitespace-nowrap";
const primaryButtonClass = "px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40 disabled:cursor-not-allowed whitespace-nowrap";
const dangerButtonClass = "px-2 py-1 text-xs border border-error text-error rounded hover:bg-error/10 disabled:opacity-40 disabled:cursor-not-allowed whitespace-nowrap";
const inputClass = "w-full mt-1 bg-bg-input border border-border rounded px-2 py-1 text-sm text-text";
const labelClass = "block text-xs text-text-muted";
const sectionTitleClass = "text-xs uppercase tracking-wide text-text-dim";

function Status({ value }: { value: string }) {
  const tone = ["ready", "active", "passed"].includes(value) ? "bg-success/10 text-success"
    : ["configured", "running", "queued", "recording", "processing"].includes(value) ? "bg-info/10 text-info"
    : ["cleanup_failed", "unauthorized", "setup_required"].includes(value) ? "bg-warn/10 text-warn"
    : ["failed", "timed_out", "interrupted"].includes(value) ? "bg-error/10 text-error" : "bg-bg-hover text-text-dim";
  return <span className={`inline-flex px-1.5 py-0.5 rounded text-[11px] whitespace-nowrap ${tone}`}>{value.replaceAll("_", " ")}</span>;
}
