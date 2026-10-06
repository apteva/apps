import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

test("user bubbles keep accent styling in dark mode and use neutral light surfaces", () => {
  const source = readFileSync(new URL("../src/ConversationsPanel.tsx", import.meta.url), "utf8");
  const styles = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

  expect(source).toContain("chat-message-user-bubble bg-accent/15 border border-accent/30");
  expect(styles).toContain('[data-mode="light"] .apteva-conversations .chat-message-user-bubble');
  expect(styles).toContain("background-color:var(--bg-input,#f6f2e9)");
  expect(styles).toContain("border-color:var(--border,#d4ccb8)");
});
