import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

test("worker dependencies preserve the installed panel routing scope", async () => {
  const requested: string[] = [];
  const scope = "?project_id=project-a&install_id=31400";
  let done: () => void;
  const ready = new Promise<void>((resolve) => { done = resolve; });
  runInNewContext(readFileSync(new URL("../ui/engine.worker.js", import.meta.url), "utf8"), {
    URL,
    self: { location: { href: "http://localhost:5280/api/apps/3d-studio/ui/engine.worker.js" + scope, search: scope } },
    importScripts: (url: string) => requested.push(url),
    Go: class { importObject = {}; run() { return new Promise(() => {}); } },
    fetch: async (url: string) => { requested.push(url); return { ok: true, arrayBuffer: async () => new ArrayBuffer(0) }; },
    WebAssembly: { instantiate: async () => ({ instance: {} }) },
    postMessage: (message: any) => { expect(message).toEqual({ ready: true }); done(); },
  });
  await ready;
  expect(requested).toEqual([
    "http://localhost:5280/api/apps/3d-studio/ui/wasm_exec.js" + scope,
    "http://localhost:5280/api/apps/3d-studio/ui/engine.wasm" + scope,
  ]);
});
