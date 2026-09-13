import { plugin } from "bun";
// The native panel lives above the standalone desk. Resolve its external React
// imports to this test host so both surfaces use the same React instance.
await plugin({ name: "trading-panel-test-react", setup(build) {
  build.onLoad({ filter: /\/TradingPanel\.tsx$/ }, async args => {
    const transpiler = new Bun.Transpiler({ loader: "tsx", tsconfig: { compilerOptions: { jsx: "react" } } });
    const reactPath = JSON.stringify(Bun.resolveSync("react", import.meta.dir));
    const code = transpiler.transformSync(await Bun.file(args.path).text()).replaceAll('from "react"', `from ${reactPath}`);
    return { contents: `import * as React from ${reactPath};\n` + code, loader: "js" };
  });
} });
import { Window } from "happy-dom";
const win = new Window({ url: "http://localhost" });
for (const key of ["window", "document", "navigator", "HTMLElement", "HTMLInputElement", "HTMLTextAreaElement", "Event", "MouseEvent"] as const) Object.defineProperty(globalThis, key, { configurable: true, value: key === "window" ? win : key === "document" ? win.document : (win as any)[key] });
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
