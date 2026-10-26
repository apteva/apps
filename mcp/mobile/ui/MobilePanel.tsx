import { useCallback, useEffect, useRef, useState, type PointerEvent } from "react";
import { StatusDot } from "@apteva/ui-kit";

interface Props { projectId: string; installId?: number; appName?: string }
interface Device { id: string; name: string; platform: string; physical: boolean; driver: string; state: string; hint?: string; capabilities: string[] }
interface Session { id: string; device_id: string; device: Device; status: string; mode: string; expires_at: number; error?: string }
interface Result { index: number; action: string; label?: string; status: string; error?: string; artifact_id?: string }
interface Run { id: string; name: string; device_id: string; build_ref?: string; status: string; failure_kind?: string; error?: string; created_at: number; results: Result[] }

const API = "/api/apps/mobile/api";
const sample = JSON.stringify([
  { action: "navigate", label: "Open website", params: { url: "https://example.com" } },
  { action: "wait_for_text", label: "Check page loaded", params: { text: "Example Domain", timeout_ms: 10000 } },
  { action: "screenshot", label: "Capture result" },
], null, 2);

export default function MobilePanel({ projectId }: Props) {
  const [tab, setTab] = useState<"devices" | "tests" | "setup">("devices");
  const [devices, setDevices] = useState<Device[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
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
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const pointer = useRef<{ x: number; y: number } | null>(null);

  const call = useCallback(async (tool: string, args: Record<string, unknown> = {}, signal?: AbortSignal) => {
    const response = await fetch(`${API}/call?project_id=${encodeURIComponent(projectId)}`, {
      method: "POST", credentials: "same-origin", signal,
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ tool, args }),
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
    return body;
  }, [projectId]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const [inventory, active, history] = await Promise.all([
      call("mobile_devices", {}, signal), call("mobile_session", { action: "list" }, signal), call("mobile_test_get", {}, signal),
    ]);
    if (signal?.aborted) return;
    setDevices(inventory.devices || []); setDiagnostics(inventory.diagnostics || []);
    setSessions(active.sessions || []); setRuns(history.runs || []);
    setSelected(current => current || inventory.devices?.[0]?.id || "");
    setLoading(false);
  }, [call]);

  useEffect(() => {
    setSessionID(""); setImage(""); setSource(""); setSelected(""); setSelectedRun(""); setLive(false); setLoading(true);
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try { await refresh(abort.signal); }
      catch (e) { if (!abort.signal.aborted) { setError((e as Error).message); setLoading(false); } }
      if (!abort.signal.aborted) timer = setTimeout(poll, 6000);
    };
    void poll();
    return () => { abort.abort(); clearTimeout(timer); };
  }, [refresh]);

  const session = sessions.find(s => s.id === sessionID);
  const active = session?.status === "active" && session.expires_at > Date.now() / 1000;
  const device = devices.find(d => d.id === selected);
  const report = runs.find(r => r.id === selectedRun);

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
    setBusy(true); setError("");
    try { await fn(); await refresh(); } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  async function act(action: string, args: Record<string, unknown> = {}) {
    await call("mobile_action", { ...args, session_id: sessionID, action });
    await observe();
  }
  async function open() {
    const result = await call("mobile_session", { action: "open", device_id: selected, mode });
    setSessionID(result.session.id); setSessions(current => [result.session, ...current]); setLive(true); setImage(""); setSource("");
  }
  function selectDevice(id: string) {
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
    const result = await call("mobile_test_run", { device_id: selected, mode, name: testName, build_ref: buildRef, steps });
    setSelectedRun(result.run_id); setTab("tests");
  }
  const evidenceURL = (id: string) => `${API}/artifacts/${encodeURIComponent(id)}?project_id=${encodeURIComponent(projectId)}`;

  return <div className="h-full min-h-0 flex flex-col bg-bg text-text">
    <nav className="shrink-0 flex items-center gap-1 border-b border-border px-3 pt-2 text-xs" aria-label="Mobile views">
      {(["devices", "tests", "setup"] as const).map(t => <button
        type="button"
        key={t}
        aria-current={tab === t ? "page" : undefined}
        onClick={() => setTab(t)}
        className={`px-3 py-1.5 rounded-t border-b-2 ${tab === t ? "border-accent text-accent" : "border-transparent text-text-muted hover:text-text"}`}
      >{t === "devices" ? "Devices" : t === "tests" ? "Test runs" : "Setup"}</button>)}
      <button type="button" className={`${buttonClass} ml-auto mb-1`} disabled={busy || loading} onClick={() => void perform(async () => {})}>Refresh</button>
    </nav>

    {error && <div role="alert" className="shrink-0 flex items-start justify-between gap-3 border-b border-error/30 bg-error/10 px-4 py-2 text-xs text-error">
      <span>{error}</span>
      <button type="button" aria-label="Dismiss error" className="shrink-0 hover:underline" onClick={() => setError("")}>Dismiss</button>
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
            <div className="text-xs text-text-muted mt-1">{d.platform === "ios" ? "iOS" : "Android"} · {d.physical ? "Physical" : "Virtual"} · {d.driver === "adb" ? "ADB" : "Appium"}</div>
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
                  <h2 className={sectionTitleClass}>Applications</h2>
                  <label className={labelClass}>App identifier<input className={inputClass} placeholder="com.example.app" value={appID} onChange={e => setAppID(e.target.value)} /></label>
                  <div className="flex gap-2">{["launch", "stop"].map(action => <button type="button" key={action} className={buttonClass} disabled={busy || !appID} onClick={() => void perform(async () => { await call("mobile_app", { session_id: sessionID, action, app_id: appID }); await observe(); })}>{action === "launch" ? "Launch" : "Stop"}</button>)}</div>
                  <label className={labelClass}>Install artifact<input className={inputClass} placeholder="app.apk or app.ipa" value={artifact} onChange={e => setArtifact(e.target.value)} /></label>
                  <button type="button" className={buttonClass} disabled={busy || !artifact} onClick={() => void perform(async () => { await call("mobile_app", { session_id: sessionID, action: "install", artifact }); await observe(); })}>Install on device</button>
                  <p className="text-xs text-text-dim">File must be in the runner’s artifact directory.</p>
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
            <p className="text-xs text-text-dim">Use navigate, tap, swipe, text, launch, install, wait_for_text, assert_text or screenshot. Close the device’s interactive session first.</p>
            <button type="submit" className={primaryButtonClass} disabled={busy || !selected}>Run test</button>
          </form>
        </>}
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
            <ol className="list-decimal pl-5 text-sm text-text-muted space-y-2"><li>Prepare a Mac with Xcode and Appium’s XCUITest driver.</li><li>Trust the computer and enable Developer Mode.</li><li>Configure signed WebDriverAgent and Safari automation.</li><li>Set appium_url and register the UDID in devices_json.</li></ol>
            <p className="text-xs text-text-dim">Native apps need a device-compatible signed IPA.</p>
          </section>
        </div>
        <section className="border-t border-border pt-5 space-y-3">
          <h2 className={sectionTitleClass}>Runner diagnostics</h2>
          {diagnostics.map((hint, i) => <p className="text-sm text-text-muted" key={i}>{hint}</p>)}
          <p className="text-xs text-text-dim">Configure runner_url, runner_token, appium_url, devices_json and the artifact directory in the app’s settings. Runner commands and examples are in the app README.</p>
        </section>
        <p className="border-t border-border pt-4 text-xs text-text-dim">Use dedicated test devices and accounts. Phones retain application state. Live view uses screenshot polling.</p>
      </div>
    </main>}
  </div>;
}

const buttonClass = "px-2 py-1 text-xs border border-border rounded text-text-muted hover:text-text hover:bg-bg-input disabled:opacity-40 disabled:cursor-not-allowed whitespace-nowrap";
const primaryButtonClass = "px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40 disabled:cursor-not-allowed whitespace-nowrap";
const dangerButtonClass = "px-2 py-1 text-xs border border-error text-error rounded hover:bg-error/10 disabled:opacity-40 disabled:cursor-not-allowed whitespace-nowrap";
const inputClass = "w-full mt-1 bg-bg-input border border-border rounded px-2 py-1 text-sm text-text";
const labelClass = "block text-xs text-text-muted";
const sectionTitleClass = "text-xs uppercase tracking-wide text-text-dim";

function Status({ value }: { value: string }) {
  const tone = ["ready", "active", "passed"].includes(value) ? "bg-success/10 text-success"
    : ["configured", "running", "queued"].includes(value) ? "bg-info/10 text-info"
    : ["cleanup_failed", "unauthorized", "setup_required"].includes(value) ? "bg-warn/10 text-warn"
    : ["failed", "timed_out", "interrupted"].includes(value) ? "bg-error/10 text-error" : "bg-bg-hover text-text-dim";
  return <span className={`inline-flex px-1.5 py-0.5 rounded text-[11px] whitespace-nowrap ${tone}`}>{value.replaceAll("_", " ")}</span>;
}
