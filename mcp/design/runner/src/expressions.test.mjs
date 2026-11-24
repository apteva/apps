import { expect, test } from "bun:test";
import { evaluate } from "./expressions.mjs";
import fixture from "../../testdata/expressions.json";

for (const item of fixture.cases) {
  test(item.expr, () => {
    if (item.error) {
      expect(() => evaluate(item.expr, fixture.params)).toThrow(item.error);
    } else {
      expect(evaluate(item.expr, fixture.params)).toBeCloseTo(item.value, 9);
    }
  });
}
