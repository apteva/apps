import React, { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/persona-studio";
const h = React.createElement;

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
    aspect: "9:16",
    size: "",
    duration: "",
  });

  const qs = useMemo(() => `project_id=${encodeURIComponent(projectId || "")}`, [projectId]);

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
    if (generation.aspect) settings.aspect = generation.aspect;
    if (generation.size) settings.size = generation.size;
    if (generation.duration) settings.duration = Number(generation.duration);
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

  const persona = bundle?.persona;
  const items = bundle?.items || [];
  const refs = bundle?.references || [];
  const assets = bundle?.assets || [];

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
                h("form", { onSubmit: addReference, className: "grid grid-cols-[1fr_120px] gap-2 mb-3" },
                  h("input", { value: reference.storage_file_id, onChange: (e) => setReference({ ...reference, storage_file_id: e.target.value }), placeholder: "Storage file id", className: fieldClass() }),
                  h("select", { value: reference.kind, onChange: (e) => setReference({ ...reference, kind: e.target.value }), className: fieldClass() },
                    ["face", "style", "outfit", "pose", "voice", "product", "location", "brand"].map((k) => h("option", { key: k, value: k }, k))
                  ),
                  h("input", { value: reference.label, onChange: (e) => setReference({ ...reference, label: e.target.value }), placeholder: "Label", className: fieldClass() }),
                  h("button", { className: buttonClass() }, "Link")
                ),
                refs.length === 0 ? empty("No references yet.") : refs.map((r) => chip(`#${r.storage_file_id} · ${r.kind}${r.label ? " · " + r.label : ""}`, r.id))
              )),
              panel("Items", h("div", null,
                h("form", { onSubmit: addItem, className: "grid gap-2 mb-3" },
                  h("div", { className: "grid grid-cols-[1fr_120px] gap-2" },
                    h("input", { value: item.name, onChange: (e) => setItem({ ...item, name: e.target.value }), placeholder: "Item name", className: fieldClass() }),
                    h("select", { value: item.kind, onChange: (e) => setItem({ ...item, kind: e.target.value }), className: fieldClass() },
                      ["product", "wardrobe", "prop", "location", "brand_asset", "screen_asset", "set", "offer"].map((k) => h("option", { key: k, value: k }, k))
                    )
                  ),
                  h("input", { value: item.storage_file_ids, onChange: (e) => setItem({ ...item, storage_file_ids: e.target.value }), placeholder: "Storage ids, comma-separated", className: fieldClass() }),
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
                      h("span", { className: "block text-xs text-text-dim truncate" }, `${it.kind}${it.storage_file_ids?.length ? " · storage " + it.storage_file_ids.join(", ") : ""}`)
                    )
                  )
                )
              ))
            ),
            h("section", { className: "flex flex-col gap-4 min-w-0" },
              panel("Generate In Place", h("form", { onSubmit: generateAsset, className: "grid gap-3" },
                h("div", { className: "grid grid-cols-4 gap-2" },
                  h("select", { value: generation.asset_type, onChange: (e) => setGeneration({ ...generation, asset_type: e.target.value }), className: fieldClass() },
                    ["image", "video", "audio_tts", "audio_sfx", "music", "avatar"].map((k) => h("option", { key: k, value: k }, k))
                  ),
                  h("input", { value: generation.aspect, onChange: (e) => setGeneration({ ...generation, aspect: e.target.value }), placeholder: "Aspect", className: fieldClass() }),
                  h("input", { value: generation.size, onChange: (e) => setGeneration({ ...generation, size: e.target.value }), placeholder: "Size", className: fieldClass() }),
                  h("input", { value: generation.duration, onChange: (e) => setGeneration({ ...generation, duration: e.target.value }), placeholder: "Duration", className: fieldClass() })
                ),
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
    )
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

function fieldClass() {
  return "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text min-w-0";
}

function buttonClass() {
  return "bg-accent text-bg rounded px-3 py-1.5 text-sm font-semibold disabled:opacity-50";
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
