import { describe, expect, test } from "bun:test";
import { todoApiUrl } from "./api";

describe("todoApiUrl", () => {
  test("scopes an endpoint without an existing query", () => {
    expect(todoApiUrl("/lists", "personal-project")).toBe(
      "/api/apps/todo/lists?project_id=personal-project",
    );
  });

  test("preserves an existing query and escapes the project id", () => {
    expect(todoApiUrl("/todos?view=all", "client/a b")).toBe(
      "/api/apps/todo/todos?view=all&project_id=client%2Fa%20b",
    );
  });
});
