import { defineAppExtension, type AppHandle } from "@apteva/web-sdk";
import { createMicrophonePreview, listMicrophones } from "./audio";
import { HeadlessSoftphone, type SoftphoneOptions } from "./softphone";

export interface Call {
  id: string;
  status: string;
  direction: string;
  peer_kind: string;
  from_number: string;
  to_number: string;
  routing_waiting?: boolean;
  ring_offers?: Array<{ destination_id: string; kind: string }>;
}
export interface CallSession {
  call_id: string;
  media_url: string;
  session_token?: string;
  lease_seconds?: number;
}
export interface DialRequest {
  to: string;
  from?: string;
  timeout_sec?: number;
  recording?: boolean;
  /** Retain the same key and request when retrying an uncertain response. */
  idempotency_key: string;
}
export interface AnswerRequest { destination_id?: string; rejoin?: boolean }
export function isIncomingBrowserCall(call: Pick<Call, "direction" | "status" | "peer_kind" | "routing_waiting" | "ring_offers">): boolean {
  return call.direction === "inbound" && call.status === "pending" && !call.routing_waiting &&
    (call.peer_kind === "human" || Boolean(call.ring_offers?.some(offer => offer.kind === "browser")));
}
export interface WatchCallsOptions {
  intervalMs?: number;
  signal?: AbortSignal;
  onError?: (error: unknown) => void;
}
export const isTerminalCall = (status: string) =>
  ["completed", "failed", "no-answer", "no_answer", "busy", "canceled", "cancelled"].includes(status);

function callID(id: string): string {
  if (!id || /[\s/\\?#]/.test(id)) throw new Error("Invalid call ID");
  return encodeURIComponent(id);
}

export interface TelephonyClientOptions {
  /** Online provider configured by the installation administrator. */
  authProvider?: string;
}

/** Human-call API. Does not invoke the AI-call MCP tools or open a microphone. */
export class TelephonyClient {
  listMicrophones = listMicrophones;
  createMicrophonePreview = createMicrophonePreview;
  constructor(readonly app: AppHandle, private readonly options: TelephonyClientOptions = {}) {
    if (app.name !== "telephony" || !app.projectId || !app.installId) {
      throw new Error("Telephony requires an explicit project and installation");
    }
  }

  private path(path: string): string {
    if (!this.options.authProvider) return path;
    return `/user${path}${path.includes("?") ? "&" : "?"}auth_provider=${encodeURIComponent(this.options.authProvider)}`;
  }

  async listCalls(signal?: AbortSignal): Promise<Call[]> {
    const result = await this.app.get<{ calls: Call[] }>(this.path("/calls"), { signal });
    if (!Array.isArray(result?.calls) || result.calls.some(c => !c || typeof c.id !== "string" || typeof c.status !== "string")) {
      throw new Error("Invalid Telephony calls response");
    }
    return result.calls;
  }

  async getCall(id: string, signal?: AbortSignal): Promise<Call | undefined> {
    const result = await this.app.get<{ calls: Call[] }>(this.path(`/calls?call_id=${callID(id)}`), { signal });
    if (!Array.isArray(result?.calls)) throw new Error("Invalid Telephony call response");
    return result.calls.find(call => call.id === id);
  }

  incomingCalls(calls: readonly Call[]): Call[] {
    return calls.filter(isIncomingBrowserCall);
  }

  /** One request at a time; closure/abort suppresses late delivery and retries. */
  watchCalls(onCalls: (calls: Call[]) => void, options: WatchCallsOptions = {}): { close(): void } {
    const interval = options.intervalMs ?? 2000;
    if (!Number.isFinite(interval) || interval < 100) throw new Error("Call watch interval must be at least 100 ms");
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const close = () => { clearTimeout(timer); controller.abort(); options.signal?.removeEventListener("abort", close); };
    options.signal?.addEventListener("abort", close, { once: true });
    if (options.signal?.aborted) close();
    const poll = async () => {
      if (controller.signal.aborted) return;
      try {
        const calls = await this.listCalls(controller.signal);
        if (!controller.signal.aborted) onCalls(calls);
      } catch (error) {
        if (!controller.signal.aborted) {
          try { options.onError?.(error); } catch { close(); }
        }
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(poll, interval);
      }
    };
    void poll();
    return { close };
  }

  async place(request: DialRequest): Promise<CallSession> {
    if (!/^\+[1-9]\d{7,14}$/.test(request.to) || !request.idempotency_key?.trim()) {
      throw new Error("Dial requires an E.164 number and an idempotency key");
    }
    return this.session(await this.app.post(this.path("/softphone/place"), request));
  }

  async answer(id: string, request: AnswerRequest = {}): Promise<CallSession> {
    return this.session(await this.app.post(this.path(`/softphone/answer/${callID(id)}`), request), id);
  }

  /** Attach an assigned human call; never dials or takes another user's call. */
  async attach(id: string): Promise<CallSession> {
    return this.session(await this.app.post(this.path(`/softphone/attach/${callID(id)}`), {}), id);
  }

  async takeover(id: string): Promise<CallSession> {
    return this.session(await this.app.post(this.path(`/softphone/takeover/${callID(id)}`), {}), id);
  }

  async renew(session: CallSession): Promise<void> {
    await this.app.post(this.path(`/softphone/renew/${callID(session.call_id)}`), { session_token: session.session_token });
  }

  async release(session: CallSession): Promise<void> {
    if (!session.session_token) throw new Error("Answer session has no release token");
    await this.app.post(this.path(`/softphone/release/${callID(session.call_id)}`), { session_token: session.session_token });
  }

  async hangup(id: string): Promise<void> {
    await this.app.post(this.path(`/calls/${callID(id)}/hangup`), {});
  }

  createSoftphone(options: SoftphoneOptions = {}): HeadlessSoftphone {
    return new HeadlessSoftphone(this, options);
  }

  /** Resolve only the selected installation's media path on the SDK gateway. */
  mediaURL(session: CallSession): string {
    const gateway = new URL(this.app.mcpURL(), typeof location === "undefined" ? undefined : location.href);
    const url = new URL(session.media_url, gateway);
    const prefix = `/api/apps/telephony/_install/${this.app.installId}/softphone/media/${callID(session.call_id)}/`;
    if (url.origin !== gateway.origin || !["http:", "https:"].includes(url.protocol) ||
        url.username || url.password || url.search || url.hash || !url.pathname.startsWith(prefix) ||
        !/^[A-Za-z0-9_-]+$/.test(url.pathname.slice(prefix.length))) {
      throw new Error("Invalid Telephony media endpoint");
    }
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    return url.href;
  }

  private session(value: unknown, expectedID?: string): CallSession {
    const session = value as CallSession;
    if (!session || typeof session.call_id !== "string" || typeof session.media_url !== "string" ||
        (expectedID && session.call_id !== expectedID) ||
        (session.session_token !== undefined && typeof session.session_token !== "string")) {
      throw new Error("Invalid Telephony session response");
    }
    if (session.lease_seconds !== undefined && (!Number.isFinite(session.lease_seconds) || session.lease_seconds < 10 || session.lease_seconds > 3600 || !session.session_token)) throw new Error("Invalid media lease");
    callID(session.call_id);
    this.mediaURL(session);
    return session;
  }
}

export const telephonyExtension = defineAppExtension({
  app: "telephony",
  create: ({ app }) => new TelephonyClient(app),
});
