import { describe, expect, test } from "bun:test";
import { formatCheckoutOffer } from "./offerPricing";
import type { PublicOffer } from "./types";
const monthly: PublicOffer = { catalog_price_id: 1, kind: "recurring", name: "Monthly", unit_amount_cents: 4900, currency: "USD", interval: "month", interval_count: 1, scope_type: "all_courses", course_slugs: [] };
describe("prepared checkout pricing", () => {
  test("monthly checkout preserves renewal period", () => {
    const result = formatCheckoutOffer(monthly, 4900, "USD", true);
    expect(result).toContain("49.00"); expect(result).toContain("/ month"); expect(result).not.toContain("one-time");
  });
  test("annual discounted checkout preserves renewal period and prepared amount", () => {
    const result = formatCheckoutOffer({...monthly, interval: "year", unit_amount_cents: 49000}, 39200, "USD", true);
    expect(result).toContain("392.00"); expect(result).toContain("/ year"); expect(result).not.toContain("one-time");
  });
  test("individual discounted course remains one-time", () => {
    const result = formatCheckoutOffer({...monthly, kind: "one_time", interval: undefined}, 900, "USD", true);
    expect(result).toContain("9.00"); expect(result).toContain("one-time"); expect(result).not.toContain("/ month");
  });
});
