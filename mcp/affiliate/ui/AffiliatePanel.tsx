import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/affiliate";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Network {
  id: number;
  key: string;
  name: string;
  enabled: boolean;
  last_refreshed_at?: string;
}

interface Offer {
  id: number;
  network_key: string;
  external_id: string;
  merchant_name: string;
  offer_name: string;
  status: string;
  category?: string;
  commission_summary?: string;
  cookie_window?: string;
  tracking_deeplink: boolean;
}

interface Link {
  id: number;
  network_key: string;
  offer_id?: number;
  destination_url: string;
  affiliate_url: string;
  short_url?: string;
  campaign?: string;
  status: string;
}

interface StatRow {
  date?: string;
  network_key?: string;
  offer_id?: number;
  link_id?: number;
  clicks: number;
  conversions: number;
  revenue_cents: number;
  commission_cents: number;
  currency: string;
}

type Tab = "networks" | "offers" | "links" | "stats";

const INPUT_CLASS = "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm";

export default function AffiliatePanel({}: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("offers");
  const [networks, setNetworks] = useState<Network[]>([]);
  const [offers, setOffers] = useState<Offer[]>([]);
  const [links, setLinks] = useState<Link[]>([]);
  const [stats, setStats] = useState<StatRow[]>([]);
  const [status, setStatus] = useState("");
  const [q, setQ] = useState("");
  const [network, setNetwork] = useState("");
  const [linkingOffer, setLinkingOffer] = useState<Offer | null>(null);

  const loadNetworks = useCallback(async () => {
    const res = await fetch(`${API}/networks`, { credentials: "same-origin" });
    const data = await res.json();
    setNetworks(data.networks || []);
  }, []);

  const loadOffers = useCallback(async () => {
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (network) params.set("network", network);
    const res = await fetch(`${API}/offers?${params}`, { credentials: "same-origin" });
    const data = await res.json();
    setOffers(data.offers || []);
  }, [q, network]);

  const loadLinks = useCallback(async () => {
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (network) params.set("network", network);
    const res = await fetch(`${API}/links?${params}`, { credentials: "same-origin" });
    const data = await res.json();
    setLinks(data.links || []);
  }, [q, network]);

  const loadStats = useCallback(async () => {
    const params = new URLSearchParams({ group_by: "day" });
    if (network) params.set("network", network);
    const res = await fetch(`${API}/stats?${params}`, { credentials: "same-origin" });
    const data = await res.json();
    setStats(data.stats || []);
  }, [network]);

  const refreshCurrent = useCallback(async () => {
    try {
      setStatus("Refreshing...");
      await Promise.all([loadNetworks(), loadOffers(), loadLinks(), loadStats()]);
      setStatus("");
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [loadNetworks, loadOffers, loadLinks, loadStats]);

  useEffect(() => { refreshCurrent(); }, [refreshCurrent]);

  const refreshNetwork = async (key: string, kind = "all") => {
    setStatus(`Refreshing ${key}...`);
    const res = await fetch(`${API}/refresh`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ network: key, kind }),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    const data = await res.json();
    setStatus(`${data.offers_upserted || 0} offers, ${data.stats_days_upserted || 0} stat rows`);
    refreshCurrent();
  };

  const visibleTabs: { key: Tab; label: string }[] = [
    { key: "networks", label: "Networks" },
    { key: "offers", label: "Offers" },
    { key: "links", label: "Links" },
    { key: "stats", label: "Stats" },
  ];

  return (
    <div className="h-full flex flex-col text-sm">
      <header className="border-b border-border px-4 py-2 flex items-center gap-3">
        <div className="font-medium text-text">Affiliate</div>
        <div className="flex rounded border border-border overflow-hidden">
          {visibleTabs.map((t) => (
            <button
              key={t.key}
              onClick={() => setTab(t.key)}
              className={`px-3 py-1 text-xs ${tab === t.key ? "bg-accent text-bg" : "text-text-muted hover:bg-bg-elev"}`}
            >
              {t.label}
            </button>
          ))}
        </div>
        <select
          value={network}
          onChange={(e) => setNetwork(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
        >
          <option value="">All networks</option>
          {networks.map((n) => <option key={n.key} value={n.key}>{n.name}</option>)}
        </select>
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search"
          className="bg-bg-input border border-border rounded px-2 py-1 text-xs w-48"
        />
        <button onClick={refreshCurrent} className="px-2 py-1 border border-border rounded text-xs">
          Refresh
        </button>
        <span className="ml-auto text-xs text-text-dim truncate">{status}</span>
      </header>

      <main className="flex-1 overflow-auto">
        {tab === "networks" && <NetworksView networks={networks} onRefresh={refreshNetwork} />}
        {tab === "offers" && <OffersView offers={offers} onCreateLink={setLinkingOffer} />}
        {tab === "links" && <LinksView links={links} />}
        {tab === "stats" && <StatsView stats={stats} />}
      </main>

      {linkingOffer && (
        <CreateLinkDialog
          offer={linkingOffer}
          onClose={() => setLinkingOffer(null)}
          onDone={() => { setLinkingOffer(null); loadLinks(); setTab("links"); }}
        />
      )}
    </div>
  );
}

function NetworksView({ networks, onRefresh }: { networks: Network[]; onRefresh: (key: string, kind?: string) => void }) {
  const known = useMemo(() => {
    const seen = new Set(networks.map((n) => n.key));
    return [
      "target-circle", "impact", "awin", "cj-affiliate", "amazon-associates", "skimlinks", "sovrn", "partnerstack", "shareasale",
    ].filter((k) => !seen.has(k)).map((key) => ({ id: 0, key, name: labelNetwork(key), enabled: false }));
  }, [networks]);
  const rows = [...networks, ...known];
  return (
    <Table empty="No networks have been refreshed yet.">
      <thead>
        <tr className="text-left text-xs text-text-dim border-b border-border">
          <th className="px-3 py-2">Network</th>
          <th className="px-3 py-2">State</th>
          <th className="px-3 py-2">Last refresh</th>
          <th className="px-3 py-2 text-right">Actions</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((n) => (
          <tr key={n.key} className="border-b border-border last:border-b-0">
            <td className="px-3 py-2 font-medium">{n.name}</td>
            <td className="px-3 py-2 text-text-dim">{n.enabled ? "Enabled" : "Available"}</td>
            <td className="px-3 py-2 text-text-dim">{n.last_refreshed_at ? timeAgo(n.last_refreshed_at) : "-"}</td>
            <td className="px-3 py-2 text-right">
              <button onClick={() => onRefresh(n.key)} className="px-2 py-1 border border-border rounded text-xs">
                Refresh
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function OffersView({ offers, onCreateLink }: { offers: Offer[]; onCreateLink: (offer: Offer) => void }) {
  return (
    <Table empty="No offers found. Refresh a network or adjust the search.">
      <thead>
        <tr className="text-left text-xs text-text-dim border-b border-border">
          <th className="px-3 py-2">Merchant</th>
          <th className="px-3 py-2">Offer</th>
          <th className="px-3 py-2">Network</th>
          <th className="px-3 py-2">Status</th>
          <th className="px-3 py-2">Commission</th>
          <th className="px-3 py-2 text-right">Actions</th>
        </tr>
      </thead>
      <tbody>
        {offers.map((o) => (
          <tr key={o.id} className="border-b border-border last:border-b-0">
            <td className="px-3 py-2 font-medium">{o.merchant_name}</td>
            <td className="px-3 py-2 text-text-muted">{o.offer_name || "-"}</td>
            <td className="px-3 py-2 text-text-dim">{labelNetwork(o.network_key)}</td>
            <td className="px-3 py-2 text-text-dim">{o.status || "-"}</td>
            <td className="px-3 py-2 text-text-dim max-w-[260px] truncate" title={o.commission_summary}>{o.commission_summary || "-"}</td>
            <td className="px-3 py-2 text-right">
              <button onClick={() => onCreateLink(o)} className="px-2 py-1 bg-accent text-bg rounded text-xs">
                Link
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function LinksView({ links }: { links: Link[] }) {
  return (
    <Table empty="No links created yet.">
      <thead>
        <tr className="text-left text-xs text-text-dim border-b border-border">
          <th className="px-3 py-2">Destination</th>
          <th className="px-3 py-2">Affiliate URL</th>
          <th className="px-3 py-2">Short URL</th>
          <th className="px-3 py-2">Campaign</th>
          <th className="px-3 py-2">Status</th>
        </tr>
      </thead>
      <tbody>
        {links.map((l) => (
          <tr key={l.id} className="border-b border-border last:border-b-0">
            <td className="px-3 py-2 max-w-[280px] truncate" title={l.destination_url}>{l.destination_url}</td>
            <td className="px-3 py-2 max-w-[280px] truncate text-text-dim" title={l.affiliate_url}>{l.affiliate_url}</td>
            <td className="px-3 py-2 max-w-[220px] truncate">{l.short_url || "-"}</td>
            <td className="px-3 py-2 text-text-dim">{l.campaign || "-"}</td>
            <td className="px-3 py-2 text-text-dim">{l.status}</td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function StatsView({ stats }: { stats: StatRow[] }) {
  return (
    <Table empty="No stats imported yet.">
      <thead>
        <tr className="text-left text-xs text-text-dim border-b border-border">
          <th className="px-3 py-2">Date</th>
          <th className="px-3 py-2 text-right">Clicks</th>
          <th className="px-3 py-2 text-right">Conversions</th>
          <th className="px-3 py-2 text-right">Revenue</th>
          <th className="px-3 py-2 text-right">Commission</th>
        </tr>
      </thead>
      <tbody>
        {stats.map((s, i) => (
          <tr key={`${s.date}-${i}`} className="border-b border-border last:border-b-0">
            <td className="px-3 py-2">{s.date || "-"}</td>
            <td className="px-3 py-2 text-right">{s.clicks}</td>
            <td className="px-3 py-2 text-right">{s.conversions}</td>
            <td className="px-3 py-2 text-right">{money(s.revenue_cents, s.currency)}</td>
            <td className="px-3 py-2 text-right">{money(s.commission_cents, s.currency)}</td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function CreateLinkDialog({ offer, onClose, onDone }: { offer: Offer; onClose: () => void; onDone: () => void }) {
  const [url, setUrl] = useState("");
  const [campaign, setCampaign] = useState("");
  const [subid, setSubid] = useState("");
  const [shorten, setShorten] = useState(true);
  const [shortHostname, setShortHostname] = useState("");
  const [shortPath, setShortPath] = useState(`/${slug(offer.merchant_name)}`);
  const [status, setStatus] = useState("");

  const submit = async () => {
    setStatus("Creating...");
    const res = await fetch(`${API}/links`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        url,
        network: offer.network_key,
        offer_id: offer.id,
        campaign,
        subid,
        shorten,
        short_hostname: shortHostname,
        short_path: shortPath,
      }),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    onDone();
  };

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50" onClick={onClose}>
      <div className="bg-bg border border-border rounded-lg p-4 w-[560px] max-w-full" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center mb-3">
          <div className="font-medium flex-1">Create link for {offer.merchant_name}</div>
          <button onClick={onClose} className="text-text-dim hover:text-text">x</button>
        </div>
        <div className="space-y-3">
          <Field label="Destination URL">
            <input value={url} onChange={(e) => setUrl(e.target.value)} autoFocus className={INPUT_CLASS} placeholder="https://merchant.com/product" />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Campaign">
              <input value={campaign} onChange={(e) => setCampaign(e.target.value)} className={INPUT_CLASS} />
            </Field>
            <Field label="Sub ID">
              <input value={subid} onChange={(e) => setSubid(e.target.value)} className={INPUT_CLASS} />
            </Field>
          </div>
          <label className="flex items-center gap-2 text-sm text-text-muted">
            <input type="checkbox" checked={shorten} onChange={(e) => setShorten(e.target.checked)} />
            Create branded redirect
          </label>
          {shorten && (
            <div className="grid grid-cols-2 gap-3">
              <Field label="Short hostname">
                <input value={shortHostname} onChange={(e) => setShortHostname(e.target.value)} className={INPUT_CLASS} placeholder="go.example.com" />
              </Field>
              <Field label="Short path">
                <input value={shortPath} onChange={(e) => setShortPath(e.target.value)} className={INPUT_CLASS} />
              </Field>
            </div>
          )}
          {status && <div className="text-xs text-text-dim">{status}</div>}
          <div className="flex justify-end gap-2 pt-2">
            <button onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
            <button onClick={submit} disabled={!url} className="px-3 py-1.5 bg-accent text-bg rounded text-sm disabled:opacity-50">
              Create
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}</div>
      {children}
    </label>
  );
}

function Table({ empty, children }: { empty: string; children: React.ReactNode }) {
  const hasRows = String(children).length >= 0;
  return (
    <div className="p-4">
      <div className="border border-border rounded overflow-hidden">
        <table className="w-full border-collapse">{children}</table>
      </div>
      {!hasRows && <div className="text-center text-text-dim py-8">{empty}</div>}
    </div>
  );
}

function labelNetwork(key: string): string {
  const labels: Record<string, string> = {
    "target-circle": "Target Circle (Circlewise)",
    circlewise: "Target Circle (Circlewise)",
    impact: "Impact",
    awin: "Awin",
    "cj-affiliate": "CJ Affiliate",
    cj: "CJ Affiliate",
    "amazon-associates": "Amazon Associates",
    skimlinks: "Skimlinks",
    sovrn: "Sovrn",
    partnerstack: "PartnerStack",
    shareasale: "ShareASale",
  };
  return labels[key] || key;
}

function timeAgo(value: string): string {
  const t = new Date(value).getTime();
  if (!Number.isFinite(t)) return "-";
  const seconds = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

function money(cents: number, currency: string): string {
  return `${currency || "USD"} ${(cents / 100).toFixed(2)}`;
}

function slug(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "") || "affiliate-link";
}
