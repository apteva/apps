import { expect, test } from "@playwright/test";

for (const host of ["dashboard", "panel", "external", "package"]) {
  test(`${host}: message web links open a new tab and preserve the conversation`, async ({ page, request, context }) => {
    await request.post("/reset");
    const chat = host === "dashboard" || host === "panel" ? "chat-operator" : "chat-visitor-a";
    if (host === "panel") {
      await request.post("/seed-panel", { data: [{ id: chat, project_id: "project", lead_agent_id: 41, title: "Link test", kind: "direct", audience: "operator", origin: "web", created_at: "", updated_at: "" }] });
    }
    const preview = "http://127.0.0.1:5292/link-preview";
    const content = [
      preview,
      "[Remote details](https://example.com/report)",
      "[Protocol relative](//example.com/report)",
      "[Local route](/inside-app)",
      "[Section](#details)",
      "[Email](mailto:hello@example.com)",
      '<a href="https://example.com/raw" target="_self" rel="nofollow" onclick="window.linkInjected=true">Raw web link</a>',
      '<a href="javascript:alert(1)">Unsafe link</a>',
    ].join("\n\n");
    await request.post("/append-message", { data: { id: 101, conversation_id: chat, role: "agent", agent_id: 41, content, components: [], created_at: new Date().toISOString() } });
    await request.post("/append-message", { data: { id: 102, conversation_id: chat, role: "agent", agent_id: 41, content: "", component_kind: "report", components: [{ app: "conversations", name: "report-card", props: { title: "Preview verification", summary: "Ready", sections: [{ title: "Evidence", body: "[Report evidence](https://example.com/evidence)" }] } }], created_at: new Date().toISOString() } });
    await page.goto(host === "panel" ? "/?host=dashboard&surface=panel" : `/?host=${host}`);
    for (const name of [preview, "Remote details", "Protocol relative", "Raw web link", "Report evidence"]) {
      const link = page.getByRole("link", { name, exact: true });
      await expect(link).toHaveAttribute("target", "_blank");
      await expect(link).toHaveAttribute("rel", /noopener/);
      await expect(link).toHaveAttribute("rel", /noreferrer/);
    }
    for (const name of ["Local route", "Section", "Email"]) {
      await expect(page.getByRole("link", { name, exact: true })).not.toHaveAttribute("target", "_blank");
    }
    await expect(page.getByRole("link", { name: "Raw web link" })).toHaveAttribute("rel", /nofollow/);
    await expect(page.getByRole("link", { name: "Raw web link" })).not.toHaveAttribute("onclick");
    await expect(page.getByRole("link", { name: "Unsafe link" })).toHaveCount(0);
    const draft = page.getByRole("textbox").first();
    await draft.fill("Keep my draft while I inspect the preview");
    const originalURL = page.url();
    await context.route(preview, route => route.fulfill({ contentType: "text/html", body: "<p>Local preview</p>" }));
    const popupPromise = page.waitForEvent("popup");
    await page.getByRole("link", { name: preview, exact: true }).click();
    const popup = await popupPromise;
    await expect(popup.locator("p")).toHaveText("Local preview");
    expect(await popup.evaluate(() => window.opener === null)).toBe(true);
    expect(page.url()).toBe(originalURL);
    await expect(draft).toHaveValue("Keep my draft while I inspect the preview");
    await expect(page.getByRole("link", { name: "Remote details" })).toBeVisible();
    await popup.close();
  });
}
