import { mkdir } from "node:fs/promises";
await mkdir("browser/dist", { recursive: true });
const result = await Bun.build({
  entrypoints: ["browser/entry.tsx"],
  outdir: "browser/dist",
  target: "browser",
  define: { "process.env.NODE_ENV": '"development"' },
});
if (!result.success) throw new Error(result.logs.join("\n"));
const css = Bun.spawn(
  [
    "bunx",
    "--no-install",
    "tailwindcss",
    "-i",
    "browser/styles.css",
    "-o",
    "browser/dist/styles.css",
  ],
  { stdout: "inherit", stderr: "inherit" },
);
if (await css.exited) process.exit(1);
await Bun.write(
  "browser/dist/index.html",
  '<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/styles.css"></head><body><div id="root"></div><script type="module" src="/entry.js"></script></body></html>',
);
const child = Bun.spawn(
  ["go", "test", "-run", "^TestBrowserHarness$", "-count=1", "-timeout", "10m"],
  {
    env: { ...process.env, GOWORK: "off", CALENDAR_BROWSER: "1" },
    stdout: "inherit",
    stderr: "inherit",
  },
);
for (const signal of ["SIGTERM", "SIGINT"] as const)
  process.on(signal, () => {
    child.kill();
    process.exit(0);
  });
process.exit(await child.exited);
