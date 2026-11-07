import { useEffect, useRef, useState } from "react";
import { scopedAppURL } from "./panelScope";
import { createRequestGate } from "./requestIdentity";
import { mcpEnvelopeError } from "./accountFlow";

export interface AudienceCapabilities {
  discovery: "supported" | "unsupported";
  publishing: "native" | "manual" | "unsupported";
  tier_targeting: boolean;
  verification: string;
  staging_visibility?: string;
}
export interface AudienceSelection { type: "standard" | "members"; level_ids?: string[] }
export interface MembershipDiscovery {
  status: string; reason?: string; stale: boolean;
  levels: { id: string; name: string; price: number | null }[];
}
async function audienceRequest(path: string, projectId?: string | null, init?: RequestInit) {
  const response = await fetch(scopedAppURL("/api/apps/social", path, projectId), { credentials: "same-origin", ...init });
  if (!response.ok) throw new Error(await response.text());
  const result = await response.json();
  const error = mcpEnvelopeError(result);
  if (error) throw new Error(error);
  return result;
}
export function MembershipAudience({ accountId, projectId, capabilities, value, onChange }: {
  accountId: number; projectId?: string | null; capabilities?: AudienceCapabilities;
  value?: AudienceSelection; onChange: (value: AudienceSelection) => void;
}) {
  const [discovery, setDiscovery] = useState<MembershipDiscovery | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const requests = useRef(createRequestGate());
  useEffect(() => { requests.current.invalidate(); setDiscovery(null); setError(""); setBusy(false); return () => requests.current.invalidate(); }, [accountId, projectId]);
  // No discovery, including provider refresh, happens merely by opening Compose.
  if (!capabilities || capabilities.publishing === "unsupported") return null;
  const refresh = async () => {
    const current = requests.current.begin();
    setBusy(true); setError("");
    try {
      const result = await audienceRequest(`/audiences/accounts/${accountId}/memberships?refresh=true`, projectId);
      if (!current()) return;
      setDiscovery(result);
      if (!result.stale && ["available", "empty"].includes(result.status)) {
        const known = new Set(result.levels.map((level: {id: string}) => level.id));
        onChange({type:"members",level_ids:(value?.level_ids || []).filter(id => known.has(id))});
      }
    } catch (error) { if (current()) setError(error instanceof Error ? error.message : "Discovery failed"); }
    finally { if (current()) setBusy(false); }
  };
  return <div className="flex flex-col gap-2">
    <label className="text-xs uppercase text-text-dim">Audience
      <select aria-label="Audience" value={value?.type || "standard"} className="block bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text mt-1"
        onChange={event => onChange({type:event.target.value as "standard"|"members"})}>
        <option value="standard">Standard</option><option value="members">Members</option>
      </select>
    </label>
    {value?.type === "members" && <>
      {capabilities.publishing === "manual" && <p className="text-xs text-warn">Requires completion on the platform. The video is staged as {capabilities.staging_visibility || "restricted"}. {capabilities.staging_visibility === "unlisted" && "Anyone with the link can view it until you set members-only access."} Then confirm completion in Social.</p>}
      {capabilities.discovery === "supported" && <button type="button" disabled={busy} onClick={refresh} className="text-accent text-xs text-left disabled:opacity-50">{busy ? "Discovering tiers…" : "Discover membership tiers"}</button>}
      {error && <p role="alert" className="text-xs text-error">{error}</p>}
      {discovery && <>
        {discovery.reason && <p role="status" className="text-xs text-warn">{discovery.reason}</p>}
        {discovery.status === "empty" && !discovery.stale && <p className="text-xs text-text-dim">No membership tiers were returned.</p>}
        {discovery.stale && <p className="text-xs text-text-dim">Discovery is unavailable or stale. Tier selections require a successful refresh.</p>}
        {!discovery.stale && discovery.status === "available" && capabilities.tier_targeting && <label className="text-xs text-text-dim">Tier
          <select aria-label="Membership tier" multiple value={value.level_ids || []} className="block bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
            onChange={event => onChange({type:"members",level_ids:Array.from(event.target.selectedOptions, option => option.value)})}>
            {discovery.levels.map(level => <option key={level.id} value={level.id}>{level.name || level.id}</option>)}
          </select>
        </label>}
      </>}
      <p className="text-xs text-text-dim">No selected tier means members generally. For a manual workflow, choose the final access tier on the platform.</p>
    </>}
  </div>;
}
export function MembershipConfirmation({ postId, targetId, revision, setupURL, levelIds = [], projectId, onConfirmed }: {
  postId: number; targetId: number; revision: number; setupURL: string; levelIds?: string[];
  projectId?: string | null; onConfirmed: () => void | Promise<void>;
}) {
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async () => {
    if (!confirmed) return;
    setBusy(true); setError("");
    try {
      await audienceRequest("/audiences/confirm",projectId,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({post_id:postId,target_id:targetId,expected_revision:revision,confirmed:true})});
      await onConfirmed();
    } catch (error) { setError(error instanceof Error ? error.message : "Confirmation failed"); }
    finally { setBusy(false); }
  };
  return <div className="flex flex-col gap-2 mt-2 text-xs">
    {setupURL && <a href={setupURL} target="_blank" rel="noreferrer" className="text-accent hover:underline">Open membership settings ↗</a>}
    {levelIds.length > 0 && <p>Requested tier IDs: {levelIds.join(", ")}</p>}
    <label><input type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} /> I set members-only access and the requested tiers on the platform.</label>
    <p className="text-text-dim">Social records your confirmation. It cannot verify membership access through this provider’s API.</p>
    <button type="button" disabled={!confirmed || busy} onClick={submit} className="text-accent text-left disabled:opacity-50">{busy ? "Confirming…" : "Confirm membership setup"}</button>
    {error && <p role="alert" className="text-error">{error}</p>}
  </div>;
}
