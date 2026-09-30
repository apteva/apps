import type { AppHandle } from "@apteva/web-sdk";
import source from "../../ui/listener-worklet.js" with { type: "text" };
import { PreviewResampler } from "../../ui/softphone-audio";

export interface ListenerDiagnostics {
  dropped_ms: number[];
  played_ms: number[];
  max_queue_ms: number;
  max_late_ms: number;
  sequence_gaps: number[];
  network_excess_ms: number;
  network_dropped_ms: number[];
}
export interface ListenerAudioCallbacks {
  onReady(): void;
  onClose(reason: string): void;
  onDiagnostics?(value: ListenerDiagnostics): void;
}
export interface ListenerAudioConnection {
  start(url: string): Promise<void>;
  stop(): void;
  setOutputVolume(value: number): void;
}
export interface ListenerAudioRuntime { create(callbacks: ListenerAudioCallbacks): ListenerAudioConnection }
export interface ListenerPlaybackOptions { stereo?: boolean; outputVolume?: number; outputDeviceId?: string }

/** Validate the directional protocol before passing any samples to the renderer. */
export function decodeListenerFrame(buffer: ArrayBuffer) {
  if (buffer.byteLength < 26 || buffer.byteLength > 984 || buffer.byteLength % 2) throw new Error("Invalid listener audio frame");
  const view = new DataView(buffer);
  const direction = view.getUint32(4, true);
  if (view.getUint32(0, true) !== 0x314c5441 || direction > 1) throw new Error("Invalid listener audio protocol");
  const sequence = Number(view.getBigUint64(8, true));
  const timestampMS = Number(view.getBigUint64(16, true));
  if (!Number.isSafeInteger(sequence) || !Number.isSafeInteger(timestampMS)) throw new Error("Invalid listener audio timing");
  const frame = new Float32Array((buffer.byteLength - 24) / 2);
  for (let i = 0; i < frame.length; i++) frame[i] = view.getInt16(24 + i * 2, true) / 32768;
  return { direction, sequence, timestampMS, frame };
}

export function createListenerAudio(app: AppHandle, options: ListenerPlaybackOptions = {}): ListenerAudioRuntime {
  return { create(callbacks) {
    let context: AudioContext | undefined, node: AudioWorkletNode | undefined, gain: GainNode | undefined;
    let socket: WebSocket | undefined, blobURL: string | undefined, closed = false;
    let audioOffset: number | undefined, transitBase: number | undefined, networkExcess = 0;
    let ready = false;
    const networkDroppedMS = [0, 0];
    const sequenceGaps = [0, 0], expected: Array<number | undefined> = [undefined, undefined];
    const resamplers = [new PreviewResampler(), new PreviewResampler()];
    let volume = options.outputVolume ?? 1;
    let cancelStart: (() => void) | undefined;
    const stop = () => {
      if (closed) return;
      closed = true; cancelStart?.();
      if (socket) { socket.onmessage = socket.onclose = socket.onerror = null; socket.close(); }
      node?.disconnect(); gain?.disconnect();
      if (context) { context.onstatechange = null; void context.close().catch(() => {}); }
      if (blobURL) URL.revokeObjectURL(blobURL);
    };
    const fail = (reason: string) => { if (!closed) { stop(); callbacks.onClose(reason); } };
    const ensureOpen = () => { if (closed) throw new Error("Listening cancelled"); };
    return {
      async start(url) {
        ensureOpen();
        try {
          context = new AudioContext({ latencyHint: "interactive" });
          if (context.state === "suspended") await context.resume();
          ensureOpen();
          if (options.outputDeviceId && "setSinkId" in context) await (context as AudioContext & { setSinkId(id: string): Promise<void> }).setSinkId(options.outputDeviceId);
          ensureOpen();
          let workletURL: string;
          const gateway = new URL(app.mcpURL(), location.href);
          if (gateway.origin === location.origin) {
            const hash = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(source))), b => b.toString(16).padStart(2, "0")).join("");
            const path = `/ui/frontend/listener-${hash}.js`;
            const served = await app.get<string>(path, { cache: "no-cache", redirect: "error", headers: { Accept: "text/plain" } });
            ensureOpen();
            if (served !== source) throw new Error("Listener audio asset integrity mismatch");
            const asset = new URL(`/api/apps/telephony/_install/${app.installId}${path}`, gateway);
            asset.searchParams.set("project_id", app.projectId!); asset.searchParams.set("install_id", String(app.installId));
            workletURL = asset.href;
          } else { blobURL = URL.createObjectURL(new Blob([source], { type: "text/javascript" })); workletURL = blobURL; }
          await context.audioWorklet.addModule(workletURL); ensureOpen();
          node = new AudioWorkletNode(context, "telephony-listener", { numberOfInputs: 0, outputChannelCount: [options.stereo ? 2 : 1], processorOptions: { stereo: options.stereo } });
          gain = context.createGain(); gain.gain.value = volume;
          node.connect(gain).connect(context.destination);
          node.onprocessorerror = () => fail("listener_audio_error");
          node.port.onmessage = ({ data }) => { if (data?.type === "listener.diagnostics" && !closed) { try { callbacks.onDiagnostics?.({ ...data, sequence_gaps: [...sequenceGaps], network_excess_ms: networkExcess, network_dropped_ms: [...networkDroppedMS] }); } catch { /* host isolation */ } } };
          context.onstatechange = () => { if (context?.state === "suspended" || (context?.state as string) === "interrupted") fail("listener_audio_paused"); };
          await new Promise<void>((resolve, reject) => {
            let settled = false;
            const timeout = setTimeout(() => { finish(new Error("Listener connection timed out")); fail("listener_network_error"); }, 10_000);
            const finish = (error?: Error) => { if (settled) return; settled = true; clearTimeout(timeout); cancelStart = undefined; error ? reject(error) : resolve(); };
            cancelStart = () => finish(new Error("Listening cancelled"));
            socket = new WebSocket(url); socket.binaryType = "arraybuffer";
            socket.onmessage = ({ data }) => {
              if (closed || !context || !node) return;
              try {
                if (typeof data === "string") { const event = JSON.parse(data); if (event.type === "listener.ready") { ready = true; finish(); callbacks.onReady(); } return; }
                if (!ready || !(data instanceof ArrayBuffer)) return;
                const decoded = decodeListenerFrame(data);
                const d = decoded.direction;
                if (expected[d] !== undefined && decoded.sequence < expected[d]!) return;
                if (expected[d] !== undefined && decoded.sequence > expected[d]!) sequenceGaps[d] += decoded.sequence - expected[d]!;
                expected[d] = decoded.sequence + 1;
                const transit = performance.now() - decoded.timestampMS;
                transitBase = Math.min(transitBase ?? transit, transit);
                networkExcess = Math.max(0, transit - transitBase);
                if (networkExcess > 200) { networkDroppedMS[d] += decoded.frame.length * 1000 / 24_000; return; } // Never play accumulated network backlog.
                audioOffset ??= context.currentTime * 1000 + 60 - decoded.timestampMS;
                const frame = resamplers[d].process(decoded.frame, 24_000, context.sampleRate);
                node.port.postMessage({ direction: d, frame, playAtMS: decoded.timestampMS + audioOffset }, [frame.buffer]);
              } catch { finish(new Error("Invalid listener media")); fail("listener_protocol_error"); }
            };
            socket.onerror = () => { finish(new Error("Listener connection failed")); fail("listener_network_error"); };
            socket.onclose = event => { finish(new Error(event.reason || "Listener disconnected")); fail(event.reason || "listener_disconnected"); };
          });
          ensureOpen();
        } catch (error) { stop(); throw error; }
      },
      stop,
      setOutputVolume(value) { if (!Number.isFinite(value) || value < 0 || value > 1) throw new RangeError("Listener volume must be 0–1"); volume = value; if (gain) gain.gain.value = value; },
    };
  } };
}
