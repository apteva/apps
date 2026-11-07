import {expect, test} from "bun:test";
import {verifyPanelBundle} from "./verify-bundle";

test("shipped panel uses the dashboard's production JSX exports", async () => {
  const shipped = await Bun.file(new URL("./InstancesPanel.mjs", import.meta.url)).text();
  expect(() => verifyPanelBundle(shipped)).not.toThrow();
});

test("panel verification rejects the development JSX import that crashed v0.6.3", () => {
  expect(() => verifyPanelBundle('import{jsxDEV as M}from"react/jsx-dev-runtime";')).toThrow("development JSX");
});
