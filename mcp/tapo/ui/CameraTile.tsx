// CameraTile — chat-attachment component. The agent emits one with:
//   respond(components=[{app:"tapo", name:"camera-tile", props:{camera_id:N}}])
// and the dashboard mounts this under the message bubble.
//
// Renders the same look as the panel's grid tiles so the visual
// language stays consistent across surfaces. Live-updates the
// snapshot every 5s while mounted; pauses when document is hidden so
// background tabs don't keep polling the camera.

import { useEffect, useRef, useState } from "react";

const API = "/api/apps/tapo";
const REFRESH_MS = 5000;

interface Camera {
  id: number;
  name: string;
  room: string;
  online: boolean;
  model: string;
}

interface Props {
  camera_id: number;
  /** Soft convention: when true, render synthetic dummy data so the
   *  dashboard's app catalog can preview the tile on a fresh install. */
  preview?: boolean;
  /** Injected by the host. */
  projectId?: string;
}

const previewSample: Camera = {
  id: 0,
  name: "Front porch",
  room: "Outside",
  online: true,
  model: "C220",
};

export default function CameraTile({ camera_id, preview }: Props) {
  const [camera, setCamera] = useState<Camera | null>(preview ? previewSample : null);
  const [bust, setBust] = useState(0);
  const [err, setErr] = useState("");
  const visibleRef = useRef(!document.hidden);

  useEffect(() => {
    if (preview) return;
    fetch(`${API}/cameras/${camera_id}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.statusText)))
      .then(setCamera)
      .catch((e) => setErr(String(e)));
  }, [camera_id, preview]);

  useEffect(() => {
    const onVis = () => { visibleRef.current = !document.hidden; };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, []);

  useEffect(() => {
    if (!camera?.online || preview) return;
    const t = setInterval(() => {
      if (visibleRef.current) setBust((n) => n + 1);
    }, REFRESH_MS);
    return () => clearInterval(t);
  }, [camera?.online, preview]);

  if (err) {
    return <div className="text-xs text-error border border-border rounded p-2">camera: {err}</div>;
  }
  if (!camera) {
    return <div className="text-xs text-text-dim border border-border rounded p-2">loading camera…</div>;
  }

  const src = preview
    ? "data:image/svg+xml;utf8," + encodeURIComponent(previewSVG(camera.name))
    : `${API}/snapshots/${camera.id}.jpg?t=${bust}`;

  return (
    <a
      href={preview ? "#" : `#/cameras/${camera.id}`}
      className="block w-72 bg-bg-elev border border-border rounded-lg overflow-hidden no-underline hover:border-accent transition"
      onClick={(e) => preview && e.preventDefault()}
    >
      <div className="aspect-video bg-black flex items-center justify-center text-text-dim">
        {camera.online ? (
          <img
            key={bust}
            src={src}
            alt={camera.name}
            className="w-full h-full object-cover"
            onError={(e) => { (e.target as HTMLImageElement).style.opacity = "0.2"; }}
          />
        ) : (
          <span className="text-xs">offline</span>
        )}
      </div>
      <div className="px-3 py-2 flex items-center gap-2">
        <span className={`w-2 h-2 rounded-full ${camera.online ? "bg-success" : "bg-error"}`} />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium truncate">{camera.name}</div>
          <div className="text-xs text-text-dim truncate">
            {camera.room || "—"} · {camera.model || "Tapo"}
          </div>
        </div>
      </div>
    </a>
  );
}

function previewSVG(name: string): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 480 270">
  <rect width="480" height="270" fill="#1a1a1a"/>
  <circle cx="240" cy="135" r="40" fill="none" stroke="#444" stroke-width="3"/>
  <circle cx="240" cy="135" r="14" fill="#444"/>
  <text x="240" y="240" text-anchor="middle" fill="#666"
        font-family="ui-sans-serif" font-size="14">${escapeXML(name)} · preview</text>
</svg>`;
}

function escapeXML(s: string): string {
  return s.replace(/[&<>'"]/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&apos;", '"': "&quot;",
  }[c] as string));
}
