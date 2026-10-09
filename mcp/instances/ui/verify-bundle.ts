// Native panels share the dashboard's production React runtime through its
// import map. Minification alone does not select production JSX in Bun.
export function verifyPanelBundle(source: string): void {
  if (source.includes("react/jsx-dev-runtime") || /\bjsxDEV\b/.test(source)) {
    throw new Error("Instances panel uses development JSX; build with --production for the dashboard's React import map");
  }
  if (!/\bfrom\s*["']react\/jsx-runtime["']/.test(source)) {
    throw new Error("Instances panel must import the dashboard's shared production JSX runtime");
  }
}

if (import.meta.main) {
  verifyPanelBundle(await Bun.file(new URL("./InstancesPanel.mjs", import.meta.url)).text());
  console.log("Panel production JSX runtime verified");
}
