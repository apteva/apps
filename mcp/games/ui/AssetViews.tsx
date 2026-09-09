import { useEffect, useRef, useState } from "react";

type Item = Record<string, any>;
const field = "w-full rounded border border-border bg-bg-input p-2 text-sm";
const button =
  "rounded border border-border px-3 py-2 text-sm disabled:opacity-40 hover:bg-bg-input";
const box = "rounded border border-border p-4 space-y-3";
export async function assetRequest(
  project: string,
  game: string,
  action: string,
  data: Item = {},
): Promise<any> {
  const res = await fetch(
    `/api/apps/games/admin/games/${encodeURIComponent(game)}/assets/${action}?project_id=${encodeURIComponent(project)}`,
    {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    },
  );
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body.data;
}
function Field({
  label,
  value,
  set,
  type = "text",
}: {
  label: string;
  value: string;
  set: (v: string) => void;
  type?: string;
}) {
  return (
    <label className="block text-xs space-y-1">
      <span>{label}</span>
      <input
        className={field}
        type={type}
        value={value}
        onChange={(e) => set(e.target.value)}
      />
    </label>
  );
}
export function SpritePreview({
  url,
  frames,
  animations,
}: {
  url: string;
  frames: Item[];
  animations: Item[];
}) {
  const [clip, setClip] = useState("");
  const [index, setIndex] = useState(0);
  const animation = animations.find((a) => a.name === clip);
  const sequence = animation
    ? animation.frames
        .map((n: string) => frames.find((f) => f.name === n))
        .filter(Boolean)
    : frames;
  useEffect(() => {
    setIndex(0);
  }, [clip, url]);
  useEffect(() => {
    if (sequence.length < 2) return;
    if (animation && !animation.loop && index >= sequence.length - 1) return;
    const timer = setTimeout(
      () => setIndex((i) => (i + 1) % sequence.length),
      sequence[index % sequence.length]?.duration_ms || 100,
    );
    return () => clearTimeout(timer);
  }, [index, clip, url, frames, animations]);
  const f = sequence[index % sequence.length];
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        {animations.map((a) => (
          <button
            className={button}
            key={a.name}
            onClick={() => {
              setClip(a.name);
              setIndex(0);
            }}
          >
            {a.name}
          </button>
        ))}
      </div>
      <div
        className="overflow-auto rounded p-4"
        style={{
          background:
            "repeating-conic-gradient(#ddd 0% 25%, #aaa 0% 50%) 50% / 16px 16px",
        }}
      >
        {f ? (
          <div
            role="img"
            aria-label={`Frame ${f.name}`}
            style={{
              width: f.width,
              height: f.height,
              backgroundImage: `url(${JSON.stringify(url)})`,
              backgroundPosition: `-${f.x}px -${f.y}px`,
              backgroundRepeat: "no-repeat",
              imageRendering: "pixelated",
            }}
          />
        ) : (
          <img
            src={url}
            alt="Asset source"
            className="max-w-full"
            style={{ imageRendering: "pixelated" }}
          />
        )}
      </div>
      {f && (
        <p className="text-xs text-text-muted">
          {f.name} · {f.duration_ms} ms · pivot {f.pivot_x}, {f.pivot_y}
        </p>
      )}
    </div>
  );
}
const kinds = [
  "sprite",
  "spriteset",
  "rig",
  "tileset",
  "style",
  "material",
  "font",
  "sfx",
  "music",
  "stream",
  "locale",
  "table",
  "blob",
];
function specFor(kind: string): Item {
  const base = { license: "", description: "" };
  switch (kind) {
    case "style":
      return { ...base, palette: [], rules: "" };
    case "rig":
      return {
        ...base,
        dependencies: [],
        rig: {
          bones: [{ name: "root", x: 0, y: 0, rotation: 0 }],
          attachments: [],
          clips: [],
        },
      };
    case "material":
      return { ...base, material: { model: "sprite2d", tint: [1, 1, 1, 1] } };
    case "font":
      return { ...base, font: { family: "", fallbacks: [] } };
    case "stream":
      return {
        ...base,
        dependencies: [],
        stream: { mode: "sequence", tracks: [] },
      };
    case "locale":
      return {
        ...base,
        locale: { language: "en", entries: { welcome: { text: "Welcome" } } },
      };
    case "table":
      return {
        ...base,
        table: {
          schema_version: 1,
          key: "id",
          columns: [{ name: "id", type: "string", required: true }],
          rows: [],
        },
      };
    case "sfx":
    case "music":
    case "blob":
      return base;
    default:
      return initialSpec;
  }
}
const initialSpec = {
  license: "",
  description: "",
  require_alpha: true,
  pixels_per_unit: 100,
  frames: [],
  animations: [],
};
export function AssetPanel({
  projectId,
  gameId,
  logoVersionId = "",
  onLogoChanged,
}: {
  projectId: string;
  gameId: string;
  logoVersionId?: string;
  onLogoChanged?: (version: string) => void;
}) {
  const [history, setHistory] = useState<Item[]>([]);
  const [assets, setAssets] = useState<Item[]>([]),
    [more, setMore] = useState(false),
    [offset, setOffset] = useState(0);
  const [version, setVersion] = useState<Item | null>(null),
    [renditions, setRenditions] = useState<Item[]>([]),
    [content, setContent] = useState<Item>({ manifests: [], heads: {} }),
    [jobs, setJobs] = useState<Item[]>([]);
  const [id, setID] = useState(""),
    [name, setName] = useState(""),
    [kind, setKind] = useState("sprite"),
    [spec, setSpec] = useState(JSON.stringify(initialSpec, null, 2)),
    [storage, setStorage] = useState(""),
    [upload, setUpload] = useState("");
  const [target, setTarget] = useState("godot"),
    [engine, setEngine] = useState("4.5.1"),
    [platform, setPlatform] = useState("desktop"),
    [scale, setScale] = useState("1");
  const [selected, setSelected] = useState<string[]>([]),
    [digest, setDigest] = useState(""),
    [environment, setEnvironment] = useState("dev"),
    [note, setNote] = useState("");
  const [error, setError] = useState(""),
    [message, setMessage] = useState(""),
    [busy, setBusy] = useState(false);
  const generation = useRef(0),
    selection = useRef(0);
  const api = (action: string, args: Item = {}) =>
    assetRequest(projectId, gameId, action, args);
  async function setLogo(versionId: string) {
    const response = await fetch(
      `/api/apps/games/admin/games/${encodeURIComponent(gameId)}/logo?project_id=${encodeURIComponent(projectId)}`,
      {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          logo_version_id: versionId,
          expected_logo_version_id: logoVersionId,
        }),
      },
    );
    const result = await response.json();
    if (!response.ok)
      throw new Error(result.error || `HTTP ${response.status}`);
    onLogoChanged?.(result.game.logo_version_id || "");
    setMessage(
      versionId
        ? "Game logo updated. This version remains an asset for your builds."
        : "Game logo cleared. The asset and its versions are preserved.",
    );
  }
  async function refresh(page = offset) {
    const n = ++generation.current;
    const [a, r, c, j] = await Promise.all([
      api("assets", { offset: page }),
      api("renditions"),
      api("content"),
      api("jobs"),
    ]);
    if (n !== generation.current) return;
    setAssets(a.assets);
    setMore(a.has_more);
    setRenditions(r);
    setContent(c);
    setJobs(j);
  }
  useEffect(() => {
    void refresh().catch((e) => setError(e.message));
    return () => {
      generation.current++;
      selection.current++;
    };
  }, [projectId, gameId, offset]);
  useEffect(() => {
    let active = true;
    if (!version) {
      setHistory([]);
      return;
    }
    api("versions", { asset_id: version.asset_id })
      .then((items) => {
        if (active) setHistory(items);
      })
      .catch((e) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [projectId, gameId, version?.id]);
  async function run(fn: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function load(v: Item | null) {
    setVersion(v);
    setID(v?.asset_id || "");
    setName(v?.name || "");
    setKind(v?.kind || "sprite");
    setSpec(JSON.stringify(v?.spec || initialSpec, null, 2));
    setStorage("");
    setUpload("");
  }
  async function choose(a: Item) {
    const n = ++selection.current;
    try {
      const v = await api("version", { version_id: a.head });
      if (n === selection.current) load(v);
    } catch (e) {
      if (n === selection.current) setError((e as Error).message);
    }
  }
  const blobURL = (sha: string) =>
    `/api/apps/games/admin/games/${encodeURIComponent(gameId)}/content/preview/blobs/${sha}?project_id=${encodeURIComponent(projectId)}`;
  let parsedSpec: Item = {};
  try {
    parsedSpec = JSON.parse(spec);
  } catch {}
  function editSpec(patch: Item) {
    try {
      setSpec(JSON.stringify({ ...JSON.parse(spec), ...patch }, null, 2));
      setError("");
    } catch {
      setError("Fix the specification JSON before editing these fields.");
    }
  }
  const selectedR = renditions.filter((r) => selected.includes(r.id));
  return (
    <section className="space-y-4">
      <div>
        <h3 className="font-medium">Assets and content</h3>
        <p className="text-sm text-text-muted">
          Save source versions, bake for your engine, then release an exact set
          of content.
        </p>
      </div>
      {logoVersionId && (
        <div className="flex items-center gap-3 text-sm">
          <span>Game logo is selected from a saved asset version.</span>
          <button
            className={button}
            disabled={busy}
            onClick={() => void run(() => setLogo(""))}
          >
            Clear game logo
          </button>
        </div>
      )}
      {error && (
        <p role="alert" className="text-red">
          {error}
        </p>
      )}
      {message && <p role="status">{message}</p>}
      <div className="grid gap-4 lg:grid-cols-3">
        <aside className={box}>
          <button
            className={button}
            disabled={busy}
            onClick={() => {
              selection.current++;
              load(null);
            }}
          >
            New asset
          </button>
          {assets.map((a) => (
            <button
              key={a.id}
              disabled={busy}
              className="block w-full text-left rounded border border-border p-2"
              onClick={() => void choose(a)}
            >
              <strong>{a.name}</strong>
              <span className="block text-xs text-text-muted">
                {a.kind} · {a.id}
              </span>
            </button>
          ))}
          <div className="flex gap-2">
            <button
              className={button}
              disabled={offset === 0 || busy}
              onClick={() => setOffset((v) => Math.max(0, v - 25))}
            >
              Previous
            </button>
            <button
              className={button}
              disabled={!more || busy}
              onClick={() => setOffset((v) => v + 25)}
            >
              Next
            </button>
          </div>
        </aside>
        <div className={box + " lg:col-span-2"}>
          <h4 className="font-medium">
            {version ? "Edit asset · save a new version" : "Create an asset"}
          </h4>
          {version && history.length > 0 && (
            <label className="block text-xs">
              Version history
              <select
                className={field}
                value={version.id}
                onChange={(e) => void choose({ head: e.target.value })}
              >
                {history.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.created_at} · {v.id.slice(0, 12)}
                  </option>
                ))}
              </select>
            </label>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Asset ID" value={id} set={setID} />
            <Field label="Name" value={name} set={setName} />
          </div>
          <label className="block text-xs">
            Kind
            <select
              className={field}
              disabled={!!version}
              value={kind}
              onChange={(e) => {
                setKind(e.target.value);
                setSpec(JSON.stringify(specFor(e.target.value), null, 2));
              }}
            >
              {kinds.map((k) => (
                <option key={k}>{k}</option>
              ))}
            </select>
          </label>
          <Field
            label="License or rights status"
            value={parsedSpec.license || ""}
            set={(value) => editSpec({ license: value })}
          />
          <Field
            label="Description"
            value={parsedSpec.description || ""}
            set={(value) => editSpec({ description: value })}
          />
          {kind === "font" && (
            <Field
              label="Font family"
              value={parsedSpec.font?.family || ""}
              set={(value) =>
                editSpec({ font: { ...parsedSpec.font, family: value } })
              }
            />
          )}
          {kind === "locale" && (
            <Field
              label="Language"
              value={parsedSpec.locale?.language || "en"}
              set={(value) =>
                editSpec({ locale: { ...parsedSpec.locale, language: value } })
              }
            />
          )}
          {["sprite", "spriteset", "tileset", "sfx", "music"].includes(
            kind,
          ) && (
            <Field
              label="Generation prompt (optional)"
              value={parsedSpec.recipe?.prompt || ""}
              set={(value) =>
                editSpec({
                  recipe: value
                    ? { ...parsedSpec.recipe, prompt: value }
                    : undefined,
                })
              }
            />
          )}
          <details>
            <summary className="text-sm">Advanced specification</summary>
            <label className="block text-xs space-y-1">
              <span>
                Specification: rights, style dependencies, frames, animations
                and optional generation recipe
              </span>
              <textarea
                aria-label="Asset specification"
                className={field + " font-mono min-h-48"}
                value={spec}
                onChange={(e) => setSpec(e.target.value)}
              />
            </label>
          </details>
          <details className="text-xs">
            <summary>Sprite specification example</summary>
            <pre className="whitespace-pre-wrap">
              {JSON.stringify(
                {
                  license: "Owned artwork",
                  width: 64,
                  height: 32,
                  require_alpha: true,
                  pixels_per_unit: 32,
                  dependencies: [
                    {
                      asset: "gloaming-style",
                      version: "exact style version digest",
                    },
                  ],
                  frames: [
                    {
                      name: "idle-1",
                      x: 0,
                      y: 0,
                      width: 32,
                      height: 32,
                      pivot_x: 0.5,
                      pivot_y: 1,
                      duration_ms: 120,
                    },
                    {
                      name: "idle-2",
                      x: 32,
                      y: 0,
                      width: 32,
                      height: 32,
                      pivot_x: 0.5,
                      pivot_y: 1,
                      duration_ms: 120,
                    },
                  ],
                  animations: [
                    { name: "idle", frames: ["idle-1", "idle-2"], loop: true },
                  ],
                  recipe: {
                    prompt:
                      "A two-frame idle animation, transparent background",
                    model: "choose from Media Studio",
                  },
                },
                null,
                2,
              )}
            </pre>
          </details>
          {[
            "sprite",
            "spriteset",
            "tileset",
            "font",
            "sfx",
            "music",
            "blob",
          ].includes(kind) && (
            <>
              <Field
                label="Import from Storage file ID (optional)"
                value={storage}
                set={(v) => {
                  setStorage(v);
                  setUpload("");
                }}
                type="number"
              />
              <label className="block text-xs">
                Or upload a source (up to 25 MiB)
                <input
                  aria-label="Source file"
                  className={field}
                  type="file"
                  onChange={async (e) => {
                    const f = e.target.files?.[0];
                    if (!f) return;
                    if (f.size > 25 * 1024 * 1024) {
                      setError("Source exceeds 25 MiB");
                      return;
                    }
                    const currentSelection = selection.current;
                    const bytes = new Uint8Array(await f.arrayBuffer());
                    if (currentSelection !== selection.current) return;
                    let str = "";
                    for (let i = 0; i < bytes.length; i += 8192)
                      str += String.fromCharCode(
                        ...bytes.subarray(i, i + 8192),
                      );
                    setUpload(btoa(str));
                    setStorage("");
                  }}
                />
              </label>
            </>
          )}
          <button
            className={button}
            disabled={busy || !id || !name}
            onClick={() =>
              void run(async () => {
                const v = await api("save", {
                  asset_id: id,
                  name,
                  kind,
                  expected_parent: version?.id || "",
                  spec: JSON.parse(spec),
                  ...(storage
                    ? { storage_id: Number(storage) }
                    : upload
                      ? { content_base64: upload }
                      : { source: version?.source || "" }),
                });
                load(v);
                setMessage("Saved an immutable asset version.");
              })
            }
          >
            Save version
          </button>
          {version && (
            <>
              <p className="text-xs break-all">Version: {version.id}</p>
              {version.kind === "sprite" && version.source && (
                <button
                  className={button}
                  disabled={busy || version.id === logoVersionId}
                  onClick={() => void run(() => setLogo(version.id))}
                >
                  {version.id === logoVersionId
                    ? "Current game logo"
                    : "Use as game logo"}
                </button>
              )}
              {version.source &&
                (kind === "sprite" ||
                  kind === "spriteset" ||
                  kind === "tileset") && (
                  <SpritePreview
                    url={blobURL(version.source)}
                    frames={version.spec.frames || []}
                    animations={version.spec.animations || []}
                  />
                )}
              <div className="flex gap-2 flex-wrap">
                <button
                  className={button}
                  disabled={busy || !version.spec.recipe}
                  onClick={() =>
                    void run(async () => {
                      const result = await api("generate", {
                        version_id: version.id,
                        request_key: "generate-" + crypto.randomUUID(),
                      });
                      setMessage(
                        result.status === "unknown"
                          ? "Generation outcome is uncertain. Inspect the job before another generation."
                          : "Candidate generated. Attach it from the generation jobs below.",
                      );
                    })
                  }
                >
                  Generate candidate with Media Studio
                </button>
              </div>
              <p className="text-xs text-text-muted">
                Generation uses the saved recipe and may incur provider charges.
                Saving edits does not generate automatically.
              </p>
            </>
          )}
        </div>
      </div>
      <div className={box}>
        <h4 className="font-medium">Bake for an engine</h4>
        <div className="grid gap-3 sm:grid-cols-4">
          <label className="text-xs">
            Target
            <select
              className={field}
              value={target}
              onChange={(e) => {
                setTarget(e.target.value);
                setEngine(
                  e.target.value === "godot"
                    ? "4.5.1"
                    : e.target.value === "unity"
                      ? "6000.0"
                      : "1",
                );
              }}
            >
              {["generic", "godot", "unity"].map((t) => (
                <option key={t}>{t}</option>
              ))}
            </select>
          </label>
          <Field label="Engine version" value={engine} set={setEngine} />
          <Field label="Platform" value={platform} set={setPlatform} />
          <Field
            label="Scale (1–4)"
            value={scale}
            set={setScale}
            type="number"
          />
        </div>
        <button
          className={button}
          disabled={busy || !version}
          onClick={() =>
            void run(async () => {
              const r = await api("bake", {
                version_id: version!.id,
                target,
                engine_version: engine,
                platform,
                scale: Number(scale),
              });
              setSelected((prev) => [
                ...prev.filter(
                  (id) =>
                    !renditions.some(
                      (old) =>
                        old.id === id &&
                        old.asset === r.asset &&
                        old.target === r.target &&
                        old.platform === r.platform &&
                        old.engine_version === r.engine_version,
                    ),
                ),
                r.id,
              ]);
              setMessage("Rendition baked and selected for the next manifest.");
            })
          }
        >
          Bake selected version
        </button>
      </div>
      <div className={box}>
        <h4 className="font-medium">Freeze and release content</h4>
        <p className="text-xs text-text-muted">
          Choose one rendition per asset and target. All targets must reference
          the same asset versions.
        </p>
        <div className="max-h-60 overflow-auto space-y-2">
          {renditions.map((r) => (
            <label key={r.id} className="flex gap-2 text-sm">
              <input
                type="checkbox"
                checked={selected.includes(r.id)}
                onChange={(e) =>
                  setSelected((prev) =>
                    e.target.checked
                      ? [...prev, r.id]
                      : prev.filter((id) => id !== r.id),
                  )
                }
              />
              <span>
                {r.asset} · {r.target} {r.engine_version} · {r.platform} ·{" "}
                {r.version.slice(0, 8)}
              </span>
            </label>
          ))}
        </div>
        <button
          className={button}
          disabled={busy || !selectedR.length}
          onClick={() =>
            void run(async () => {
              const out = await api("freeze", { rendition_ids: selected });
              setDigest(out.digest);
              setMessage(
                "Manifest frozen. Downloads always use these exact bytes.",
              );
            })
          }
        >
          Freeze manifest
        </button>
        <label className="block text-xs">
          Manifest
          <select
            className={field}
            value={digest}
            onChange={(e) => setDigest(e.target.value)}
          >
            <option value="">Select a manifest</option>
            {content.manifests.map((m: Item) => (
              <option key={m.digest} value={m.digest}>
                {m.digest.slice(0, 16)} ·{" "}
                {m.approved ? "reviewed" : "awaiting review"}
              </option>
            ))}
          </select>
        </label>
        {digest && (
          <a
            className={button + " inline-block"}
            href={`/api/apps/games/admin/games/${encodeURIComponent(gameId)}/content/${digest}?project_id=${encodeURIComponent(projectId)}&download=zip`}
          >
            Download locked content
          </a>
        )}
        <Field label="Review note" value={note} set={setNote} />
        <button
          className={button}
          disabled={busy || !digest || note.length < 3}
          onClick={() =>
            void run(async () => {
              await api("approve", { digest, note });
              setMessage("Review recorded for this exact manifest.");
            })
          }
        >
          Approve manifest
        </button>
        <label className="block text-xs">
          Environment
          <select
            className={field}
            value={environment}
            onChange={(e) => setEnvironment(e.target.value)}
          >
            {["dev", "staging", "prod"].map((e) => (
              <option key={e}>{e}</option>
            ))}
          </select>
        </label>
        <p className="text-xs break-all">
          Current: {content.heads[environment] || "No release"}
        </p>
        <button
          className={button}
          disabled={busy || !digest}
          onClick={() =>
            void run(async () => {
              await api("promote", {
                digest,
                environment,
                expected_head: content.heads[environment] || "",
              });
              setMessage(`Moved ${environment} to ${digest.slice(0, 12)}.`);
            })
          }
        >
          Move environment to selected manifest
        </button>
      </div>
      <div className={box}>
        <h4 className="font-medium">Generation jobs</h4>
        {jobs.length === 0 && (
          <p className="text-sm text-text-muted">No generation requests.</p>
        )}
        {jobs.map((j) => (
          <div
            key={j.request_key}
            className="border-b border-border py-2 text-sm"
          >
            <p>
              {j.asset_id} · {j.status}
            </p>
            <p className="text-xs break-all">{j.request_key}</p>
            {j.result.error && <p className="text-red">{j.result.error}</p>}
            {j.status !== "attached" && (
              <>
                <Field
                  label={`Generation ID for ${j.asset_id}`}
                  value={String(j.result.generation_id || "")}
                  set={(value) =>
                    setJobs((old) =>
                      old.map((x) =>
                        x.request_key === j.request_key
                          ? {
                              ...x,
                              result: { ...x.result, generation_id: value },
                            }
                          : x,
                      ),
                    )
                  }
                  type="number"
                />
                <button
                  className={button}
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      const out = await api("generation_sync", {
                        request_key: j.request_key,
                        generation_id:
                          Number(j.result.generation_id) || undefined,
                      });
                      if (out.version) load(out.version);
                      setMessage(
                        "Candidate attached as a new version. Review and bake before publishing.",
                      );
                    })
                  }
                >
                  Attach candidate
                </button>
              </>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}
