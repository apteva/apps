import { rtcICEConfiguration } from "./rtc-ice";
export type NetworkCheckPath = "configured" | "direct" | "relay";
export interface NetworkPathResult {
  path: NetworkCheckPath;
  state: "ready" | "degraded" | "unavailable" | "inconclusive";
  setupMs: number;
  durationMs: number;
  sent: number;
  received: number;
  uploadLossPct: number;
  downloadLossPct: number;
  rttMs?: number;
  jitterMs: number;
  browserPauseMs: number;
  candidateType?: string;
  relayProtocol?: string;
  reason?: string;
}
export interface NetworkCheckResult {
  state: "ready" | "degraded" | "unavailable" | "cancelled" | "inconclusive";
  checkedAt: string;
  paths: NetworkPathResult[];
  recommendedPath?: NetworkCheckPath;
}
export interface NetworkCheckOptions {
  paths?: NetworkCheckPath[];
  signal?: AbortSignal;
}
export type NetworkCheckRPC = (
  body: Record<string, unknown>,
  signal?: AbortSignal,
) => Promise<any>;

function abortError() {
  return new DOMException("Network check cancelled", "AbortError");
}
export function withNetworkAbort<T>(
  work: Promise<T>,
  signal: AbortSignal,
): Promise<T> {
  return new Promise((resolve, reject) => {
    const aborted = () => {
      cleanup();
      reject(abortError());
    };
    const cleanup = () => signal.removeEventListener("abort", aborted);
    signal.addEventListener("abort", aborted, { once: true });
    work.then(
      (v) => {
        cleanup();
        signal.aborted ? reject(abortError()) : resolve(v);
      },
      (e) => {
        cleanup();
        reject(e);
      },
    );
    if (signal.aborted) aborted();
  });
}
/** Fixed-size unique-packet accounting tolerates reordering and duplicates.
 * Loss excludes unsent tail packets; browser scheduling pauses are separate. */
export class NetworkProbeSamples {
  private seen = new Set<number>();
  private highest = -1;
  private lastAt?: number;
  private lastSource?: number;
  private uploadCount = 0;
  private uploadHighest = -1;
  private upstreamJitter = 0;
  private jitter = 0;
  private rtts: number[] = [];
  private lastEcho = 0;
  sent = 0;
  browserPauseMs = 0;
  private lastSend?: number;
  sentAt(now: number) {
    if (this.lastSend !== undefined)
      this.browserPauseMs = Math.max(
        this.browserPauseMs,
        now - this.lastSend - 20,
      );
    this.lastSend = now;
    this.sent++;
  }
  receive(data: ArrayBuffer, now: number) {
    if (data.byteLength !== 96) return;
    const view = new DataView(data);
    if (view.getUint32(0, true) !== 0x31504e54) return;
    const seq = view.getUint32(4, true);
    if (seq >= 1000 || this.seen.has(seq)) return;
    this.seen.add(seq);
    this.highest = Math.max(this.highest, seq);
    const source = Number(view.getBigUint64(8, true)) / 1000;
    if (this.lastAt !== undefined && this.lastSource !== undefined) {
      const delta = Math.abs(now - this.lastAt - (source - this.lastSource));
      this.jitter += (delta - this.jitter) / 16;
    }
    this.lastAt = now;
    this.lastSource = source;
    this.uploadCount = Math.max(this.uploadCount, view.getUint32(16, true));
    this.uploadHighest = Math.max(this.uploadHighest, view.getUint32(20, true));
    this.upstreamJitter = view.getUint32(32, true) / 1000;
    const echo = Number(view.getBigUint64(24, true)) / 1000;
    if (echo > this.lastEcho && echo <= now) {
      this.lastEcho = echo;
      this.rtts.push(now - echo);
      if (this.rtts.length > 300) this.rtts.shift();
    }
  }
  result(
    path: NetworkCheckPath,
    setupMs: number,
    durationMs: number,
  ): NetworkPathResult {
    const uploadTotal = this.uploadCount > 0 ? this.uploadHighest + 1 : 0,
      downloadTotal = this.highest + 1;
    const uploadLossPct =
      uploadTotal > 0
        ? Math.max(0, ((uploadTotal - this.uploadCount) / uploadTotal) * 100)
        : 0;
    const downloadLossPct =
      downloadTotal > 0
        ? Math.max(0, ((downloadTotal - this.seen.size) / downloadTotal) * 100)
        : 0;
    const sorted = this.rtts.slice().sort((a, b) => a - b),
      rttMs = sorted.length
        ? sorted[Math.floor((sorted.length - 1) * 0.95)]
        : undefined;
    const jitterMs = Math.max(this.jitter, this.upstreamJitter);
    const reason =
      this.browserPauseMs > 100
        ? "browser_busy"
        : this.seen.size < 100 || this.uploadCount < 100
          ? "insufficient_samples"
          : undefined;
    const state = reason
      ? "inconclusive"
      : Math.max(uploadLossPct, downloadLossPct) >= 3 ||
          jitterMs > 30 ||
          (rttMs ?? 0) > 300
        ? "degraded"
        : "ready";
    return {
      path,
      state,
      setupMs,
      durationMs,
      sent: this.sent,
      received: this.seen.size,
      uploadLossPct,
      downloadLossPct,
      rttMs,
      jitterMs,
      browserPauseMs: this.browserPauseMs,
      reason,
    };
  }
}
async function probePath(
  rpc: NetworkCheckRPC,
  path: NetworkCheckPath,
  signal: AbortSignal,
): Promise<NetworkPathResult> {
  const begun = performance.now();
  let id: string | undefined,
    peer: RTCPeerConnection | undefined,
    timer: ReturnType<typeof setInterval> | undefined;
  const close = () => {
    if (timer) clearInterval(timer);
    peer?.close();
  };
  signal.addEventListener("abort", close, { once: true });
  try {
    const preparing = rpc({ action: "prepare", path }, signal).then(
      (config) => {
        if (signal.aborted && typeof config?.id === "string")
          void rpc(
            { action: "cancel", id: config.id },
            AbortSignal.timeout(1000),
          ).catch(() => {});
        return config;
      },
    );
    const config = await withNetworkAbort(preparing, signal);
    id = config.id;
    if (
      typeof id !== "string" ||
      !/^session-[a-f0-9-]{1,64}$/.test(id) ||
      config.packet_bytes !== 96 ||
      config.interval_ms !== 20
    )
      throw Error("Invalid network test configuration");
    peer = new RTCPeerConnection(rtcICEConfiguration(config));
    const channel = peer.createDataChannel("telephony-network-check", {
      ordered: false,
      maxRetransmits: 0,
    });
    channel.binaryType = "arraybuffer";
    const samples = new NetworkProbeSamples();
    channel.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer)
        samples.receive(e.data, performance.now());
    };
    const open = new Promise<void>((resolve, reject) => {
      channel.onopen = () => resolve();
      channel.onerror = () => reject(Error("Network test connection failed"));
    });
    const offer = await withNetworkAbort(peer.createOffer(), signal);
    await withNetworkAbort(peer.setLocalDescription(offer), signal);
    if (peer.iceGatheringState !== "complete")
      await withNetworkAbort(
        new Promise<void>((resolve) => {
          peer!.onicegatheringstatechange = () => {
            if (peer!.iceGatheringState === "complete") resolve();
          };
        }),
        signal,
      );
    const answer = await withNetworkAbort(
      rpc({ action: "offer", id, sdp: peer.localDescription!.sdp }, signal),
      signal,
    );
    await withNetworkAbort(
      peer.setRemoteDescription({ type: "answer", sdp: answer.sdp }),
      signal,
    );
    await withNetworkAbort(open, signal);
    const setupMs = performance.now() - begun,
      start = performance.now();
    let sequence = 0;
    timer = setInterval(() => {
      if (
        signal.aborted ||
        channel.readyState !== "open" ||
        channel.bufferedAmount > 384 ||
        sequence >= 400
      )
        return;
      const packet = new ArrayBuffer(96),
        v = new DataView(packet);
      const now = performance.now();
      v.setUint32(0, 0x31504e54, true);
      v.setUint32(4, sequence++, true);
      v.setBigUint64(8, BigInt(Math.round(now * 1000)), true);
      try {
        channel.send(packet);
        samples.sentAt(now);
      } catch {
        /* test completes with insufficient evidence */
      }
    }, 20);
    await withNetworkAbort(
      new Promise<void>((resolve) => {
        const done = setTimeout(resolve, 6000);
        signal.addEventListener("abort", () => clearTimeout(done), {
          once: true,
        });
      }),
      signal,
    );
    const result = samples.result(path, setupMs, performance.now() - start);
    const stats = await withNetworkAbort(peer.getStats(), signal);
    let pairID = "";
    stats.forEach((s: any) => {
      if (s.type === "transport") pairID = s.selectedCandidatePairId ?? "";
    });
    const pair = stats.get(pairID),
      local = pair && stats.get(pair.localCandidateId);
    result.candidateType = local?.candidateType;
    result.relayProtocol = local?.relayProtocol;
    return result;
  } finally {
    signal.removeEventListener("abort", close);
    close();
    if (id) {
      const cleanup = rpc(
        { action: "cancel", id },
        AbortSignal.timeout(1000),
      ).catch(() => {});
      if (!signal.aborted)
        await withNetworkAbort(cleanup, AbortSignal.timeout(1000)).catch(
          () => {},
        );
    }
  }
}
export async function runNetworkCheck(
  rpc: NetworkCheckRPC,
  options: NetworkCheckOptions = {},
): Promise<NetworkCheckResult> {
  const paths = options.paths ?? ["configured"];
  if (
    paths.length < 1 ||
    paths.length > 3 ||
    new Set(paths).size !== paths.length ||
    paths.some((p) => !["configured", "direct", "relay"].includes(p))
  )
    throw Error("Invalid network test paths");
  const result: NetworkCheckResult = {
    state: "unavailable",
    checkedAt: new Date().toISOString(),
    paths: [],
  };
  for (const path of paths) {
    if (options.signal?.aborted) {
      result.state = "cancelled";
      return result;
    }
    const controller = new AbortController(),
      cancel = () => controller.abort();
    options.signal?.addEventListener("abort", cancel, { once: true });
    const timeout = setTimeout(cancel, 18000);
    try {
      result.paths.push(await probePath(rpc, path, controller.signal));
    } catch {
      if (options.signal?.aborted) {
        result.state = "cancelled";
        return result;
      }
      result.paths.push({
        path,
        state: "unavailable",
        setupMs: 0,
        durationMs: 0,
        sent: 0,
        received: 0,
        uploadLossPct: 0,
        downloadLossPct: 0,
        jitterMs: 0,
        browserPauseMs: 0,
        reason: controller.signal.aborted ? "timeout" : "path_unavailable",
      });
    } finally {
      clearTimeout(timeout);
      options.signal?.removeEventListener("abort", cancel);
    }
  }
  const ranked = result.paths
    .filter((p) => p.state === "ready" || p.state === "degraded")
    .sort(
      (a, b) =>
        (a.uploadLossPct + a.downloadLossPct) * 50 +
        a.jitterMs +
        (a.rttMs ?? 1000) -
        (b.uploadLossPct + b.downloadLossPct) * 50 -
        b.jitterMs -
        (b.rttMs ?? 1000),
    );
  if (ranked.length) {
    result.state = ranked[0].state as "ready" | "degraded";
    result.recommendedPath = ranked[0].path;
  } else if (result.paths.some((p) => p.state === "inconclusive"))
    result.state = "inconclusive";
  return result;
}
