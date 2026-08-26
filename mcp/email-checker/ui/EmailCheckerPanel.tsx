// EmailCheckerPanel — interactive email validation.
//
// The app is stateless: no DB, no history. So this panel is a live form
// rather than a list — type an address, hit Check, and render the result
// of the email_check tool via the app's REST mirror at
//   GET /api/apps/email-checker/check?email=
// That's the same store-nothing logic the email_check MCP tool runs, so
// the panel is just the human-facing front door to it.

import { useCallback, useEffect, useState } from "react";

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
  domain_status?: string;
  implicit_mx: boolean;
  mx?: string[];
  suggested_email?: string;
  disposable: boolean;
  role: boolean;
  free: boolean;
  smtp: SMTPProbe;
  verdict: "deliverable" | "undeliverable" | "risky" | "unknown";
  confidence: "high" | "medium" | "low";
  recommendation: "send" | "allow" | "do_not_send" | "review" | "retry" | "run_smtp_probe";
  mailbox_status: "verified" | "rejected" | "catch_all" | "not_checked" | "probe_blocked" | "temporarily_unavailable" | "unverified" | string;
  routable: boolean;
  risk_level: "none" | "unassessed" | "elevated" | "not_applicable";
  provider?: ProviderResult;
}

interface SMTPProbe {
  checked: boolean;
  email?: string;
  mx?: string;
  rcpt_status?: string;
  code?: number;
  enhanced_code?: string;
  response?: string;
  informative?: boolean;
  catch_all?: boolean;
  retryable: boolean;
  note?: string;
  attempts?: SMTPAttempt[];
}

interface SMTPAttempt {
  mx: string;
  kind: "recipient" | "catch_all";
  status: string;
  code?: number;
  enhanced_code?: string;
  response?: string;
}

interface ProviderResult {
  checked: boolean;
  provider: string;
  connection_id?: number;
  verdict?: string;
  recommendation?: string;
  status?: string;
  reason?: string;
  score?: number;
  suggested_email?: string;
  catch_all?: boolean;
  disposable?: boolean;
  role?: boolean;
  free?: boolean;
  error?: string;
}

interface ProviderBinding {
  provider: string;
  connection_id: number;
  default: boolean;
}

// Disqualifying reasons → human labels. Falls back to the raw key for
// anything the server adds before this map catches up.
const REASON_LABELS: Record<string, string> = {
  bad_syntax: "Invalid syntax",
  no_mail_server: "No receiving mail server",
  domain_does_not_accept_mail: "Domain explicitly rejects email",
  dns_temporary_error: "Temporary DNS failure",
  disposable_domain: "Disposable provider",
  role_account: "Role account",
  possible_typo: "Possible domain typo",
};

const SMTP_STATUS_LABELS: Record<string, string> = {
  ok: "Accepted",
  catch_all: "Catch-all",
  reject: "Rejected",
  tempfail: "Temporary failure",
  timeout: "Timeout",
  blocked: "Blocked",
  connect_failed: "Connect failed",
  no_mx: "No MX records",
  bad_syntax: "Invalid syntax",
  unknown: "Unknown",
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

function smtpBadgeClass(status?: string) {
  switch (status) {
    case "ok":
      return "text-success";
    case "catch_all":
      return "text-info";
    case "reject":
    case "bad_syntax":
    case "no_mx":
      return "text-error";
    case "tempfail":
    case "timeout":
    case "blocked":
    case "connect_failed":
      return "text-warn";
    default:
      return "text-text-muted";
  }
}

function verdictBadgeClass(verdict?: string) {
  switch (verdict) {
    case "deliverable": return "text-success";
    case "undeliverable": return "text-error";
    case "risky": return "text-warn";
    default: return "text-text-muted";
  }
}

function reasonBadgeClass(reason: string) {
  switch (reason) {
    case "role_account": return "text-info";
    case "possible_typo":
    case "dns_temporary_error": return "text-warn";
    default: return "text-error";
  }
}

function titleCase(value: string) {
  return value.replace(/[_-]+/g, " ").replace(/\b\w/g, (x) => x.toUpperCase());
}

function resultPresentation(r: CheckResult) {
  const routable = r.routable ?? (r.syntax_ok && !!(r.mx && r.mx.length));
  if (r.mailbox_status === "catch_all") {
    return { title: "Routable — catch-all domain", confidence: "Mailbox not individually verified", recommendation: "Allowed", neutral: true };
  }
  if (r.verdict === "unknown" && routable && (r.mailbox_status === "not_checked" || !r.smtp.checked)) {
    return { title: "Routable — mailbox not checked", confidence: "Domain checks passed", recommendation: "SMTP optional" };
  }
  if (r.verdict === "unknown" && routable && (r.mailbox_status === "probe_blocked" || r.smtp.rcpt_status === "blocked")) {
    return { title: "Routable — SMTP probe blocked", confidence: "Mailbox unverified", recommendation: "Allowed", neutral: true };
  }
  return {
    title: titleCase(r.verdict),
    confidence: `${titleCase(r.confidence)} confidence`,
    recommendation: titleCase(r.recommendation),
    neutral: false,
  };
}

function Result({ r }: { r: CheckResult }) {
  const hasMX = !!(r.mx && r.mx.length);
  const positive = r.verdict === "deliverable";
  const negative = r.verdict === "undeliverable";
  const presentation = resultPresentation(r);
  return (
    <section className="border border-border rounded overflow-hidden">
      <div className={`flex items-center gap-2 px-4 py-3 ${positive ? "text-success" : negative ? "text-error" : presentation.neutral ? "text-info" : "text-warn"}`}>
        {positive ? <CheckIcon /> : negative ? <XIcon /> : null}
        <span className="font-medium" style={{ fontSize: "16px" }}>
          {presentation.title}
        </span>
        <span
          className="text-text-muted text-sm ml-2"
          style={{ fontFamily: "ui-monospace, monospace" }}
        >
          {r.email}
        </span>
        <span className="ml-auto">
          <span className="flex items-center gap-2">
            <Badge text={presentation.confidence} cls={verdictBadgeClass(r.verdict)} />
            <Badge text={presentation.recommendation} cls={verdictBadgeClass(r.verdict)} />
          </span>
        </span>
      </div>

      {r.reasons.length > 0 && (
        <div className="px-4 pb-3 flex flex-wrap gap-2">
          {r.reasons.map((x) => (
            <Badge key={x} text={REASON_LABELS[x] ?? x} cls={reasonBadgeClass(x)} />
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
        {r.mailbox_status && (
          <SignalRow
            label="Mailbox status"
            badge={<Badge text={titleCase(r.mailbox_status)} cls={r.risk_level === "elevated" ? "text-warn" : "text-info"} />}
          />
        )}
        {r.domain_status && (
          <SignalRow
            label="Mail routing"
            badge={<Badge text={r.implicit_mx ? "A/AAAA fallback" : titleCase(r.domain_status)} cls={hasMX ? "text-success" : "text-error"} />}
          />
        )}
        {r.suggested_email && <SignalRow label="Did you mean?" badge={<span />} mono={r.suggested_email} />}
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
            ? <Badge text="Yes — informational" cls="text-info" />
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

      {r.smtp.checked && (
        <div className="border-t border-border">
          <div className="px-4 py-2 text-text-dim text-xs uppercase">SMTP probe</div>
          <SignalRow
            label="RCPT status"
            badge={
              <Badge
                text={SMTP_STATUS_LABELS[r.smtp.rcpt_status ?? ""] ?? r.smtp.rcpt_status ?? "Unknown"}
                cls={smtpBadgeClass(r.smtp.rcpt_status)}
              />
            }
          />
          {r.smtp.mx && <SignalRow label="MX host" badge={<span />} mono={r.smtp.mx} />}
          {r.smtp.code !== undefined && <SignalRow label="SMTP code" badge={<span />} mono={String(r.smtp.code)} />}
          {r.smtp.enhanced_code && <SignalRow label="Enhanced status" badge={<span />} mono={r.smtp.enhanced_code} />}
          <SignalRow
            label="Informative"
            badge={r.smtp.informative
              ? <Badge text="Yes" cls="text-success" />
              : <Badge text="No" cls={r.smtp.rcpt_status === "catch_all" ? "text-info" : "text-warn"} />}
          />
          {r.smtp.catch_all !== undefined && (
            <SignalRow label="Catch-all" badge={<Badge text={r.smtp.catch_all ? "Yes — allowed" : "No"} cls={r.smtp.catch_all ? "text-info" : "text-success"} />} />
          )}
          {r.smtp.retryable && <SignalRow label="Retry" badge={<Badge text="Recommended" cls="text-warn" />} />}
          {r.smtp.attempts && r.smtp.attempts.length > 0 && (
            <SignalRow label="SMTP attempts" badge={<span />} mono={String(r.smtp.attempts.length)} />
          )}
          {r.smtp.note && <SignalRow label="Note" badge={<span />} mono={r.smtp.note} />}
          {r.smtp.response && <SignalRow label="Response" badge={<span />} mono={r.smtp.response} />}
        </div>
      )}

      {r.provider && (
        <div className="border-t border-border">
          <div className="px-4 py-2 text-text-dim text-xs uppercase">
            Provider verification — {titleCase(r.provider.provider)}
          </div>
          <SignalRow
            label="Provider call"
            badge={r.provider.checked
              ? <Badge text="Completed" cls="text-success" />
              : <Badge text="Skipped" cls="text-text-muted" />}
          />
          {r.provider.verdict && (
            <SignalRow
              label="Verdict"
              badge={<Badge text={r.provider.catch_all ? "Catch-all — allowed" : titleCase(r.provider.verdict)} cls={r.provider.catch_all ? "text-info" : verdictBadgeClass(r.provider.verdict)} />}
            />
          )}
          {r.provider.status && <SignalRow label="Provider status" badge={<span />} mono={r.provider.status} />}
          {r.provider.reason && <SignalRow label="Reason" badge={<span />} mono={r.provider.reason} />}
          {r.provider.score !== undefined && <SignalRow label="Score" badge={<span />} mono={String(r.provider.score)} />}
          {r.provider.suggested_email && <SignalRow label="Suggested email" badge={<span />} mono={r.provider.suggested_email} />}
          {r.provider.catch_all !== undefined && (
            <SignalRow label="Catch-all" badge={<Badge text={r.provider.catch_all ? "Yes — allowed" : "No"} cls={r.provider.catch_all ? "text-info" : "text-text-muted"} />} />
          )}
          {r.provider.error && <SignalRow label="Provider error" badge={<span />} mono={r.provider.error} />}
        </div>
      )}
    </section>
  );
}

export default function EmailCheckerPanel(props: NativePanelProps) {
  const [email, setEmail] = useState("");
  const [smtp, setSmtp] = useState(false);
  const [provider, setProvider] = useState("local");
  const [providers, setProviders] = useState<ProviderBinding[]>([]);
  const [result, setResult] = useState<CheckResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const params = new URLSearchParams();
    if (props.projectId) params.set("project_id", props.projectId);
    fetch(`${API}/providers?${params.toString()}`, { credentials: "same-origin" })
      .then((res) => res.ok ? res.json() : Promise.reject(new Error(`Provider list failed: ${res.status}`)))
      .then((body: { providers?: ProviderBinding[] }) => setProviders(body.providers ?? []))
      .catch(() => setProviders([]));
  }, [props.projectId]);

  const run = useCallback(async () => {
    const q = email.trim();
    if (!q) return;
    setLoading(true);
    setError("");
    try {
      const params = new URLSearchParams({ email: q });
      if (props.projectId) params.set("project_id", props.projectId);
      if (smtp) params.set("smtp", "true");
      if (provider !== "local") {
        const [providerSlug, connectionId] = provider.split(":");
        params.set("provider", providerSlug);
        if (connectionId) params.set("connection_id", connectionId);
      }
      const res = await fetch(`${API}/check?${params.toString()}`, {
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
  }, [email, smtp, provider, props.projectId]);

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="text-text font-medium">Email Checker</div>
        <span className="text-text-dim text-xs">
          Local checks with optional mailbox-verification providers
        </span>
      </header>

      <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
        <section className="flex flex-col gap-3">
          <div className="flex gap-2">
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
          </div>
          <label className="flex items-center gap-2 text-sm text-text-muted">
            <input
              type="checkbox"
              checked={smtp}
              onChange={(e) => setSmtp(e.target.checked)}
            />
            <span>SMTP probe</span>
          </label>
          <label className="flex items-center gap-2 text-sm text-text-muted">
            <span>Verification provider</span>
            <select
              value={provider}
              onChange={(e) => setProvider(e.target.value)}
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm text-text"
            >
              <option value="local">Local checks only</option>
              {providers.map((binding) => (
                <option key={binding.connection_id} value={`${binding.provider}:${binding.connection_id}`}>
                  {titleCase(binding.provider)}{binding.default ? " (default)" : ""}
                </option>
              ))}
            </select>
            {provider !== "local" && <span className="text-text-dim text-xs">May consume provider credits</span>}
          </label>
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
