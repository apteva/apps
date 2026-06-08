import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/whatsapp-web";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Account {
  status: string;
  connected: boolean;
  logged_in: boolean;
  jid?: string;
  phone?: string;
  push_name?: string;
  business_name?: string;
  platform?: string;
  qr_event?: string;
  has_qr: boolean;
  last_error?: string;
}

interface MessageRow {
  id: number;
  direction: "in" | "out";
  from: string;
  to: string;
  message_id: string;
  body_text: string;
  status: string;
  occurred_at: string;
}

export default function WhatsAppWebPanel({}: NativePanelProps) {
  const [account, setAccount] = useState<Account>({ status: "loading", connected: false, logged_in: false, has_qr: false });
  const [messages, setMessages] = useState<MessageRow[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const qrSrc = useMemo(() => `${API}/qr.png?t=${Date.now()}`, [account.has_qr, account.qr_event]);

  const load = useCallback(async () => {
    try {
      const [statusRes, msgRes] = await Promise.all([
        fetch(`${API}/status`, { credentials: "same-origin" }),
        fetch(`${API}/messages?limit=30`, { credentials: "same-origin" }),
      ]);
      if (statusRes.ok) {
        const data = await statusRes.json();
        setAccount(data.account ?? data);
      }
      if (msgRes.ok) {
        const data = await msgRes.json();
        setMessages(data.messages ?? []);
      }
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    load();
    const id = window.setInterval(load, account.has_qr || account.status === "connecting" || account.status === "pairing" ? 2500 : 8000);
    return () => window.clearInterval(id);
  }, [load, account.has_qr, account.status]);

  const post = async (path: string, label: string) => {
    setBusy(label);
    setError("");
    try {
      const res = await fetch(`${API}${path}`, { method: "POST", credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setAccount(data.account ?? data);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  };

  const connected = account.connected && account.logged_in;
  const name = account.business_name || account.push_name || account.phone || "Not paired";

  return (
    <div className="min-h-full bg-bg text-text">
      <div className="border-b border-border px-4 py-3">
        <div className="flex flex-wrap items-center gap-3">
          <h2 className="text-lg font-semibold">WhatsApp Web</h2>
          <StatusBadge account={account} />
          <div className="ml-auto flex gap-2">
            <button className="rounded border border-border px-3 py-1.5 text-sm hover:bg-bg-muted disabled:opacity-50" disabled={!!busy || connected} onClick={() => post("/connect", "connect")}>
              Connect
            </button>
            <button className="rounded border border-border px-3 py-1.5 text-sm hover:bg-bg-muted disabled:opacity-50" disabled={!!busy || !account.logged_in} onClick={() => post("/disconnect", "disconnect")}>
              Disconnect
            </button>
            <button className="rounded border border-red-900/60 px-3 py-1.5 text-sm text-red-300 hover:bg-red-950/40 disabled:opacity-50" disabled={!!busy || !account.logged_in} onClick={() => post("/logout", "logout")}>
              Log out
            </button>
          </div>
        </div>
        {error ? <div className="mt-2 text-sm text-red-300">{error}</div> : null}
        {account.last_error ? <div className="mt-2 text-sm text-red-300">{account.last_error}</div> : null}
      </div>

      <div className="grid gap-6 p-4 lg:grid-cols-[360px_1fr]">
        <section className="space-y-4">
          <div>
            <div className="text-xs uppercase tracking-wide text-text-dim">Linked account</div>
            <div className="mt-1 text-base font-medium">{name}</div>
            <dl className="mt-3 grid grid-cols-[110px_1fr] gap-y-2 text-sm">
              <dt className="text-text-dim">Phone</dt>
              <dd>{account.phone || "-"}</dd>
              <dt className="text-text-dim">JID</dt>
              <dd className="break-all">{account.jid || "-"}</dd>
              <dt className="text-text-dim">Platform</dt>
              <dd>{account.platform || "-"}</dd>
              <dt className="text-text-dim">QR</dt>
              <dd>{account.qr_event || (account.has_qr ? "ready" : "-")}</dd>
            </dl>
          </div>

          {account.has_qr ? (
            <div>
              <div className="mb-2 text-xs uppercase tracking-wide text-text-dim">Pairing QR</div>
              <div className="inline-block rounded bg-white p-3">
                <img src={qrSrc} alt="WhatsApp pairing QR code" className="h-72 w-72" />
              </div>
            </div>
          ) : null}

          <div className="text-sm text-text-dim">
            Connect a WhatsApp or WhatsApp Business app account through Linked Devices. This is not the official WhatsApp Business Platform API.
          </div>
        </section>

        <section>
          <div className="mb-3 flex items-center gap-3">
            <h3 className="font-semibold">Recent messages</h3>
            <button className="ml-auto rounded border border-border px-3 py-1.5 text-sm hover:bg-bg-muted" onClick={load}>Refresh</button>
          </div>
          <div className="overflow-hidden border-y border-border">
            <table className="w-full text-left text-sm">
              <thead className="text-xs uppercase tracking-wide text-text-dim">
                <tr>
                  <th className="px-3 py-2">Time</th>
                  <th className="px-3 py-2">Dir</th>
                  <th className="px-3 py-2">From / To</th>
                  <th className="px-3 py-2">Body</th>
                  <th className="px-3 py-2">Status</th>
                </tr>
              </thead>
              <tbody>
                {messages.map((m) => (
                  <tr key={m.id} className="border-t border-border">
                    <td className="whitespace-nowrap px-3 py-2 text-text-dim">{formatTime(m.occurred_at)}</td>
                    <td className="px-3 py-2">{m.direction}</td>
                    <td className="px-3 py-2">
                      <div>{m.from || "-"}</div>
                      <div className="text-text-dim">{m.to || "-"}</div>
                    </td>
                    <td className="max-w-xl px-3 py-2">{m.body_text || "(non-text message)"}</td>
                    <td className="px-3 py-2"><span className="rounded bg-bg-muted px-2 py-0.5">{m.status || "-"}</span></td>
                  </tr>
                ))}
                {!messages.length ? (
                  <tr><td className="px-3 py-8 text-center text-text-dim" colSpan={5}>No messages observed yet.</td></tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  );
}

function StatusBadge({ account }: { account: Account }) {
  const ok = account.connected && account.logged_in;
  const cls = ok ? "bg-emerald-950 text-emerald-300" : account.has_qr ? "bg-blue-950 text-blue-300" : "bg-bg-muted text-text-dim";
  const label = ok ? "connected" : account.has_qr ? "pairing" : account.status || "offline";
  return <span className={`rounded px-2 py-1 text-xs font-medium ${cls}`}>{label}</span>;
}

function formatTime(value: string) {
  if (!value) return "-";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString();
}
