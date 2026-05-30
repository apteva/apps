// DeviceFrame — live device screen + input for a Simulator-app sim.
//
// Protocol (must match simulator/stream.go):
//   WS URL:  stream_url from sims_run (wss://…/api/apps/simulator/stream/<sim_id>?t=<token>)
//   server→client:
//     • first text frame: {"type":"meta","platform","width","height","codec":"h264"|"png"}
//     • binary frames: H.264 Annex-B access units, or complete PNG images
//   client→server (text JSON):
//     • {"type":"input","kind":"tap","x":0.42,"y":0.31}      (x/y normalized 0..1)
//     • {"type":"input","kind":"swipe","x":..,"y":..,"x2":..,"y2":..,"ms":300}
//     • {"type":"input","kind":"key","key":"BACK"|"HOME"|"ENTER"|…}
//     • {"type":"input","kind":"text","text":"hello"}
//
// Decode path: H.264 uses WebCodecs VideoDecoder. PNG screenshot
// streams use createImageBitmap and do not need WebCodecs.

import { useEffect, useRef, useState } from "react";

interface MetaMsg {
  type: "meta";
  platform: string;
  width: number;
  height: number;
  codec: string;
}

type InputKind = "tap" | "swipe" | "key" | "text";

export function DeviceFrame({
  streamUrl,
  platform,
}: {
  streamUrl: string;
  platform?: string;
}) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [status, setStatus] = useState<"connecting" | "live" | "error" | "unsupported" | "closed">("connecting");
  const [errMsg, setErrMsg] = useState("");
  const dims = useRef<{ w: number; h: number }>({ w: 0, h: 0 });
  const dragStart = useRef<{ x: number; y: number; t: number } | null>(null);

  useEffect(() => {
    let closed = false;
    let decoder: VideoDecoder | null = null;
    let configured = false;
    let streamCodec = "h264";

    const canvas = canvasRef.current;
    const ctx2d = canvas?.getContext("2d") ?? null;

    const drawFrame = (frame: VideoFrame) => {
      if (!canvas || !ctx2d) {
        frame.close();
        return;
      }
      if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
        canvas.width = frame.displayWidth;
        canvas.height = frame.displayHeight;
        dims.current = { w: frame.displayWidth, h: frame.displayHeight };
      }
      ctx2d.drawImage(frame, 0, 0);
      frame.close();
    };

    const drawImageFrame = async (data: Uint8Array) => {
      if (!canvas || !ctx2d) return;
      try {
        const blob = new Blob([data], { type: streamCodec === "jpeg" ? "image/jpeg" : "image/png" });
        const bitmap = await createImageBitmap(blob);
        if (closed) {
          bitmap.close();
          return;
        }
        if (canvas.width !== bitmap.width || canvas.height !== bitmap.height) {
          canvas.width = bitmap.width;
          canvas.height = bitmap.height;
          dims.current = { w: bitmap.width, h: bitmap.height };
        }
        ctx2d.drawImage(bitmap, 0, 0);
        bitmap.close();
      } catch (e) {
        setErrMsg("image decode: " + String(e));
        setStatus("error");
      }
    };

    const ensureDecoder = (codec: string) => {
      if (decoder) return;
      const hasWebCodecs = typeof (globalThis as { VideoDecoder?: unknown }).VideoDecoder !== "undefined";
      if (!hasWebCodecs) {
        setStatus("unsupported");
        return;
      }
      decoder = new VideoDecoder({
        output: drawFrame,
        error: (e) => {
          setErrMsg(String(e));
          setStatus("error");
        },
      });
      try {
        decoder.configure({
          codec: codec || "avc1.42E01E",
          optimizeForLatency: true,
        });
        configured = true;
      } catch (e) {
        setErrMsg("decoder configure: " + String(e));
        setStatus("error");
      }
    };

    const isKeyframe = (data: Uint8Array): boolean => {
      // Annex-B: scan NAL headers for type 5 (IDR) — good-enough
      // keyframe heuristic for the decoder's first-chunk requirement.
      for (let i = 0; i + 4 < data.length; i++) {
        if (data[i] === 0 && data[i + 1] === 0 && data[i + 2] === 0 && data[i + 3] === 1) {
          const nalType = data[i + 4] & 0x1f;
          if (nalType === 5 || nalType === 7) return true; // IDR or SPS
          i += 4;
        }
      }
      return false;
    };

    let sawKeyframe = false;
    const ws = new WebSocket(streamUrl);
    ws.binaryType = "arraybuffer";
    wsRef.current = ws;

    ws.onopen = () => setStatus("live");
    ws.onclose = () => {
      if (!closed) setStatus("closed");
    };
    ws.onerror = () => {
      if (!closed) {
        setStatus("error");
        setErrMsg("WebSocket error");
      }
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        try {
          const msg = JSON.parse(ev.data) as MetaMsg;
          if (msg.type === "meta") {
            dims.current = { w: msg.width, h: msg.height };
            streamCodec = msg.codec || "h264";
            if (streamCodec === "h264") {
              ensureDecoder(streamCodec);
            }
          }
        } catch {
          /* ignore non-JSON text */
        }
        return;
      }
      const data = new Uint8Array(ev.data as ArrayBuffer);
      if (streamCodec === "png" || streamCodec === "jpeg") {
        void drawImageFrame(data);
        return;
      }
      if (!decoder) ensureDecoder("avc1.42E01E");
      if (!decoder || !configured) return;
      const key = isKeyframe(data);
      if (key) sawKeyframe = true;
      if (!sawKeyframe) return; // decoder must start on a keyframe
      try {
        decoder.decode(
          new EncodedVideoChunk({
            type: key ? "key" : "delta",
            timestamp: performance.now() * 1000,
            data,
          }),
        );
      } catch (e) {
        setErrMsg("decode: " + String(e));
      }
    };

    return () => {
      closed = true;
      try {
        ws.close();
      } catch {
        /* noop */
      }
      if (decoder && decoder.state !== "closed") {
        try {
          decoder.close();
        } catch {
          /* noop */
        }
      }
    };
  }, [streamUrl]);

  const sendInput = (payload: Record<string, unknown>) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "input", ...payload }));
    }
  };

  const normXY = (e: React.PointerEvent) => {
    const canvas = canvasRef.current;
    if (!canvas) return { x: 0, y: 0 };
    const r = canvas.getBoundingClientRect();
    return {
      x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)),
      y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height)),
    };
  };

  const onPointerDown = (e: React.PointerEvent) => {
    const { x, y } = normXY(e);
    dragStart.current = { x, y, t: Date.now() };
  };

  const onPointerUp = (e: React.PointerEvent) => {
    const start = dragStart.current;
    dragStart.current = null;
    if (!start) return;
    const { x, y } = normXY(e);
    const dx = Math.abs(x - start.x);
    const dy = Math.abs(y - start.y);
    if (dx < 0.02 && dy < 0.02) {
      sendInput({ kind: "tap", x, y });
    } else {
      sendInput({ kind: "swipe", x: start.x, y: start.y, x2: x, y2: y, ms: Math.max(50, Date.now() - start.t) });
    }
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    e.preventDefault();
    if (e.key === "Enter") sendInput({ kind: "key", key: "ENTER" });
    else if (e.key === "Backspace") sendInput({ kind: "key", key: "DEL" });
    else if (e.key.length === 1) sendInput({ kind: "text", text: e.key });
  };

  return (
    <div className="flex flex-col items-center gap-2">
      <div
        className="relative rounded-lg overflow-hidden border border-border bg-black"
        style={{ maxWidth: 360 }}
      >
        <canvas
          ref={canvasRef}
          tabIndex={0}
          onPointerDown={onPointerDown}
          onPointerUp={onPointerUp}
          onKeyDown={onKeyDown}
          className="block w-full touch-none outline-none cursor-pointer"
          style={{ aspectRatio: dims.current.w && dims.current.h ? `${dims.current.w}/${dims.current.h}` : "9/19.5" }}
        />
        {status !== "live" && (
          <div className="absolute inset-0 flex items-center justify-center text-xs text-text-muted bg-black/60 text-center px-4">
            {status === "connecting" && "Connecting to device…"}
            {status === "closed" && "Stream closed"}
            {status === "error" && `Stream error: ${errMsg}`}
            {status === "unsupported" &&
              "Live device streaming needs a WebCodecs-capable browser (Chrome/Edge 94+, Safari 16.4+)."}
          </div>
        )}
      </div>
      <div className="flex gap-2">
        <DeviceKeyButton label="Back" onClick={() => sendInput({ kind: "key", key: "BACK" })} disabled={platform === "ios"} />
        <DeviceKeyButton label="Home" onClick={() => sendInput({ kind: "key", key: "HOME" })} />
        <DeviceKeyButton label="Recents" onClick={() => sendInput({ kind: "key", key: "APP_SWITCH" })} disabled={platform === "ios"} />
      </div>
    </div>
  );
}

function DeviceKeyButton({ label, onClick, disabled }: { label: string; onClick: () => void; disabled?: boolean }) {
  if (disabled) return null;
  return (
    <button
      type="button"
      onClick={onClick}
      className="px-2 py-0.5 text-xs border border-border rounded text-text-muted hover:text-text hover:border-accent"
    >
      {label}
    </button>
  );
}
