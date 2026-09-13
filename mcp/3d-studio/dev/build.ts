import { resolve } from "node:path";
const root = resolve(import.meta.dir, "..");
const deps = resolve(root, "../../../dashboard/node_modules");
const result = await Bun.build({
  entrypoints: [resolve(import.meta.dir, "preview.tsx")],
  outdir: resolve(root, "ui"),
  naming: "preview.mjs",
  target: "browser",
  format: "esm",
  minify: true,
  define: { "process.env.NODE_ENV": '"production"' },
  alias: {
    react: resolve(deps, "react"),
    "react-dom/client": resolve(deps, "react-dom/client.js"),
    "react/jsx-runtime": resolve(deps, "react/jsx-runtime.js"),
  },
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
await Bun.write(
  resolve(root, "ui/preview.html"),
  '<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>3D Studio</title><style>body{margin:0;padding:18px;background:#080d13}#root>.s3{height:calc(100vh - 36px);min-height:620px}</style></head><body><div id="root"></div><script type="module" src="/ui/preview.mjs"></script></body></html>',
);
