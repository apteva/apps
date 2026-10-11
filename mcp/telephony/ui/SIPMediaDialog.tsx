import { useEffect, useState } from "react";

interface MediaSettings {
  codecs?: string[];
  speech_tool?: string;
  speech_input?: Record<string, unknown>;
  sample_rate?: number;
  prompts?: Record<string, number>;
  gather_timeout_seconds?: number;
  voicemail_max_seconds?: number;
}

export default function SIPMediaDialog({ routeId, label, request, onClose }: {
  routeId: string; label: string;
  request: (body: Record<string, unknown>) => Promise<{ config?: MediaSettings }>;
  onClose: () => void;
}) {
  const [config, setConfig] = useState<MediaSettings>({});
  const [input, setInput] = useState("{}");
  const [prompts, setPrompts] = useState("{}");
  const [busy, setBusy] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    setBusy(true); setLoaded(false); setError("");
    request({ route_id: routeId }).then(data => {
      if (!active) return;
      setLoaded(true);
      setConfig(data.config ?? {});
      setInput(JSON.stringify(data.config?.speech_input ?? {}, null, 2));
      setPrompts(JSON.stringify(data.config?.prompts ?? {}, null, 2));
    }).catch(e => { if (active) setError(String(e.message ?? e)); })
      .finally(() => { if (active) setBusy(false); });
    return () => { active = false; };
  }, [routeId, request]);
  const save = async (inherit = false) => {
    setError(""); setBusy(true);
    try {
      await request(inherit ? { route_id: routeId, inherit: true } : {
        route_id: routeId, config: { ...config, speech_input: JSON.parse(input), prompts: JSON.parse(prompts) },
      });
      onClose();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true" aria-label={`SIP media settings for ${label}`}>
    <section className="max-h-[90vh] w-full max-w-xl overflow-auto rounded-lg border border-border bg-bg p-5 space-y-4">
      <header className="flex items-center justify-between"><h2 className="font-semibold">SIP media · {label}</h2><button type="button" onClick={onClose} aria-label="Close">×</button></header>
      <p className="text-sm text-text-muted">Applies to new calls. Existing calls keep their settings. Browser audio transport is selected separately in the softphone.</p>
      <label className="block text-sm">Codec preference
        <select className="mt-1 w-full rounded border border-border bg-bg p-2" disabled={busy || !loaded} value={(config.codecs ?? ["G722", "PCMU", "PCMA"]).join(",")} onChange={e => setConfig({ ...config, codecs: e.target.value.split(",") })}>
          <option value="G722,PCMU,PCMA">Wideband preferred, G.711 fallback</option>
          <option value="PCMU,PCMA">G.711 only</option><option value="PCMA,PCMU">G.711 A-law preferred</option><option value="G722">Require G.722 wideband</option>
          {config.codecs?.length && !["G722,PCMU,PCMA", "PCMU,PCMA", "PCMA,PCMU", "G722"].includes(config.codecs.join(",")) ? <option value={config.codecs.join(",")}>{config.codecs.join(" → ")}</option> : null}
        </select>
      </label>
      <div className="grid grid-cols-2 gap-3">
        <label className="text-sm">Keypad timeout (seconds)<input className="mt-1 w-full rounded border border-border bg-bg p-2" disabled={busy || !loaded} type="number" min="1" max="60" value={config.gather_timeout_seconds || 6} onChange={e => setConfig({ ...config, gather_timeout_seconds: Number(e.target.value) })}/></label>
        <label className="text-sm">Voicemail maximum (seconds)<input className="mt-1 w-full rounded border border-border bg-bg p-2" disabled={busy || !loaded} type="number" min="1" max="600" value={config.voicemail_max_seconds || 120} onChange={e => setConfig({ ...config, voicemail_max_seconds: Number(e.target.value) })}/></label>
      </div>
      <details><summary className="cursor-pointer text-sm">Prompts and speech generation</summary>
        <p className="my-2 text-xs text-text-muted">Bind an optional speech integration, or map exact prompt text to private Storage WAV/MP3 file IDs. Text prompts must be configured before switching an IVR to SIP.</p>
        <label className="block text-sm">Speech tool<input className="my-1 w-full rounded border border-border bg-bg p-2" disabled={busy || !loaded} value={config.speech_tool || ""} placeholder="Integration tool name" onChange={e => setConfig({ ...config, speech_tool: e.target.value })}/></label>
        <label className="block text-sm">Speech input (JSON)<textarea className="my-1 w-full rounded border border-border bg-bg p-2 font-mono text-xs" disabled={busy || !loaded} rows={3} value={input} onChange={e => setInput(e.target.value)}/></label>
        <label className="block text-sm">Raw PCM sample rate<select className="my-1 w-full rounded border border-border bg-bg p-2" disabled={busy || !loaded} value={config.sample_rate || 0} onChange={e => setConfig({ ...config, sample_rate: Number(e.target.value) })}><option value={0}>WAV/MP3 supplies its rate</option>{[8000,16000,24000,48000].map(rate => <option key={rate} value={rate}>{rate} Hz</option>)}</select></label>
        <label className="block text-sm">Prerecorded prompts (JSON)<textarea className="my-1 w-full rounded border border-border bg-bg p-2 font-mono text-xs" disabled={busy || !loaded} rows={4} value={prompts} onChange={e => setPrompts(e.target.value)}/></label>
      </details>
      {error ? <p role="alert" className="text-sm text-error">{error}</p> : null}
      <footer className="flex gap-2">{routeId ? <button disabled={busy || !loaded} type="button" className="rounded border border-border px-3 py-2 text-sm disabled:opacity-40" onClick={() => void save(true)}>Use project defaults</button> : null}<button disabled={busy || !loaded} type="button" className="ml-auto rounded border border-border px-3 py-2 text-sm disabled:opacity-40" onClick={() => void save()}>Save</button></footer>
    </section>
  </div>;
}
