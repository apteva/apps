import { expect, test } from "bun:test";
import {
  isPostLifecycleEvent,
  postStatusVariant,
  postTitle,
  socialURL,
} from "./socialCardData";

test("social card URLs retain query parameters and project scope", () => {
  expect(socialURL("/posts/17", "project one"))
    .toBe("/api/apps/social/posts/17?project_id=project%20one");
  expect(socialURL("/posts?from=start&to=end", "project one"))
    .toBe("/api/apps/social/posts?from=start&to=end&project_id=project%20one");
});

test("post presentation is compact and status-aware", () => {
  expect(postTitle({ body: "\n First line\nSecond line" })).toBe("First line");
  expect(postTitle({ body: "" })).toBe("Untitled post");
  expect(postStatusVariant("published")).toBe("ok");
  expect(postStatusVariant("failed")).toBe("err");
  expect(postStatusVariant("scheduled")).toBe("muted");
});

test("live cards refresh only for post lifecycle topics", () => {
  expect(isPostLifecycleEvent("post.created")).toBe(true);
  expect(isPostLifecycleEvent("target.published")).toBe(true);
  expect(isPostLifecycleEvent("account.checked")).toBe(false);
});
