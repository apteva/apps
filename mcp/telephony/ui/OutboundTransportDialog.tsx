import { useEffect, useState } from "react";
import CarrierJitterBufferControls, {type CarrierBufferRequest} from "./CarrierJitterBufferControls";
import SIPAudioProfileControls, {type SIPAudioProfileRequest} from "./SIPAudioProfileControls";
import type { OutboundSIPConfig, OutboundSIPSetupOptions, OutboundTransport } from "../frontend/src/client";

export default function OutboundTransportDialog({ provider, connection, number, request, carrierRequest, profileRequest, onClose }: {
  provider: string; connection: number; number?: string;
  request: (body: Record<string, unknown>) => Promise<{ outbound_transport?: OutboundTransport; setup_options?: OutboundSIPSetupOptions }>;
  carrierRequest?: CarrierBufferRequest;
  profileRequest?: SIPAudioProfileRequest;
  onClose: () => void;
}) {
  const [transport, setTransport] = useState<OutboundTransport["transport"]>("programmable_websocket");
  const [automatic, setAutomatic] = useState(true);
  const [config, setConfig] = useState<OutboundSIPConfig>({ host: provider === "telnyx" ? "sip.telnyx.com" : "", resource_id: "", auth_mode: "digest", media_encryption: "srtp_sdes" });
  const [credentials, setCredentials] = useState({ username: "", password: "" });
  const [existing, setExisting] = useState<OutboundTransport>();
  const [options, setOptions] = useState<OutboundSIPSetupOptions>();
  const [profile, setProfile] = useState("");
  const [countries, setCountries] = useState("");
  const [spend, setSpend] = useState("");
  const [busy, setBusy] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    request({ carrier_connection_id: connection, phone_number: number || "", setup_options: true }).then(r => {
      if (!active) return;
      setExisting(r.outbound_transport);
      setTransport(r.outbound_transport?.transport || "programmable_websocket");
      if (r.outbound_transport?.config) setConfig(r.outbound_transport.config);
      setOptions(r.setup_options);
      const enabled = r.setup_options?.profiles.filter(p => p.enabled) || [];
      if (enabled.length === 1 && r.outbound_transport?.transport !== "sip_direct") setProfile(enabled[0].id);
      if (!enabled.length && !r.setup_options?.warning && r.outbound_transport?.transport !== "sip_direct") setProfile("new");
      setLoaded(true);
    }).catch(e => { if (active) setError(e.message); }).finally(() => { if (active) setBusy(false); });
    return () => { active = false; };
  }, [connection, number, request]);
  const save = async (inherit = false) => {
    setBusy(true); setError("");
    try {
      const sip = automatic ? {
        provision: true,
        ...(profile === "new" ? { profile_policy: { destinations: countries.split(/[\s,;]+/).map(s => s.trim().toUpperCase()).filter(Boolean), daily_spend_limit: spend } } : profile ? { outbound_profile_id: profile } : {}),
      } : { config, ...(credentials.username || credentials.password ? credentials : {}) };
      await request({ carrier_connection_id: connection, phone_number: number || "", ...(inherit ? { inherit: true } : { transport, ...(transport === "sip_direct" ? sip : {}) }) });
      onClose();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const patch = (p: Partial<OutboundSIPConfig>) => setConfig(c => ({ ...c, ...p }));
  const input = "mt-1 w-full rounded border border-border bg-bg p-2";
  const enabledProfiles = options?.profiles.filter(p => p.enabled) || [];
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true" aria-label="Outbound transport settings">
    <section className="max-h-[90vh] w-full max-w-xl overflow-auto rounded-lg border border-border bg-bg p-5 space-y-4">
      <header className="flex items-center justify-between"><h2 className="font-semibold">Outbound calling · {number || `${provider} account ${connection}`}</h2><button type="button" disabled={busy} onClick={onClose} aria-label="Close">×</button></header>
      <p className="text-sm text-text-muted">Applies to new calls {number ? "from this number" : "from every number in this account without an override"}. Existing calls and inbound routing continue unchanged. Browser audio settings are independent.</p>
      {existing?.scope && <p className="text-xs text-text-muted">Current configuration: {existing.scope}{existing.verified_at ? ` · verified ${new Date(existing.verified_at).toLocaleString()}` : ""}</p>}
      <fieldset disabled={busy || !loaded} className="space-y-3">
        <label className="block text-sm">Carrier transport<select className={input} value={transport} onChange={e => setTransport(e.target.value as typeof transport)}><option value="programmable_websocket">Voice API / WebSocket</option><option value="sip_direct">Direct SIP / RTP</option></select></label>
        {transport === "sip_direct" && <>
          <label className="block text-sm">Setup<select aria-label="Setup" className={input} value={automatic ? "automatic" : "existing"} onChange={e => setAutomatic(e.target.value === "automatic")}><option value="automatic">Automatic setup</option><option value="existing">Use an existing resource (advanced)</option></select></label>
          {automatic ? <div className="rounded border border-border p-3 space-y-3">
            <p className="text-sm">Telephony creates or reuses a dedicated {provider === "twilio" ? "outbound SIP trunk" : "outbound SIP connection"}, configures authentication, and verifies readiness before enabling it. SIP credentials are generated and stored encrypted. Setup progress is saved so retries can resume.</p>
            <p className="text-xs text-text-muted">Requires a configured SIP gateway with TLS certificate and reachable media ports. Recording uses Telephony’s local recording settings.</p>
            {options?.warning && <p role="status" className="text-sm text-text-muted">Could not load setup options: {options.warning}. Retry by reopening this dialog.</p>}
            {provider === "telnyx" && <>
              <label className="block text-sm">Outbound calling profile<select className={input} value={profile} onChange={e => setProfile(e.target.value)}>
                <option value="">{existing?.transport === "sip_direct" ? "Keep the configured connection and profile" : (enabledProfiles.length > 1 ? "Choose a calling profile" : "Automatically use the sole enabled profile")}</option>
                {options?.profiles.filter(p => p.enabled).map(p => <option key={p.id} value={p.id}>{p.name}{p.destinations.length ? ` · ${p.destinations.join(", ")}` : ""}</option>)}
                <option value="new">Create a profile with calling limits</option>
              </select></label>
              {profile === "new" && <><label className="block text-sm">Allowed destination countries<input className={input} value={countries} placeholder="FR, ES" onChange={e => setCountries(e.target.value)} /></label><label className="block text-sm">Daily spending limit (USD)<input className={input} type="number" min="0.01" step="0.01" value={spend} onChange={e => setSpend(e.target.value)} /></label><p className="text-xs text-text-muted">These limits belong to the new carrier profile. Choose the countries and budget your account should allow.</p></>}
            </>}
          </div> : <>
            <p className="text-xs text-text-muted">Use an existing {provider === "twilio" ? "Elastic SIP trunk with termination enabled" : "SIP connection with an enabled outbound voice profile"}. Telephony verifies it without modifying carrier settings.</p>
            <label className="block text-sm">{provider === "twilio" ? "Trunk SID (TK…)" : "SIP connection ID"}<input className={input} value={config.resource_id} onChange={e => patch({ resource_id: e.target.value })} /></label>
            <label className="block text-sm">Termination hostname<input className={input} value={config.host} placeholder={provider === "twilio" ? "your-trunk.pstn.twilio.com" : "sip.telnyx.com"} onChange={e => patch({ host: e.target.value })} /></label>
            {provider === "telnyx" ? <label className="block text-sm">Connection type<select className={input} value={config.resource_type || "credential_connection"} onChange={e => patch({ resource_type: e.target.value as OutboundSIPConfig["resource_type"], auth_mode: e.target.value === "credential_connection" ? "digest" : "ip" })}><option value="credential_connection">Credentials</option><option value="ip_connection">IP authentication</option><option value="fqdn_connection">FQDN with outbound IP authentication</option></select></label> : <label className="block text-sm">Authentication<select className={input} value={config.auth_mode} onChange={e => patch({ auth_mode: e.target.value as "digest" | "ip" })}><option value="digest">SIP credentials</option><option value="ip">Allowed server IP</option></select></label>}
            {config.auth_mode === "digest" && <><label className="block text-sm">SIP username<input className={input} autoComplete="off" value={credentials.username} onChange={e => setCredentials(c => ({ ...c, username: e.target.value }))} /></label><label className="block text-sm">SIP password<input className={input} type="password" autoComplete="new-password" value={credentials.password} onChange={e => setCredentials(c => ({ ...c, password: e.target.value }))} /></label><p className="text-xs text-text-muted">{existing?.credentials_configured && existing.scope === (number ? "number" : "account") ? "Leave both blank to retain saved credentials." : "Enter separate SIP credentials; these are not the API credentials."} Passwords are never returned.</p></>}
            <label className="block text-sm">Media encryption<select className={input} value={config.media_encryption} onChange={e => patch({ media_encryption: e.target.value as typeof config.media_encryption })}><option value="srtp_sdes">SRTP required</option><option value="disabled">Plain RTP (only if gateway policy allows)</option></select></label>
            <label className="block text-sm">Codec preference<select className={input} value={(config.codecs || (provider === "telnyx" ? ["G722", "PCMU", "PCMA"] : ["PCMU", "PCMA"])).join(",")} onChange={e => patch({ codecs: e.target.value.split(",") as OutboundSIPConfig["codecs"] })}>{provider === "telnyx" && <><option value="G722,PCMU,PCMA">G.722 wideband preferred</option><option value="G722">G.722 only</option></>}<option value="PCMU,PCMA">G.711 μ-law preferred</option><option value="PCMA,PCMU">G.711 A-law preferred</option>{config.codecs?.length && !["G722,PCMU,PCMA", "G722", "PCMU,PCMA", "PCMA,PCMU"].includes(config.codecs.join(",")) ? <option value={config.codecs.join(",")}>{config.codecs.join(" → ")}</option> : null}</select></label>
          </>}
        </>}
      </fieldset>
      {error && <p role="alert" className="text-sm text-error">{error}</p>}
      {profileRequest ? <SIPAudioProfileControls target={{direction:"outbound",carrier_connection_id:connection,...(number?{phone_number:number}:{})}} request={profileRequest}/> : null}
      {provider==="telnyx" && existing?.transport==="sip_direct" && carrierRequest ? <CarrierJitterBufferControls target={{carrier_connection_id:connection,phone_number:number||""}} request={carrierRequest}/> : null}
      <footer className="flex flex-wrap justify-end gap-2"><button className="rounded border border-border px-3 py-2 text-sm" disabled={busy || !loaded} onClick={() => void save(true)}>{number ? "Use account default" : "Reset to Voice API default"}</button><button className="rounded border border-border px-3 py-2 text-sm" disabled={busy} onClick={onClose}>Cancel</button><button className="rounded bg-text text-bg px-3 py-2 text-sm" disabled={busy || !loaded} onClick={() => void save()}>{busy ? "Setting up…" : transport === "sip_direct" && automatic ? "Set up and enable SIP" : "Apply to new calls"}</button></footer>
    </section>
  </div>;
}
