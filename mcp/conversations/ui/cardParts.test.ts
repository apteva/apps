import { expect, test } from "bun:test";
import { alertParts, plainLine } from "../frontend/src/cardParts";

test("a bold lead-in becomes the alert title and the source line its reference", () => {
  const text = "**Bunny.net security notice — check TLS setup.** Bunny reports unauthorized access on Nov 17.\n\nSource: `conversation:4514:inbound:7378`";
  expect(alertParts(text)).toEqual({
    title: "Bunny.net security notice — check TLS setup",
    body: "Bunny reports unauthorized access on Nov 17.",
    source: "conversation:4514:inbound:7378",
  });
});

test("plain alerts keep their whole text as the body", () => {
  expect(alertParts("Test alert: 30 seconds have elapsed.")).toEqual({ title: "", body: "Test alert: 30 seconds have elapsed.", source: "" });
  // A bold-only alert has nothing after it, so it stays body text.
  expect(alertParts("**Disk almost full**")).toEqual({ title: "", body: "**Disk almost full**", source: "" });
  // A one-line "Source: x" alert is the message, not a reference.
  expect(alertParts("Source: nightly sync failed")).toEqual({ title: "", body: "Source: nightly sync failed", source: "" });
});

test("headings, bold source labels and explicit titles are understood", () => {
  expect(alertParts("## Backup failed\nThe nightly job exited with code 2.\n**Source:** https://example.com/runs/9")).toEqual({
    title: "Backup failed", body: "The nightly job exited with code 2.", source: "https://example.com/runs/9",
  });
  expect(alertParts("**Lead.** Body", "Given title").title).toBe("Given title");
});

test("list rows show plain text", () => {
  expect(plainLine("**Train Station #24-1299** — `confirm` [plans](https://x.y)")).toBe("Train Station #24-1299 — confirm plans");
});
