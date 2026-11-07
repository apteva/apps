import { useEffect, useRef, useState } from "react";
import { scopedAppURL } from "./panelScope";
import { mcpEnvelopeError } from "./accountFlow";
export interface DestinationCapabilities {
  kind?: string;
  label?: string;
  required?: boolean;
  list?: boolean;
  create?: boolean;
  set_default?: boolean;
  privacy?: string[];
}
interface Destination {
  id: string;
  name: string;
  description?: string;
  privacy?: string;
}
interface DestinationResult {
  status: string;
  error?: string;
  reason?: string;
  items: Destination[];
  cursor?: string;
  default_destination_id?: string;
  default_destination_name?: string;
  capabilities?: DestinationCapabilities;
}
export function DestinationPicker({
  accountId,
  projectId,
  capabilities,
  value = "",
  defaultId = "",
  onChange,
  onDefaultChanged,
  manage = false,
}: {
  accountId: number;
  projectId?: string | null;
  capabilities?: DestinationCapabilities;
  value?: string;
  defaultId?: string;
  onChange?: (value: string) => void;
  onDefaultChanged?: () => void;
  manage?: boolean;
}) {
  const [result, setResult] = useState<DestinationResult>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [selected, setSelected] = useState(value);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [privacy, setPrivacy] = useState("PUBLIC");
  const [revision, setRevision] = useState(0);
  const identity = `${projectId}:${accountId}`;
  const current = useRef(identity);
  current.current = identity;
  useEffect(() => {
    current.current = identity;
    setCreating(false);
    setName("");
    setDescription("");
    return () => {
      current.current = "";
    };
  }, [identity]);
  useEffect(() => {
    setSelected(value);
  }, [value, identity]);
  const url = scopedAppURL(
    "/api/apps/social",
    `/destinations/accounts/${accountId}`,
    projectId,
  );
  const request = async (method: string, body?: unknown) => {
    const response = await fetch(url, {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!response.ok) throw new Error(await response.text());
    const data = await response.json();
    const issue = mcpEnvelopeError(data);
    if (issue) throw new Error(issue);
    if (data.status === "failed" || data.status === "unsupported")
      throw new Error(
        data.error || data.reason || "Destination operation unavailable",
      );
    return data;
  };
  useEffect(() => {
    if (!capabilities?.list) return;
    const controller = new AbortController();
    setResult(undefined);
    setError("");
    setBusy(true);
    const load = async () => {
      let cursor = "";
      const seen = new Set<string>();
      const items: Destination[] = [];
      let latest: DestinationResult | undefined;
      for (let page = 0; page < 20; page++) {
        const target = new URL(url, "http://localhost");
        target.searchParams.set("limit", "250");
        if (cursor) target.searchParams.set("cursor", cursor);
        const response = await fetch(target.pathname + target.search, {
          credentials: "same-origin",
          signal: controller.signal,
        });
        if (!response.ok) throw new Error(await response.text());
        const data = (await response.json()) as DestinationResult;
        const issue = mcpEnvelopeError(data);
        if (issue) throw new Error(issue);
        if (data.status !== "ok")
          throw new Error(
            data.error || data.reason || "Destinations unavailable",
          );
        latest = data;
        for (const item of data.items || []) {
          if (!items.some((existing) => existing.id === item.id))
            items.push(item);
        }
        if (!data.cursor) {
          if (!controller.signal.aborted) setResult({ ...data, items });
          return;
        }
        if (seen.has(data.cursor))
          throw new Error("Destination list repeated a cursor");
        seen.add(data.cursor);
        cursor = data.cursor;
      }
      if (!controller.signal.aborted) {
        setResult({ ...latest!, items });
        setError(
          "More destinations exist; refine the selection through the destination API.",
        );
      }
    };
    load()
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [identity, url, capabilities?.list, revision]);
  if (!capabilities?.list) return null;
  const label = capabilities.label || "Destination";
  const defaultValue = defaultId || result?.default_destination_id || "";
  const choose = (id: string) => {
    setSelected(id);
    onChange?.(id);
  };
  const create = async () => {
    const captured = identity;
    setBusy(true);
    setError("");
    try {
      const data = await request("POST", { name, description, privacy });
      if (current.current !== captured) return;
      choose(data.destination.id);
      setName("");
      setDescription("");
      setCreating(false);
      setRevision((n) => n + 1);
    } catch (e) {
      if (current.current === captured) setError((e as Error).message);
    } finally {
      if (current.current === captured) setBusy(false);
    }
  };
  const saveDefault = async () => {
    const captured = identity;
    setBusy(true);
    setError("");
    try {
      await request("PUT", { destination_id: selected });
      if (current.current !== captured) return;
      onDefaultChanged?.();
      setRevision((n) => n + 1);
    } catch (e) {
      if (current.current === captured) setError((e as Error).message);
    } finally {
      if (current.current === captured) setBusy(false);
    }
  };
  return (
    <section
      className="flex flex-col gap-2 text-xs"
      aria-label={`${label} destinations`}
    >
      <label className="flex flex-col gap-1">
        {label}
        <select
          aria-label={`Select ${label}`}
          value={selected}
          disabled={busy}
          onChange={(e) => choose(e.target.value)}
          className="bg-bg-input border border-border rounded p-2"
        >
          <option value="">
            {defaultValue
              ? `Use default: ${result?.items.find((item) => item.id === defaultValue)?.name || defaultValue}`
              : `Select ${label.toLowerCase()}`}
          </option>
          {result?.items.map((item) => (
            <option key={item.id} value={item.id}>
              {item.name}
              {item.privacy === "SECRET" ? " (secret)" : ""}
            </option>
          ))}
        </select>
      </label>
      {busy && <div>Loading…</div>}
      {error && (
        <div role="alert" className="text-error">
          {error}
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => setRevision((n) => n + 1)}
          disabled={busy}
        >
          Refresh
        </button>
        {capabilities.create && (
          <button
            type="button"
            onClick={() => setCreating((v) => !v)}
            disabled={busy}
          >
            Create {label.toLowerCase()}
          </button>
        )}
        {manage && capabilities.set_default && (
          <button
            type="button"
            onClick={saveDefault}
            disabled={busy || !selected}
          >
            Set as default
          </button>
        )}
      </div>
      {creating && (
        <div className="flex flex-col gap-2 border border-border rounded p-2">
          <label>
            Name
            <input
              aria-label="Destination name"
              maxLength={180}
              value={name}
              onInput={(e) => setName(e.currentTarget.value)}
              className="w-full bg-bg-input border border-border rounded p-2"
            />
          </label>
          <label>
            Description
            <textarea
              aria-label="Destination description"
              maxLength={500}
              value={description}
              onInput={(e) => setDescription(e.currentTarget.value)}
              className="w-full bg-bg-input border border-border rounded p-2"
            />
          </label>
          <label>
            Privacy
            <select
              aria-label="Destination privacy"
              value={privacy}
              onChange={(e) => setPrivacy(e.target.value)}
              className="bg-bg-input border border-border rounded p-2"
            >
              {(capabilities.privacy || ["PUBLIC"]).map((item) => (
                <option key={item}>{item}</option>
              ))}
            </select>
          </label>
          <button
            type="button"
            disabled={busy || !name.trim()}
            onClick={create}
          >
            Create
          </button>
        </div>
      )}
      {manage && (
        <div className="text-text-dim">
          Changing the default affects new posts. Existing posts keep their
          selected destination.
        </div>
      )}
    </section>
  );
}
