import React, { useCallback, useEffect, useRef, useState } from "react";

const API = "/api/apps/composer";
const KINDS = ["song", "image_video", "full_clip"] as const;
type Kind = typeof KINDS[number];
type Settings = { format: string; resolution: string; aspect: string; fps: number; excerpt_start?: number; excerpt_end?: number };
type Attempt = { input_revision?: string; generation_cost_usd?: number | null; render_cost_usd?: number | null; idempotency_key: string; id: number; status: string; error?: string; storage_url?: string; local_cache_url?: string; pending?: string[]; settings: Settings; duration_ms: number; created_at: string; output_revision: number };
type SavedOutput = { id: number; kind: Kind; revision: number; settings: Settings; plan: any; plan_error?: string; out_of_date: boolean; latest_attempt?: Attempt; latest_successful_render?: Attempt };
type Bundle = { costs?: { shared_audio_usd: number | null; visual_generation_usd: number | null }; outputs: SavedOutput[]; master: any; shared_revision: number };
const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-xs w-full";
const button = "border border-border rounded px-3 py-1.5 text-xs hover:bg-bg-input disabled:opacity-50";
const busyStatus = (s?: string) => ["queued", "preparing", "rendering", "waiting_ai"].includes(s || "");
function artifactURL(r?: Attempt) { const url = r?.storage_url || r?.local_cache_url || ""; return url.startsWith("/") ? url : ""; }

export default function OutputPresets({ compositionId, projectId, songLabels = false, sourceRevision = "" }: { compositionId: number; projectId?: string; songLabels?: boolean; sourceRevision?: string }) {
  const [bundle, setBundle] = useState<Bundle | null>(null);
  const [kind, setKind] = useState<Kind>("song");
  const [settings, setSettings] = useState<Settings>({ format: "mp3", resolution: "hd", aspect: "9:16", fps: 30 });
  const [plan, setPlan] = useState("{}");
  const [visuals, setVisuals] = useState("");
  const [model, setModel] = useState("");
  const [masterId, setMasterId] = useState("");
  const [masterPrompt, setMasterPrompt] = useState("");
  const [masterModel, setMasterModel] = useState("");
  const [masterDuration, setMasterDuration] = useState(30);
  const [adoptId, setAdoptId] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState<Attempt[] | null>(null);
  const serial = useRef(0);
  const polling = useRef(false);
  const base = `${API}/composition/${compositionId}/outputs`;
  const labels: Record<Kind, string> = { song: songLabels ? "Song" : "Audio", image_video: "Image video", full_clip: songLabels ? "Full clip" : "Full video" };
  const request = useCallback(async (path: string, method = "GET", body?: any) => {
    const res = await fetch(`${base}${path}?project_id=${encodeURIComponent(projectId || "")}`, { method, credentials: "same-origin", headers: body ? { "Content-Type": "application/json" } : undefined, body: body ? JSON.stringify(body) : undefined });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }, [base, projectId]);
  const load = useCallback(async () => { const ticket = serial.current; const data = await request(""); if (serial.current === ticket) setBundle(data); }, [request]);
  useEffect(() => { serial.current++; setBundle(null); setHistory(null); setMessage(""); setKind("song"); setBusy(false); load().catch(e => setMessage(e.message)); return () => { serial.current++; }; }, [load]);
  useEffect(() => { load().catch(e => setMessage(e.message)); }, [sourceRevision, load]);
  const output = bundle?.outputs.find(o => o.kind === kind);
  useEffect(() => { if (output) { setSettings(output.settings); setPlan(JSON.stringify(output.plan, null, 2)); } setHistory(null); }, [kind, output?.revision]);
  useEffect(() => { const m = bundle?.master; setMasterId(m?.asset?.src?.replace(/^storage:/, "") || ""); setMasterDuration(m?.length || 30); setMasterPrompt(m?.ai?.prompt || ""); setMasterModel(m?.ai?.model || ""); }, [bundle?.shared_revision]);
  useEffect(() => {
    if (!bundle?.outputs.some(o => busyStatus(o.latest_attempt?.status))) return;
    const timer = setInterval(async () => {
      if (polling.current) return;
      polling.current = true;
      try {
        for (const o of bundle.outputs) {
          const attempt = o.latest_attempt;
          if (attempt?.status === "waiting_ai") await request(`/${o.kind}/render`, "POST", { expected_revision: attempt.output_revision, idempotency_key: attempt.idempotency_key });
        }
        await load();
      } catch (e) { setMessage((e as Error).message); }
      finally { polling.current = false; }
    }, 3000);
    return () => clearInterval(timer);
  }, [bundle, load, request]);
  const act = async (fn: () => Promise<void>) => { setBusy(true); setMessage(""); const ticket = serial.current; try { await fn(); } catch (e) { if (serial.current === ticket) setMessage((e as Error).message); } finally { if (serial.current === ticket) { setBusy(false); await load().catch(e => setMessage(e.message)); } } };
  const save = async () => {
    if (!output) return;
    await request(`/${kind}`, "PATCH", { settings, plan: JSON.parse(plan), expected_revision: output.revision });
    setMessage("Output settings saved. Existing previews remain available.");
  };
  const render = async () => {
    if (!output) return;
    const pending = output.latest_attempt;
    const resume = pending?.status === "waiting_ai";
    const key = resume ? pending.idempotency_key : crypto.randomUUID();
    const result = await request(`/${kind}/render`, "POST", { expected_revision: resume ? pending.output_revision : output.revision, idempotency_key: key });
    setMessage(result.error || (result.status === "waiting_ai" ? "Assets are generating. This panel will resume the export automatically." : `Export ${result.status}.`));
  };
  const setMaster = async (generate: boolean) => {
    if (!bundle) return;
    const master = generate
      ? { asset: { type: "audio", src: "" }, length: masterDuration, ai: { media_kind: "music", prompt: masterPrompt, model: masterModel, duration: masterDuration, cache_key: `song-version:${crypto.randomUUID()}` } }
      : { asset: { type: "audio", src: `storage:${Number(masterId)}` }, length: masterDuration };
    await request("/master", "PATCH", { master, expected_revision: bundle.shared_revision });
    setMessage(generate ? "New song version selected. Export Audio/Song to generate it; other outputs retain their previews." : "Shared audio master selected.");
  };
  const makeVisualPlan = () => {
    const entries = visuals.split("\n").map(x => x.trim()).filter(Boolean);
    if (!entries.length) return;
    const duration = settings.excerpt_end || masterDuration;
    const length = duration / entries.length;
    const type = kind === "image_video" ? "image" : "video";
    setPlan(JSON.stringify({ timeline: { tracks: [{ type: "visual", clips: entries.map((line, i) => ({ uid: `scene-${i + 1}`, start: i * length, length, source_audio: "mute", asset: { type, src: /^storage:\d+$/.test(line) ? line : "" }, ...(/^storage:\d+$/.test(line) ? {} : { ai: { media_kind: type, prompt: line, model, ...(type === "video" ? { duration: Math.ceil(length) } : {}), aspect: settings.aspect } }) })) }] } }, null, 2));
    setMessage("Visual plan prepared. Save settings before exporting.");
  };
  const dirty = !!output && (JSON.stringify(settings)!==JSON.stringify(output.settings) || plan!==JSON.stringify(output.plan,null,2));
  const preview = output?.latest_successful_render;
  const src = artifactURL(preview);
  const cost = (v: number | null | undefined) => v == null ? "unknown" : `$${v.toFixed(4)}`;
  return <section className="p-4 space-y-4 overflow-auto text-text" aria-label="Composition outputs">
    <div className="flex gap-2" role="tablist" aria-label="Outputs">{KINDS.map(k => <button key={k} role="tab" aria-selected={kind === k} className={`${button} ${kind === k ? "bg-accent/15 border-accent" : ""}`} onClick={() => { setKind(k); setMessage(""); }}>
      {labels[k]}{bundle?.outputs.find(o => o.kind === k)?.latest_successful_render ? " ✓" : ""}
    </button>)}</div>
    {!bundle ? <p className="text-sm">{message || "Loading outputs…"}</p> : output && <>
      <div className="flex flex-wrap gap-2 items-center text-xs"><span>{output.latest_attempt?.status || "Not generated"}</span>{output.out_of_date && <span className="text-amber-500">{preview?.input_revision === "adopted" ? "Imported artifact — input versions unknown" : "Out of date — previous preview retained"}</span>}<span className="text-text-dim">Additional cost: unknown</span></div>
      <p className="text-xs text-text-dim">Recorded generation: shared audio {cost(bundle.costs?.shared_audio_usd)} · visuals {cost(bundle.costs?.visual_generation_usd)}. Latest attempt: generation {cost(output.latest_attempt?.generation_cost_usd)}, rendering {cost(output.latest_attempt?.render_cost_usd)}.</p>
      {output.latest_attempt?.error && <p role="alert" className="text-sm text-red-500">{output.latest_attempt.error}</p>}
      {!!output.latest_attempt?.pending?.length && <p className="text-xs">{output.latest_attempt.pending.join(" · ")}</p>}
      {src && <div className="space-y-2">
        {preview?.settings.format !== "mp4" ? <audio key={`${kind}:${preview?.id}`} controls src={src} className="w-full" /> : <video key={`${kind}:${preview?.id}`} controls playsInline src={src} style={{ maxHeight: 360, width: "100%", objectFit: "contain" }} />}
        <a href={src} download className="text-xs text-accent">Download {labels[kind]}</a>
      </div>}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
        <label className="text-xs">Format<select className={field} value={settings.format} onChange={e => setSettings({ ...settings, format: e.target.value })}>{(kind === "song" ? ["mp3", "wav", "m4a", "aac"] : ["mp4"]).map(x => <option key={x}>{x}</option>)}</select></label>
        {kind !== "song" && <><label className="text-xs">Aspect<select className={field} value={settings.aspect} onChange={e => setSettings({ ...settings, aspect: e.target.value })}>{["9:16", "16:9", "1:1", "4:3"].map(x => <option key={x}>{x}</option>)}</select></label><label className="text-xs">Resolution<select className={field} value={settings.resolution} onChange={e => setSettings({ ...settings, resolution: e.target.value })}>{["sd", "hd", "fullhd", "4k"].map(x => <option key={x}>{x}</option>)}</select></label></>}
        <label className="text-xs">Excerpt start (seconds)<input type="number" min="0" step="0.1" className={field} value={settings.excerpt_start || 0} onChange={e => setSettings({ ...settings, excerpt_start: Number(e.target.value) })} /></label>
        <label className="text-xs">Excerpt end (blank = full)<input type="number" min="0" step="0.1" className={field} value={settings.excerpt_end || ""} onChange={e => setSettings({ ...settings, excerpt_end: Number(e.target.value) })} /></label>
      </div>
      {kind !== "song" && <details><summary className="text-sm cursor-pointer">Visual scenes</summary><p className="text-xs text-text-dim my-2">One Storage reference (storage:123) or generation prompt per line. Audio comes from the shared master.</p><textarea aria-label="Visual scene sources or prompts" className={field} rows={3} value={visuals} onChange={e => setVisuals(e.target.value)} /><input aria-label="Visual generation model" className={field} placeholder="Generation model (optional)" value={model} onChange={e => setModel(e.target.value)} /><button className={button} onClick={makeVisualPlan}>Prepare {kind === "image_video" ? "image" : "video"} scenes</button><details className="mt-2"><summary className="text-xs">Advanced visual plan JSON</summary><textarea aria-label="Visual plan JSON" rows={8} className={`${field} font-mono`} value={plan} onChange={e => setPlan(e.target.value)} /></details><button className={button} onClick={() => setPlan("{}")}>Use composition visuals</button></details>}
      {dirty && <p className="text-xs">Save settings before exporting.</p>}
      {output.plan_error && <p className="text-xs text-text-dim">{output.plan_error}</p>}
      <div className="flex gap-2 flex-wrap"><button className={button} disabled={busy} onClick={() => act(save)}>Save settings</button><button className={`${button} bg-accent/15`} disabled={busy || dirty || (busyStatus(output.latest_attempt?.status) && output.latest_attempt?.status !== "waiting_ai")} onClick={() => act(render)}>{busy ? "Working…" : output.latest_attempt?.status === "waiting_ai" ? "Check generation" : "Generate / export"}</button><button className={button} onClick={() => act(async () => { const e = await request(`/${kind}/estimate`, "POST", {}); setMessage(`Missing assets: ${JSON.stringify(e.missing_dependencies)}. Additional generation cost: ${cost(e.additional_generation_cost_usd)}; rendering: ${cost(e.render_cost_usd)}.`); })}>Estimate</button><button className={button} onClick={() => act(async () => { setHistory((await request(`/${kind}/renders`)).renders); })}>Render history</button></div>
      {history && <ul className="text-xs space-y-2">{history.map(r => <li key={r.id}>#{r.id} · {r.status} · {new Date(r.created_at).toLocaleString()} {artifactURL(r) && <a className="text-accent" href={artifactURL(r)} download>Download</a>}{r.error && <span className="text-red-500"> — {r.error}</span>}</li>)}</ul>}
      <details className="border-t border-border pt-3"><summary className="text-sm cursor-pointer">Shared audio master</summary><p className="text-xs text-text-dim my-2">Choose one audio asset for every output. Replacing it preserves previous exports and marks dependent outputs out of date.</p><div className="grid grid-cols-2 gap-2"><label className="text-xs">Storage audio file ID<input className={field} type="number" min="1" value={masterId} onChange={e => setMasterId(e.target.value)} /></label><label className="text-xs">Full master duration (seconds)<input className={field} type="number" min="0.1" step="0.1" value={masterDuration} onChange={e => setMasterDuration(Number(e.target.value))} /></label></div><button className={button} disabled={busy || !Number(masterId)} onClick={() => act(() => setMaster(false))}>Select audio master</button><details className="mt-3"><summary className="text-xs">Generate a new song version</summary><textarea aria-label="Song generation prompt" className={field} value={masterPrompt} onChange={e => setMasterPrompt(e.target.value)} placeholder="Song prompt" /><input className={field} value={masterModel} onChange={e => setMasterModel(e.target.value)} placeholder="Music model (optional)" /><button className={button} disabled={busy || !masterPrompt.trim()} onClick={() => act(() => setMaster(true))}>Prepare new song version</button></details></details>
      <details><summary className="text-xs">Use an existing export</summary><p className="text-xs text-text-dim my-2">Attach a retained Storage artifact to this output. Original media and render history are preserved.</p><input aria-label="Existing export Storage ID" className={field} type="number" min="1" value={adoptId} onChange={e => setAdoptId(e.target.value)} /><button className={button} disabled={busy || !Number(adoptId)} onClick={() => act(async () => { await request(`/${kind}/adopt`, "POST", { storage_id: Number(adoptId), expected_revision: output.revision, idempotency_key: crypto.randomUUID() }); setMessage("Existing export attached."); })}>Attach existing export</button></details>
      {message && <p role="status" className="text-sm">{message}</p>}
    </>}
  </section>;
}
