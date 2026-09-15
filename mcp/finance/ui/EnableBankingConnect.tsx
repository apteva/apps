import { useCallback, useEffect, useRef, useState } from "react";
import type { FinanceAPI } from "./finance-api";
type Bank = { name: string; country: string };
type Authorization = {
  id: string;
  bank_name: string;
  country: string;
  status: string;
  session_id: string;
};
export default function EnableBankingConnect({
  api,
  connectionId,
  callbackURL,
  onSession,
}: {
  api: FinanceAPI;
  connectionId: number;
  callbackURL: string;
  onSession: (id: string) => void;
}) {
  const [country, setCountry] = useState("ES"),
    [psu, setPsu] = useState("personal"),
    [banks, setBanks] = useState<Bank[]>([]),
    [bank, setBank] = useState("");
  const [redirect, setRedirect] = useState(callbackURL),
    [authorizations, setAuthorizations] = useState<Authorization[]>([]),
    [selected, setSelected] = useState("");
  const [setup, setSetup] = useState<{
    environment: string;
    redirect_urls: string[];
    callback_url: string;
  } | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [link, setLink] = useState("");
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  const refreshing = useRef(false);
  const chosen = useRef("");
  const pending = useRef("");
  const onSessionRef = useRef(onSession);
  onSessionRef.current = onSession;
  const refresh = useCallback(async () => {
    if (refreshing.current) return;
    refreshing.current = true;
    try {
      const out = await api<{ authorizations: Authorization[] }>(
        `/banking/enable/authorizations?connection_id=${connectionId}`,
      );
      if (!active.current) return;
      setAuthorizations(out.authorizations);
      const next = out.authorizations.find(
        (a) =>
          a.status === "authorized" &&
          a.session_id &&
          (!pending.current || a.id === pending.current),
      );
      if (next && !chosen.current) {
        chosen.current = next.id;
        pending.current = "";
        setSelected(next.id);
        onSessionRef.current(next.session_id);
        setLink("");
      }
    } finally {
      refreshing.current = false;
    }
  }, [api, connectionId]);
  useEffect(() => {
    let alive = true;
    const run = () => {
      if (alive)
        void refresh().catch((e) => {
          if (alive) setError(String(e.message || e));
        });
    };
    run();
    const timer = setInterval(run, 5000);
    window.addEventListener("focus", run);
    return () => {
      alive = false;
      clearInterval(timer);
      window.removeEventListener("focus", run);
    };
  }, [refresh]);
  const checkSetup = useCallback(async () => {
    const out = await api<{
      environment: string;
      redirect_urls: string[];
      callback_url: string;
    }>(`/banking/enable/setup?connection_id=${connectionId}`);
    setSetup(out);
    return out;
  }, [api, connectionId]);
  useEffect(() => {
    let cancelled = false;
    checkSetup()
      .then((out) => {
        if (!cancelled && out.callback_url) setRedirect(out.callback_url);
      })
      .catch((e) => {
        if (!cancelled) setError(e.message);
      });
    return () => {
      cancelled = true;
    };
  }, [checkSetup]);
  const load = async () => {
    setBusy(true);
    setError("");
    try {
      const out = await api<{ aspsps: Bank[] }>(
        `/banking/enable/banks?connection_id=${connectionId}&country=${encodeURIComponent(country)}&psu_type=${psu}`,
      );
      setBanks(out.aspsps || []);
      setBank(out.aspsps?.[0]?.name || "");
      if (!out.aspsps?.length)
        setError("No banks available for this country and account type.");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  const start = async () => {
    setBusy(true);
    setError("");
    try {
      const config = await checkSetup();
      if (!config.redirect_urls?.includes(redirect))
        throw new Error(
          "Add the exact callback URL shown below to your Enable Banking application, then try Connect bank again.",
        );
      const out = await api<{ id: string; url: string }>(
        "/banking/enable/authorizations",
        {
          method: "POST",
          body: JSON.stringify({
            connection_id: connectionId,
            country,
            psu_type: psu,
            bank_name: bank,
            redirect_url: redirect,
          }),
        },
      );
      if (!active.current) return;
      chosen.current = "";
      pending.current = out.id;
      setSelected("");
      onSessionRef.current("");
      setLink(out.url);
      await refresh();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-3 space-y-3">
      <h3 className="font-medium">Connect your bank</h3>
      <p className="text-xs text-text-muted">
        Authorize account access with your bank, then choose the accounts to
        import. Your bank login stays with your bank.
      </p>
      {authorizations.some((a) => a.status === "authorized") && (
        <label className="block text-sm">
          Connected banks
          <select
            className="input"
            value={selected}
            onChange={(e) => {
              chosen.current = e.target.value;
              setSelected(e.target.value);
              onSessionRef.current(
                authorizations.find((a) => a.id === e.target.value)
                  ?.session_id || "",
              );
            }}
          >
            <option value="">Choose a bank session</option>
            {authorizations
              .filter((a) => a.status === "authorized")
              .map((a) => (
                <option key={a.id} value={a.id}>
                  {a.bank_name} ({a.country})
                </option>
              ))}
          </select>
        </label>
      )}
      <div className="flex gap-2">
        <label className="text-sm">
          Country
          <input
            className="input"
            maxLength={2}
            value={country}
            onChange={(e) => {
              setCountry(e.target.value.toUpperCase());
              setBanks([]);
              setBank("");
            }}
          />
        </label>
        <label className="text-sm">
          Account type
          <select
            className="input"
            value={psu}
            onChange={(e) => {
              setPsu(e.target.value);
              setBanks([]);
              setBank("");
            }}
          >
            <option value="personal">Personal</option>
            <option value="business">Business</option>
          </select>
        </label>
      </div>
      <button className="btn-secondary" disabled={busy} onClick={load}>
        {busy ? "Loading…" : "Find banks"}
      </button>
      {banks.length > 0 && (
        <label className="block text-sm">
          Bank
          <select
            className="input"
            value={bank}
            onChange={(e) => setBank(e.target.value)}
          >
            {banks.map((b) => (
              <option key={b.name} value={b.name}>
                {b.name}
              </option>
            ))}
          </select>
        </label>
      )}
      {setup && (
        <p className="text-xs text-text-muted">
          Environment: {setup.environment}.{" "}
          {setup.environment === "SANDBOX"
            ? "This application uses test banks/accounts."
            : "Bank consent is required for live accounts."}
        </p>
      )}
      <details
        open={!!setup && !setup.redirect_urls?.includes(redirect)}
        className="text-xs text-text-muted"
      >
        <summary>Callback setup (once per Enable Banking application)</summary>
        <p className="my-2">
          Register this exact callback URL in your Enable Banking application's
          allowed redirect URLs. Use the address where you open Finance.
        </p>
        <input
          aria-label="Callback URL"
          className="input"
          value={redirect}
          onChange={(e) => setRedirect(e.target.value)}
        />
        <button
          className="btn-secondary mt-2"
          onClick={() =>
            void navigator.clipboard
              .writeText(redirect)
              .catch(() => setError("Select and copy the callback URL above."))
          }
        >
          Copy callback URL
        </button>
      </details>
      <button className="btn-primary" disabled={!bank || busy} onClick={start}>
        Connect bank / renew access
      </button>
      {link && (
        <div className="space-y-2">
          <a
            className="btn-primary inline-block"
            href={link}
            target="_blank"
            rel="noopener noreferrer"
          >
            Continue to bank →
          </a>
          <p className="text-xs text-text-muted">
            Complete authorization in the new tab, then return here. Finance
            will load the authorized accounts automatically.
          </p>
        </div>
      )}
      {authorizations[0] &&
        ["failed", "denied", "exchanging", "expired"].includes(
          authorizations[0].status,
        ) && (
          <p className="text-xs text-text-muted">
            Latest connection: {authorizations[0].status}. If authorization
            could not finish, connect again.
          </p>
        )}
      {error && (
        <p role="alert" className="text-sm text-error">
          {error}
        </p>
      )}
    </div>
  );
}
