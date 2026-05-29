// ui/MediaPanel.tsx
import { useCallback, useEffect, useRef, useState } from "react";
import { jsxDEV, Fragment } from "react/jsx-dev-runtime";
function useAppEvents(app, projectId, onEvent) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId)
      return;
    const handler = (ev) => handlerRef.current(ev);
    const bridge = window.__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    let lastSeq = 0;
    let es = null;
    let cancelled = false;
    let reconnectTimer = null;
    const connect = () => {
      if (cancelled)
        return;
      const url = `/api/app-events/${encodeURIComponent(app)}` + `?project_id=${encodeURIComponent(projectId)}` + (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data);
          if (ev.seq <= lastSeq)
            return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer)
            window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer)
        window.clearTimeout(reconnectTimer);
      if (es)
        es.close();
    };
  }, [app, projectId]);
}
function formatCost(n) {
  if (!n || n <= 0)
    return "";
  if (n >= 0.01)
    return "$" + n.toFixed(2);
  if (n >= 0.001)
    return "$" + n.toFixed(4);
  return "$" + n.toFixed(6);
}
var API = "/api/apps/media-studio";
var TAB_LABELS = {
  image: "Images",
  video: "Videos",
  audio_tts: "Audio",
  music: "Music",
  avatar: "Avatar"
};
var IMAGE_MODEL_LABELS = {
  "gpt-image-2": "GPT Image 2 (current)",
  "gpt-image-1.5": "GPT Image 1.5",
  "gpt-image-1": "GPT Image 1",
  "gpt-image-1-mini": "GPT Image 1 Mini",
  "dall-e-3": "DALL·E 3 (legacy)",
  "dall-e-2": "DALL·E 2 (legacy)"
};
var IMAGE_MODELS = [
  "gpt-image-2",
  "gpt-image-1.5",
  "gpt-image-1",
  "gpt-image-1-mini",
  "dall-e-3",
  "dall-e-2"
];
var IMAGE_SIZES = {
  "gpt-image-2": ["1024x1024", "1024x1536", "1536x1024", "2048x2048", "3840x2160"],
  "gpt-image-1.5": ["1024x1024", "1024x1536", "1536x1024"],
  "gpt-image-1": ["1024x1024", "1024x1536", "1536x1024"],
  "gpt-image-1-mini": ["1024x1024", "1024x1536", "1536x1024"],
  "dall-e-3": ["1024x1024", "1792x1024", "1024x1792"],
  "dall-e-2": ["256x256", "512x512", "1024x1024"]
};
var GPT_IMAGE_QUALITIES = ["auto", "low", "medium", "high"];
var DALLE3_QUALITIES = ["standard", "hd"];
function isGptImage(m) {
  return m.startsWith("gpt-image");
}
var EDIT_MODELS = [
  "firered-image-edit",
  "qwen-edit",
  "grok-imagine-edit",
  "flux-2-max-edit",
  "gpt-image-2-edit"
];
var EDIT_MODEL_SOURCE_LIMITS = {
  "firered-image-edit": 3,
  "qwen-edit": 3,
  "grok-imagine-edit": 3,
  "flux-2-max-edit": 3,
  "gpt-image-2-edit": 3
};
function IconImage() {
  return /* @__PURE__ */ jsxDEV("svg", {
    width: "14",
    height: "14",
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: "1.5",
    children: [
      /* @__PURE__ */ jsxDEV("rect", {
        x: "1.5",
        y: "2.5",
        width: "13",
        height: "11",
        rx: "1"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("circle", {
        cx: "5.5",
        cy: "6",
        r: "1"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("path", {
        d: "M2 12l3.5-3.5 3 3L11 7l3 3"
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function IconVideo() {
  return /* @__PURE__ */ jsxDEV("svg", {
    width: "14",
    height: "14",
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: "1.5",
    children: [
      /* @__PURE__ */ jsxDEV("rect", {
        x: "1.5",
        y: "3.5",
        width: "10",
        height: "9",
        rx: "1"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("path", {
        d: "M11.5 7l3-2v6l-3-2z"
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function IconAudio() {
  return /* @__PURE__ */ jsxDEV("svg", {
    width: "14",
    height: "14",
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: "1.5",
    children: [
      /* @__PURE__ */ jsxDEV("path", {
        d: "M3 6v4h2l3 2.5v-9L5 6H3z"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("path", {
        d: "M10 5.5a3 3 0 010 5"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("path", {
        d: "M12 3.5a6 6 0 010 9"
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function IconMusic() {
  return /* @__PURE__ */ jsxDEV("svg", {
    width: "14",
    height: "14",
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: "1.5",
    children: [
      /* @__PURE__ */ jsxDEV("path", {
        d: "M6 12V3l7-1.5v9"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("circle", {
        cx: "4.5",
        cy: "12",
        r: "1.5"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("circle", {
        cx: "11.5",
        cy: "10.5",
        r: "1.5"
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function imageSrc(g) {
  if (g.storage_urls && g.storage_urls.length > 0)
    return g.storage_urls[0];
  if (g.local_cache_url)
    return g.local_cache_url;
  if (g.thumbnail_b64)
    return `data:image/jpeg;base64,${g.thumbnail_b64}`;
  return "";
}
function MediaPanel({ projectId }) {
  const [tab, setTab] = useState("image");
  const [audioSubKind, setAudioSubKind] = useState("audio_tts");
  const activeKind = tab === "audio" ? audioSubKind : tab;
  const [items, setItems] = useState([]);
  const [bindings, setBindings] = useState(null);
  const [status, setStatus] = useState("");
  const [generating, setGenerating] = useState(false);
  const [selected, setSelected] = useState(null);
  const [lightbox, setLightbox] = useState(null);
  const [prompt, setPrompt] = useState("");
  const [imageModel, setImageModel] = useState("gpt-image-2");
  const [imageSize, setImageSize] = useState("1024x1024");
  const [imageQuality, setImageQuality] = useState("auto");
  const [imageFormat, setImageFormat] = useState("png");
  const [duration, setDuration] = useState(5);
  const [aspect, setAspect] = useState("16:9");
  const [voice, setVoice] = useState("");
  const [videoModel, setVideoModel] = useState("");
  const [audioModel, setAudioModel] = useState("");
  const [sfxModel, setSfxModel] = useState("");
  const [musicModel, setMusicModel] = useState("");
  const [safeMode, setSafeMode] = useState(false);
  const [avatars, setAvatars] = useState([]);
  const [selectedAvatar, setSelectedAvatar] = useState("");
  const [voices, setVoices] = useState([]);
  const [sourceImages, setSourceImages] = useState([]);
  const [editModel, setEditModel] = useState("firered-image-edit");
  const isEditMode = activeKind === "image" && sourceImages.length > 0;
  const [liveModels, setLiveModels] = useState(null);
  const [liveProvider, setLiveProvider] = useState("");
  const [videoJobs, setVideoJobs] = useState([]);
  useEffect(() => {
    const allowed = IMAGE_SIZES[imageModel] || ["1024x1024"];
    if (!allowed.includes(imageSize))
      setImageSize(allowed[0]);
    if (isGptImage(imageModel)) {
      if (!GPT_IMAGE_QUALITIES.includes(imageQuality))
        setImageQuality("auto");
    } else if (imageModel === "dall-e-3") {
      if (!DALLE3_QUALITIES.includes(imageQuality))
        setImageQuality("standard");
    }
  }, [imageModel, imageSize, imageQuality]);
  const loadBindings = useCallback(async () => {
    try {
      const res = await fetch(`${API}/bindings`, { credentials: "same-origin" });
      if (!res.ok)
        return;
      const data = await res.json();
      setBindings(data);
    } catch {}
  }, []);
  const loadGenerations = useCallback(async () => {
    try {
      const res = await fetch(`${API}/generations?project_id=${encodeURIComponent(projectId)}&kind=${activeKind}`, { credentials: "same-origin" });
      if (!res.ok) {
        setStatus(`Error: ${res.status}`);
        return;
      }
      const data = await res.json();
      setItems(data.generations || []);
      const n = (data.generations || []).length;
      setStatus(`${n} generation${n === 1 ? "" : "s"}`);
    } catch (e) {
      setStatus("Error: " + e.message);
    }
  }, [projectId, activeKind]);
  useEffect(() => {
    loadBindings();
  }, [loadBindings]);
  useEffect(() => {
    loadGenerations();
  }, [loadGenerations]);
  useEffect(() => {
    if (activeKind !== "video" && activeKind !== "avatar")
      return;
    let cancelled = false;
    let prevInFlight = new Set;
    const load = () => {
      fetch(`${API}/video-jobs?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin"
      }).then((r) => r.ok ? r.json() : null).then((data) => {
        if (cancelled || !data)
          return;
        const jobs = Array.isArray(data.jobs) ? data.jobs : [];
        setVideoJobs(jobs);
        const nowInFlight = new Set(jobs.filter((j) => j.status === "queued" || j.status === "polling").map((j) => j.id));
        let transitioned = false;
        for (const id of prevInFlight)
          if (!nowInFlight.has(id))
            transitioned = true;
        if (transitioned)
          loadGenerations();
        prevInFlight = nowInFlight;
      }).catch(() => {});
    };
    load();
    const t = window.setInterval(load, 5000);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [activeKind, projectId, loadGenerations]);
  useEffect(() => {
    if (activeKind !== "avatar" && activeKind !== "audio_tts")
      return;
    let cancelled = false;
    if (activeKind === "avatar") {
      fetch(`${API}/avatars`, { credentials: "same-origin" }).then((r) => r.ok ? r.json() : null).then((data) => {
        if (cancelled || !data)
          return;
        const list = Array.isArray(data.avatars) ? data.avatars : [];
        setAvatars(list);
        if (list.length > 0 && !list.some((x) => x.id === selectedAvatar)) {
          setSelectedAvatar(list[0].id);
        }
      }).catch(() => {});
    }
    fetch(`${API}/voices?kind=${encodeURIComponent(activeKind)}`, { credentials: "same-origin" }).then((r) => r.ok ? r.json() : null).then((data) => {
      if (cancelled || !data)
        return;
      const list = Array.isArray(data.voices) ? data.voices : [];
      setVoices(list);
      if (list.length > 0 && !list.some((x) => x.id === voice)) {
        setVoice(list[0].id);
      }
    }).catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [activeKind, bindings]);
  useEffect(() => {
    const currentBoundSlug = bindings?.[activeKind]?.slug || "";
    if (!currentBoundSlug) {
      setLiveModels(null);
      setLiveProvider("");
      return;
    }
    let cancelled = false;
    fetch(`${API}/models?kind=${activeKind}`, { credentials: "same-origin" }).then((r) => r.ok ? r.json() : null).then((data) => {
      if (cancelled || !data)
        return;
      if (Array.isArray(data.models)) {
        setLiveModels(data.models);
        setLiveProvider(String(data.provider || ""));
        if (data.models.length > 0) {
          if (activeKind === "image") {
            const have = data.models.some((m) => m.id === imageModel);
            if (!have)
              setImageModel(data.models[0].id);
            const editModels = data.models.filter((m) => m.supports_image_edit);
            if (editModels.length > 0 && !editModels.some((m) => m.id === editModel)) {
              setEditModel(editModels[0].id);
            }
          } else if (activeKind === "video") {
            const have = data.models.some((m) => m.id === videoModel);
            if (!have)
              setVideoModel(data.models[0].id);
          } else if (activeKind === "audio_tts") {
            const have = data.models.some((m) => m.id === audioModel);
            if (!have)
              setAudioModel(data.models[0].id);
          } else if (activeKind === "audio_sfx") {
            const have = data.models.some((m) => m.id === sfxModel);
            if (!have)
              setSfxModel(data.models[0].id);
          } else if (activeKind === "music") {
            const have = data.models.some((m) => m.id === musicModel);
            if (!have)
              setMusicModel(data.models[0].id);
          }
        }
      }
    }).catch(() => {
      if (!cancelled)
        setLiveModels(null);
    });
    return () => {
      cancelled = true;
    };
  }, [activeKind, bindings]);
  useAppEvents("media-studio", projectId, (ev) => {
    if (ev.topic === "media.generated") {
      if (ev.data?.kind === activeKind)
        loadGenerations();
    }
  });
  const currentBinding = bindings ? bindings[activeKind] : null;
  const isBound = !!currentBinding?.bound;
  const currentModelId = activeKind === "image" ? isEditMode ? editModel : imageModel : activeKind === "video" ? videoModel : activeKind === "audio_tts" ? audioModel : activeKind === "audio_sfx" ? sfxModel : activeKind === "music" ? musicModel : "";
  const currentModel = liveModels?.find((m) => m.id === currentModelId);
  const showVideoRefInput = activeKind === "video" && !!currentModel?.supports_image_to_video;
  const editSourceLimit = activeKind === "image" ? currentModel?.max_source_images || EDIT_MODEL_SOURCE_LIMITS[editModel] || 1 : 1;
  const referenceInputMax = activeKind === "image" ? editSourceLimit : 1;
  const addSourceImage = (value, label) => {
    const trimmed = value.trim();
    if (!trimmed)
      return;
    setSourceImages((cur) => {
      const withoutExisting = cur.filter((x) => x.value !== trimmed);
      return [...withoutExisting, { value: trimmed, label }].slice(0, referenceInputMax);
    });
  };
  const removeSourceImage = (index) => {
    setSourceImages((cur) => cur.filter((_, i) => i !== index));
  };
  useEffect(() => {
    if (!currentModel)
      return;
    if (currentModel.aspect_ratios && currentModel.aspect_ratios.length > 0 && !currentModel.aspect_ratios.includes(aspect)) {
      setAspect(currentModel.default_aspect_ratio || currentModel.aspect_ratios[0]);
    }
    if (currentModel.durations && currentModel.durations.length > 0) {
      const want = `${duration}s`;
      if (!currentModel.durations.includes(want)) {
        const first = currentModel.durations[0];
        const n = parseInt(first.replace(/[^\d]/g, ""), 10);
        if (!isNaN(n))
          setDuration(n);
      }
    }
  }, [currentModelId]);
  const generate = async () => {
    if (!prompt.trim() || generating)
      return;
    setGenerating(true);
    setStatus("Generating…");
    try {
      const body = {
        kind: activeKind,
        prompt,
        project_id: projectId
      };
      if (activeKind === "image") {
        if (isEditMode) {
          if (sourceImages.length > editSourceLimit) {
            setStatus(`Error: ${editModel} supports at most ${editSourceLimit} source image${editSourceLimit === 1 ? "" : "s"}.`);
            return;
          }
          body.model = editModel;
          if (sourceImages.length === 1) {
            body.source_image = sourceImages[0].value;
          } else {
            body.source_images = sourceImages.map((x) => x.value);
          }
          body.options = { output_format: imageFormat, safe_mode: safeMode };
        } else {
          body.model = imageModel;
          body.size = imageSize;
          const options = { safe_mode: safeMode };
          if (imageModel !== "dall-e-2")
            options.quality = imageQuality;
          if (isGptImage(imageModel))
            options.output_format = imageFormat;
          body.options = options;
        }
      } else if (activeKind === "video") {
        if (videoModel)
          body.model = videoModel;
        body.duration = duration;
        body.aspect = aspect;
        if (showVideoRefInput && sourceImages[0]?.value) {
          body.source_image = sourceImages[0].value;
        }
      } else if (activeKind === "audio_tts") {
        if (audioModel)
          body.model = audioModel;
        if (voice)
          body.voice = voice;
      } else if (activeKind === "audio_sfx") {
        if (sfxModel)
          body.model = sfxModel;
        body.duration = duration;
      } else if (activeKind === "music") {
        if (musicModel)
          body.model = musicModel;
        body.duration = duration;
      } else if (activeKind === "avatar") {
        body.avatar = selectedAvatar;
        if (voice)
          body.voice = voice;
      }
      const res = await fetch(`${API}/generate`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Error ${res.status}: ${text.slice(0, 300)}`);
        return;
      }
      let result = {};
      try {
        result = JSON.parse(text);
      } catch {}
      if (result.isError) {
        const msg = result.content?.find((c) => c.type === "text")?.text || "generation failed";
        setStatus(`Error: ${msg}`);
        return;
      }
      const meta = result._meta;
      if (meta?.status === "queued") {
        setPrompt("");
        const costStr = meta.cost_usd ? ` · est. ${formatCost(meta.cost_usd)}` : "";
        setStatus(`Queued — job #${meta.job_id}${costStr}, polling for completion…`);
        return;
      }
      setPrompt("");
      setStatus("Done.");
      loadGenerations();
    } catch (e) {
      setStatus("Error: " + e.message);
    } finally {
      setGenerating(false);
    }
  };
  return /* @__PURE__ */ jsxDEV("div", {
    className: "h-full flex flex-col",
    children: [
      /* @__PURE__ */ jsxDEV("nav", {
        className: "flex items-center border-b border-border px-4",
        children: Object.keys(TAB_LABELS).map((k) => {
          const t = k === "audio_tts" ? "audio" : k;
          const active = tab === t;
          const bindingKey = k;
          const bound = bindings ? bindings[bindingKey]?.bound : false;
          return /* @__PURE__ */ jsxDEV("button", {
            onClick: () => setTab(t),
            className: "flex items-center gap-1.5 px-3 py-2.5 text-sm border-b-2 transition-colors " + (active ? "border-accent text-text" : "border-transparent text-text-muted hover:text-text"),
            children: [
              /* @__PURE__ */ jsxDEV(KindIcon, {
                kind: k
              }, undefined, false, undefined, this),
              TAB_LABELS[k],
              /* @__PURE__ */ jsxDEV(BoundDot, {
                bound
              }, undefined, false, undefined, this)
            ]
          }, t, true, undefined, this);
        })
      }, undefined, false, undefined, this),
      tab === "audio" && /* @__PURE__ */ jsxDEV("div", {
        className: "flex items-center gap-1 px-4 py-1.5 border-b border-border bg-bg-card",
        children: [
          /* @__PURE__ */ jsxDEV(SubTabButton, {
            label: "TTS",
            active: audioSubKind === "audio_tts",
            onClick: () => setAudioSubKind("audio_tts"),
            bound: !!bindings?.audio_tts.bound
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV(SubTabButton, {
            label: "SFX",
            active: audioSubKind === "audio_sfx",
            onClick: () => setAudioSubKind("audio_sfx"),
            bound: !!bindings?.audio_sfx.bound
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      bindings && !isBound && /* @__PURE__ */ jsxDEV("div", {
        className: "px-4 py-2 text-xs text-text-muted bg-bg-card border-b border-border",
        children: [
          "No provider bound for ",
          /* @__PURE__ */ jsxDEV("strong", {
            className: "text-text",
            children: activeKind
          }, undefined, false, undefined, this),
          ". Open the app settings to pick one."
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "flex-1 flex min-h-0",
        children: [
          /* @__PURE__ */ jsxDEV("div", {
            className: "flex-1 flex flex-col p-6 gap-4 min-w-0",
            children: [
              (activeKind === "image" || showVideoRefInput) && /* @__PURE__ */ jsxDEV(ReferenceImageInput, {
                sources: showVideoRefInput ? sourceImages.slice(0, 1) : sourceImages,
                maxSources: referenceInputMax,
                onAdd: addSourceImage,
                onRemove: removeSourceImage,
                onClear: () => setSourceImages([]),
                hint: showVideoRefInput ? "Source image for the image-to-video model (required)" : undefined
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV(Composer, {
                kind: activeKind,
                prompt,
                setPrompt,
                generate,
                generating,
                disabled: !isBound,
                isEditMode,
                liveModels,
                liveProvider,
                imageModel,
                setImageModel,
                imageSize,
                setImageSize,
                imageQuality,
                setImageQuality,
                imageFormat,
                setImageFormat,
                editModel,
                setEditModel,
                editSourceLimit,
                editModels: liveModels?.filter((m) => m.supports_image_edit) || [],
                videoModel,
                setVideoModel,
                audioModel,
                setAudioModel,
                sfxModel,
                setSfxModel,
                musicModel,
                setMusicModel,
                currentModel,
                safeMode,
                setSafeMode,
                duration,
                setDuration,
                aspect,
                setAspect,
                voice,
                setVoice,
                avatars,
                selectedAvatar,
                setSelectedAvatar,
                voices
              }, undefined, false, undefined, this),
              (activeKind === "video" || activeKind === "avatar") && videoJobs.length > 0 && /* @__PURE__ */ jsxDEV(VideoJobsBanner, {
                jobs: videoJobs
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "flex-1 overflow-auto border border-border rounded",
                children: items.length === 0 && !generating ? /* @__PURE__ */ jsxDEV("div", {
                  className: "py-12 px-6 text-center text-text-muted text-sm",
                  children: status || "No generations yet for this kind."
                }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV(Gallery, {
                  kind: activeKind,
                  items,
                  onSelect: setSelected,
                  onOpenLightbox: setLightbox,
                  generating,
                  generatingPrompt: prompt,
                  generatingModel: isEditMode ? editModel : imageModel
                }, undefined, false, undefined, this)
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "text-xs text-text-dim",
                children: status
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          selected && /* @__PURE__ */ jsxDEV(DetailAside, {
            selected,
            onClose: () => setSelected(null),
            onUseAsReference: selected.kind === "image" && selected.storage_ids.length > 0 ? () => {
              const id = selected.storage_ids[0];
              addSourceImage(`storage:${id}`, `Storage #${id}`);
              setSelected(null);
              setTab("image");
            } : undefined
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      lightbox && /* @__PURE__ */ jsxDEV(Lightbox, {
        item: lightbox,
        onClose: () => setLightbox(null),
        onUseAsReference: lightbox.kind === "image" && lightbox.storage_ids.length > 0 ? () => {
          const id = lightbox.storage_ids[0];
          addSourceImage(`storage:${id}`, `Storage #${id}`);
          setLightbox(null);
          setTab("image");
        } : undefined
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function KindIcon({ kind }) {
  if (kind === "image")
    return /* @__PURE__ */ jsxDEV(IconImage, {}, undefined, false, undefined, this);
  if (kind === "video")
    return /* @__PURE__ */ jsxDEV(IconVideo, {}, undefined, false, undefined, this);
  if (kind === "music")
    return /* @__PURE__ */ jsxDEV(IconMusic, {}, undefined, false, undefined, this);
  if (kind === "avatar")
    return /* @__PURE__ */ jsxDEV(IconAvatar, {}, undefined, false, undefined, this);
  return /* @__PURE__ */ jsxDEV(IconAudio, {}, undefined, false, undefined, this);
}
function IconAvatar() {
  return /* @__PURE__ */ jsxDEV("svg", {
    width: "14",
    height: "14",
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: "1.5",
    children: [
      /* @__PURE__ */ jsxDEV("circle", {
        cx: "8",
        cy: "5.5",
        r: "2.5"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("path", {
        d: "M3 13.5c0-2.8 2.2-4.5 5-4.5s5 1.7 5 4.5"
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function BoundDot({ bound }) {
  return /* @__PURE__ */ jsxDEV("span", {
    className: "rounded-full ml-1",
    style: {
      width: 6,
      height: 6,
      background: bound ? "var(--apteva-accent, #4ade80)" : "var(--apteva-text-dim, #555)"
    }
  }, undefined, false, undefined, this);
}
function SubTabButton({
  label,
  active,
  bound,
  onClick
}) {
  return /* @__PURE__ */ jsxDEV("button", {
    onClick,
    className: "flex items-center gap-1.5 px-2.5 py-1 text-xs rounded transition-colors " + (active ? "bg-bg-input text-text" : "text-text-muted hover:text-text"),
    children: [
      label,
      /* @__PURE__ */ jsxDEV(BoundDot, {
        bound
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function Composer(p) {
  const promptPlaceholder = p.isEditMode ? "Edit instruction — 'remove the tree', 'change sky to sunset'" : p.kind === "avatar" ? "Script the avatar will speak…" : p.kind === "audio_tts" ? "Text to speak" : p.kind === "music" ? "A jazzy lo-fi loop with piano" : p.kind === "video" ? "A cat walking through a sunlit garden" : p.kind === "audio_sfx" ? "A door creaking open" : "a cat in a hat";
  return /* @__PURE__ */ jsxDEV("div", {
    className: "flex items-end gap-3 flex-wrap",
    children: [
      /* @__PURE__ */ jsxDEV("div", {
        className: "flex-1",
        style: { minWidth: 240 },
        children: [
          /* @__PURE__ */ jsxDEV("label", {
            className: "text-text-muted text-xs",
            children: "Prompt"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("input", {
            type: "text",
            value: p.prompt,
            onChange: (e) => p.setPrompt(e.target.value),
            onKeyDown: (e) => {
              if (e.key === "Enter")
                p.generate();
            },
            placeholder: promptPlaceholder,
            className: "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      p.kind === "image" && p.isEditMode && /* @__PURE__ */ jsxDEV(EditOptions, {
        model: p.editModel,
        setModel: p.setEditModel,
        format: p.imageFormat,
        setFormat: p.setImageFormat,
        maxSources: p.editSourceLimit,
        liveModels: p.editModels,
        liveProvider: p.liveProvider
      }, undefined, false, undefined, this),
      p.kind === "image" && !p.isEditMode && /* @__PURE__ */ jsxDEV(ImageOptions, {
        model: p.imageModel,
        setModel: p.setImageModel,
        size: p.imageSize,
        setSize: p.setImageSize,
        quality: p.imageQuality,
        setQuality: p.setImageQuality,
        format: p.imageFormat,
        setFormat: p.setImageFormat,
        liveModels: p.liveModels,
        liveProvider: p.liveProvider
      }, undefined, false, undefined, this),
      p.kind === "video" && /* @__PURE__ */ jsxDEV(Fragment, {
        children: [
          /* @__PURE__ */ jsxDEV(MediaModelPicker, {
            model: p.videoModel,
            setModel: p.setVideoModel,
            liveModels: p.liveModels,
            liveProvider: p.liveProvider
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV(ConstrainedDuration, {
            durations: p.currentModel?.durations,
            value: p.duration,
            onChange: p.setDuration
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV(ConstrainedAspect, {
            aspects: p.currentModel?.aspect_ratios,
            value: p.aspect,
            onChange: p.setAspect,
            disabledHint: p.currentModel?.model_type === "image-to-video" ? "Inherited from source image" : undefined
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      p.kind === "audio_tts" && /* @__PURE__ */ jsxDEV(Fragment, {
        children: [
          /* @__PURE__ */ jsxDEV(MediaModelPicker, {
            model: p.audioModel,
            setModel: p.setAudioModel,
            liveModels: p.liveModels,
            liveProvider: p.liveProvider
          }, undefined, false, undefined, this),
          p.voices.length > 0 ? /* @__PURE__ */ jsxDEV(VoiceSelect, {
            voice: p.voice,
            setVoice: p.setVoice,
            voices: p.voices
          }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV(TextField, {
            label: "Voice",
            value: p.voice,
            onChange: p.setVoice,
            placeholder: "voice_id"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      p.kind === "audio_sfx" && /* @__PURE__ */ jsxDEV(Fragment, {
        children: [
          /* @__PURE__ */ jsxDEV(MediaModelPicker, {
            model: p.sfxModel,
            setModel: p.setSfxModel,
            liveModels: p.liveModels,
            liveProvider: p.liveProvider
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV(NumberField, {
            label: "Duration (s)",
            value: p.duration,
            onChange: p.setDuration,
            min: 1,
            max: 30
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      p.kind === "music" && /* @__PURE__ */ jsxDEV(Fragment, {
        children: [
          /* @__PURE__ */ jsxDEV(MediaModelPicker, {
            model: p.musicModel,
            setModel: p.setMusicModel,
            liveModels: p.liveModels,
            liveProvider: p.liveProvider
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV(NumberField, {
            label: "Duration (s)",
            value: p.duration,
            onChange: p.setDuration,
            min: 3,
            max: 300
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      p.kind === "avatar" && /* @__PURE__ */ jsxDEV(AvatarPicker, {
        avatars: p.avatars,
        selected: p.selectedAvatar,
        setSelected: p.setSelectedAvatar
      }, undefined, false, undefined, this),
      p.kind === "avatar" && p.voices.length > 0 && /* @__PURE__ */ jsxDEV(VoiceSelect, {
        voice: p.voice,
        setVoice: p.setVoice,
        voices: p.voices
      }, undefined, false, undefined, this),
      p.kind === "image" && /* @__PURE__ */ jsxDEV(SafeModeToggle, {
        value: p.safeMode,
        onChange: p.setSafeMode
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("button", {
        onClick: p.generate,
        disabled: !p.prompt.trim() || p.generating || p.disabled,
        className: "px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50",
        children: p.generating ? "…" : p.isEditMode ? "Edit" : p.kind === "avatar" ? "Generate avatar" : "Generate"
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function AvatarPicker({
  avatars,
  selected,
  setSelected
}) {
  if (avatars.length === 0) {
    return /* @__PURE__ */ jsxDEV("div", {
      children: [
        /* @__PURE__ */ jsxDEV("label", {
          className: "text-text-muted text-xs block",
          children: "Avatar"
        }, undefined, false, undefined, this),
        /* @__PURE__ */ jsxDEV("div", {
          className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim",
          style: { minWidth: 200 },
          children: "no replicas — train one in your provider"
        }, undefined, false, undefined, this)
      ]
    }, undefined, true, undefined, this);
  }
  return /* @__PURE__ */ jsxDEV("div", {
    style: { flexBasis: "100%" },
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-text-muted text-xs block mb-1",
        children: "Avatar / replica"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "flex gap-2 flex-wrap",
        children: avatars.map((av) => {
          const isSel = av.id === selected;
          return /* @__PURE__ */ jsxDEV("button", {
            onClick: () => setSelected(av.id),
            title: `${av.name || av.id}${av.status ? ` (${av.status})` : ""}`,
            className: "border rounded overflow-hidden text-left " + (isSel ? "border-accent" : "border-border hover:border-accent"),
            style: { width: 96 },
            children: [
              av.thumbnail ? /* @__PURE__ */ jsxDEV("video", {
                src: av.thumbnail,
                muted: true,
                loop: true,
                onMouseEnter: (e) => e.currentTarget.play(),
                onMouseLeave: (e) => e.currentTarget.pause(),
                style: { width: 96, height: 96, objectFit: "cover", display: "block" }
              }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV("div", {
                className: "flex items-center justify-center text-text-dim",
                style: { width: 96, height: 96, background: "var(--apteva-bg-input, #222)", fontSize: 10 },
                children: av.name || av.id
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "text-text truncate px-1 py-0.5",
                style: { fontSize: 10 },
                children: av.name || av.id
              }, undefined, false, undefined, this)
            ]
          }, av.id, true, undefined, this);
        })
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function SafeModeToggle({
  value,
  onChange
}) {
  return /* @__PURE__ */ jsxDEV("label", {
    className: "flex items-center gap-1.5 text-xs text-text-muted cursor-pointer select-none",
    title: "When on, Venice blurs adult-classified output. Off = pass-through (default).",
    children: [
      /* @__PURE__ */ jsxDEV("input", {
        type: "checkbox",
        checked: value,
        onChange: (e) => onChange(e.target.checked),
        style: { accentColor: "var(--apteva-accent, #4ade80)" }
      }, undefined, false, undefined, this),
      "Safe mode"
    ]
  }, undefined, true, undefined, this);
}
function EditOptions({
  model,
  setModel,
  format,
  setFormat,
  maxSources,
  liveModels,
  liveProvider
}) {
  const modelOptions = liveModels.length > 0 ? liveModels : EDIT_MODELS.map((id) => ({ id, label: id }));
  return /* @__PURE__ */ jsxDEV(Fragment, {
    children: [
      /* @__PURE__ */ jsxDEV("div", {
        children: [
          /* @__PURE__ */ jsxDEV("label", {
            className: "text-text-muted text-xs block",
            children: [
              "Edit model",
              liveModels.length > 0 && /* @__PURE__ */ jsxDEV("span", {
                className: "text-text-dim ml-1",
                style: { fontSize: 10 },
                children: [
                  "· ",
                  liveProvider,
                  " (",
                  liveModels.length,
                  ")"
                ]
              }, undefined, true, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("select", {
            value: model,
            onChange: (e) => setModel(e.target.value),
            className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
            children: modelOptions.map((m) => /* @__PURE__ */ jsxDEV("option", {
              value: m.id,
              children: m.label
            }, m.id, false, undefined, this))
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("div", {
            className: "text-text-dim mt-0.5",
            style: { fontSize: 10 },
            children: [
              "max ",
              maxSources,
              " reference",
              maxSources === 1 ? "" : "s"
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        children: [
          /* @__PURE__ */ jsxDEV("label", {
            className: "text-text-muted text-xs block",
            children: "Format"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("select", {
            value: format,
            onChange: (e) => setFormat(e.target.value),
            className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
            children: [
              /* @__PURE__ */ jsxDEV("option", {
                value: "png",
                children: "PNG"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("option", {
                value: "jpeg",
                children: "JPEG"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("option", {
                value: "webp",
                children: "WebP"
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function ReferenceImageInput({
  sources,
  maxSources,
  onAdd,
  onRemove,
  onClear,
  hint
}) {
  const [urlInput, setUrlInput] = useState("");
  const fileInputRef = useRef(null);
  const atLimit = sources.length >= maxSources;
  const handleFile = (file) => {
    const reader = new FileReader;
    reader.onload = () => {
      const result = String(reader.result || "");
      const b64 = result.includes(",") ? result.split(",", 2)[1] : result;
      onAdd(b64, `Upload (${file.name})`);
    };
    reader.readAsDataURL(file);
  };
  const handleDrop = (e) => {
    e.preventDefault();
    const files = Array.from(e.dataTransfer.files || []).filter((file) => file.type.startsWith("image/")).slice(0, Math.max(0, maxSources - sources.length));
    files.forEach(handleFile);
  };
  return /* @__PURE__ */ jsxDEV("div", {
    onDrop: handleDrop,
    onDragOver: (e) => e.preventDefault(),
    className: "flex flex-col gap-2 p-2 rounded bg-bg-card " + (sources.length > 0 ? "border border-accent" : "border border-dashed border-border"),
    children: [
      sources.length > 0 && /* @__PURE__ */ jsxDEV("div", {
        className: "flex gap-2 overflow-x-auto pb-1",
        children: sources.map((src, index) => {
          const previewSrc = sourceImagePreviewSrc(src.value);
          return /* @__PURE__ */ jsxDEV("div", {
            className: "flex items-center gap-2 border border-border rounded p-1.5 bg-bg",
            style: { minWidth: 180, maxWidth: 240 },
            children: [
              previewSrc ? /* @__PURE__ */ jsxDEV("img", {
                src: previewSrc,
                alt: "",
                style: { width: 44, height: 44, objectFit: "cover", borderRadius: 4, flexShrink: 0 }
              }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV("div", {
                style: { width: 44, height: 44, borderRadius: 4, background: "var(--apteva-bg-input, #222)", flexShrink: 0 },
                className: "flex items-center justify-center text-text-dim text-xs",
                children: "ref"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "min-w-0 flex-1",
                children: [
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "text-text-dim",
                    style: { fontSize: 10 },
                    children: [
                      "Reference ",
                      index + 1
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "text-xs text-text truncate",
                    title: src.label,
                    children: src.label || "(set)"
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("button", {
                onClick: () => onRemove(index),
                className: "text-text-muted hover:text-text text-xs px-1.5 py-0.5 border border-border rounded",
                title: "Remove reference",
                children: "x"
              }, undefined, false, undefined, this)
            ]
          }, `${src.value}-${index}`, true, undefined, this);
        })
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "flex items-center gap-3 flex-wrap",
        children: [
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text-muted text-xs",
            children: [
              hint || "Reference images",
              /* @__PURE__ */ jsxDEV("span", {
                className: "text-text-dim",
                children: [
                  " · ",
                  sources.length,
                  "/",
                  maxSources
                ]
              }, undefined, true, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("button", {
            disabled: atLimit,
            onClick: () => fileInputRef.current?.click(),
            className: "text-xs px-2 py-1 border border-border rounded text-text hover:border-accent disabled:opacity-50",
            children: "Upload"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("input", {
            ref: fileInputRef,
            type: "file",
            accept: "image/*",
            multiple: true,
            onChange: (e) => {
              const files = Array.from(e.target.files || []).slice(0, Math.max(0, maxSources - sources.length));
              files.forEach(handleFile);
              e.target.value = "";
            },
            style: { display: "none" }
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text-dim text-xs",
            children: "or paste URL:"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("input", {
            type: "text",
            value: urlInput,
            disabled: atLimit,
            onChange: (e) => setUrlInput(e.target.value),
            onKeyDown: (e) => {
              if (e.key === "Enter" && urlInput.trim() && !atLimit) {
                const trimmed = urlInput.trim();
                onAdd(trimmed, trimmed.length > 40 ? trimmed.slice(0, 37) + "..." : trimmed);
                setUrlInput("");
              }
            },
            placeholder: atLimit ? "reference limit reached" : "https://...",
            className: "flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50",
            style: { minWidth: 180 }
          }, undefined, false, undefined, this),
          sources.length > 0 && /* @__PURE__ */ jsxDEV("button", {
            onClick: onClear,
            className: "text-text-muted hover:text-text text-xs px-2 py-1 border border-border rounded",
            children: "Clear"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text-dim text-xs",
            children: "pick from history with Use as reference"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function ImageOptions({
  model,
  setModel,
  size,
  setSize,
  quality,
  setQuality,
  format,
  setFormat,
  liveModels,
  liveProvider
}) {
  const usingLive = liveModels && liveModels.length > 0;
  return /* @__PURE__ */ jsxDEV(Fragment, {
    children: [
      /* @__PURE__ */ jsxDEV("div", {
        children: [
          /* @__PURE__ */ jsxDEV("label", {
            className: "text-text-muted text-xs block",
            children: [
              "Model",
              usingLive && /* @__PURE__ */ jsxDEV("span", {
                className: "text-text-dim ml-1",
                style: { fontSize: 10 },
                children: [
                  "· ",
                  liveProvider,
                  " (",
                  liveModels.length,
                  ")"
                ]
              }, undefined, true, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("select", {
            value: model,
            onChange: (e) => setModel(e.target.value),
            className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
            children: usingLive ? liveModels.map((m) => /* @__PURE__ */ jsxDEV("option", {
              value: m.id,
              children: m.label
            }, m.id, false, undefined, this)) : IMAGE_MODELS.map((m) => /* @__PURE__ */ jsxDEV("option", {
              value: m,
              children: IMAGE_MODEL_LABELS[m]
            }, m, false, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        children: [
          /* @__PURE__ */ jsxDEV("label", {
            className: "text-text-muted text-xs block",
            children: "Size"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("select", {
            value: size,
            onChange: (e) => setSize(e.target.value),
            className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
            children: (IMAGE_SIZES[model] || ["1024x1024"]).map((s) => /* @__PURE__ */ jsxDEV("option", {
              value: s,
              children: s
            }, s, false, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      model !== "dall-e-2" && /* @__PURE__ */ jsxDEV("div", {
        children: [
          /* @__PURE__ */ jsxDEV("label", {
            className: "text-text-muted text-xs block",
            children: "Quality"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("select", {
            value: quality,
            onChange: (e) => setQuality(e.target.value),
            className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
            children: (isGptImage(model) ? GPT_IMAGE_QUALITIES : DALLE3_QUALITIES).map((q) => /* @__PURE__ */ jsxDEV("option", {
              value: q,
              children: q
            }, q, false, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      isGptImage(model) && /* @__PURE__ */ jsxDEV("div", {
        children: [
          /* @__PURE__ */ jsxDEV("label", {
            className: "text-text-muted text-xs block",
            children: "Format"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("select", {
            value: format,
            onChange: (e) => setFormat(e.target.value),
            className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
            children: [
              /* @__PURE__ */ jsxDEV("option", {
                value: "png",
                children: "PNG"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("option", {
                value: "jpeg",
                children: "JPEG"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("option", {
                value: "webp",
                children: "WebP"
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function MediaModelPicker({
  model,
  setModel,
  liveModels,
  liveProvider
}) {
  const models = liveModels || [];
  if (models.length === 0) {
    return /* @__PURE__ */ jsxDEV("div", {
      children: [
        /* @__PURE__ */ jsxDEV("label", {
          className: "text-text-muted text-xs block",
          children: "Model"
        }, undefined, false, undefined, this),
        /* @__PURE__ */ jsxDEV("div", {
          className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim",
          style: { minWidth: 200 },
          children: liveProvider ? `loading ${liveProvider}…` : "no provider bound"
        }, undefined, false, undefined, this)
      ]
    }, undefined, true, undefined, this);
  }
  return /* @__PURE__ */ jsxDEV("div", {
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-text-muted text-xs block",
        children: [
          "Model",
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text-dim ml-1",
            style: { fontSize: 10 },
            children: [
              "· ",
              liveProvider,
              " (",
              models.length,
              ")"
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("select", {
        value: model,
        onChange: (e) => setModel(e.target.value),
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
        style: { maxWidth: 280 },
        children: models.map((m) => {
          const tag = m.model_type === "image-to-video" ? " · img→vid" : "";
          const price = formatCost(m.price_usd || 0);
          const suffix = [tag, price ? ` ${price}` : ""].filter(Boolean).join("");
          return /* @__PURE__ */ jsxDEV("option", {
            value: m.id,
            children: [
              m.id,
              suffix
            ]
          }, m.id, true, undefined, this);
        })
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function VoiceSelect({
  voice,
  setVoice,
  voices
}) {
  return /* @__PURE__ */ jsxDEV("div", {
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-text-muted text-xs block",
        children: "Voice"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("select", {
        value: voice,
        onChange: (e) => setVoice(e.target.value),
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
        style: { maxWidth: 260 },
        children: voices.map((v) => /* @__PURE__ */ jsxDEV("option", {
          value: v.id,
          children: [
            v.name || v.id,
            v.language ? ` · ${v.language}` : "",
            v.gender ? ` · ${v.gender}` : ""
          ]
        }, v.id, true, undefined, this))
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function ConstrainedDuration({
  durations,
  value,
  onChange
}) {
  if (!durations || durations.length === 0) {
    return /* @__PURE__ */ jsxDEV(NumberField, {
      label: "Duration (s)",
      value,
      onChange,
      min: 1,
      max: 60
    }, undefined, false, undefined, this);
  }
  return /* @__PURE__ */ jsxDEV("div", {
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-text-muted text-xs block",
        children: "Duration"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("select", {
        value: `${value}s`,
        onChange: (e) => {
          const n = parseInt(e.target.value.replace(/[^\d]/g, ""), 10);
          if (!isNaN(n))
            onChange(n);
        },
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
        children: durations.map((d) => /* @__PURE__ */ jsxDEV("option", {
          value: d,
          children: d
        }, d, false, undefined, this))
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function ConstrainedAspect({
  aspects,
  value,
  onChange,
  disabledHint
}) {
  if (disabledHint) {
    return /* @__PURE__ */ jsxDEV("div", {
      children: [
        /* @__PURE__ */ jsxDEV("label", {
          className: "text-text-muted text-xs block",
          children: "Aspect"
        }, undefined, false, undefined, this),
        /* @__PURE__ */ jsxDEV("div", {
          className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim",
          style: { minWidth: 160 },
          title: disabledHint,
          children: disabledHint
        }, undefined, false, undefined, this)
      ]
    }, undefined, true, undefined, this);
  }
  if (!aspects || aspects.length === 0) {
    return /* @__PURE__ */ jsxDEV(TextField, {
      label: "Aspect",
      value,
      onChange
    }, undefined, false, undefined, this);
  }
  return /* @__PURE__ */ jsxDEV("div", {
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-text-muted text-xs block",
        children: "Aspect"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("select", {
        value,
        onChange: (e) => onChange(e.target.value),
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
        children: aspects.map((a) => /* @__PURE__ */ jsxDEV("option", {
          value: a,
          children: a
        }, a, false, undefined, this))
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function NumberField({
  label,
  value,
  onChange,
  min,
  max
}) {
  return /* @__PURE__ */ jsxDEV("div", {
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-text-muted text-xs block",
        children: label
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("input", {
        type: "number",
        value,
        min,
        max,
        onChange: (e) => onChange(Number(e.target.value) || 0),
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
        style: { width: 96 }
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function TextField({
  label,
  value,
  onChange,
  placeholder
}) {
  return /* @__PURE__ */ jsxDEV("div", {
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-text-muted text-xs block",
        children: label
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("input", {
        type: "text",
        value,
        onChange: (e) => onChange(e.target.value),
        placeholder,
        className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm",
        style: { width: 140 }
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function Gallery({
  kind,
  items,
  onSelect,
  onOpenLightbox,
  generating,
  generatingPrompt,
  generatingModel
}) {
  if (kind === "image") {
    return /* @__PURE__ */ jsxDEV("div", {
      style: {
        display: "grid",
        gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
        gap: 8,
        padding: 8
      },
      children: [
        generating && /* @__PURE__ */ jsxDEV(GeneratingCard, {
          prompt: generatingPrompt,
          model: generatingModel
        }, undefined, false, undefined, this),
        items.map((g) => {
          const src = imageSrc(g);
          return /* @__PURE__ */ jsxDEV("div", {
            className: "border border-border rounded overflow-hidden hover:border-accent transition-colors",
            children: [
              src ? /* @__PURE__ */ jsxDEV("button", {
                onClick: () => onOpenLightbox(g),
                className: "block w-full",
                title: "Click to open",
                children: /* @__PURE__ */ jsxDEV("img", {
                  src,
                  alt: "",
                  className: "w-full",
                  loading: "lazy",
                  style: { display: "block" }
                }, undefined, false, undefined, this)
              }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV("div", {
                className: "bg-bg-input py-12 text-center text-text-muted text-xs",
                children: "no preview"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("button", {
                onClick: () => onSelect(g),
                className: "block w-full text-left",
                title: "Show details",
                children: /* @__PURE__ */ jsxDEV(CardMeta, {
                  g
                }, undefined, false, undefined, this)
              }, undefined, false, undefined, this)
            ]
          }, g.id, true, undefined, this);
        })
      ]
    }, undefined, true, undefined, this);
  }
  return /* @__PURE__ */ jsxDEV("div", {
    style: {
      display: "grid",
      gridTemplateColumns: kind === "video" || kind === "avatar" ? "repeat(auto-fill, minmax(360px, 1fr))" : "repeat(auto-fill, minmax(280px, 1fr))",
      gap: 8,
      padding: 8
    },
    children: [
      generating && (kind === "video" || kind === "avatar") && /* @__PURE__ */ jsxDEV(GeneratingCard, {
        prompt: generatingPrompt,
        model: generatingModel
      }, undefined, false, undefined, this),
      items.map((g) => {
        const url = g.storage_urls?.[0] || g.local_cache_url || g.upstream_urls?.[0] || "";
        return /* @__PURE__ */ jsxDEV("div", {
          className: "border border-border rounded overflow-hidden bg-bg-card",
          onClick: () => onSelect(g),
          children: [
            url ? kind === "video" || kind === "avatar" ? /* @__PURE__ */ jsxDEV("video", {
              controls: true,
              src: url,
              className: "w-full"
            }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV("audio", {
              controls: true,
              src: url,
              className: "w-full"
            }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV("div", {
              className: "bg-bg-input py-6 text-center text-text-muted text-xs",
              children: "no source"
            }, undefined, false, undefined, this),
            /* @__PURE__ */ jsxDEV(CardMeta, {
              g
            }, undefined, false, undefined, this)
          ]
        }, g.id, true, undefined, this);
      })
    ]
  }, undefined, true, undefined, this);
}
function GeneratingCard({ prompt, model }) {
  return /* @__PURE__ */ jsxDEV("div", {
    className: "border border-accent rounded overflow-hidden bg-bg-card flex flex-col items-center justify-center",
    style: { minHeight: 220 },
    children: [
      /* @__PURE__ */ jsxDEV(Spinner, {}, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "mt-3 text-sm text-text",
        children: "Generating…"
      }, undefined, false, undefined, this),
      prompt && /* @__PURE__ */ jsxDEV("div", {
        className: "mt-1 px-3 text-xs text-text-muted text-center",
        style: { maxWidth: 260 },
        title: prompt,
        children: prompt.length > 80 ? prompt.slice(0, 77) + "…" : prompt
      }, undefined, false, undefined, this),
      model && /* @__PURE__ */ jsxDEV("div", {
        className: "mt-1 text-text-dim",
        style: { fontSize: 10 },
        children: model
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function Spinner() {
  return /* @__PURE__ */ jsxDEV("svg", {
    width: "28",
    height: "28",
    viewBox: "0 0 24 24",
    children: [
      /* @__PURE__ */ jsxDEV("circle", {
        cx: "12",
        cy: "12",
        r: "9",
        fill: "none",
        stroke: "currentColor",
        strokeWidth: "2",
        strokeLinecap: "round",
        strokeDasharray: "44",
        strokeDashoffset: "22",
        style: { animation: "ms-spin 0.9s linear infinite" }
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("style", {
        children: `@keyframes ms-spin { to { transform: rotate(360deg); transform-origin: 12px 12px; } }`
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function CardMeta({ g }) {
  const cost = formatCost(g.cost_usd);
  return /* @__PURE__ */ jsxDEV("div", {
    className: "p-2",
    children: [
      /* @__PURE__ */ jsxDEV("div", {
        className: "text-text text-xs truncate",
        children: g.prompt
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "text-text-dim mt-0.5 flex items-center gap-1.5",
        style: { fontSize: 10 },
        children: [
          /* @__PURE__ */ jsxDEV("span", {
            children: g.provider
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            children: "·"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            children: g.model || g.size || "—"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            children: "·"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            children: new Date(g.created_at).toLocaleString()
          }, undefined, false, undefined, this),
          cost && /* @__PURE__ */ jsxDEV(Fragment, {
            children: [
              /* @__PURE__ */ jsxDEV("span", {
                children: "·"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("span", {
                className: "text-accent",
                children: cost
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function DetailAside({
  selected,
  onClose,
  onUseAsReference
}) {
  const url = selected.storage_urls?.[0] || selected.upstream_urls?.[0] || "";
  return /* @__PURE__ */ jsxDEV("aside", {
    className: "border-l border-border bg-bg-card flex flex-col",
    style: { width: 384 },
    children: [
      /* @__PURE__ */ jsxDEV("header", {
        className: "flex items-center gap-2 px-4 py-3 border-b border-border",
        children: [
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text font-medium truncate flex-1",
            children: selected.prompt
          }, undefined, false, undefined, this),
          onUseAsReference && /* @__PURE__ */ jsxDEV("button", {
            onClick: onUseAsReference,
            className: "text-xs px-2 py-1 border border-border rounded text-accent hover:border-accent",
            title: "Use this image as the reference for an edit",
            children: "Use as reference"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("button", {
            onClick: onClose,
            className: "text-text-muted hover:text-text leading-none px-1",
            style: { fontSize: 18 },
            children: "×"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "flex-1 overflow-auto",
        children: [
          url && selected.kind === "image" && /* @__PURE__ */ jsxDEV("img", {
            src: url,
            alt: "",
            className: "w-full"
          }, undefined, false, undefined, this),
          url && (selected.kind === "video" || selected.kind === "avatar") && /* @__PURE__ */ jsxDEV("video", {
            controls: true,
            src: url,
            className: "w-full"
          }, undefined, false, undefined, this),
          url && (selected.kind === "audio_tts" || selected.kind === "audio_sfx" || selected.kind === "music") && /* @__PURE__ */ jsxDEV("audio", {
            controls: true,
            src: url,
            className: "w-full p-3"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("dl", {
            className: "px-4 py-3 text-xs flex flex-col gap-2",
            children: [
              /* @__PURE__ */ jsxDEV(Row, {
                label: "Kind",
                value: selected.kind
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV(Row, {
                label: "Provider",
                value: selected.provider
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV(Row, {
                label: "Model",
                value: selected.model || "—"
              }, undefined, false, undefined, this),
              selected.size && /* @__PURE__ */ jsxDEV(Row, {
                label: "Size",
                value: selected.size
              }, undefined, false, undefined, this),
              selected.duration_ms > 0 && /* @__PURE__ */ jsxDEV(Row, {
                label: "Duration",
                value: `${(selected.duration_ms / 1000).toFixed(1)}s`
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV(Row, {
                label: "Count",
                value: String(selected.count)
              }, undefined, false, undefined, this),
              formatCost(selected.cost_usd) && /* @__PURE__ */ jsxDEV(Row, {
                label: "Cost",
                value: formatCost(selected.cost_usd)
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV(Row, {
                label: "Created",
                value: new Date(selected.created_at).toLocaleString()
              }, undefined, false, undefined, this),
              selected.revised_prompt && /* @__PURE__ */ jsxDEV(Row, {
                label: "Revised",
                value: selected.revised_prompt
              }, undefined, false, undefined, this),
              selected.storage_ids.length > 0 && /* @__PURE__ */ jsxDEV(Row, {
                label: "Storage IDs",
                value: selected.storage_ids.map((id) => `#${id}`).join(", ")
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          selected.storage_urls && selected.storage_urls.length > 0 && /* @__PURE__ */ jsxDEV("div", {
            className: "px-4 pb-3 flex flex-col gap-1",
            children: selected.storage_urls.map((u, i) => /* @__PURE__ */ jsxDEV("a", {
              href: u,
              target: "_blank",
              rel: "noopener",
              className: "text-accent text-xs hover:underline",
              children: [
                "Open #",
                selected.storage_ids[i],
                " →"
              ]
            }, i, true, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function VideoJobsBanner({
  jobs
}) {
  const inFlight = jobs.filter((j) => j.status === "queued" || j.status === "polling");
  const failed = jobs.filter((j) => j.status === "failed");
  if (inFlight.length === 0 && failed.length === 0)
    return null;
  return /* @__PURE__ */ jsxDEV("div", {
    className: "flex flex-col gap-1 p-2 rounded border border-border bg-bg-card",
    children: [
      inFlight.length > 0 && /* @__PURE__ */ jsxDEV("div", {
        className: "flex items-center gap-2 text-xs",
        children: [
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text",
            children: [
              /* @__PURE__ */ jsxDEV("strong", {
                children: inFlight.length
              }, undefined, false, undefined, this),
              " video",
              inFlight.length === 1 ? "" : "s",
              " processing"
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text-dim",
            children: [
              inFlight.slice(0, 3).map((j) => `#${j.id} (${j.model})`).join(", "),
              inFlight.length > 3 && `, +${inFlight.length - 3} more`
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this),
      failed.map((j) => /* @__PURE__ */ jsxDEV("div", {
        className: "flex items-start gap-2 text-xs",
        children: [
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text",
            style: { color: "var(--apteva-danger, #ef4444)" },
            children: [
              "Failed #",
              j.id,
              " (",
              j.model,
              ")"
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("span", {
            className: "text-text-dim flex-1 truncate",
            title: j.error,
            children: j.error || "(no detail)"
          }, undefined, false, undefined, this)
        ]
      }, j.id, true, undefined, this))
    ]
  }, undefined, true, undefined, this);
}
function sourceImagePreviewSrc(value) {
  const v = value.trim();
  if (!v)
    return "";
  if (v.startsWith("storage:")) {
    const id = v.slice("storage:".length);
    return `/api/apps/storage/files/${id}/content`;
  }
  if (v.startsWith("http://") || v.startsWith("https://") || v.startsWith("data:")) {
    return v;
  }
  return `data:image/png;base64,${v}`;
}
function Lightbox({
  item,
  onClose,
  onUseAsReference
}) {
  const url = item.storage_urls?.[0] || item.local_cache_url || item.upstream_urls?.[0] || imageSrc(item);
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === "Escape")
        onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return /* @__PURE__ */ jsxDEV("div", {
    onClick: onClose,
    style: {
      position: "fixed",
      inset: 0,
      background: "rgba(0,0,0,0.85)",
      zIndex: 9999,
      display: "flex",
      flexDirection: "column",
      alignItems: "center",
      justifyContent: "center",
      padding: 24
    },
    children: /* @__PURE__ */ jsxDEV("div", {
      onClick: (e) => e.stopPropagation(),
      style: {
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        maxWidth: "100%",
        maxHeight: "100%",
        gap: 12
      },
      children: [
        url && item.kind === "image" && /* @__PURE__ */ jsxDEV("img", {
          src: url,
          alt: "",
          style: { maxWidth: "92vw", maxHeight: "82vh", objectFit: "contain", borderRadius: 4 }
        }, undefined, false, undefined, this),
        url && (item.kind === "video" || item.kind === "avatar") && /* @__PURE__ */ jsxDEV("video", {
          controls: true,
          src: url,
          style: { maxWidth: "92vw", maxHeight: "82vh" }
        }, undefined, false, undefined, this),
        url && (item.kind === "audio_tts" || item.kind === "audio_sfx" || item.kind === "music") && /* @__PURE__ */ jsxDEV("audio", {
          controls: true,
          src: url,
          style: { width: 480 }
        }, undefined, false, undefined, this),
        /* @__PURE__ */ jsxDEV("div", {
          className: "text-text text-sm text-center",
          style: { maxWidth: 700 },
          children: item.prompt
        }, undefined, false, undefined, this),
        /* @__PURE__ */ jsxDEV("div", {
          className: "text-text-dim",
          style: { fontSize: 11 },
          children: [
            item.provider,
            " · ",
            item.model || item.size || "—",
            " ·",
            " ",
            new Date(item.created_at).toLocaleString(),
            formatCost(item.cost_usd) && /* @__PURE__ */ jsxDEV(Fragment, {
              children: [
                " · ",
                /* @__PURE__ */ jsxDEV("span", {
                  className: "text-accent",
                  children: formatCost(item.cost_usd)
                }, undefined, false, undefined, this)
              ]
            }, undefined, true, undefined, this)
          ]
        }, undefined, true, undefined, this),
        /* @__PURE__ */ jsxDEV("div", {
          className: "flex items-center gap-2",
          children: [
            onUseAsReference && /* @__PURE__ */ jsxDEV("button", {
              onClick: onUseAsReference,
              className: "text-xs px-3 py-1.5 border border-border rounded text-accent hover:border-accent",
              children: "Use as reference"
            }, undefined, false, undefined, this),
            url && /* @__PURE__ */ jsxDEV("a", {
              href: url,
              target: "_blank",
              rel: "noopener",
              className: "text-xs px-3 py-1.5 border border-border rounded text-text",
              children: "Open original"
            }, undefined, false, undefined, this),
            /* @__PURE__ */ jsxDEV("button", {
              onClick: onClose,
              className: "text-xs px-3 py-1.5 border border-border rounded text-text-muted",
              children: "Close (Esc)"
            }, undefined, false, undefined, this)
          ]
        }, undefined, true, undefined, this)
      ]
    }, undefined, true, undefined, this)
  }, undefined, false, undefined, this);
}
function Row({ label, value }) {
  return /* @__PURE__ */ jsxDEV("div", {
    className: "flex gap-2",
    children: [
      /* @__PURE__ */ jsxDEV("span", {
        className: "text-text-dim flex-shrink-0",
        style: { width: 80 },
        children: label
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("span", {
        className: "flex-1 min-w-0 text-text break-all",
        title: value,
        children: value
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
export {
  MediaPanel as default
};
