/* First-party Go engine compiled to WASM; no server-side JavaScript runtime. */
importScripts("wasm_exec.js");
const ready = (async () => {
  const go = new Go();
  const response = await fetch("engine.wasm");
  if (!response.ok) throw new Error("Go preview engine is unavailable");
  const wasm = await WebAssembly.instantiate(
    await response.arrayBuffer(),
    go.importObject,
  );
  go.run(wasm.instance).catch((error) => postMessage({ error: error.message }));
})();
ready
  .then(() => postMessage({ ready: true }))
  .catch((error) => postMessage({ error: error.message }));
onmessage = async ({ data }) => {
  try {
    await ready;
    postMessage({
      id: data.id,
      ...JSON.parse(studioEvaluate(JSON.stringify(data.request))),
    });
  } catch (error) {
    postMessage({ id: data.id, error: String(error.message || error) });
  }
};
