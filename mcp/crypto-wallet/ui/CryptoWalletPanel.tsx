// CryptoWalletPanel: the people-only side of Crypto Wallet. Create wallets,
// set policies, approve or reject transfers and recipients, freeze, read the
// log, export a key. Styled with the dashboard's theme tokens.
//
// Talks to /api/apps/crypto-wallet/api/* through the platform proxy, which
// attaches the signed-in person's identity to every request.

import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps { appName: string; installId: number; projectId: string; instanceId?: number }

interface Wallet { id: number; name: string; chain: string; chain_name?: string; testnet?: boolean; address: string; frozen: boolean; frozen_reason?: string; pending?: number; created_at: string }
interface Policy { version: number; per_tx_usd_micros: number; daily_usd_micros: number; approval_usd_micros: number; tokens: string[]; new_recipient_delay_hours: number; created_at: string }
interface Recipient { id: number; address: string; label: string; reason?: string; status: string; proposed_by_agent?: number; usable_after?: string; created_at: string }
interface Proposal { id: number; agent_id?: number; caller_app?: string; token: string; to_address: string; amount: string; usd_micros: number; memo?: string; state: string; refusal_rule?: string; refusal_detail?: string; tx_hash?: string; error?: string; created_at: string }
interface ChainTx { id: number; direction: string; token: string; hash: string; from: string; to: string; amount?: string; status: string; created_at: string }
interface LedgerEntry { id: number; kind: string; actor: string; proposal_id?: number; data: Record<string, unknown>; created_at: string }
interface ChainMeta { slug: string; name: string; chain_id: number; testnet: boolean; explorer: string }
interface Detail { wallet: Wallet; chain: ChainMeta; policy: Policy; recipients: Recipient[]; proposals: Proposal[]; transactions: ChainTx[]; ledger: LedgerEntry[]; spent_24h_usd_micros: number }
interface Overview { master_key_configured: boolean; wallets: Wallet[]; chains: { slug: string; name: string; testnet: boolean }[] }
interface Balance { token: string; amount: string; usd?: string }

const API = "/api/apps/crypto-wallet/api";
const TOKENS = ["USDC", "ETH"];
type Tab = "approvals" | "activity" | "recipients" | "policy" | "log" | "key";

const usd = (micros: number) => `$${(micros / 1_000_000).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
const short = (a: string) => (a && a.length > 12 ? `${a.slice(0, 6)}...${a.slice(-4)}` : a);
const when = (s: string) => { const d = new Date(s); return Number.isNaN(d.getTime()) ? s : d.toLocaleString(); };

const stateTone: Record<string, string> = {
  confirmed: "text-green", sent: "text-accent", sending: "text-accent", pending_approval: "text-text",
  refused: "text-error", failed: "text-error", rejected: "text-text-muted",
};

function Button({ children, onClick, tone = "default", disabled }: { children: React.ReactNode; onClick: () => void; tone?: "default" | "primary" | "danger"; disabled?: boolean }) {
  const cls = tone === "primary" ? "bg-accent text-bg hover:bg-accent" : tone === "danger" ? "border border-error/30 text-error hover:bg-error/10" : "border border-border text-text hover:bg-bg-hover";
  return <button type="button" disabled={disabled} onClick={onClick} className={`px-3 py-1 rounded text-sm font-medium ${cls} ${disabled ? "opacity-50" : ""}`}>{children}</button>;
}

function Field({ label, children, hint }: { label: string; children: React.ReactNode; hint?: string }) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-text-muted text-xs">{label}</span>
      {children}
      {hint ? <span className="text-text-dim text-xs">{hint}</span> : null}
    </label>
  );
}

const inputCls = "bg-bg-input border border-border rounded px-3 py-2 text-sm text-text";

function Section({ title, children, right }: { title: string; children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <div className="bg-bg-card border border-border rounded-lg p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between gap-3">
        <div className="text-sm font-semibold text-text">{title}</div>
        {right}
      </div>
      {children}
    </div>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return <div className="text-sm text-text-muted py-2">{children}</div>;
}

export default function CryptoWalletPanel({ projectId, installId }: NativePanelProps) {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [balances, setBalances] = useState<Balance[] | null>(null);
  const [balanceError, setBalanceError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("approvals");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [generatedKey, setGeneratedKey] = useState<string | null>(null);

  const qs = useMemo(() => new URLSearchParams({ project_id: projectId, install_id: String(installId) }).toString(), [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
    const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
    if (body !== undefined) {
      (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(`${API}${path}?${qs}`, opts);
    const text = await res.text();
    let data: unknown = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = null; }
    if (!res.ok) throw new Error((data as { error?: string })?.error || `${res.status} ${res.statusText}`);
    return data as T;
  }, [qs]);

  const loadOverview = useCallback(async () => {
    try {
      const o = await api<Overview>("GET", "/overview");
      setOverview(o);
      setSelected((cur) => cur ?? (o.wallets[0]?.id ?? null));
      setError(null);
    } catch (e) { setError((e as Error).message); }
  }, [api]);

  const loadDetail = useCallback(async (id: number) => {
    try { setDetail(await api<Detail>("GET", `/wallets/${id}`)); setError(null); }
    catch (e) { setError((e as Error).message); }
  }, [api]);

  const loadBalances = useCallback(async (id: number) => {
    setBalances(null); setBalanceError(null);
    try { setBalances((await api<{ balances: Balance[] }>("GET", `/wallets/${id}/balances`)).balances); }
    catch (e) { setBalanceError((e as Error).message); }
  }, [api]);

  useEffect(() => { loadOverview(); }, [loadOverview]);
  useEffect(() => {
    if (selected == null) { setDetail(null); return; }
    loadDetail(selected); loadBalances(selected);
    const t = setInterval(() => loadDetail(selected), 15000);
    return () => clearInterval(t);
  }, [selected, loadDetail, loadBalances]);

  const act = useCallback(async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try { await fn(); setError(null); await loadOverview(); if (selected != null) await loadDetail(selected); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }, [loadOverview, loadDetail, selected]);

  if (!overview) {
    return <div className="p-4 text-sm text-text-muted">{error ? <span className="text-error">{error}</span> : "Loading..."}</div>;
  }

  return (
    <div className="flex flex-col gap-4 p-4 max-w-5xl">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-lg font-semibold text-text">Crypto Wallet</div>
          <div className="text-xs text-text-muted">Company funds only. Agents propose; people create wallets, set policies and approve.</div>
        </div>
        {overview.master_key_configured ? <Button tone="primary" onClick={() => setCreating((v) => !v)}>New wallet</Button> : null}
      </div>

      {error ? <div className="bg-error/10 border border-error/30 rounded px-3 py-2 text-sm text-error">{error}</div> : null}

      {!overview.master_key_configured ? (
        <Section title="Set a master key first">
          <div className="text-sm text-text">Wallet keys are encrypted with a master key that the platform stores in this app's settings, outside the app's database. A database backup alone then can't spend anything.</div>
          <ol className="text-sm text-text-muted flex flex-col gap-1">
            <li>1. Generate a key below and copy it somewhere safe and offline.</li>
            <li>2. Paste it into this app's Master key setting and save. The app restarts.</li>
            <li>3. Keep that key: if it is lost or changed, existing wallets cannot sign again.</li>
          </ol>
          {generatedKey ? (
            <div className="bg-bg-input border border-border rounded px-3 py-2 font-mono text-sm text-text select-all">{generatedKey}</div>
          ) : (
            <div><Button tone="primary" disabled={busy} onClick={() => act(async () => setGeneratedKey((await api<{ master_key: string }>("POST", "/master-key/generate")).master_key))}>Generate master key</Button></div>
          )}
        </Section>
      ) : null}

      {creating ? <CreateWallet chains={overview.chains} busy={busy} onCancel={() => setCreating(false)} onCreate={(name, chain) => act(async () => {
        const w = await api<Wallet>("POST", "/wallets", { name, chain });
        setCreating(false); setSelected(w.id);
      })} /> : null}

      {overview.wallets.length === 0 ? (
        overview.master_key_configured ? <Empty>No wallets yet. Start with one on Base Sepolia, the test network, where tokens have no value.</Empty> : null
      ) : (
        <div className="flex gap-2 flex-wrap">
          {overview.wallets.map((w) => (
            <button key={w.id} type="button" onClick={() => { setSelected(w.id); setTab("approvals"); }}
              className={`px-3 py-2 rounded-lg border text-left ${selected === w.id ? "border-accent bg-bg-card" : "border-border hover:bg-bg-hover"}`}>
              <div className="text-sm font-medium text-text">{w.name}{w.pending ? <span className="text-accent"> ({w.pending} waiting)</span> : null}</div>
              <div className="text-xs text-text-muted">{w.chain_name}{w.frozen ? " · frozen" : ""}</div>
            </button>
          ))}
        </div>
      )}

      {detail && selected === detail.wallet.id ? (
        <WalletView detail={detail} balances={balances} balanceError={balanceError} busy={busy} tab={tab} setTab={setTab}
          refreshBalances={() => loadBalances(detail.wallet.id)} api={api} act={act} />
      ) : null}
    </div>
  );
}

function CreateWallet({ chains, busy, onCancel, onCreate }: { chains: Overview["chains"]; busy: boolean; onCancel: () => void; onCreate: (name: string, chain: string) => void }) {
  const [name, setName] = useState("");
  const [chain, setChain] = useState(chains[0]?.slug || "base-sepolia");
  return (
    <Section title="New wallet">
      <div className="grid grid-cols-2 gap-3">
        <Field label="Name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} placeholder="Operations" /></Field>
        <Field label="Chain" hint={chains.find((c) => c.slug === chain)?.testnet ? "Test network: tokens have no value." : "Real money. Fund it with small amounts only."}>
          <select className={inputCls} value={chain} onChange={(e) => setChain(e.target.value)}>
            {chains.map((c) => <option key={c.slug} value={c.slug}>{c.name}</option>)}
          </select>
        </Field>
      </div>
      <div className="flex gap-2">
        <Button tone="primary" disabled={busy || !name.trim()} onClick={() => onCreate(name.trim(), chain)}>Create</Button>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
    </Section>
  );
}

interface WalletViewProps {
  detail: Detail; balances: Balance[] | null; balanceError: string | null; busy: boolean; tab: Tab; setTab: (t: Tab) => void;
  refreshBalances: () => void; api: <T>(method: string, path: string, body?: unknown) => Promise<T>; act: (fn: () => Promise<unknown>) => Promise<void>;
}

function WalletView({ detail, balances, balanceError, busy, tab, setTab, refreshBalances, api, act }: WalletViewProps) {
  const { wallet, chain, policy } = detail;
  const pendingTransfers = detail.proposals.filter((p) => p.state === "pending_approval");
  const pendingRecipients = detail.recipients.filter((r) => r.status === "pending");
  const waiting = pendingTransfers.length + pendingRecipients.length;
  const tabs: [Tab, string][] = [["approvals", waiting ? `Approvals (${waiting})` : "Approvals"], ["activity", "Activity"], ["recipients", "Recipients"], ["policy", "Policy"], ["log", "Log"], ["key", "Key"]];
  const spentShare = Math.min(100, Math.round((detail.spent_24h_usd_micros / policy.daily_usd_micros) * 100));

  return (
    <div className="flex flex-col gap-4">
      <Section title={wallet.name} right={
        wallet.frozen
          ? <Button disabled={busy} onClick={() => act(() => api("POST", `/wallets/${wallet.id}/freeze`, { frozen: false }))}>Unfreeze</Button>
          : <Button tone="danger" disabled={busy} onClick={() => { const reason = window.prompt("Freeze this wallet? Every outgoing transfer stops until someone unfreezes it. Reason:"); if (reason !== null) act(() => api("POST", `/wallets/${wallet.id}/freeze`, { frozen: true, reason })); }}>Freeze</Button>
      }>
        {wallet.frozen ? <div className="bg-error/10 border border-error/30 rounded px-3 py-2 text-sm text-error">Frozen{wallet.frozen_reason ? `: ${wallet.frozen_reason}` : ""}. No transfer can leave this wallet.</div> : null}
        <div className="flex flex-col gap-1">
          <div className="text-xs text-text-muted">Receive address on {chain.name}</div>
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm text-text select-all truncate">{wallet.address}</span>
            <Button onClick={() => navigator.clipboard?.writeText(wallet.address)}>Copy</Button>
            <a className="text-sm text-accent" href={`${chain.explorer}/address/${wallet.address}`} target="_blank" rel="noreferrer">Explorer</a>
          </div>
          <div className="text-xs text-text-dim">Send only on {chain.name} (chain id {chain.chain_id}). Funds sent on another network are lost.</div>
        </div>
        <div className="grid grid-cols-3 gap-3">
          {balances ? balances.map((b) => (
            <div key={b.token} className="bg-bg-input border border-border rounded-lg px-3 py-2">
              <div className="text-xs text-text-muted">{b.token}</div>
              <div className="text-base font-semibold text-text">{b.amount}</div>
              <div className="text-xs text-text-muted">{b.usd || "price unavailable"}</div>
            </div>
          )) : <div className="text-sm text-text-muted">{balanceError ? <span className="text-error">{balanceError}</span> : "Reading balances..."}</div>}
          <div className="bg-bg-input border border-border rounded-lg px-3 py-2">
            <div className="text-xs text-text-muted">Sent in the last 24 hours</div>
            <div className="text-base font-semibold text-text">{usd(detail.spent_24h_usd_micros)}</div>
            <div className="text-xs text-text-muted">{spentShare}% of the {usd(policy.daily_usd_micros)} daily cap</div>
          </div>
        </div>
        <div><Button onClick={refreshBalances}>Refresh balances</Button></div>
      </Section>

      <div className="flex gap-1 border-b border-border">
        {tabs.map(([id, label]) => (
          <button key={id} type="button" onClick={() => setTab(id)}
            className={`px-3 py-2 text-sm ${tab === id ? "text-text font-medium border-b border-accent" : "text-text-muted hover:text-text"}`}>{label}</button>
        ))}
      </div>

      {tab === "approvals" ? <Approvals transfers={pendingTransfers} recipients={pendingRecipients} chain={chain} busy={busy} api={api} act={act} delay={policy.new_recipient_delay_hours} /> : null}
      {tab === "activity" ? <Activity proposals={detail.proposals} txs={detail.transactions} chain={chain} /> : null}
      {tab === "recipients" ? <Recipients wallet={wallet} recipients={detail.recipients} busy={busy} api={api} act={act} /> : null}
      {tab === "policy" ? <PolicyForm key={policy.version} wallet={wallet} policy={policy} busy={busy} api={api} act={act} /> : null}
      {tab === "log" ? <Log entries={detail.ledger} /> : null}
      {tab === "key" ? <KeyExport wallet={wallet} api={api} /> : null}
    </div>
  );
}

function Approvals({ transfers, recipients, chain, busy, api, act, delay }: { transfers: Proposal[]; recipients: Recipient[]; chain: ChainMeta; busy: boolean; delay: number } & Pick<WalletViewProps, "api" | "act">) {
  return (
    <div className="flex flex-col gap-4">
      <Section title="Transfers waiting for approval">
        {transfers.length === 0 ? <Empty>Nothing waiting.</Empty> : transfers.map((p) => (
          <div key={p.id} className="border border-border rounded-lg p-3 flex flex-col gap-2">
            <div className="text-sm text-text"><span className="font-semibold">{p.amount} {p.token}</span> <span className="text-text-muted">({usd(p.usd_micros)})</span> to <span className="font-mono">{p.to_address}</span></div>
            <div className="text-xs text-text-muted">Proposal #{p.id} by {p.agent_id ? `agent ${p.agent_id}` : p.caller_app || "unknown"} · {when(p.created_at)}</div>
            {p.memo ? <div className="text-sm text-text">Reason given: {p.memo}</div> : null}
            <div className="flex gap-2">
              <Button tone="primary" disabled={busy} onClick={() => { if (window.confirm(`Send ${p.amount} ${p.token} to ${p.to_address} on ${chain.name}? This cannot be reversed.`)) act(() => api("POST", `/proposals/${p.id}/decide`, { decision: "approve" })); }}>Approve and send</Button>
              <Button tone="danger" disabled={busy} onClick={() => act(() => api("POST", `/proposals/${p.id}/decide`, { decision: "reject" }))}>Reject</Button>
            </div>
          </div>
        ))}
      </Section>
      <Section title="Recipients waiting for approval">
        {recipients.length === 0 ? <Empty>Nothing waiting.</Empty> : recipients.map((r) => (
          <div key={r.id} className="border border-border rounded-lg p-3 flex flex-col gap-2">
            <div className="text-sm text-text"><span className="font-mono">{r.address}</span>{r.label ? ` · ${r.label}` : ""}</div>
            <div className="text-xs text-text-muted">Proposed by agent {r.proposed_by_agent} · {when(r.created_at)}</div>
            {r.reason ? <div className="text-sm text-text">Reason given: {r.reason}</div> : null}
            <div className="text-xs text-text-dim">If approved, it can receive funds after {delay} hours.</div>
            <div className="flex gap-2">
              <Button tone="primary" disabled={busy} onClick={() => act(() => api("POST", `/recipients/${r.id}/decide`, { decision: "approve" }))}>Approve</Button>
              <Button tone="danger" disabled={busy} onClick={() => act(() => api("POST", `/recipients/${r.id}/decide`, { decision: "reject" }))}>Reject</Button>
            </div>
          </div>
        ))}
      </Section>
    </div>
  );
}

function Activity({ proposals, txs, chain }: { proposals: Proposal[]; txs: ChainTx[]; chain: ChainMeta }) {
  return (
    <div className="flex flex-col gap-4">
      <Section title="Proposals">
        {proposals.length === 0 ? <Empty>No transfers proposed yet.</Empty> : (
          <div className="flex flex-col">
            {proposals.map((p) => (
              <div key={p.id} className="flex items-start justify-between gap-3 py-2 border-b border-border-subtle text-sm">
                <div className="min-w-0">
                  <div className="text-text">#{p.id} · {p.amount} {p.token} to <span className="font-mono">{short(p.to_address)}</span> <span className="text-text-muted">({usd(p.usd_micros)})</span></div>
                  {p.refusal_detail ? <div className="text-xs text-text-muted">{p.refusal_rule ? p.refusal_rule + ": " : ""}{p.refusal_detail}</div> : null}
                  {p.error ? <div className="text-xs text-error">{p.error}</div> : null}
                  {p.tx_hash ? <a className="text-xs text-accent" href={`${chain.explorer}/tx/${p.tx_hash}`} target="_blank" rel="noreferrer">{short(p.tx_hash)}</a> : null}
                </div>
                <div className="text-right flex-shrink-0">
                  <div className={`text-xs font-medium ${stateTone[p.state] || "text-text"}`}>{p.state.replace("_", " ")}</div>
                  <div className="text-xs text-text-dim">{when(p.created_at)}</div>
                </div>
              </div>
            ))}
          </div>
        )}
      </Section>
      <Section title="On-chain transactions">
        {txs.length === 0 ? <Empty>None yet. Incoming USDC appears here within a minute; incoming ETH shows in the balance only.</Empty> : (
          <div className="flex flex-col">
            {txs.map((t) => (
              <div key={t.id} className="flex items-center justify-between gap-3 py-2 border-b border-border-subtle text-sm">
                <div className="min-w-0 text-text">
                  {t.direction === "in" ? "Received" : "Sent"} {t.amount} {t.token} {t.direction === "in" ? "from" : "to"} <span className="font-mono">{short(t.direction === "in" ? t.from : t.to)}</span>
                  {" "}<a className="text-xs text-accent" href={`${chain.explorer}/tx/${t.hash}`} target="_blank" rel="noreferrer">{short(t.hash)}</a>
                </div>
                <div className={`text-xs flex-shrink-0 ${t.status === "failed" ? "text-error" : "text-text-muted"}`}>{t.status}</div>
              </div>
            ))}
          </div>
        )}
      </Section>
    </div>
  );
}

function Recipients({ wallet, recipients, busy, api, act }: { wallet: Wallet; recipients: Recipient[]; busy: boolean } & Pick<WalletViewProps, "api" | "act">) {
  const [address, setAddress] = useState("");
  const [label, setLabel] = useState("");
  const now = Date.now();
  return (
    <div className="flex flex-col gap-4">
      <Section title="Add a recipient">
        <div className="text-xs text-text-muted">Addresses you add here can receive funds right away. Addresses agents propose wait for approval and then a cooling-off period.</div>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Address"><input className={`${inputCls} font-mono`} value={address} onChange={(e) => setAddress(e.target.value)} placeholder="0x..." /></Field>
          <Field label="Label"><input className={inputCls} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Acme Ltd" /></Field>
        </div>
        <div><Button tone="primary" disabled={busy || !address.trim()} onClick={() => act(async () => { await api("POST", `/wallets/${wallet.id}/recipients`, { address: address.trim(), label }); setAddress(""); setLabel(""); })}>Add</Button></div>
      </Section>
      <Section title="Allowlist">
        {recipients.length === 0 ? <Empty>No recipients. Transfers are refused until at least one address is allowed.</Empty> : recipients.map((r) => {
          const cooling = r.status === "approved" && r.usable_after && new Date(r.usable_after).getTime() > now;
          return (
            <div key={r.id} className="flex items-center justify-between gap-3 py-2 border-b border-border-subtle text-sm">
              <div className="min-w-0">
                <div className="font-mono text-text truncate">{r.address}</div>
                <div className="text-xs text-text-muted">{r.label || "No label"} · {cooling ? `usable from ${when(r.usable_after!)}` : r.status}</div>
              </div>
              <Button tone="danger" disabled={busy} onClick={() => { if (window.confirm(`Remove ${r.address} from the allowlist?`)) act(() => api("DELETE", `/recipients/${r.id}`)); }}>Remove</Button>
            </div>
          );
        })}
      </Section>
    </div>
  );
}

function PolicyForm({ wallet, policy, busy, api, act }: { wallet: Wallet; policy: Policy; busy: boolean } & Pick<WalletViewProps, "api" | "act">) {
  const [perTx, setPerTx] = useState(String(policy.per_tx_usd_micros / 1_000_000));
  const [daily, setDaily] = useState(String(policy.daily_usd_micros / 1_000_000));
  const [approval, setApproval] = useState(String(policy.approval_usd_micros / 1_000_000));
  const [delay, setDelay] = useState(String(policy.new_recipient_delay_hours));
  const [tokens, setTokens] = useState<string[]>(policy.tokens);
  const toggle = (t: string) => setTokens((cur) => (cur.includes(t) ? cur.filter((x) => x !== t) : [...cur, t]));
  return (
    <Section title={`Policy (version ${policy.version})`}>
      <div className="text-xs text-text-muted">Agents cannot change these rules. Every change is saved as a new version in the log.</div>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Per-transfer cap (USD)" hint="Any single transfer above this is refused."><input className={inputCls} value={perTx} onChange={(e) => setPerTx(e.target.value)} inputMode="decimal" /></Field>
        <Field label="Rolling 24-hour cap (USD)" hint="Refused once the last 24 hours would pass this."><input className={inputCls} value={daily} onChange={(e) => setDaily(e.target.value)} inputMode="decimal" /></Field>
        <Field label="Approval above (USD)" hint="Larger transfers wait for a person."><input className={inputCls} value={approval} onChange={(e) => setApproval(e.target.value)} inputMode="decimal" /></Field>
        <Field label="New-recipient waiting period (hours)" hint="Applies to addresses agents propose."><input className={inputCls} value={delay} onChange={(e) => setDelay(e.target.value)} inputMode="numeric" /></Field>
      </div>
      <div className="flex flex-col gap-1">
        <span className="text-text-muted text-xs">Tokens that can move</span>
        <div className="flex gap-3">
          {TOKENS.map((t) => (
            <label key={t} className="flex items-center gap-2 text-sm text-text">
              <input type="checkbox" checked={tokens.includes(t)} onChange={() => toggle(t)} /> {t}
            </label>
          ))}
        </div>
      </div>
      <div><Button tone="primary" disabled={busy} onClick={() => act(() => api("POST", `/wallets/${wallet.id}/policy`, {
        per_tx_usd: perTx, daily_usd: daily, approval_usd: approval, new_recipient_delay_hours: Number(delay), tokens,
      }))}>Save policy</Button></div>
    </Section>
  );
}

function Log({ entries }: { entries: LedgerEntry[] }) {
  return (
    <Section title="Log">
      <div className="text-xs text-text-muted">Every action on this wallet, newest first. Entries are never edited or deleted.</div>
      {entries.length === 0 ? <Empty>Empty.</Empty> : (
        <div className="flex flex-col">
          {entries.map((e) => (
            <div key={e.id} className="py-2 border-b border-border-subtle text-sm flex flex-col gap-1">
              <div className="flex items-center justify-between gap-3">
                <span className="font-medium text-text">{e.kind}{e.proposal_id ? ` · #${e.proposal_id}` : ""}</span>
                <span className="text-xs text-text-dim">{when(e.created_at)}</span>
              </div>
              <div className="text-xs text-text-muted">{e.actor}</div>
              {Object.keys(e.data || {}).length ? <div className="font-mono text-xs text-text-muted truncate">{JSON.stringify(e.data)}</div> : null}
            </div>
          ))}
        </div>
      )}
    </Section>
  );
}

function KeyExport({ wallet, api }: { wallet: Wallet; api: WalletViewProps["api"] }) {
  const [confirm, setConfirm] = useState("");
  const [key, setKey] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  return (
    <Section title="Export private key">
      <div className="text-sm text-text">Anyone with this key controls the wallet's funds, outside every policy here. Export it only to move the wallet elsewhere or to keep an offline backup. The export is logged.</div>
      {key ? (
        <div className="flex flex-col gap-2">
          <div className="bg-bg-input border border-error/30 rounded px-3 py-2 font-mono text-sm text-text select-all">{key}</div>
          <div><Button onClick={() => setKey(null)}>Hide</Button></div>
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          <Field label="Type the wallet's full address to confirm"><input className={`${inputCls} font-mono`} value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={wallet.address} /></Field>
          {err ? <div className="text-sm text-error">{err}</div> : null}
          <div><Button tone="danger" disabled={confirm.trim().toLowerCase() !== wallet.address.toLowerCase()} onClick={async () => {
            try { setKey((await api<{ private_key: string }>("POST", `/wallets/${wallet.id}/export`, { confirm_address: confirm.trim() })).private_key); setErr(null); }
            catch (e) { setErr((e as Error).message); }
          }}>Reveal key</Button></div>
        </div>
      )}
    </Section>
  );
}
