import { expect, test } from "bun:test";
import { equivalentBindings } from "./verify-social-artifacts";
test("allows consistent compiler renames and debug IDs", () => {
  expect(equivalentBindings('import{A as x}from"./shared.mjs";x(1);\n//# debugId=old', 'import{A as y}from"./shared.mjs";y(1);\n//# debugId=new')).toBe(true);
});
test("rejects behavior, import, and inconsistent binding changes", () => {
  const original = 'import{A as x}from"./shared.mjs";x(1);';
  for (const changed of ['import{A as y}from"./shared.mjs";y(2);', 'import{B as y}from"./shared.mjs";y(1);', 'import{A as y}from"./other.mjs";y(1);', 'import{A as y}from"./shared.mjs";x(1);']) expect(equivalentBindings(original,changed)).toBe(false);
});
