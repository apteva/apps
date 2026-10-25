import type { AppHandle } from '@apteva/web-sdk';

export type TelephonyRuntimeMode = 'auto' | 'proxy' | 'direct';
interface RuntimeInfo {
  managed?: boolean;
  browser_api_base?: string;
  browser_media_base?: string;
  browser_signaling_base?: string;
  generation?: number;
}
export interface BrowserCredential { token: string; expires_at: number; endpoint: string }

export interface DirectTelephonyConnection {
 /** HTTPS base including /runtime-browser. */
 baseURL: string;
 /** Issues a short-lived credential for the current user, installation and origin. */
 credentialProvider: () => Promise<BrowserCredential>;
}

function browserBase(raw: string): URL {
  const base = new URL(raw);
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(base.hostname);
  if (base.username || base.password || base.search || base.hash ||
      base.protocol !== 'https:' && !(base.protocol === 'http:' && loopback) ||
      !base.pathname.endsWith('/runtime-browser')) {
    throw new Error('Invalid managed Telephony browser endpoint');
  }
  return base;
}
function requestPath(path: string): void {
  if (!path.startsWith('/') || path.startsWith('//') || path.includes('..') || path.includes('\\')) {
    throw new Error('Invalid Telephony request path');
  }
}
function retryPause(signal: AbortSignal, ms: number): Promise<void> {
  return new Promise(resolve => {
    const finish = () => { clearTimeout(timer); signal.removeEventListener('abort', finish); resolve(); };
    const timer = setTimeout(finish, ms);
    signal.addEventListener('abort', finish, { once: true });
    if (signal.aborted) finish();
  });
}

/** The proxy is the discovery/credential issuer boundary. Only scoped browser
 * credentials reach the backend; host auth headers are never copied there. */
export class RuntimeTelephonyTransport {
  private info?: RuntimeInfo;
  private discovery?: Promise<void>;
  private credential?: BrowserCredential;
  private issuing?: Promise<BrowserCredential>;
  private proxyOnly = false;

  constructor(private proxy: AppHandle, private mode: TelephonyRuntimeMode = 'auto',
              private fetchImpl: typeof fetch = globalThis.fetch, private direct?: DirectTelephonyConnection) { this.fetchImpl = fetchImpl.bind(globalThis); }

  get directBase(): string | undefined { return this.info?.browser_api_base; }

  async prepare(): Promise<void> {
    if (this.mode === 'proxy' || this.mode === 'auto' && typeof location === 'undefined') return;
    if(this.direct){browserBase(this.direct.baseURL);this.info={managed:true,browser_api_base:this.direct.baseURL};return}
    if (this.discovery) return this.discovery;
    this.discovery = (async () => {
      let info: RuntimeInfo;
      try {
        info = await this.proxy.get<RuntimeInfo>('/runtime-endpoints');
      } catch (e) {
        if (this.mode === 'direct' || ![404, 405].includes((e as { status?: number }).status ?? 0)) throw e;
        this.proxyOnly = true;
        return;
      }
      if (!info?.managed || !info.browser_api_base) {
        this.proxyOnly = true;
        if (this.mode === 'direct') throw new Error('Direct Telephony deployment unavailable');
        return;
      }
      browserBase(info.browser_api_base);
      if (info.browser_media_base) browserBase(info.browser_media_base);
      if (info.browser_signaling_base) browserBase(info.browser_signaling_base);
      const gateway = new URL(this.proxy.mcpURL(), typeof location === 'undefined' ? undefined : location.href);
      if (!['https:', 'http:'].includes(gateway.protocol)) throw new Error('Invalid Telephony management gateway');
      this.info = info;
    })();
    try { await this.discovery; } catch (e) { this.discovery = undefined; throw e; }
  }

  private async grant(): Promise<BrowserCredential> {
    if (this.credential && this.credential.expires_at * 1000 > Date.now() + 30000) return this.credential;
    if (this.issuing) return this.issuing;
    const origin = typeof location === 'undefined' ? new URL(this.proxy.mcpURL()).origin : location.origin;
    this.issuing = (async () => {
      const grant = this.direct ? await this.direct.credentialProvider() : await this.proxy.post<BrowserCredential>('/runtime-browser-bootstrap', { origin });
      if (!grant || typeof grant.token !== 'string' || !grant.token || grant.token.length > 4096 ||
          !Number.isSafeInteger(grant.expires_at) || grant.expires_at * 1000 <= Date.now() ||
          grant.expires_at * 1000 > Date.now() + 360000 || grant.endpoint !== this.directBase) {
        throw new Error('Invalid managed browser credential');
      }
      return this.credential = grant;
    })();
    try { return await this.issuing; } finally { this.issuing = undefined; }
  }

  async request<R>(method: string, path: string, body?: unknown, init?: RequestInit): Promise<R> {
    // Preserve request initiation order on legacy/local paths. An extra await
    // here changes cancellation and recovery ordering for existing clients.
    if (this.mode !== 'direct' && !this.direct || this.proxyOnly) {
      return method === 'GET' ? this.proxy.get<R>(path, init) : this.proxy.post<R>(path, body, init);
    }
    await this.prepare();
    if (!this.directBase) return method === 'GET' ? this.proxy.get<R>(path, init) : this.proxy.post<R>(path, body, init);
    requestPath(path);
    const grant = await this.grant(), url = new URL(this.directBase + path);
    if (this.proxy.projectId) url.searchParams.set('project_id', this.proxy.projectId);
    const headers = new Headers(init?.headers);
    headers.set('Authorization', 'Bearer ' + grant.token);
    headers.set('Content-Type', 'application/json');
    const response = await this.fetchImpl(url, { ...init, method, headers,
      body: body === undefined ? undefined : JSON.stringify(body), credentials: 'omit', redirect: 'error' });
    if (!response.ok) {
      if (response.status === 403) this.credential = undefined;
      const body = (await response.text()).slice(0, 65536);
      throw Object.assign(new Error(`Telephony HTTP ${response.status}`), { status: response.status, body });
    }
    return await response.json() as R;
  }

  /** Discover direct media only when attaching a media session. Ordinary UI
   * requests never depend on discovery or send platform credentials remotely. */
  requestMedia<R>(path: string, body?: unknown, init?: RequestInit): Promise<R> {
    const send = () => {
      init?.signal?.throwIfAborted();
      return this.request<R>('POST', path, body, init);
    };
    if (this.mode === 'auto' && typeof location !== 'undefined' && !this.proxyOnly)
      return this.prepare().then(send);
    return send();
  }

  mediaURL(proxyURL: URL, transport?: string): URL {
    if (!this.directBase) return proxyURL;
    const marker = `/api/apps/telephony/_install/${this.proxy.installId}`;
    const base = transport === 'webrtc' || transport === 'auto' ? this.info?.browser_signaling_base : this.info?.browser_media_base;
    const url = new URL((base || this.directBase) + proxyURL.pathname.slice(marker.length));
    url.search = proxyURL.search;
    return url;
  }

  subscribe<E>(path: string, onEvent: (event: E) => void,
               opts?: { signal?: AbortSignal; onError?: (e: unknown) => void }): { close(): void } {
    if (this.mode !== 'direct' && !this.direct || this.proxyOnly) return this.proxy.subscribe(path, onEvent, opts);
    const controller = new AbortController();
    let fallback: { close(): void } | undefined;
    const close = () => { controller.abort(); fallback?.close(); opts?.signal?.removeEventListener('abort', close); };
    opts?.signal?.addEventListener('abort', close, { once: true });
    if (opts?.signal?.aborted) close();
    void (async () => {
      try {
        await this.prepare();
        if (controller.signal.aborted) return;
        if (!this.directBase) { fallback = this.proxy.subscribe(path, onEvent, opts); return; }
        requestPath(path);
        let backoff = 500;
        while (!controller.signal.aborted) {
          try {
            const grant = await this.grant(), url = new URL(this.directBase + path);
            if (this.proxy.projectId) url.searchParams.set('project_id', this.proxy.projectId);
            const response = await this.fetchImpl(url, { headers: { Authorization: 'Bearer ' + grant.token, Accept: 'text/event-stream' },
              credentials: 'omit', redirect: 'error', signal: controller.signal });
            if (!response.ok || !response.body) {
              if (response.status === 403) this.credential = undefined;
              throw Object.assign(new Error(`Telephony stream HTTP ${response.status}`), { status: response.status });
            }
            backoff = 500;
            const reader = response.body.getReader(), decoder = new TextDecoder();
            let buffered = '';
            try {
              while (!controller.signal.aborted) {
                const { done, value } = await reader.read();
                if (done) break;
                buffered += decoder.decode(value, { stream: true });
                buffered = buffered.replace(/\r\n/g, '\n');
                if (buffered.length > 65536) throw new Error('Telephony notification frame too large');
                let end: number;
                while ((end = buffered.indexOf('\n\n')) >= 0) {
                  const event = buffered.slice(0, end);
                  buffered = buffered.slice(end + 2);
                  const data = event.split('\n').filter(s => s.startsWith('data:')).map(s => s.slice(5).trimStart()).join('\n');
                  if (data) { try { onEvent(JSON.parse(data)); } catch { /* Observer/invalid event isolation. */ } }
                }
              }
            } finally { await reader.cancel().catch(() => {}); }
          } catch (e) {
            if (controller.signal.aborted) return;
            opts?.onError?.(e);
            backoff = Math.min(10000, backoff * 2);
          }
          if (!controller.signal.aborted) await retryPause(controller.signal, backoff);
        }
      } catch (e) { if (!controller.signal.aborted) opts?.onError?.(e); }
    })();
    return { close };
  }
}
