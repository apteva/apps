import { useEffect, useState } from "react";

type CallTool = (
  tool: string,
  args: Record<string, unknown>,
) => Promise<Record<string, unknown>>;
type Domain = {
  id: number;
  name: string;
  connection_id?: number;
  connection_mode?: string;
  notes?: string;
  expires_at?: string;
};
type Connection = { id: number; name: string; app_slug: string };

export function DomainSettings({
  domain,
  connections,
  callTool,
  onUpdated,
}: {
  domain: Domain;
  connections: Connection[];
  callTool: CallTool;
  onUpdated: (d: any) => void;
}) {
  const [open, setOpen] = useState(false),
    [notes, setNotes] = useState(domain.notes || ""),
    [pick, setPick] = useState(String(domain.connection_id || 0)),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const update = async (sync = false) => {
    setBusy(true);
    setError("");
    try {
      const r = await callTool(
        sync ? "domain_sync" : "domain_update",
        sync
          ? { name: domain.name }
          : {
              name: domain.name,
              notes,
              ...(Number(pick) !== (domain.connection_id || 0)
                ? { connection_id: Number(pick) }
                : {}),
            },
      );
      onUpdated(r.domain);
      setOpen(false);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mb-3 text-xs space-y-2">
      <div className="flex gap-3">
        <button type="button" onClick={() => setOpen(!open)}>
          Domain settings
        </button>
        <button type="button" disabled={busy} onClick={() => update(true)}>
          Refresh registration expiry
        </button>
        {domain.expires_at && <span>Expires {domain.expires_at}</span>}
      </div>
      {open && (
        <div className="flex flex-wrap gap-2">
          <select
            aria-label="Change DNS connection"
            value={pick}
            onChange={(e) => setPick(e.target.value)}
            disabled={busy}
          >
            <option value="0">Unmanaged / external DNS</option>
            {connections.map((c) => (
              <option key={c.id} value={c.id}>
                {c.app_slug} / {c.name || c.id}
              </option>
            ))}
          </select>
          <input
            aria-label="Domain notes"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            disabled={busy}
          />
          <button disabled={busy} onClick={() => update()}>
            Save domain settings
          </button>
        </div>
      )}
      {error && <div className="text-red-400">{error}</div>}
    </div>
  );
}

export function DNSRecoveryNotice({ callTool }: { callTool: CallTool }) {
  const [recoveries, setRecoveries] = useState<any[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const load = () =>
    callTool("domain_dns_recovery", {})
      .then((r) => setRecoveries((r.recoveries as any[]) || []))
      .catch((e) => setError((e as Error).message));
  useEffect(() => {
    let active = true;
    callTool("domain_dns_recovery", {})
      .then((r) => {
        if (active) setRecoveries((r.recoveries as any[]) || []);
      })
      .catch((e) => {
        if (active) setError((e as Error).message);
      });
    return () => {
      active = false;
    };
  }, [callTool]);
  const inspect = async (id: string) => {
    setBusy(true);
    try {
      const r = await callTool("domain_dns_recovery", { recovery_id: id });
      setError(
        r.status === "unknown"
          ? "Both or neither value is present. Inspect DNS at the provider before changing records."
          : "",
      );
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  if (!recoveries.length && !error) return null;
  return (
    <div className="p-3 border border-yellow-500 text-sm">
      <div>DNS operations requiring review</div>
      {recoveries.map((r) => (
        <div key={r.recovery_id}>
          {r.domain} / connection {r.connection_id}: {r.error}
          <button
            disabled={busy}
            onClick={() => inspect(r.recovery_id)}
            className="ml-2 text-accent"
          >
            Recheck provider state
          </button>
        </div>
      ))}
      {error && <div>{error}</div>}
    </div>
  );
}
