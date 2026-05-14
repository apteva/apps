// context-info — returns what a handler can see about itself.
// Note context.env is *scrubbed*: the sidecar's secrets (the app
// token, the gateway URL) are never in it — only the function's own
// env map plus a small host allowlist (PATH, HOME, ...).
//
//   functions_invoke { name: "context-info", event: {} }
//   → { "function": "context-info", "runtime": "node", "envKeys": [...] }

export default async function handler(event, context) {
  return {
    function: context.functionName,
    id: context.functionId,
    runtime: context.runtime,
    envKeys: Object.keys(context.env).sort(),
  };
}
