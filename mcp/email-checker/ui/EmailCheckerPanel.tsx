// EmailCheckerPanel — interactive email validation.
//
// The app is stateless: no DB, no history. So this panel is a live form
// rather than a list — type an address, hit Check, and render the result
// of the email_check tool via the app's REST mirror at
//   GET /api/apps/email-checker/check?email=
// That's the same store-nothing logic the email_check MCP tool runs, so
// the panel is just the human-facing front door to it.

import { useCallback, useState } from "react";

const API = "/api/apps/email-checker";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface CheckResult {
  email: string;
  valid: boolean;
  reasons: string[];
  syntax_ok: boolean;
  domain?: string;
  mx?: string[];
  disposable: boolean;
  role: boolean;
  free: boolean;
}

// Disqualifying reasons → human labels. Falls back to the raw key for
// anything the server adds before this map catches up.
const REASON_LABELS: Record<string, string> = {
  bad_syntax: "Invalid syntax",
  no_mx_records: "Domain has no MX records",
  disposable_domain: "Disposable provider",
  role_account: "Role account",
};

// Icons use stroke="currentColor" so their colour is inherited from the
// container's theme class — the dashboard JIT doesn't scan app ui/, so
// colour utilities on the <svg> itself wouldn't generate; currentColor
// sidesteps that entirely.
function CheckIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="20 6 9 17 4 12" />
    </svg>
  );
}

function XIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
      <line x1="18" y1="6" x2="6" y2="18" />
      <line x1="6" y1="6" x2="18" y2="18" />
    </svg>
  );
}

function Badge({ text, cls }: { text: string; cls: string }) {
  return (
    <span className={`text-xs rounded border border-border px-2 py-0.5 ${cls}`}>
      {text}
    </span>
  );
}

function SignalRow({ label, badge, mono }: { label: string; badge: JSX.Element; mono?: string }) {
  return (
    <div className="flex items-center justify-between gap-3 px-4 py-2 border-b border-border last:border-b-0">
      <span className="text-text-muted text-sm">{label}</span>
      {mono !== undefined ? (
        <span className="text-text text-sm" style={{ fontFamily: "ui-monospace, monospace" }}>{mono}</span>
      ) : (
        badge
      )}
    </div>
  );
}

function Result({ r }: { r: CheckResult }) {
  const hasMX = !!(r.mx && r.mx.length);
  return (
    <section className="border border-border rounded overflow-hidden">
      <div className={`flex items-center gap-2 px-4 py-3 ${r.valid ? "text-success" : "text-error"}`}>
        {r.valid ? <CheckIcon /> : <XIcon />}
        <span className="font-medium" style={{ fontSize: "16px" }}>
          {r.valid ? "Valid" : "Invalid"}
        </span>
        <span
          className="text-text-muted text-sm ml-2"
          style={{ fontFamily: "ui-monospace, monospace" }}
        >
          {r.email}
        </span>
      </div>

      {r.reasons.length > 0 && (
        <div className="px-4 pb-3 flex flex-wrap gap-2">
          {r.reasons.map((x) => (
            <Badge key={x} text={REASON_LABELS[x] ?? x} cls="text-error" />
          ))}
        </div>
      )}

      <div className="border-t border-border">
        <SignalRow
          label="Syntax"
          badge={r.syntax_ok
            ? <Badge text="Valid" cls="text-success" />
            : <Badge text="Failed" cls="text-error" />}
        />
        {r.domain && <SignalRow label="Domain" badge={<span />} mono={r.domain} />}
        <SignalRow
          label="MX records"
          badge={hasMX
            ? <Badge text={`${r.mx!.length} found`} cls="text-success" />
            : <Badge text="None" cls="text-error" />}
        />
        <SignalRow
          label="Disposable provider"
          badge={r.disposable
            ? <Badge text="Yes" cls="text-error" />
            : <Badge text="No" cls="text-text-muted" />}
        />
        <SignalRow
          label="Free provider"
          badge={r.free
            ? <Badge text="Yes" cls="text-info" />
            : <Badge text="No" cls="text-text-muted" />}
        />
        <SignalRow
          label="Role account"
          badge={r.role
            ? <Badge text="Yes" cls="text-warn" />
            : <Badge text="No" cls="text-text-muted" />}
        />
      </div>

      {hasMX && (
        <div className="border-t border-border px-4 py-2 flex flex-col gap-1">
          <span className="text-text-dim text-xs uppercase">MX hosts</span>
          {r.mx!.map((h) => (
            <span
              key={h}
              className="text-text-muted text-xs"
              style={{ fontFamily: "ui-monospace, monospace" }}
            >
              {h}
            </span>
          ))}
        </div>
      )}
    </section>
  );
}

export default function EmailCheckerPanel(_props: NativePanelProps) {
  const [email, setEmail] = useState("");
  const [result, setResult] = useState<CheckResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const run = useCallback(async () => {
    const q = email.trim();
    if (!q) return;
    setLoading(true);
    setError("");
    try {
      const res = await fetch(`${API}/check?email=${encodeURIComponent(q)}`, {
        credentials: "same-origin",
      });
      if (!res.ok) {
        setError(`Check failed: ${res.status} ${await res.text()}`.trim());
        setResult(null);
      } else {
        setResult(await res.json());
      }
    } catch (e) {
      setError((e as Error).message);
      setResult(null);
    } finally {
      setLoading(false);
    }
  }, [email]);

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="text-text font-medium">Email Checker</div>
        <span className="text-text-dim text-xs">
          Stateless validation — syntax, MX, classification
        </span>
      </header>

      <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
        <section className="flex gap-2">
          <input
            type="email"
            placeholder="name@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") run(); }}
            className="flex-1 bg-bg-input border border-border rounded px-3 py-1.5 text-sm text-text"
            autoFocus
          />
          <button
            onClick={run}
            disabled={!email.trim() || loading}
            className="px-4 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {loading ? "Checking…" : "Check"}
          </button>
        </section>

        {error && <div className="text-error text-sm">{error}</div>}

        {!result && !error && (
          <div className="py-12 text-center text-text-muted text-sm">
            Enter an email address to validate its syntax, MX records, and
            provider type. Nothing is stored — every check resolves fresh.
          </div>
        )}

        {result && <Result r={result} />}
      </div>
    </div>
  );
}
