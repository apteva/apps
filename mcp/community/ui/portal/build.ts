import { copyFileSync, mkdirSync, rmSync } from "fs";

rmSync("./dist", { recursive: true, force: true });
mkdirSync("./dist", { recursive: true });

const API_BASE = process.env.API_BASE || "";
const COMMUNITY_APP = process.env.COMMUNITY_APP || "community";
const AUTH_APP = process.env.AUTH_APP || "auth";

console.log(`Building Community portal... (API_BASE=${API_BASE || "same-origin"})`);

const result = await Bun.build({
  entrypoints: ["./src/main.tsx"],
  outdir: "./dist",
  target: "browser",
  minify: true,
  sourcemap: "linked",
  define: {
    __API_BASE__: JSON.stringify(API_BASE),
    __COMMUNITY_APP__: JSON.stringify(COMMUNITY_APP),
    __AUTH_APP__: JSON.stringify(AUTH_APP),
  },
  naming: {
    entry: "[name]-[hash].[ext]",
    chunk: "[name]-[hash].[ext]",
    asset: "[name]-[hash].[ext]",
  },
});

if (!result.success) {
  console.error("Build failed:");
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

const jsOutput = result.outputs.find((o) => o.path.endsWith(".js"));
const jsFile = jsOutput ? jsOutput.path.split("/").pop() : "main.js";

copyFileSync("./src/styles.css", "./dist/style.css");

const html = `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Community · Apteva</title>
    <link rel="stylesheet" href="./style.css" />
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="./${jsFile}"></script>
  </body>
</html>`;

await Bun.write("./dist/index.html", html);

console.log("Build complete:");
for (const output of result.outputs) {
  const size = (output.size / 1024).toFixed(1);
  console.log(`  ${output.path.split("/").pop()} (${size} KB)`);
}
console.log("  style.css");
console.log("  index.html");
