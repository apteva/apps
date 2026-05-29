import React, { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/persona-studio";
const MEDIA_API = "/api/apps/media-studio";
const h = React.createElement;

const IMAGE_FORMAT_PRESETS = [
  { key: "square", label: "Square", size: "1024x1024", aspect: "1:1" },
  { key: "portrait", label: "Portrait", size: "1024x1536", aspect: "2:3" },
  { key: "landscape", label: "Landscape", size: "1536x1024", aspect: "3:2" },
  { key: "story", label: "Story 9:16", size: "1024x1792", aspect: "9:16" },
  { key: "wide", label: "Wide 16:9", size: "1792x1024", aspect: "16:9" },
  { key: "custom", label: "Custom", size: "", aspect: "" },
];
const VIDEO_ASPECTS = ["9:16", "16:9", "1:1", "4:3"];
const IMAGE_QUALITIES = ["auto", "low", "medium", "high"];
const OUTPUT_FORMATS = ["png", "jpeg", "webp"];

export default function PersonaPanel({ projectId }) {
  const [personas, setPersonas] = useState([]);
  const [selectedId, setSelectedId] = useState(0);
  const [bundle, setBundle] = useState(null);
  const [status, setStatus] = useState("");
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({
    name: "",
    handle: "",
    tone: "",
    audience: "",
    visual_style: "",
    bio: "",
  });
  const [item, setItem] = useState({ name: "", kind: "product", storage_file_ids: "", visual_rules: "" });
  const [reference, setReference] = useState({ storage_file_id: "", kind: "face", label: "" });
  const [generation, setGeneration] = useState({
    asset_type: "image",
    prompt: "",
    item_ids: [],
    model: "",
    format_preset: "portrait",
    aspect: "9:16",
    size: "1024x1536",
    duration: "",
    quality: "auto",
    output_format: "png",
  });
  const [mediaModels, setMediaModels] = useState([]);
  const [mediaProvider, setMediaProvider] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);
  const [storageFiles, setStorageFiles] = useState([]);
  const [storageLoading, setStorageLoading] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [storageQuery, setStorageQuery] = useState("");
  const [pickerTarget, setPickerTarget] = useState(null);

  const qs = useMemo(() => `project_id=${encodeURIComponent(projectId || "")}`, [projectId]);
  const persona = bundle?.persona;
  const items = bundle?.items || [];
  const refs = bundle?.references || [];
  const assets = bundle?.assets || [];
  const selectedItems = useMemo(
    () => items.filter((it) => generation.item_ids.includes(it.id)),
    [items, generation.item_ids]
  );
  const imageReferenceCount = useMemo(() => imageSourceOptions(refs, selectedItems).length, [refs, selectedItems]);
  const requiresImageEditModel = generation.asset_type === "image" && imageReferenceCount > 0;
  const modelOptions = useMemo(() => {
    if (!requiresImageEditModel) return mediaModels;
    return mediaModels.filter((model) => modelSupportsImageEdit(model, mediaProvider));
  }, [mediaModels, mediaProvider, requiresImageEditModel]);
  const selectedModel = modelOptions.find((m) => m.id === generation.model);

  const loadPersonas = useCallback(async () => {
    if (!projectId) return;
    const res = await fetch(`${API}/personas?${qs}`, { credentials: "same-origin" });
    if (!res.ok) {
      setStatus(`Load personas: ${res.status}`);
      return;
    }
    const data = await res.json();
    const rows = data.personas || [];
    setPersonas(rows);
    if (!selectedId && rows[0]) setSelectedId(rows[0].id);
  }, [projectId, qs, selectedId]);

  const loadBundle = useCallback(async () => {
    if (!projectId || !selectedId) {
      setBundle(null);
      return;
    }
    const res = await fetch(`${API}/personas/${selectedId}?${qs}`, { credentials: "same-origin" });
    if (!res.ok) {
      setStatus(`Load persona: ${res.status}`);
      return;
    }
    setBundle(await res.json());
  }, [projectId, selectedId, qs]);

  useEffect(() => {
    loadPersonas().catch((e) => setStatus(e.message));
  }, [loadPersonas]);
  useEffect(() => {
    loadBundle().catch((e) => setStatus(e.message));
  }, [loadBundle]);
  useEffect(() => {
    if (!projectId || !generation.asset_type) return;
    let cancelled = false;
    const params = new URLSearchParams({ kind: generation.asset_type });
    if (generation.asset_type === "image" && requiresImageEditModel) {
      params.set("capability", "image.edit");
    }
    setModelsLoading(true);
    fetch(`${MEDIA_API}/models?${params.toString()}`, { credentials: "same-origin" })
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (cancelled) return;
        const models = Array.isArray(data?.models) ? data.models : [];
        setMediaModels(models);
        setMediaProvider(String(data?.provider || ""));
      })
      .catch(() => {
        if (!cancelled) {
          setMediaModels([]);
          setMediaProvider("");
        }
      })
      .finally(() => {
        if (!cancelled) setModelsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [projectId, generation.asset_type, requiresImageEditModel]);
  useEffect(() => {
    if (!generation.asset_type) return;
    if (modelOptions.length > 0 && !modelOptions.some((m) => m.id === generation.model)) {
      setGeneration((cur) => ({ ...cur, model: modelOptions[0].id }));
      return;
    }
    if (requiresImageEditModel && modelOptions.length === 0 && generation.model) {
      setGeneration((cur) => ({ ...cur, model: "" }));
    }
  }, [generation.asset_type, generation.model, modelOptions, requiresImageEditModel]);

  async function createPersona(e) {
    e.preventDefault();
    if (!form.name.trim()) return;
    setCreating(true);
    try {
      const res = await post(`${API}/personas?${qs}`, form);
      const p = res.persona;
      setForm({ name: "", handle: "", tone: "", audience: "", visual_style: "", bio: "" });
      await loadPersonas();
      if (p?.id) setSelectedId(p.id);
    } catch (e) {
      setStatus(e.message);
    } finally {
      setCreating(false);
    }
  }

  async function addReference(e) {
    e.preventDefault();
    if (!selectedId || !reference.storage_file_id) return;
    try {
      await post(`${API}/references?${qs}`, {
        persona_id: selectedId,
        storage_file_id: Number(reference.storage_file_id),
        kind: reference.kind,
        label: reference.label,
      });
      setReference({ storage_file_id: "", kind: "face", label: "" });
      loadBundle();
    } catch (e) {
      setStatus(e.message);
    }
  }

  async function addItem(e) {
    e.preventDefault();
    if (!selectedId || !item.name.trim()) return;
    try {
      await post(`${API}/items?${qs}`, {
        persona_id: selectedId,
        name: item.name,
        kind: item.kind,
        visual_rules: item.visual_rules,
        storage_file_ids: parseIDs(item.storage_file_ids),
      });
      setItem({ name: "", kind: "product", storage_file_ids: "", visual_rules: "" });
      loadBundle();
    } catch (e) {
      setStatus(e.message);
    }
  }

  async function generateAsset(e) {
    e.preventDefault();
    if (!selectedId || !generation.prompt.trim()) return;
    const settings = {};
    const selectedModelSupportsEdit = selectedModel
      ? modelSupportsImageEdit(selectedModel, mediaProvider)
      : modelSupportsImageEdit({ id: generation.model }, mediaProvider);
    if (generation.model && (!requiresImageEditModel || selectedModelSupportsEdit)) {
      settings.model = generation.model;
    }
    if (generation.asset_type === "image") {
      if (generation.size) settings.size = generation.size;
      const options = {};
      if (generation.quality) {
        settings.quality = generation.quality;
        options.quality = generation.quality;
      }
      if (generation.output_format) {
        settings.output_format = generation.output_format;
        options.output_format = generation.output_format;
      }
      if (generation.aspect) {
        settings.aspect = generation.aspect;
        options.aspect_ratio = generation.aspect;
      }
      if (Object.keys(options).length > 0) settings.options = options;
    } else {
      if ((generation.asset_type === "video" || generation.asset_type === "avatar") && generation.aspect) {
        settings.aspect = generation.aspect;
      }
      if ((generation.asset_type === "video" || generation.asset_type === "audio_sfx" || generation.asset_type === "music") && generation.duration) {
        settings.duration = Number(generation.duration);
      }
    }
    try {
      setStatus("Generating...");
      const res = await post(`${API}/generate?${qs}`, {
        persona_id: selectedId,
        asset_type: generation.asset_type,
        prompt: generation.prompt,
        item_ids: generation.item_ids,
        settings,
        use_cache: true,
      });
      setStatus(res.cached ? "Returned cached asset" : "Generated asset");
      setGeneration((g) => ({ ...g, prompt: "" }));
      loadBundle();
    } catch (e) {
      setStatus(e.message);
    }
  }

  async function loadStorageFiles(query = storageQuery) {
    if (!projectId) return;
    setStorageLoading(true);
    setStorageError("");
    try {
      const params = new URLSearchParams({ project_id: projectId, folder: "/", recursive: "true", limit: "240" });
      if (query.trim()) params.set("q", query.trim());
      const res = await fetch(`${API}/storage-files?${params.toString()}`, { credentials: "same-origin" });
      const text = await res.text();
      const data = text ? JSON.parse(text) : {};
      if (!res.ok) throw new Error(data.error || text || `HTTP ${res.status}`);
      setStorageFiles(data.files || []);
    } catch (e) {
      setStorageFiles([]);
      setStorageError(e.message);
    } finally {
      setStorageLoading(false);
    }
  }

  function openStoragePicker(target) {
    setPickerTarget(target);
    loadStorageFiles("").catch((e) => setStorageError(e.message));
  }

  function chooseStorageFile(file) {
    if (pickerTarget === "reference") {
      setReference((cur) => ({
        ...cur,
        storage_file_id: String(file.id),
        label: cur.label || file.name || `storage:${file.id}`,
      }));
    }
    if (pickerTarget === "item") {
      setItem((cur) => {
        const ids = parseIDs(cur.storage_file_ids);
        if (!ids.includes(file.id)) ids.push(file.id);
        return { ...cur, storage_file_ids: ids.join(", ") };
      });
    }
    setPickerTarget(null);
  }

  const modelAspects = selectedModel?.aspect_ratios || [];
  const modelDurations = selectedModel?.durations || [];

  return h("div", { className: "h-full w-full flex overflow-hidden bg-bg text-text" },
    h("aside", { className: "w-72 border-r border-border flex flex-col min-w-0" },
      h("div", { className: "px-4 py-3 border-b border-border flex items-center gap-2" },
        h("div", { className: "font-semibold" }, "Personas"),
        h("span", { className: "ml-auto text-xs text-text-dim" }, `${personas.length}`)
      ),
      h("div", { className: "flex-1 overflow-auto p-2" },
        personas.length === 0
          ? h("div", { className: "text-sm text-text-muted p-3" }, "Create a persona to start.")
          : personas.map((p) =>
              h("button", {
                key: p.id,
                onClick: () => setSelectedId(p.id),
                className: `w-full text-left rounded px-3 py-2 mb-1 border ${selectedId === p.id ? "border-accent bg-bg-card" : "border-transparent hover:bg-bg-card"}`
              },
                h("div", { className: "font-medium truncate" }, p.name),
                h("div", { className: "text-xs text-text-dim truncate" }, p.handle || p.tone || "No handle")
              )
            )
      ),
      h("form", { onSubmit: createPersona, className: "border-t border-border p-3 flex flex-col gap-2" },
        h("div", { className: "text-xs uppercase text-text-dim" }, "New persona"),
        input("Name", form.name, (v) => setForm({ ...form, name: v })),
        input("Handle", form.handle, (v) => setForm({ ...form, handle: v })),
        input("Tone", form.tone, (v) => setForm({ ...form, tone: v })),
        h("textarea", {
          value: form.visual_style,
          onChange: (e) => setForm({ ...form, visual_style: e.target.value }),
          placeholder: "Visual style",
          className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-[58px]"
        }),
        h("button", {
          disabled: creating || !form.name.trim(),
          className: "bg-accent text-bg rounded px-3 py-1.5 text-sm font-semibold disabled:opacity-50"
        }, creating ? "Creating..." : "Create")
      )
    ),
    h("main", { className: "flex-1 min-w-0 flex flex-col overflow-hidden" },
      h("header", { className: "border-b border-border px-4 py-3 flex items-center gap-3" },
        h("div", { className: "min-w-0" },
          h("div", { className: "font-semibold truncate" }, persona?.name || "Persona Studio"),
          h("div", { className: "text-xs text-text-dim truncate" }, persona ? [persona.handle, persona.audience, persona.tone].filter(Boolean).join(" · ") || "Identity workspace" : "Reusable identity, items, media, and clips")
        ),
        h("span", { className: "ml-auto text-xs text-text-dim" }, status)
      ),
      persona
        ? h("div", { className: "flex-1 overflow-auto p-4 grid gap-4", style: { gridTemplateColumns: "minmax(320px, 420px) minmax(420px, 1fr)" } },
            h("section", { className: "flex flex-col gap-4 min-w-0" },
              panel("Identity", h("div", { className: "text-sm text-text-muted whitespace-pre-wrap" },
                persona.bio || persona.visual_style || "Add bio, audience, tone, and style through the tools or API."
              )),
              panel("References", h("div", null,
                h("form", { onSubmit: addReference, className: "grid gap-2 mb-3" },
                  h("div", { className: "grid grid-cols-[1fr_96px] gap-2" },
                    h("input", { value: reference.storage_file_id, onChange: (e) => setReference({ ...reference, storage_file_id: e.target.value }), placeholder: "Storage file id", className: fieldClass() }),
                    h("button", { type: "button", onClick: () => openStoragePicker("reference"), className: secondaryButtonClass() }, "Browse")
                  ),
                  h("div", { className: "grid grid-cols-[120px_1fr_76px] gap-2" },
                    h("select", { value: reference.kind, onChange: (e) => setReference({ ...reference, kind: e.target.value }), className: fieldClass() },
                      ["face", "style", "outfit", "pose", "voice", "product", "location", "brand"].map((k) => h("option", { key: k, value: k }, k))
                    ),
                    h("input", { value: reference.label, onChange: (e) => setReference({ ...reference, label: e.target.value }), placeholder: "Label", className: fieldClass() }),
                    h("button", { className: buttonClass() }, "Link")
                  ),
                  reference.storage_file_id && h("div", { className: "mt-1" },
                    h(StoragePreviewCard, {
                      id: Number(reference.storage_file_id),
                      title: reference.label || `storage:${reference.storage_file_id}`,
                      meta: `${reference.kind} reference`,
                    })
                  )
                ),
                refs.length === 0 ? empty("No references yet.") : h("div", { className: "grid gap-2" },
                  refs.map((r) => h(StoragePreviewCard, {
                    key: r.id,
                    id: r.storage_file_id,
                    title: r.label || `${r.kind} reference`,
                    meta: `${r.kind} · storage:${r.storage_file_id}`,
                  }))
                )
              )),
              panel("Items", h("div", null,
                h("form", { onSubmit: addItem, className: "grid gap-2 mb-3" },
                  h("div", { className: "grid grid-cols-[1fr_120px] gap-2" },
                    h("input", { value: item.name, onChange: (e) => setItem({ ...item, name: e.target.value }), placeholder: "Item name", className: fieldClass() }),
                    h("select", { value: item.kind, onChange: (e) => setItem({ ...item, kind: e.target.value }), className: fieldClass() },
                      ["product", "wardrobe", "prop", "location", "brand_asset", "screen_asset", "set", "offer"].map((k) => h("option", { key: k, value: k }, k))
                    )
                  ),
                  h("div", { className: "grid grid-cols-[1fr_96px] gap-2" },
                    h("input", { value: item.storage_file_ids, onChange: (e) => setItem({ ...item, storage_file_ids: e.target.value }), placeholder: "Storage ids, comma-separated", className: fieldClass() }),
                    h("button", { type: "button", onClick: () => openStoragePicker("item"), className: secondaryButtonClass() }, "Browse")
                  ),
                  parseIDs(item.storage_file_ids).length > 0 && h(StoragePreviewStrip, {
                    ids: parseIDs(item.storage_file_ids),
                    titlePrefix: item.name || "Selected item file",
                  }),
                  h("textarea", { value: item.visual_rules, onChange: (e) => setItem({ ...item, visual_rules: e.target.value }), placeholder: "Visual rules", className: `${fieldClass()} min-h-[56px]` }),
                  h("button", { className: buttonClass() }, "Add item")
                ),
                items.length === 0 ? empty("No items yet.") : items.map((it) =>
                  h("label", { key: it.id, className: "flex items-start gap-2 border border-border rounded p-2 mb-2" },
                    h("input", {
                      type: "checkbox",
                      checked: generation.item_ids.includes(it.id),
                      onChange: (e) => setGeneration((g) => ({ ...g, item_ids: e.target.checked ? [...g.item_ids, it.id] : g.item_ids.filter((id) => id !== it.id) })),
                      className: "mt-1"
                    }),
                    h("span", { className: "min-w-0" },
                      h("span", { className: "block text-sm font-medium truncate" }, it.name),
                      h("span", { className: "block text-xs text-text-dim truncate" }, `${it.kind}${it.storage_file_ids?.length ? " · storage " + it.storage_file_ids.join(", ") : ""}`),
                      it.storage_file_ids?.length ? h("span", { className: "block mt-2" },
                        h(StoragePreviewStrip, { ids: it.storage_file_ids, titlePrefix: it.name })
                      ) : null
                    )
                  )
                )
              ))
            ),
            h("section", { className: "flex flex-col gap-4 min-w-0" },
              panel("Generate With Persona", h("form", { onSubmit: generateAsset, className: "grid gap-3" },
                h("div", { className: "text-xs text-text-muted" }, "Creates a new Media Studio asset using this persona's identity, references, selected items, style profile, and cache key."),
                h("div", { className: "grid grid-cols-[150px_1fr] gap-2" },
                  h("select", { value: generation.asset_type, onChange: (e) => setGeneration({ ...generation, asset_type: e.target.value }), className: fieldClass() },
                    ["image", "video", "audio_tts", "audio_sfx", "music", "avatar"].map((k) => h("option", { key: k, value: k }, k))
                  ),
                  h(ModelSelect, {
                    models: modelOptions,
                    provider: mediaProvider,
                    loading: modelsLoading,
                    editRequired: requiresImageEditModel,
                    value: generation.model,
                    onChange: (model) => setGeneration({ ...generation, model }),
                  })
                ),
                generation.asset_type === "image"
                  ? h("div", { className: "grid gap-2" },
                    imageReferenceCount > 0 && h("div", { className: "text-xs text-text-muted" },
                      `${imageReferenceCount} linked reference${imageReferenceCount === 1 ? "" : "s"} will be sent automatically to Media Studio, up to the selected model limit.`
                    ),
                    h("div", { className: "grid grid-cols-2 lg:grid-cols-5 gap-2" },
                      h("select", {
                        value: generation.format_preset,
                        onChange: (e) => {
                          const preset = IMAGE_FORMAT_PRESETS.find((p) => p.key === e.target.value) || IMAGE_FORMAT_PRESETS[0];
                          setGeneration({ ...generation, format_preset: preset.key, size: preset.size || generation.size, aspect: preset.aspect || generation.aspect });
                        },
                        className: fieldClass(),
                        title: "Output format preset",
                      },
                        IMAGE_FORMAT_PRESETS.map((p) => h("option", { key: p.key, value: p.key }, p.label))
                      ),
                      h("select", {
                        value: generation.aspect,
                        onChange: (e) => setGeneration({ ...generation, aspect: e.target.value, format_preset: "custom" }),
                        className: fieldClass(),
                        title: "Aspect ratio",
                      },
                        (modelAspects.length ? modelAspects : ["1:1", "2:3", "3:2", "9:16", "16:9"]).map((a) => h("option", { key: a, value: a }, a))
                      ),
                      h("input", { value: generation.size, onChange: (e) => setGeneration({ ...generation, size: e.target.value, format_preset: "custom" }), placeholder: "Size", className: fieldClass(), title: "Pixel size, e.g. 1024x1536" }),
                      h("select", { value: generation.quality, onChange: (e) => setGeneration({ ...generation, quality: e.target.value }), className: fieldClass(), title: "Quality" },
                        IMAGE_QUALITIES.map((q) => h("option", { key: q, value: q }, q))
                      ),
                      h("select", { value: generation.output_format, onChange: (e) => setGeneration({ ...generation, output_format: e.target.value }), className: fieldClass(), title: "File format" },
                        OUTPUT_FORMATS.map((f) => h("option", { key: f, value: f }, f.toUpperCase()))
                      )
                    )
                    )
                  : (generation.asset_type === "video" || generation.asset_type === "avatar")
                    ? h("div", { className: "grid grid-cols-2 gap-2" },
                      h("select", { value: generation.aspect, onChange: (e) => setGeneration({ ...generation, aspect: e.target.value }), className: fieldClass(), title: "Aspect ratio" },
                        (modelAspects.length ? modelAspects : VIDEO_ASPECTS).map((a) => h("option", { key: a, value: a }, a))
                      ),
                      modelDurations.length > 0
                        ? h("select", { value: generation.duration ? `${generation.duration}s` : modelDurations[0], onChange: (e) => setGeneration({ ...generation, duration: e.target.value.replace(/[^0-9]/g, "") }), className: fieldClass(), title: "Duration" },
                            modelDurations.map((d) => h("option", { key: d, value: d }, d))
                          )
                        : h("input", { value: generation.duration, onChange: (e) => setGeneration({ ...generation, duration: e.target.value }), placeholder: "Duration seconds", className: fieldClass() })
                    )
                    : (generation.asset_type === "audio_sfx" || generation.asset_type === "music")
                      ? h("div", { className: "grid grid-cols-1 gap-2" },
                          modelDurations.length > 0
                            ? h("select", { value: generation.duration ? `${generation.duration}s` : modelDurations[0], onChange: (e) => setGeneration({ ...generation, duration: e.target.value.replace(/[^0-9]/g, "") }), className: fieldClass(), title: "Duration" },
                                modelDurations.map((d) => h("option", { key: d, value: d }, d))
                              )
                            : h("input", { value: generation.duration, onChange: (e) => setGeneration({ ...generation, duration: e.target.value }), placeholder: "Duration seconds", className: fieldClass() })
                        )
                      : null,
                h("textarea", { value: generation.prompt, onChange: (e) => setGeneration({ ...generation, prompt: e.target.value }), placeholder: "Prompt for this persona...", className: `${fieldClass()} min-h-[86px]` }),
                h("button", { className: buttonClass(), disabled: !generation.prompt.trim() }, "Generate")
              )),
              panel("Recent Assets", h("div", { className: "grid gap-2" },
                assets.length === 0 ? empty("No generated assets yet.") : assets.map((a) =>
                  h("div", { key: a.id, className: "border border-border rounded p-3" },
                    h("div", { className: "flex gap-2 text-sm" },
                      h("span", { className: "font-medium" }, a.asset_type),
                      h("span", { className: "text-text-dim" }, a.status),
                      a.storage_file_id ? h("span", { className: "ml-auto text-accent" }, `storage:${a.storage_file_id}`) : null
                    ),
                    h("div", { className: "text-xs text-text-muted mt-1 line-clamp-2" }, a.prompt),
                    h("div", { className: "text-[11px] text-text-dim mt-1" }, [a.provider_slug, a.provider_model, new Date(a.created_at).toLocaleString()].filter(Boolean).join(" · "))
                  )
                )
              ))
            )
          )
        : h("div", { className: "flex-1 grid place-items-center text-text-muted" }, "Create or select a persona.")
    ),
    pickerTarget && h(StoragePicker, {
      files: storageFiles,
      loading: storageLoading,
      error: storageError,
      query: storageQuery,
      target: pickerTarget,
      onQuery: setStorageQuery,
      onSearch: () => loadStorageFiles(storageQuery),
      onClose: () => setPickerTarget(null),
      onChoose: chooseStorageFile,
    })
  );
}

async function post(url, body) {
  const res = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : {};
  if (!res.ok) throw new Error(data.error || text || `HTTP ${res.status}`);
  return data;
}

function parseIDs(value) {
  return String(value || "")
    .split(/[,\s]+/)
    .map((x) => Number(x))
    .filter((x) => Number.isFinite(x) && x > 0);
}

function imageSourceOptions(refs, items) {
  const out = [];
  const seen = new Set();
  const refPriority = { face: 0, style: 1, pose: 2, outfit: 3, product: 4, location: 5, brand: 6 };
  const sortedRefs = [...(refs || [])].sort((a, b) => {
    const ap = refPriority[a.kind] ?? 99;
    const bp = refPriority[b.kind] ?? 99;
    return ap - bp || Number(a.id || 0) - Number(b.id || 0);
  });
  for (const ref of sortedRefs) {
    if (ref.kind === "voice") continue;
    const id = Number(ref.storage_file_id);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push({
      value: `storage:${id}`,
      label: `Reference: ${ref.kind}${ref.label ? ` - ${ref.label}` : ""} - storage:${id}`,
    });
  }
  for (const item of items || []) {
    for (const raw of item.storage_file_ids || []) {
      const id = Number(raw);
      if (!id || seen.has(id)) continue;
      seen.add(id);
      out.push({
        value: `storage:${id}`,
        label: `Item: ${item.name || item.kind} - storage:${id}`,
      });
    }
  }
  return out;
}

function modelSupportsImageEdit(model, provider) {
  if (!model) return false;
  if (model.supports_image_edit) return true;
  const id = String(model.id || "").toLowerCase();
  if (String(provider || "").toLowerCase() === "venice-ai") {
    return id.endsWith("-edit") || id.includes("image-edit");
  }
  return id.endsWith("-edit") ||
    id.startsWith("gpt-image") ||
    id === "dall-e-2" ||
    id.startsWith("gemini-") ||
    id.includes("image-edit");
}

function fieldClass() {
  return "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text min-w-0";
}

function buttonClass() {
  return "bg-accent text-bg rounded px-3 py-1.5 text-sm font-semibold disabled:opacity-50";
}

function secondaryButtonClass() {
  return "border border-border rounded px-2 py-1.5 text-sm text-text-muted hover:text-text hover:bg-bg-input";
}

function ModelSelect({ models, provider, loading, editRequired, value, onChange }) {
  if (loading && models.length === 0) {
    return h("div", { className: `${fieldClass()} text-text-dim` }, provider ? `Loading ${provider} models...` : "Loading models...");
  }
  if (models.length === 0) {
    return h("input", {
      value,
      onChange: (e) => onChange(e.target.value),
      placeholder: editRequired ? "Edit model (provider default if empty)" : "Model (provider default if empty)",
      className: fieldClass(),
      title: editRequired
        ? "No edit-capable model list returned by Media Studio. Leave empty for the provider's edit default or type an edit model id."
        : "No model list returned by Media Studio. Leave empty for provider default or type a model id.",
    });
  }
  return h("select", {
    value,
    onChange: (e) => onChange(e.target.value),
    className: fieldClass(),
    title: provider ? `${editRequired ? "Edit models" : "Models"} from ${provider}` : "Media Studio models",
  },
    models.map((m) => h("option", { key: m.id, value: m.id },
      `${m.id}${m.max_source_images ? ` - ${m.max_source_images} refs` : ""}${m.price_usd ? ` - ${formatCost(m.price_usd)}` : ""}${m.model_type ? ` - ${m.model_type}` : ""}`
    ))
  );
}

function input(label, value, onChange) {
  return h("input", {
    value,
    onChange: (e) => onChange(e.target.value),
    placeholder: label,
    className: fieldClass(),
  });
}

function panel(title, body) {
  return h("section", { className: "border border-border rounded bg-bg-card min-w-0" },
    h("div", { className: "px-3 py-2 border-b border-border text-xs uppercase text-text-dim tracking-wide" }, title),
    h("div", { className: "p-3" }, body)
  );
}

function chip(text, key) {
  return h("div", { key, className: "inline-flex mr-2 mb-2 px-2 py-1 rounded border border-border text-xs text-text-muted" }, text);
}

function empty(text) {
  return h("div", { className: "text-sm text-text-muted py-3" }, text);
}

function formatCost(n) {
  if (!n || n <= 0) return "";
  if (n >= 0.01) return "$" + n.toFixed(2);
  if (n >= 0.001) return "$" + n.toFixed(4);
  return "$" + n.toFixed(6);
}

function StoragePreviewCard({ id, title, meta }) {
  if (!id || !Number.isFinite(id)) return null;
  return h("a", {
    href: `/api/apps/storage/files/${id}/content`,
    target: "_blank",
    rel: "noopener",
    className: "flex items-center gap-3 border border-border rounded p-2 hover:bg-bg-input min-w-0",
    title: `Open storage:${id}`,
  },
    h(StorageThumb, { id, size: 52 }),
    h("span", { className: "min-w-0 flex-1" },
      h("span", { className: "block text-sm text-text truncate" }, title || `storage:${id}`),
      h("span", { className: "block text-xs text-text-dim truncate" }, meta || `storage:${id}`)
    )
  );
}

function StoragePreviewStrip({ ids, titlePrefix }) {
  const clean = (ids || []).filter((id) => id && Number.isFinite(Number(id)));
  if (clean.length === 0) return null;
  return h("span", { className: "flex flex-wrap gap-2" },
    clean.map((id) => h("a", {
      key: id,
      href: `/api/apps/storage/files/${id}/content`,
      target: "_blank",
      rel: "noopener",
      title: `${titlePrefix || "Storage file"} · storage:${id}`,
      className: "inline-flex items-center gap-2 border border-border rounded p-1.5 hover:bg-bg-input max-w-[160px]",
    },
      h(StorageThumb, { id, size: 38 }),
      h("span", { className: "text-[11px] text-text-dim truncate min-w-0" }, `storage:${id}`)
    ))
  );
}

function StorageThumb({ id, size }) {
  return h("span", {
    className: "relative rounded border border-border bg-bg-input overflow-hidden shrink-0 grid place-items-center text-[10px] text-text-dim",
    style: { width: size, height: size },
  },
    h("span", { className: "absolute inset-0 grid place-items-center" }, `#${id}`),
    h("img", {
      src: `/api/apps/storage/files/${id}/content`,
      alt: "",
      loading: "lazy",
      className: "relative w-full h-full object-cover bg-bg-input",
      onError: (e) => {
        e.currentTarget.style.display = "none";
      },
    })
  );
}

function StoragePicker({ files, loading, error, query, target, onQuery, onSearch, onClose, onChoose }) {
  const title = target === "item" ? "Choose item reference file" : "Choose persona reference file";
  const hint = target === "item"
    ? "Pick packshots, product photos, logos, screen captures, or set references."
    : "Pick face, style, outfit, voice, product, or location references.";
  return h("div", { className: "fixed inset-0 bg-black/60 flex items-center justify-center p-6", style: { zIndex: 9998 } },
    h("div", { className: "bg-bg border border-border rounded shadow-xl w-full max-w-4xl max-h-[84vh] flex flex-col" },
      h("header", { className: "px-4 py-3 border-b border-border flex items-center gap-3" },
        h("div", { className: "min-w-0 flex-1" },
          h("div", { className: "text-sm text-text font-medium" }, title),
          h("div", { className: "text-xs text-text-dim truncate" }, hint)
        ),
        h("button", { onClick: onClose, className: "text-text-dim hover:text-text px-2 text-lg leading-none" }, "x")
      ),
      h("form", {
        onSubmit: (e) => {
          e.preventDefault();
          onSearch();
        },
        className: "px-4 py-3 border-b border-border grid grid-cols-[1fr_88px] gap-2"
      },
        h("input", { value: query, onChange: (e) => onQuery(e.target.value), placeholder: "Search storage...", className: fieldClass() }),
        h("button", { className: secondaryButtonClass() }, "Search")
      ),
      h("div", { className: "flex-1 overflow-auto" },
        loading && h("div", { className: "p-4 text-text-muted text-sm" }, "Loading storage..."),
        error && h("div", { className: "p-4 text-red text-sm whitespace-pre-wrap" }, error),
        !loading && !error && files.length === 0 && h("div", { className: "p-4 text-text-muted text-sm" }, "No files found in linked Storage."),
        !loading && !error && files.length > 0 && h("ul", { className: "divide-y divide-border" },
          files.map((file) => h("li", { key: file.id },
            h("button", {
              type: "button",
              onClick: () => onChoose(file),
              className: "w-full text-left px-4 py-3 hover:bg-bg-input flex items-center gap-3"
            },
              h(FileThumb, { file }),
              h("span", { className: "min-w-0 flex-1" },
                h("span", { className: "block text-sm text-text truncate" }, file.name || `file #${file.id}`),
                h("span", { className: "block text-xs text-text-dim truncate" },
                  `${file.folder || "/"} · storage:${file.id}${file.content_type ? ` · ${file.content_type}` : ""}${fileSize(file.size_bytes) ? ` · ${fileSize(file.size_bytes)}` : ""}`
                )
              ),
              h("span", { className: "text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted uppercase" }, storageFileKind(file))
            )
          ))
        )
      )
    )
  );
}

function FileThumb({ file }) {
  const kind = storageFileKind(file);
  if (kind === "image") {
    return h("span", { className: "w-12 h-12 rounded border border-border bg-bg-input overflow-hidden shrink-0" },
      h("img", { src: `/api/apps/storage/files/${file.id}/content`, alt: "", className: "w-full h-full object-cover", loading: "lazy" })
    );
  }
  return h("span", { className: "w-12 h-12 rounded border border-border bg-bg-input text-text-dim shrink-0 grid place-items-center text-[10px] uppercase" }, kind);
}

function storageFileKind(file) {
  const ct = (file.content_type || "").toLowerCase();
  const name = (file.name || "").toLowerCase();
  if (ct.startsWith("image/") || /\.(png|jpe?g|webp|gif|avif)$/.test(name)) return "image";
  if (ct.startsWith("audio/") || /\.(mp3|wav|m4a|aac|flac|ogg)$/.test(name)) return "audio";
  if (ct.startsWith("video/") || /\.(mp4|mov|webm|mkv)$/.test(name)) return "video";
  return "file";
}

function fileSize(bytes) {
  if (!bytes || bytes <= 0) return "";
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
