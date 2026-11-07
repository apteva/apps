import { useState } from "react";
import { assetTitle } from "./assetTitle";

type TaggedAsset = { id: string; name: string; title?: string; revision?: number; tags?: string[] };

export function AssetTagsForm({ asset, busy, onSubmit }: {
  asset: TaggedAsset;
  busy: boolean;
  onSubmit: (input: Record<string, unknown>) => void;
}) {
  const original = asset.tags || [];
  const [tags, setTags] = useState(original);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");

  function pendingTags(): string[] | null {
    const additions = draft.split(",").map(tag => tag.trim().toLowerCase()).filter(Boolean);
    const next = [...new Set([...tags, ...additions])].sort();
    if (next.some(tag => new TextEncoder().encode(tag).length > 64 || /[ ,\t\r\n]/.test(tag))) {
      setError("Use tags up to 64 bytes without spaces. Separate tags with commas.");
      return null;
    }
    if (next.length > 25) {
      setError("An asset can have up to 25 tags.");
      return null;
    }
    setError("");
    return next;
  }

  function addTags() {
    const next = pendingTags();
    if (next) {
      setTags(next);
      setDraft("");
    }
  }

  return <form className="space-y-4" onSubmit={event => {
    event.preventDefault();
    if (busy || asset.revision == null) return;
    const next = pendingTags();
    if (!next) return;
    onSubmit({
      asset_ids: [asset.id],
      expected_revisions: { [asset.id]: asset.revision },
      tags_add: next.filter(tag => !original.includes(tag)),
      tags_remove: original.filter(tag => !next.includes(tag)),
    });
  }}>
    <div>
      <h3 className="text-lg font-semibold">Edit tags</h3>
      <p className="mt-1 break-words text-sm">{assetTitle(asset)}</p>
      {asset.title?.trim() && <p className="break-all text-xs text-text-muted">{asset.name}</p>}
      <p className="text-xs text-text-muted">Asset tags are stored in Catalog.</p>
    </div>
    <fieldset disabled={busy} className="space-y-3">
      <legend className="mb-2 text-xs text-text-muted">{tags.length} / 25 tags</legend>
      <div className="flex flex-wrap gap-2" aria-label="Asset tags">
        {tags.map(tag => <span key={tag} className="inline-flex max-w-full items-center gap-1 rounded-md bg-accent/10 px-2 py-1 text-sm text-accent">
          <span className="break-all">{tag}</span>
          <button type="button" className="shrink-0 rounded px-1 hover:bg-accent/20" aria-label={`Remove tag ${tag}`} onClick={() => {
            setTags(current => current.filter(value => value !== tag));
            setError("");
          }}>×</button>
        </span>)}
        {!tags.length && <p className="text-sm text-text-muted">No tags</p>}
      </div>
      <label className="block space-y-1 text-xs">
        <span>Add tags</span>
        <input className="w-full rounded border border-border bg-bg-input px-3 py-2 text-sm" value={draft} placeholder="share-next, review:hold" onChange={event => {
          setDraft(event.target.value);
          setError("");
        }} onKeyDown={event => {
          if (event.key === "Enter") {
            event.preventDefault();
            addTags();
          }
        }} />
      </label>
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-text-muted">Separate tags with commas. Press Enter to add.</p>
        <button type="button" className="shrink-0 rounded border border-border px-3 py-1 text-sm" disabled={!draft.trim()} onClick={addTags}>Add</button>
      </div>
    </fieldset>
    {error && <p role="alert" className="text-sm text-red-400">{error}</p>}
    <button className="rounded bg-accent px-3 py-2 text-sm font-medium text-white disabled:opacity-50" disabled={busy || asset.revision == null}>{busy ? "Saving…" : "Save tags"}</button>
  </form>;
}
