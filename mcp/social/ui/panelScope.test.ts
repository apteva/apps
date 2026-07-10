import { expect, test } from "bun:test";
import { isTrustedOAuthMessage, scopedAppURL } from "./panelScope";

test("scopedAppURL never retains a prior project", () => {
  expect(scopedAppURL("/api/apps/social", "/profiles", "project-a"))
    .toBe("/api/apps/social/profiles?project_id=project-a");
  expect(scopedAppURL("/api/apps/social", "/profiles", "project-b"))
    .toBe("/api/apps/social/profiles?project_id=project-b");
  expect(scopedAppURL("/api/apps/social", "/posts?limit=10", "project b"))
    .toBe("/api/apps/social/posts?limit=10&project_id=project%20b");
});

test("OAuth messages must come from this origin and have numeric ids", () => {
  const message = { type: "social.oauth_ready", pending_account_id: 12, connection_id: 34 };
  expect(isTrustedOAuthMessage("https://agents.example", "https://agents.example", message)).toBe(true);
  expect(isTrustedOAuthMessage("https://attacker.example", "https://agents.example", message)).toBe(false);
  expect(isTrustedOAuthMessage("https://agents.example", "https://agents.example", {
    ...message,
    pending_account_id: "12",
  })).toBe(false);
});
