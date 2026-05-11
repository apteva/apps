// VPNPanel — monitor-only surface for the VPN sidecar.
//
// Layout:
//   ┌─────────────────────────────────────────────────────────────┐
//   │  ● WireGuard installed                                      │
//   │  203.0.113.7:51820  ·  host #0  ·  installed 4 days ago     │
//   │  3 active peers  ·  1 revoked  ·  polled 9s ago              │
//   ├─────────────────────────────────────────────────────────────┤
//   │  Peers                                                       │
//   │  marco-macbook   10.13.13.5/32   12s ago   4.2 MB / 1.1 MB  │
//   │  marco-iphone    10.13.13.6/32   idle 4m   188 KB / 22 KB   │
//   │  contractor      10.13.13.7/32   never     —     —          │
//   └─────────────────────────────────────────────────────────────┘
//
// Peer add/remove is intentionally absent — those are agent-driven
// via MCP tools (vpn_peer_add / vpn_peer_remove) so the conversation
// stays the source of truth for who has access.

import { useCallback, useEffect, useState } from "react";

const API = "/api/apps/vpn";
const POLL_MS = 10_000;

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Status {
  installed: boolean;
  backend?: string;
  host_id?: number;
  endpoint?: string;
  listen_port?: number;
  public_key?: string;
  network_cidr?: string;
  installed_at?: number;
  active_peers: number;
  revoked_peers: number;
  last_poll_at?: number;
  last_poll_ok: boolean;
}

interface Peer {
  id: number;
  name: string;
  public_key: string;
  address: string;
  allowed_ips: string;
  dns: string;
  created_at: number;
  revoked_at: number;
  last_handshake_at: number;
  rx_bytes: number;
  tx_bytes: number;
}

export default function VPNPanel({}: NativePanelProps) {
  const [status, setStatus] = useState<Status | null>(null);
  const [peers, setPeers] = useState<Peer[]>([]);
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      const [s, p] = await Promise.all([
        fetch(`${API}/status`).then((r) => r.json()),
        fetch(`${API}/peers?include_revoked=${includeRevoked}`).then((r) => r.json()),
      ]);
      setStatus(s);
      setPeers(p.peers || []);
      setError("");
    } catch (e: any) {
      setError(e?.message || String(e));
    }
  }, [includeRevoked]);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, POLL_MS);
    return () => clearInterval(t);
  }, [refresh]);

  if (!status) {
    return (
      <div className="p-6 text-sm text-[var(--apt-text-muted)]">
        {error ? `Error: ${error}` : "Loading…"}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4 p-4">
      <ServerCard status={status} />
      <PeersCard
        peers={peers}
        includeRevoked={includeRevoked}
        onToggleRevoked={() => setIncludeRevoked((v) => !v)}
      />
      {error ? <div className="text-sm text-[var(--apt-error)]">Error: {error}</div> : null}
    </div>
  );
}

// ─── Server card ────────────────────────────────────────────────────

function ServerCard({ status }: { status: Status }) {
  if (!status.installed) {
    return (
      <section className="rounded-lg border border-[var(--apt-border)] p-4">
        <header className="flex items-center gap-2">
          <StatusDot ok={false} />
          <h2 className="text-base font-semibold">VPN not installed</h2>
        </header>
        <p className="mt-2 text-sm text-[var(--apt-text-muted)]">
          Run the <code>vpn_install</code> tool to provision the daemon on the bound Instances host.
        </p>
      </section>
    );
  }

  const installedAgo = status.installed_at ? relative(status.installed_at) : "—";
  const polledAgo = status.last_poll_at ? relative(status.last_poll_at) : "never";

  return (
    <section className="rounded-lg border border-[var(--apt-border)] p-4">
      <header className="flex items-center gap-2">
        <StatusDot ok={status.last_poll_ok} />
        <h2 className="text-base font-semibold capitalize">
          {status.backend ?? "vpn"} installed
        </h2>
      </header>
      <dl className="mt-3 grid grid-cols-1 gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
        <Row label="Endpoint" value={status.endpoint} mono />
        <Row label="Host" value={`#${status.host_id}`} />
        <Row label="Network" value={status.network_cidr} mono />
        <Row label="Listen port" value={String(status.listen_port ?? "")} />
        <Row label="Installed" value={installedAgo} />
        <Row label="Last poll" value={polledAgo + (status.last_poll_ok ? "" : " (failed)")} />
        <Row label="Active peers" value={String(status.active_peers)} />
        <Row label="Revoked peers" value={String(status.revoked_peers)} />
      </dl>
    </section>
  );
}

function Row({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <dt className="w-28 shrink-0 text-[var(--apt-text-muted)]">{label}</dt>
      <dd className={mono ? "font-mono break-all" : "break-words"}>{value || "—"}</dd>
    </div>
  );
}

// ─── Peers card ─────────────────────────────────────────────────────

function PeersCard({
  peers,
  includeRevoked,
  onToggleRevoked,
}: {
  peers: Peer[];
  includeRevoked: boolean;
  onToggleRevoked: () => void;
}) {
  return (
    <section className="rounded-lg border border-[var(--apt-border)] p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Peers</h2>
        <label className="flex items-center gap-2 text-sm text-[var(--apt-text-muted)]">
          <input
            type="checkbox"
            checked={includeRevoked}
            onChange={onToggleRevoked}
          />
          Show revoked
        </label>
      </header>

      {peers.length === 0 ? (
        <p className="mt-3 text-sm text-[var(--apt-text-muted)]">
          No peers yet. Ask the agent to add one with <code>vpn_peer_add</code>.
        </p>
      ) : (
        <table className="mt-3 w-full text-sm">
          <thead className="text-left text-[var(--apt-text-muted)]">
            <tr>
              <th className="py-1 pr-3 font-medium">Name</th>
              <th className="py-1 pr-3 font-medium">Address</th>
              <th className="py-1 pr-3 font-medium">Last handshake</th>
              <th className="py-1 pr-3 font-medium">Rx</th>
              <th className="py-1 font-medium">Tx</th>
            </tr>
          </thead>
          <tbody>
            {peers.map((p) => (
              <PeerRow key={p.id} peer={p} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function PeerRow({ peer }: { peer: Peer }) {
  const revoked = peer.revoked_at > 0;
  const handshake = peer.last_handshake_at
    ? relative(peer.last_handshake_at)
    : "never";
  return (
    <tr className={revoked ? "opacity-50" : ""}>
      <td className="py-1 pr-3">
        <span className="flex items-center gap-2">
          <PeerDot peer={peer} />
          <span className="break-all">{peer.name}</span>
          {revoked ? <span className="text-xs">(revoked)</span> : null}
        </span>
      </td>
      <td className="py-1 pr-3 font-mono">{peer.address}</td>
      <td className="py-1 pr-3">{handshake}</td>
      <td className="py-1 pr-3 font-mono">{humanBytes(peer.rx_bytes)}</td>
      <td className="py-1 font-mono">{humanBytes(peer.tx_bytes)}</td>
    </tr>
  );
}

// ─── icons / dots ───────────────────────────────────────────────────
//
// SVG attrs + CSS vars only — no Tailwind color utilities inside the
// SVG markup. The dashboard's JIT doesn't scan apps/mcp/*/ui/, so any
// `bg-green-500` here would silently fall through to no styling.

function StatusDot({ ok }: { ok: boolean }) {
  return (
    <svg
      width="10"
      height="10"
      viewBox="0 0 10 10"
      aria-label={ok ? "healthy" : "degraded"}
    >
      <circle
        cx="5"
        cy="5"
        r="4"
        fill={ok ? "var(--apt-success, #22c55e)" : "var(--apt-warn, #f59e0b)"}
      />
    </svg>
  );
}

function PeerDot({ peer }: { peer: Peer }) {
  const now = Math.floor(Date.now() / 1000);
  let color = "var(--apt-text-muted, #94a3b8)"; // never handshook
  if (peer.revoked_at > 0) {
    color = "var(--apt-error, #ef4444)";
  } else if (peer.last_handshake_at > 0) {
    const age = now - peer.last_handshake_at;
    // WireGuard's default rekey is 120s; ~180s is the "definitely
    // idle / probably-disconnected" threshold most dashboards use.
    color = age < 180 ? "var(--apt-success, #22c55e)" : "var(--apt-text-muted, #94a3b8)";
  }
  return (
    <svg width="8" height="8" viewBox="0 0 8 8">
      <circle cx="4" cy="4" r="3" fill={color} />
    </svg>
  );
}

// ─── formatting ────────────────────────────────────────────────────

function relative(unixSec: number): string {
  if (!unixSec) return "never";
  const diff = Math.floor(Date.now() / 1000) - unixSec;
  if (diff < 5) return "just now";
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86_400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86_400)}d ago`;
}

function humanBytes(n: number): string {
  if (!n) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
