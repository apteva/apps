import { describe, expect, test } from "bun:test";
import { qrSVG } from "./qr";

describe("qrSVG", () => {
  test("encodes ordinary tunnel URLs", () => {
    const svg = qrSVG("https://example.trycloudflare.com");
    expect(svg).toContain("<svg");
    expect(svg).toContain("shape-rendering=\"crispEdges\"");
  });

  test("writes version information for version 7 payloads", () => {
    const svg = qrSVG("https://example.com/" + "a".repeat(95), { margin: 4 });
    // Version 7 is 45 modules wide; the 4-module quiet zone makes 53.
    expect(svg).toContain('viewBox="0 0 53 53"');
    // Version 7 BCH bits are 0x07C94. These symmetric dark modules are in
    // the reserved version-information areas, not the payload region.
    expect(svg).toContain("M40 4h1v1h-1z");
    expect(svg).toContain("M4 40h1v1h-1z");
  });

  test("rejects payloads beyond the supported table", () => {
    expect(() => qrSVG("https://example.com/" + "x".repeat(300))).toThrow();
  });
});
