import { expect, test } from "bun:test";
import { assetTitle } from "./assetTitle";

test("uses the Catalog display title for human-readable headings", () => {
  const asset = { name: "IMG_1234.mov", title: " Studio session — First take " };
  expect(assetTitle(asset)).toBe("Studio session — First take");
  expect(asset.name).toBe("IMG_1234.mov");
});

test("falls back to the source filename when a title is unavailable", () => {
  expect(assetTitle({ name: "IMG_1234.mov" })).toBe("IMG_1234.mov");
  expect(assetTitle({ name: "IMG_1234.mov", title: "   " })).toBe("IMG_1234.mov");
});
