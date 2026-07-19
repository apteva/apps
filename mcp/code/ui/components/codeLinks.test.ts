import { describe, expect, test } from "bun:test";
import {
  codePanelURL,
  eventTouchesFile,
  eventTouchesRepository,
  parseCodePanelLink,
} from "./codeLinks";

describe("Code component links", () => {
  test("round-trips repository and file targets", () => {
    const url = codePanelURL({
      view: "repositories",
      repo: "site demo",
      path: "src/a file.ts",
      line: 14,
    });
    expect(url).toBe("/apps/code/page?view=repositories&repo=site+demo&path=src%2Fa+file.ts&line=14");
    expect(parseCodePanelLink(url.slice(url.indexOf("?")))).toEqual({
      view: "repositories",
      repo: "site demo",
      path: "src/a file.ts",
      line: 14,
      issue: undefined,
    });
  });

  test("parses issue targets and rejects invalid positive integers", () => {
    expect(parseCodePanelLink("?view=issues&repo=code&issue=42&line=-2")).toEqual({
      view: "issues",
      repo: "code",
      path: undefined,
      line: undefined,
      issue: 42,
    });
  });
});

describe("Code component event matching", () => {
  test("repository cards follow repo, file, template, and dev events", () => {
    expect(eventTouchesRepository("file.changed", { slug: "code", path: "main.go" }, "code")).toBe(true);
    expect(eventTouchesRepository("issue.updated", { slug: "code", number: 1 }, "code")).toBe(false);
    expect(eventTouchesRepository("dev.changed", { slug: "other" }, "code")).toBe(false);
  });

  test("source cards only follow their file and renames", () => {
    expect(eventTouchesFile("file.changed", { slug: "code", path: "main.go" }, "code", "main.go")).toBe(true);
    expect(eventTouchesFile("file.changed", { slug: "code", path: "other.go" }, "code", "main.go")).toBe(false);
    expect(eventTouchesFile("file.renamed", { slug: "code", from: "main.go", to: "cmd/main.go" }, "code", "main.go")).toBe(true);
    expect(eventTouchesFile("repo.deleted", { slug: "code" }, "code", "main.go")).toBe(true);
  });
});
