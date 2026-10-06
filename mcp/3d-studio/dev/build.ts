import { cp, mkdir } from "node:fs/promises";
import { resolve } from "node:path";

// Exercise the exact deployed panel artifact, rather than rebundling its source
// with a private React copy (which hid the v0.1.0 jsxDEV import failure).
const root = resolve(import.meta.dir, "..");
const vendor = resolve(process.env.STUDIO_DASHBOARD_VENDOR || resolve(root, "../../../dashboard/dist/vendor"));
const previewVendor = resolve(root, "ui/preview-vendor");
await mkdir(previewVendor, { recursive: true });
await cp(vendor, previewVendor, { recursive: true });
const mapping = {
  react: "/ui/preview-vendor/react.mjs",
  "react/jsx-runtime": "/ui/preview-vendor/react-jsx-runtime.mjs",
  "react/jsx-dev-runtime": "/ui/preview-vendor/react-jsx-runtime.mjs",
  "react-dom": "/ui/preview-vendor/react-dom.mjs",
  "react-dom/client": "/ui/preview-vendor/react-dom-client.mjs",
  "@apteva/ui-kit": "/ui/preview-vendor/ui-kit.mjs",
};
await Bun.write(resolve(root, "ui/preview.mjs"), `
import {createElement} from "react";
import {createRoot} from "react-dom/client";
const container = document.getElementById("root");
try {
  const {default: StudioPanel} = await import("./StudioPanel.mjs");
  createRoot(container).render(createElement(StudioPanel, {projectId:"studio-dev", apiBase:"/api", uiBase:"/ui"}));
} catch (error) {
  container.setAttribute("role", "alert");
  container.textContent = "Panel import failed: " + error.message;
  console.error(error);
}
`);
await Bun.write(resolve(root, "ui/preview.html"), `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>3D Studio</title><style>body{margin:0;padding:18px;background:#080d13;color:#e0e7ee}#root>.s3{height:calc(100vh - 36px);min-height:620px}</style><script type="importmap">${JSON.stringify({imports:mapping})}</script></head><body><div id="root"></div><script type="module" src="/ui/preview.mjs"></script></body></html>`);
console.log("Preview uses the built StudioPanel.mjs and dashboard vendor modules");
