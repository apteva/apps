import { describe, expect, test } from "bun:test";
import { todoApiUrl, tzOffsetMinutes } from "./api";

describe("todoApiUrl", () => {
  test("scopes an endpoint without an existing query", () => {
    expect(todoApiUrl("/lists", "personal-project", 120)).toBe(
      "/api/apps/todo/lists?project_id=personal-project&tz_offset=120",
    );
  });

  test("preserves an existing query and escapes the project id", () => {
    expect(todoApiUrl("/todos?view=all", "client/a b", -420)).toBe(
      "/api/apps/todo/todos?view=all&project_id=client%2Fa%20b&tz_offset=-420",
    );
  });

  test("reports the offset east of UTC, the sign the server expects", () => {
    // getTimezoneOffset() is minutes *behind* UTC, so CEST reports -120.
    const cest = { getTimezoneOffset: () => -120 } as Date;
    expect(tzOffsetMinutes(cest)).toBe(120);
  });
});
