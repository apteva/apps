// The element SDK, inlined into every element's srcdoc by ElementHost.
// Kept as a string so it ships inside the portal bundle (elements run in an
// opaque-origin sandbox and cannot load it from the portal's origin).
// Also imported by the operator panel for element previews.
export const ELEMENT_SDK = "// community-element \u2014 the only API an element program gets.\n//\n// Elements run in <iframe sandbox=\"allow-scripts\">: an opaque origin with no\n// cookies, no storage and no access to the host page. The host hands each\n// element a private MessagePort; everything below goes through it.\n(function () {\n  let port = null;\n  let nextId = 1;\n  const pending = new Map();\n  const listeners = new Map();\n  const queue = [];\n  let connectedResolve;\n  const connected = new Promise((r) => (connectedResolve = r));\n\n  window.addEventListener(\"message\", function onConnect(event) {\n    if (event.data?.type !== \"course-element:connect\" || !event.ports?.[0] || port) return;\n    port = event.ports[0];\n    port.onmessage = (e) => {\n      const msg = e.data || {};\n      if (msg.id && pending.has(msg.id)) {\n        const { resolve, reject } = pending.get(msg.id);\n        pending.delete(msg.id);\n        msg.error ? reject(new Error(msg.error)) : resolve(msg.result);\n      } else if (msg.event) {\n        for (const fn of listeners.get(msg.event) || []) fn(msg.payload, msg.from);\n      }\n    };\n    window.removeEventListener(\"message\", onConnect);\n    connectedResolve();\n    while (queue.length) port.postMessage(queue.shift());\n  });\n  // Handshake: announce ourselves; the host answers with a private port.\n  window.parent.postMessage({ type: \"course-element:hello\" }, \"*\");\n\n  function call(method, params) {\n    const id = nextId++;\n    const msg = { id, method, params };\n    return new Promise((resolve, reject) => {\n      pending.set(id, { resolve, reject });\n      port ? port.postMessage(msg) : queue.push(msg);\n    });\n  }\n\n  let saveTimer = null;\n  let lastState = null;\n\n  window.lesson = {\n    /** Wait for the host. Returns { settings, state, lesson, member }. */\n    async ready() {\n      await connected;\n      return call(\"ready\");\n    },\n    /** Persist this member's state for this element (debounced). */\n    save(state) {\n      lastState = state;\n      clearTimeout(saveTimer);\n      saveTimer = setTimeout(() => call(\"save\", lastState), 250);\n    },\n    /** Broadcast an event to the other elements in the lesson. */\n    emit(name, payload) {\n      return call(\"emit\", { name, payload });\n    },\n    /** Listen to events from other elements (or the host). */\n    on(name, fn) {\n      if (!listeners.has(name)) listeners.set(name, []);\n      listeners.get(name).push(fn);\n    },\n    /** Report a completed step; the host decides when the lesson is done. */\n    complete(step) {\n      return call(\"complete\", { step });\n    },\n    /** Execute code on a runner this element is allowed to use. */\n    run(runner, input) {\n      return call(\"run\", { runner, input });\n    },\n  };\n})();\n";

const ELEMENT_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline' 'unsafe-eval' blob: https://cdn.jsdelivr.net https://unpkg.com https://cdnjs.cloudflare.com",
  "style-src 'unsafe-inline' https://fonts.googleapis.com",
  "font-src data: https://fonts.gstatic.com",
  "img-src data: blob: https:",
  "media-src data: blob: https:",
  "worker-src blob:",
  "connect-src 'none'",
  "form-action 'none'",
  "base-uri 'none'",
].join("; ");

export function buildSrcdoc(source: string): string {
  const cleaned = source.replace(/<script[^>]*src=["'][^"']*element-sdk\.js["'][^>]*><\/script>/gi, "");
  const head =
    `<meta http-equiv="Content-Security-Policy" content="${ELEMENT_CSP}">` +
    `<script>${ELEMENT_SDK.replace(/<\/script/gi, "<\\/script")}</script>`;
  if (/<head[^>]*>/i.test(cleaned)) return cleaned.replace(/<head[^>]*>/i, (m) => m + head);
  if (/<html[^>]*>/i.test(cleaned)) return cleaned.replace(/<html[^>]*>/i, (m) => m + "<head>" + head + "</head>");
  return `<!doctype html><html><head>${head}</head><body>${cleaned}</body></html>`;
}

