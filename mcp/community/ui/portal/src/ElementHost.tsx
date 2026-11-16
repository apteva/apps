import { useEffect, useRef, useState } from "react";
import { api, type LessonBlock, type StepResult } from "./api";
import { buildSrcdoc } from "./elementSdk";

export { buildSrcdoc };

// Elements are course-author HTML/JS programs. Each one runs in an iframe with
// sandbox="allow-scripts" (no allow-same-origin): an opaque origin with no
// cookies, storage or access to this page. The SDK and a strict CSP are
// inserted into the srcdoc; the element talks to us only through a private
// MessagePort handed over after its hello.

const sourceCache = new Map<string, Promise<string>>();
function loadSource(lessonId: string, block: LessonBlock): Promise<string> {
  const key = `${block.element_id}@${block.element_version}`;
  let pending = sourceCache.get(key);
  if (!pending) {
    pending = api.courses
      .elementSource(lessonId, block.element_id || "", block.element_version || 0)
      .then((r) => buildSrcdoc(r.source));
    pending.catch(() => sourceCache.delete(key));
    sourceCache.set(key, pending);
  }
  return pending;
}

/** Relays events between the elements of one lesson. */
export class ElementBus {
  private ports = new Map<string, MessagePort>();
  attach(blockId: string, port: MessagePort) {
    this.ports.set(blockId, port);
  }
  detach(blockId: string, port: MessagePort) {
    if (this.ports.get(blockId) === port) this.ports.delete(blockId);
  }
  broadcast(from: string, event: string, payload: unknown) {
    for (const [id, port] of this.ports) if (id !== from) port.postMessage({ event, payload, from });
  }
}

type Request = { id?: number; method?: string; params?: Record<string, unknown> };

export function ElementFrame({
  lessonId,
  lessonTitle,
  block,
  state,
  steps,
  bus,
  onStep,
}: {
  lessonId: string;
  lessonTitle: string;
  block: LessonBlock;
  state?: Record<string, unknown>;
  steps?: string[];
  bus: ElementBus;
  onStep: (result: StepResult) => void;
}) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [srcdoc, setSrcdoc] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  // Latest values without re-running the connection effect.
  const live = useRef({ state, steps, onStep });
  live.current = { state, steps, onStep };

  useEffect(() => {
    let active = true;
    setError("");
    loadSource(lessonId, block)
      .then((doc) => active && setSrcdoc(doc))
      .catch((e) => active && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      active = false;
    };
  }, [lessonId, block.element_id, block.element_version]);

  useEffect(() => {
    if (!srcdoc) return;
    let port: MessagePort | null = null;
    let saveTimer: number | undefined;
    let pendingState: Record<string, unknown> | null = null;
    const flush = () => {
      if (!pendingState) return;
      const value = pendingState;
      pendingState = null;
      api.courses.saveElementState(lessonId, block.id, value).catch((e) => setNotice(`Progress not saved: ${e.message || e}`));
    };
    async function handle(msg: Request) {
      const reply = (result: unknown) => port?.postMessage({ id: msg.id, result });
      const fail = (message: string) => port?.postMessage({ id: msg.id, error: message });
      const params = msg.params || {};
      try {
        switch (msg.method) {
          case "ready":
            return reply({
              settings: block.settings || {},
              state: live.current.state || null,
              steps: live.current.steps || [],
              lesson: { id: lessonId, title: lessonTitle },
              block: { id: block.id, title: block.title, element: block.element_slug, version: block.element_version },
            });
          case "save": {
            if (!params || typeof params !== "object" || Array.isArray(params)) return fail("state must be an object");
            pendingState = params;
            window.clearTimeout(saveTimer);
            saveTimer = window.setTimeout(flush, 800);
            return reply({ ok: true });
          }
          case "emit": {
            const name = String(params.name || "");
            if (!name) return fail("event name required");
            bus.broadcast(block.id, name, params.payload);
            return reply({ ok: true });
          }
          case "complete": {
            const step = String(params.step || "");
            const result = await api.courses.completeStep(lessonId, block.id, step);
            live.current.onStep(result);
            return reply({ lesson_completed: result.lesson_completed, done: result.done, required: result.required });
          }
          case "run": {
            const runner = String(params.runner || "");
            if (!(block.runners || []).includes(runner)) return fail(`This element may not use runner "${runner}"`);
            const input = (params.input && typeof params.input === "object" ? params.input : {}) as Record<string, unknown>;
            return reply(await api.courses.runElement(lessonId, block.id, runner, input));
          }
          default:
            return fail(`Unknown method ${msg.method}`);
        }
      } catch (e) {
        return fail(e instanceof Error ? e.message : String(e));
      }
    }
    function onMessage(event: MessageEvent) {
      if (event.source !== frame.current?.contentWindow) return;
      if (event.data?.type !== "course-element:hello") return;
      // A reload of the frame says hello again; replace the old channel.
      if (port) {
        bus.detach(block.id, port);
        port.close();
      }
      const channel = new MessageChannel();
      port = channel.port1;
      port.onmessage = (e) => void handle(e.data || {});
      bus.attach(block.id, port);
      frame.current?.contentWindow?.postMessage({ type: "course-element:connect" }, "*", [channel.port2]);
    }
    window.addEventListener("message", onMessage);
    return () => {
      window.removeEventListener("message", onMessage);
      window.clearTimeout(saveTimer);
      flush();
      if (port) {
        bus.detach(block.id, port);
        port.close();
      }
    };
  }, [srcdoc, lessonId, block.id]);

  const height = block.height || (block.slot === "stage" ? 560 : block.slot === "side" ? 420 : 460);
  return (
    <div className="element-block" data-element={block.element_slug}>
      {error ? (
        <p role="alert" className="element-error">
          This interactive element could not be loaded. {error}
        </p>
      ) : (
        <iframe
          ref={frame}
          title={block.title || block.element_name || "Interactive element"}
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          srcDoc={srcdoc || undefined}
          style={{ height }}
        />
      )}
      {notice && (
        <p role="status" className="muted element-notice">
          {notice}
        </p>
      )}
    </div>
  );
}
