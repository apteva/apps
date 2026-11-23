import { describe, expect, test } from "bun:test";
import { marked } from "marked";
import { keepListItemNumbers } from "../src/markdown";

marked.setOptions({ breaks: true, gfm: true });
const render = (source: string) => marked.parse(keepListItemNumbers(source), { async: false }) as string;

describe("list items whose text starts with a number", () => {
  // Live prod replies: "- 01. Define" showed "• 1." and "1. 01. Define" showed
  // "1. 1.", because CommonMark reads the item's number as a nested list.
  test("bullets keep the item's own number", () => {
    const html = render("- 01. Define the concept (Dec 2)\n- 02. Research market (Dec 4)");
    expect(html).not.toContain("<ol");
    expect(html).toContain("<li>01. Define the concept (Dec 2)</li>");
    expect(html).toContain("<li>02. Research market (Dec 4)</li>");
  });

  test("numbered items keep the item's own number", () => {
    const html = render("1. 01. Define the goal\n2. 02. Legal check");
    expect(html.match(/<ol/g)?.length).toBe(1);
    expect(html).toContain("<li>01. Define the goal</li>");
  });

  test("other markers, parentheses, years and nesting", () => {
    expect(render("* 2026. was the pilot year")).toContain("<li>2026. was the pilot year</li>");
    expect(render("+ 3) Ship it")).toContain("<li>3) Ship it</li>");
    const nested = render("- Phase one\n  - 01. Kickoff");
    expect(nested).not.toContain("<ol");
    expect(nested).toContain("<li>01. Kickoff</li>");
    expect(render("> - 01. Quoted step")).toContain("<li>01. Quoted step</li>");
  });

  test("ordinary lists and text are unchanged", () => {
    for (const source of ["1. Define the goal\n2. Legal check", "- Define\n- Research", "Version 1. 2. 3 shipped", "- Step 1: define", "- 1.5 hours left"]) {
      expect(keepListItemNumbers(source)).toBe(source);
    }
    const html = render("1. Define the goal\n2. Legal check");
    expect(html).toContain("<li>Define the goal</li>");
  });

  test("fenced code is left alone", () => {
    const source = "```\n- 01. literal\n```\n- 01. Define\n~~~md\n1. 02. literal\n~~~";
    expect(keepListItemNumbers(source)).toBe("```\n- 01. literal\n```\n- 01\\. Define\n~~~md\n1. 02. literal\n~~~");
    expect(render(source)).toContain("- 01. literal");
  });
});
