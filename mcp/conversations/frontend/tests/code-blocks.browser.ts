import { expect, test, type APIRequestContext } from "@playwright/test";

// Agents put text meant to be pasted elsewhere (prompts, commands, configs) in
// fenced blocks. The block must be copyable as written, readable without
// sideways scrolling when it is prose, and must not swallow the transcript.
const prose = Array.from({ length: 24 }, (_, i) =>
  `Paragraph ${i + 1}: a long line of plain instructions that is much wider than the chat column, so it has to wrap to be readable without scrolling sideways.`);
const PROMPT = prose.join("\n\n");
const GO = 'func main() {\n\tfmt.Println("a line of code that is long enough to need horizontal scrolling in a narrow chat column instead of wrapping")\n}';
const SHELL = "curl -s http://localhost:5280/health";

async function seed(request: APIRequestContext, chat: string) {
  await request.post("/reset");
  await request.post("/append-message", { data: { id: 1, conversation_id: chat, role: "agent", agent_id: 41, components: [], created_at: new Date().toISOString(),
    content: `Here is the prompt:\n\n\`\`\`\n${PROMPT}\n\`\`\`\n\nAnd the code:\n\n\`\`\`go\n${GO}\n\`\`\`\n\nThen run \`${SHELL}\`:\n\n\`\`\`sh\n${SHELL}\n\`\`\`` } });
}

for (const width of [1280, 390]) {
  test(`code blocks at ${width}px: header, wrap, collapse and copy`, async ({ page, request, context, browserName }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await seed(request, "chat-operator");
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/?host=dashboard&theme=terminal");
    await expect(page.getByTitle("Live")).toBeVisible();
    const blocks = page.locator(".chat-md .chat-code");
    await expect(blocks).toHaveCount(3);
    const [text, go, sh] = [blocks.nth(0), blocks.nth(1), blocks.nth(2)];

    // Header names the language; an unlabelled fence is "Text".
    await expect(text.locator(".chat-code-lang")).toHaveText("Text");
    await expect(go.locator(".chat-code-lang")).toHaveText("go");
    await expect(sh.locator(".chat-code-lang")).toHaveText("sh");

    // Prose wraps; code keeps its lines and scrolls sideways.
    await expect(text).toHaveClass(/chat-code--wrap/);
    await expect(go).not.toHaveClass(/chat-code--wrap/);
    const textPre = text.locator("pre");
    expect(await textPre.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    const goPre = go.locator("pre");
    if (width === 390) expect(await goPre.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true);
    await go.getByRole("button", { name: "Wrap lines" }).click();
    await expect(go).toHaveClass(/chat-code--wrap/);
    await expect(go.getByRole("button", { name: "Wrap lines" })).toHaveAttribute("aria-pressed", "true");
    expect(await goPre.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);

    // The tall block starts collapsed; short ones have no footer.
    await expect(go.locator(".chat-code-more")).toHaveCount(0);
    const body = text.locator(".chat-code-body");
    const collapsed = (await body.boundingBox())!.height;
    expect(collapsed).toBeLessThan(400);
    const expand = text.getByRole("button", { name: /Show all \d+ lines/ });
    await expand.click();
    await expect(text.getByRole("button", { name: "Show less" })).toHaveAttribute("aria-expanded", "true");
    expect((await body.boundingBox())!.height).toBeGreaterThan(collapsed + 200);

    // Copy gives back exactly what the agent wrote, wrapped or not.
    if (browserName === "chromium") {
      await text.getByRole("button", { name: "Copy" }).click();
      await expect(text.getByRole("button", { name: "Copied" })).toBeVisible();
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(PROMPT);
      await go.getByRole("button", { name: "Copy" }).click();
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(GO);
      await expect(text.getByRole("button", { name: "Copy" })).toBeVisible({ timeout: 3000 });
    }

    // An unstuck header covers nothing: the block's top border and the line
    // above it stay visible (0.30.0 painted the stuck-header mask always).
    await go.scrollIntoViewIfNeeded();
    // Shadows are not hit-testable, so check the paint itself.
    expect(await go.locator(".chat-code-bar").evaluate((el) => getComputedStyle(el).boxShadow)).toBe("none");

    // The header stays in view while scrolling through the expanded block.
    const header = text.locator(".chat-code-header");
    await text.locator("pre").evaluate((el) => el.scrollIntoView({ block: "center" }));
    const headerBox = (await header.boundingBox())!;
    expect(headerBox.y).toBeGreaterThanOrEqual(0);
    expect(headerBox.y).toBeLessThan(800);
    if (await page.evaluate(() => CSS.supports("container-type", "scroll-state"))) {
      await expect.poll(() => text.locator(".chat-code-bar").evaluate((el) => getComputedStyle(el).boxShadow)).not.toBe("none");
    }

    // Inline code stays inline; the page never scrolls sideways.
    await expect(page.locator(".chat-md p code", { hasText: SHELL })).toHaveCount(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await text.locator(".chat-code-header").scrollIntoViewIfNeeded();
    await page.screenshot({ path: test.info().outputPath(`code-blocks-${width}.png`) });
  });
}

test("code block labels follow the conversation's language", async ({ page, request }) => {
  await seed(request, "chat-operator");
  await page.goto("/?host=dashboard&theme=terminal&locale=fr");
  await expect(page.locator(".chat-md .chat-code")).toHaveCount(3);
  const first = page.locator(".chat-md .chat-code").first();
  await expect(first.locator(".chat-code-lang")).toHaveText("Texte");
  await expect(first.getByRole("button", { name: "Copier" })).toBeVisible();
  await expect(first.getByRole("button", { name: /Afficher les \d+ lignes/ })).toBeVisible();
});
