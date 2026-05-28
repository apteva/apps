// LiveCard — chat-attached live view of the agent's browser.
// Manifest name "live-view".
//
// Two modes:
//   thumb (default) — polls /api/instances/{id}/screenshot every ~3s
//                     and renders a static image. Cheap; safe for
//                     long transcripts. The operator can click to
//                     promote to live.
//   live            — fetches /api/instances/{id}/computer/stream
//                     and connects directly to the CDP WebSocket
//                     (or iframes the hosted URL for browserbase /
//                     steel). Renders the screencast onto a canvas.
//
// The full canvas + CDP client lives in this file rather than a
// shared module so the bundle can be loaded standalone by the chat
// renderer without resolving relative imports across module
// boundaries — apps are independently installable.

import { useEffect, useRef, useState } from "react";
import { Card, CardHeader, DataList, type CardVendor } from "@apteva/ui-kit";

interface Props {
  instance_id: string;
  height?: number;
  mode?: "thumb" | "live";
  preview?: boolean;
}

type Descriptor =
  | { kind: "cdp-ws"; wsURL: string; display: { w: number; h: number }; backend: string }
  | { kind: "iframe"; url: string; backend: string };

const computerLogo = (
  <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden>
    <rect x="3" y="4" width="18" height="12" rx="2" />
    <path d="M8 20h8" />
    <path d="M12 16v4" />
  </svg>
);

const computerVendor: CardVendor = {
  name: "Computer",
  logo: computerLogo,
  color: { light: "#2563eb", dark: "#93c5fd" },
};

export default function LiveCard(props: Props) {
  const initialMode = props.mode ?? "thumb";
  const [mode, setMode] = useState<"thumb" | "live">(initialMode);
  const height = props.height ?? 360;
  const panelURL = `/apps/computer/?instance=${encodeURIComponent(props.instance_id)}`;

  return (
    <Card>
      <CardHeader
        vendor={computerVendor}
        title={props.preview ? "Live browser preview" : "Live browser"}
        subtitle={props.instance_id}
        status={{ label: mode === "live" ? "live" : "snapshot", variant: mode === "live" ? "live" : "muted" }}
        action={props.preview ? undefined : { label: "Open", href: panelURL }}
      />
      <div
        className="relative w-full bg-black border-t border-border overflow-hidden"
        style={{ height }}
      >
        {props.preview ? (
          <PreviewFrame />
        ) : mode === "thumb" ? (
          <ThumbStream instanceId={props.instance_id} />
        ) : (
          <LiveStream instanceId={props.instance_id} />
        )}
      </div>
      <div className="px-4 py-3 border-t border-border">
        {!props.preview && (
          <div className="flex justify-end mb-3">
            {mode === "thumb" ? (
              <button
                onClick={() => setMode("live")}
                className="text-xs px-2 py-1 rounded border border-border hover:bg-bg-input"
              >
                Go live
              </button>
            ) : (
              <button
                onClick={() => setMode("thumb")}
                className="text-xs px-2 py-1 rounded border border-border hover:bg-bg-input"
              >
                Pause
              </button>
            )}
          </div>
        )}
        <DataList
          items={[
            { label: "Mode", value: mode === "live" ? "Live stream" : "Snapshot" },
            { label: "Agent", value: props.instance_id },
          ]}
        />
      </div>
    </Card>
  );
}

// ─── thumb mode ─────────────────────────────────────────────────────

function ThumbStream({ instanceId }: { instanceId: string }) {
  const [src, setSrc] = useState<string>("");
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    const refresh = () => {
      // Cache-bust each poll so the browser always re-fetches.
      const url = `/api/instances/${encodeURIComponent(instanceId)}/screenshot?t=${Date.now()}`;
      // Pre-load to avoid flicker.
      const img = new Image();
      img.onload = () => {
        if (alive) {
          setSrc(url);
          setErr(null);
        }
      };
      img.onerror = () => {
        if (alive) setErr("screenshot endpoint unreachable");
      };
      img.src = url;
    };
    refresh();
    const t = setInterval(refresh, 3000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [instanceId]);

  if (err) return <CenterMsg>{err}</CenterMsg>;
  if (!src) return <CenterMsg>loading…</CenterMsg>;
  return (
    <img
      src={src}
      alt="browser screenshot"
      className="w-full h-full object-contain"
    />
  );
}

// ─── live mode ──────────────────────────────────────────────────────

function LiveStream({ instanceId }: { instanceId: string }) {
  const [descriptor, setDescriptor] = useState<Descriptor | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    fetch(`/api/instances/${encodeURIComponent(instanceId)}/computer/stream`)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json();
      })
      .then((d: Descriptor) => {
        if (alive) setDescriptor(d);
      })
      .catch((e) => {
        if (alive) setErr(String(e));
      });
    return () => {
      alive = false;
    };
  }, [instanceId]);

  if (err) return <CenterMsg>stream descriptor: {err}</CenterMsg>;
  if (!descriptor) return <CenterMsg>connecting…</CenterMsg>;
  if (descriptor.kind === "iframe") {
    return (
      <iframe
        src={descriptor.url}
        className="w-full h-full border-0"
        title="live view"
      />
    );
  }
  return <CDPCanvas wsURL={descriptor.wsURL} display={descriptor.display} />;
}

// CDPCanvas — the proven cmd/debug-view JS, restructured. Speaks
// raw CDP over a WebSocket: Page.startScreencast, paints frames,
// acks. Read-only here (chat embed); the operator panel uses the
// shared component with input forwarding turned on.
function CDPCanvas({
  wsURL,
  display,
}: {
  wsURL: string;
  display: { w: number; h: number };
}) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [status, setStatus] = useState<"connecting" | "connected" | "error">(
    "connecting",
  );

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    let nextId = 1;
    const ws = new WebSocket(wsURL);

    const send = (method: string, params?: object) => {
      const id = nextId++;
      ws.send(JSON.stringify({ id, method, params: params ?? {} }));
    };

    ws.onopen = () => {
      setStatus("connected");
      send("Page.enable");
      send("Page.startScreencast", {
        format: "jpeg",
        quality: 60,
        maxWidth: display.w,
        maxHeight: display.h,
        everyNthFrame: 2,
      });
    };
    ws.onerror = () => setStatus("error");
    ws.onclose = () => setStatus("error");
    ws.onmessage = (msg) => {
      let m: any;
      try {
        m = JSON.parse(msg.data);
      } catch {
        return;
      }
      if (m.method === "Page.screencastFrame") {
        const img = new Image();
        img.onload = () => {
          if (
            img.naturalWidth !== canvas.width ||
            img.naturalHeight !== canvas.height
          ) {
            canvas.width = img.naturalWidth;
            canvas.height = img.naturalHeight;
          }
          ctx.drawImage(img, 0, 0);
        };
        img.src = "data:image/jpeg;base64," + m.params.data;
        send("Page.screencastFrameAck", { sessionId: m.params.sessionId });
      }
    };

    return () => {
      try {
        ws.close();
      } catch {}
    };
  }, [wsURL, display.w, display.h]);

  return (
    <>
      <canvas
        ref={canvasRef}
        width={display.w}
        height={display.h}
        className="w-full h-full object-contain bg-black"
      />
      {status !== "connected" && (
        <div className="absolute inset-0 flex items-center justify-center text-xs text-zinc-300 bg-black/40">
          {status}
        </div>
      )}
    </>
  );
}

// ─── shared bits ────────────────────────────────────────────────────

function CenterMsg({ children }: { children: React.ReactNode }) {
  return (
    <div className="w-full h-full flex items-center justify-center text-xs text-zinc-400">
      {children}
    </div>
  );
}

function PreviewFrame() {
  return (
    <div className="w-full h-full flex items-center justify-center bg-black text-zinc-400 text-sm">
      <div className="text-center">
        <div className="text-3xl mb-2">▣</div>
        <p>Live browser</p>
        <p className="text-xs text-zinc-500 mt-1">
          Connects when an agent session is active
        </p>
      </div>
    </div>
  );
}
