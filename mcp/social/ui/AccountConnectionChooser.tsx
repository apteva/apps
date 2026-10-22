import { useEffect, useState } from "react";
import { scopedAppURL } from "./panelScope";
import { mcpEnvelopeError } from "./accountFlow";
interface ConnectionChoice {
  id: number;
  name: string;
  accounts: {
    id: number;
    name: string;
    profile_id: number;
    profile_name: string;
  }[];
}
export function AccountConnectionChooser({
  platform,
  label,
  projectId,
  busy,
  onExisting,
  onNew,
  onBack,
}: {
  platform: string;
  label: string;
  projectId?: string | null;
  busy: boolean;
  onExisting: (id: number) => void;
  onNew: () => void;
  onBack: () => void;
}) {
  const [state, setState] = useState<{
    identity: string;
    items: ConnectionChoice[];
    loading: boolean;
    error: string;
  }>({ identity: "", items: [], loading: true, error: "" });
  const [revision, setRevision] = useState(0);
  const identity = `${projectId}:${platform}`;
  useEffect(() => {
    const controller = new AbortController();
    setState({ identity, items: [], loading: true, error: "" });
    (async () => {
      try {
        const response = await fetch(
          scopedAppURL(
            "/api/apps/social",
            "/accounts/connections?platform=" + encodeURIComponent(platform),
            projectId,
          ),
          { credentials: "same-origin", signal: controller.signal },
        );
        if (!response.ok) throw new Error(await response.text());
        const data = await response.json();
        const issue = mcpEnvelopeError(data);
        if (issue) throw new Error(issue);
        if (!Array.isArray(data.connections))
          throw new Error("Unable to load account connections");
        if (!controller.signal.aborted)
          setState({
            identity,
            items: data.connections,
            loading: false,
            error: "",
          });
      } catch (error) {
        if (!controller.signal.aborted)
          setState({
            identity,
            items: [],
            loading: false,
            error: (error as Error).message,
          });
      }
    })();
    return () => controller.abort();
  }, [identity, platform, projectId, revision]);
  const current =
    state.identity === identity
      ? state
      : { items: [], loading: true, error: "" };
  return (
    <section
      className="flex flex-col gap-3"
      aria-label={`${label} account connections`}
    >
      <button
        type="button"
        onClick={onBack}
        disabled={busy}
        className="self-start text-xs text-text-muted"
      >
        ← Platforms
      </button>
      <h3 className="text-sm font-semibold text-text">Add a {label} account</h3>
      {current.loading && (
        <p className="text-xs text-text-dim">Loading existing connections…</p>
      )}
      {current.error && (
        <div role="alert" className="text-xs text-error">
          {current.error}
          <button
            type="button"
            onClick={() => setRevision((n) => n + 1)}
            disabled={busy}
            className="ml-2 underline"
          >
            Retry
          </button>
        </div>
      )}
      {current.items.length > 0 && (
        <div className="flex flex-col gap-2">
          <p className="text-xs text-text-dim">Use an existing connection</p>
          {current.items.map((connection) => (
            <button
              type="button"
              key={connection.id}
              disabled={busy}
              onClick={() => onExisting(connection.id)}
              className="rounded border border-border p-3 text-left hover:border-accent disabled:opacity-40"
            >
              <span className="block text-sm text-text">
                {connection.accounts
                  .map((account) => account.name)
                  .join(", ") || connection.name}
              </span>
              <span className="block text-xs text-text-dim">
                {connection.name} · Connection {connection.id}
              </span>
              {connection.accounts.map((account) => (
                <span key={account.id} className="block text-xs text-text-dim">
                  {account.profile_name
                    ? `Linked to ${account.profile_name}`
                    : "Unassigned account"}
                </span>
              ))}
            </button>
          ))}
        </div>
      )}
      {!current.loading && !current.error && current.items.length === 0 && (
        <p className="text-xs text-text-dim">
          No existing {label} connections in this project.
        </p>
      )}
      <button
        type="button"
        disabled={busy}
        onClick={onNew}
        className="rounded bg-accent px-3 py-2 text-sm text-white disabled:opacity-40"
      >
        Connect another account
      </button>
      <p className="text-xs text-text-dim">
        Sign in to the account you want to add in the authorization window.
      </p>
    </section>
  );
}
