import { expect, test } from "bun:test";

test("TURN is registered as a native Apteva React panel", async () => {
  const manifest = await Bun.file(new URL("../apteva.yaml", import.meta.url)).text();
  const source = await Bun.file(new URL("./TurnPanel.tsx", import.meta.url)).text();
  expect(manifest).toContain("slot: project.page");
  expect(manifest).toContain("entry: /ui/TurnPanel.mjs");
  expect(source).toContain("export default function TurnPanel(props: PanelProps)");
  expect(source).not.toContain("<iframe");
  expect(source).not.toContain("createRoot(");
});

test("panel palette, typography and geometry follow the host theme", async () => {
  const source = await Bun.file(new URL("./TurnPanel.tsx", import.meta.url)).text();
  const css = source.match(/const css = `([\s\S]*?)`;/)?.[1] || "";
  for (const token of ["--bg", "--bg-card", "--text", "--text-muted", "--border", "--accent", "--success", "--warn", "--error", "--font-base", "--font-mono-fixed", "--radius-sm", "--radius-md", "--radius-lg"]) expect(css).toContain(`var(${token})`);
  expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  expect(css).not.toMatch(/(?:^|\})\s*(?:html|body|:root)\b/);
  expect(source).not.toContain("data-theme=");
});

test("published bundle uses the host React and production JSX modules", async () => {
  const bundle = await Bun.file(new URL("./TurnPanel.mjs", import.meta.url)).text();
  expect(bundle).toMatch(/from["']react["']/);
  expect(bundle).toMatch(/from["']react\/jsx-runtime["']/);
  expect(bundle).not.toContain("react/jsx-dev-runtime");
  expect(bundle).toMatch(/as default/);
  expect(bundle).not.toContain("__SECRET_INTERNALS");
});
