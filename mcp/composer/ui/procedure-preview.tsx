// Live preview for web-canvas-1 procedural clips. The server builds the same
// self-contained document the renderer captures; it runs here in a sandboxed
// iframe and is seeked by postMessage, so scrubbing needs no render.
import React, { useEffect, useMemo, useRef, useState } from "react";
import { Pause, Play } from "lucide-react";

const API = "/api/apps/composer";

export interface ProceduralClipRef {
  key: string;
  track: number;
  clip: number;
  uid: string;
  start: number;
  length: number;
  sourceStart: number;
  procedureId: number;
  revision: number;
  parameters: Record<string, unknown>;
  inputs: Record<string, unknown>;
}

export function proceduralClips(editText: string): ProceduralClipRef[] {
  let parsed: any;
  try { parsed = JSON.parse(editText); } catch { return []; }
  const out: ProceduralClipRef[] = [];
  (parsed?.timeline?.tracks || []).forEach((track: any, ti: number) => {
    (track?.clips || []).forEach((clip: any, ci: number) => {
      const procedure = clip?.asset?.procedure;
      if (!procedure?.procedure_id) return;
      out.push({
        key: `${ti}:${ci}`, track: ti, clip: ci, uid: String(clip.uid || `clip ${ci + 1}`),
        start: Number(clip.start) || 0, length: Number(clip.length) || 0, sourceStart: Number(clip.source_start) || 0,
        procedureId: Number(procedure.procedure_id), revision: Number(procedure.revision) || 0,
        parameters: procedure.parameters || {}, inputs: procedure.inputs || {},
      });
    });
  });
  return out;
}

export function withClipParameters(editText: string, ref: ProceduralClipRef, parameters: Record<string, unknown>): string {
  const parsed = JSON.parse(editText);
  parsed.timeline.tracks[ref.track].clips[ref.clip].asset.procedure.parameters = parameters;
  return JSON.stringify(parsed, null, 2);
}

// Mirrors resolutionWH in compositions.go.
export function canvasSize(outputText: string): { width: number; height: number; fps: number } {
  let output: any = {};
  try { output = JSON.parse(outputText) || {}; } catch {}
  let [w, h] = ({ sd: [854, 480], fullhd: [1920, 1080], "4k": [3840, 2160] } as Record<string, number[]>)[output.resolution] || [1280, 720];
  if (output.aspect === "9:16") [w, h] = [h, w];
  else if (output.aspect === "1:1") w = h;
  else if (output.aspect === "4:3") w = Math.floor((h * 4) / 3);
  return { width: w, height: h, fps: Number(output.fps) || 30 };
}

function withProject(path: string, projectId: string): string {
  return projectId ? `${path}${path.includes("?") ? "&" : "?"}project_id=${encodeURIComponent(projectId)}` : path;
}

export default function ProcedurePreview({ projectId, editText, outputText, onEdit }: {
  projectId: string;
  editText: string;
  outputText: string;
  onEdit: (value: string) => void;
}) {
  const clips = useMemo(() => proceduralClips(editText), [editText]);
  const [selectedKey, setSelectedKey] = useState("");
  const selected = clips.find((c) => c.key === selectedKey) || clips[0];
  const canvas = useMemo(() => canvasSize(outputText), [outputText]);
  const [doc, setDoc] = useState("");
  const [message, setMessage] = useState("");
  const [time, setTime] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [paramsText, setParamsText] = useState("");
  const [paramsError, setParamsError] = useState("");
  const [frameWidth, setFrameWidth] = useState(640);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const timeRef = useRef(0);
  timeRef.current = time;

  const paramsKey = selected ? JSON.stringify(selected.parameters) : "";
  const requestKey = selected ? JSON.stringify([selected.procedureId, selected.revision, selected.parameters, selected.inputs, selected.length, selected.sourceStart, canvas]) : "";

  useEffect(() => {
    // Keep the user's own formatting while their text already matches the clip.
    let current = "";
    try { current = JSON.stringify(JSON.parse(paramsText)); } catch {}
    if (current === paramsKey) return;
    setParamsText(selected ? JSON.stringify(selected.parameters, null, 2) : "");
    setParamsError("");
  }, [selected?.key, paramsKey]);

  useEffect(() => {
    if (!selected) return;
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const res = await fetch(withProject(`${API}/procedure/${selected.procedureId}/preview`, projectId), {
          method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            revision: selected.revision, parameters: selected.parameters, inputs: selected.inputs,
            width: canvas.width, height: canvas.height, fps: canvas.fps, duration: selected.length, source_start: selected.sourceStart,
          }),
        });
        const text = await res.text();
        if (cancelled) return;
        if (!res.ok) { setDoc(""); setMessage(text.trim() || `Preview failed (${res.status})`); return; }
        setMessage("Loading preview...");
        setDoc(text);
      } catch (err: any) {
        if (!cancelled) setMessage(err?.message || "Preview failed");
      }
    }, 300);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [requestKey, projectId]);

  const seek = (t: number) => {
    if (!selected) return;
    iframeRef.current?.contentWindow?.postMessage({ type: "composer:seek", t: selected.sourceStart + t }, "*");
  };

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (event.source !== iframeRef.current?.contentWindow) return;
      const data = event.data || {};
      if (data.type === "composer:ready") { setMessage(""); seek(timeRef.current); }
      if (data.type === "composer:error") setMessage(`Scene error: ${data.message}`);
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [selected?.key, selected?.sourceStart]);

  useEffect(() => { seek(time); }, [time]);

  useEffect(() => {
    if (!playing || !selected) return;
    let last = performance.now();
    const id = setInterval(() => {
      const now = performance.now();
      const next = timeRef.current + (now - last) / 1000;
      last = now;
      if (next >= selected.length) { setTime(selected.length); setPlaying(false); return; }
      setTime(next);
    }, 1000 / Math.min(canvas.fps, 60));
    return () => clearInterval(id);
  }, [playing, selected?.key, selected?.length, canvas.fps]);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const observer = new ResizeObserver(() => setFrameWidth(el.clientWidth));
    observer.observe(el);
    setFrameWidth(el.clientWidth);
    return () => observer.disconnect();
  }, []);

  if (!clips.length) return null;
  const scale = frameWidth / canvas.width;

  const applyParams = (value: string) => {
    setParamsText(value);
    if (!selected) return;
    let parsed: unknown;
    try { parsed = JSON.parse(value); } catch (err: any) { setParamsError(err?.message || "Invalid JSON"); return; }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) { setParamsError("Parameters must be a JSON object"); return; }
    setParamsError("");
    onEdit(withClipParameters(editText, selected, parsed as Record<string, unknown>));
  };

  return (
    <section className="border border-border rounded-lg p-3 space-y-3">
      <div className="flex items-center gap-2 text-xs">
        <span className="text-text-muted">Procedural clip</span>
        <select value={selected?.key} onChange={(e) => { setSelectedKey(e.target.value); setTime(0); setPlaying(false); }} className="bg-bg-input border border-border rounded px-2 py-1 text-xs">
          {clips.map((c) => <option key={c.key} value={c.key}>{c.uid} · procedure {c.procedureId}{c.revision ? ` r${c.revision}` : ""}</option>)}
        </select>
        <span className="text-text-dim ml-auto">{canvas.width}x{canvas.height} · {canvas.fps} fps</span>
      </div>
      <div ref={wrapRef} className="relative w-full overflow-hidden rounded bg-bg-input" style={{ height: canvas.height * scale }}>
        {doc && (
          <iframe
            ref={iframeRef}
            title="Procedure preview"
            srcDoc={doc}
            sandbox="allow-scripts"
            style={{ width: canvas.width, height: canvas.height, border: 0, transform: `scale(${scale})`, transformOrigin: "0 0", position: "absolute", left: 0, top: 0 }}
          />
        )}
        {message && <div className="absolute inset-0 flex items-center justify-center p-4 text-xs text-text-muted text-center">{message}</div>}
      </div>
      <div className="flex items-center gap-2">
        <button
          onClick={() => { if (!playing && selected && time >= selected.length) setTime(0); setPlaying((v) => !v); }}
          className="border border-border rounded p-1.5 hover:bg-bg-input"
          aria-label={playing ? "Pause preview" : "Play preview"}
        >
          {playing ? <Pause size={14} /> : <Play size={14} />}
        </button>
        <input
          type="range"
          min={0}
          max={selected?.length || 0}
          step={1 / canvas.fps}
          value={Math.min(time, selected?.length || 0)}
          onChange={(e) => { setPlaying(false); setTime(Number(e.target.value)); }}
          className="flex-1"
          aria-label="Preview time"
        />
        <span className="text-xs text-text-dim tabular-nums">{time.toFixed(2)}s / {(selected?.length || 0).toFixed(2)}s</span>
      </div>
      <label className="block">
        <span className="text-text-muted text-xs block mb-1">Parameters</span>
        <textarea
          value={paramsText}
          onChange={(e) => applyParams(e.target.value)}
          spellCheck={false}
          style={{ fontFamily: "ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace", minHeight: 160 }}
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
        />
        {paramsError && <span className="text-xs text-red">{paramsError}</span>}
      </label>
    </section>
  );
}
