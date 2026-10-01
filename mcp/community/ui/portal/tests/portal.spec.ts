import { test, expect, type Page } from "@playwright/test";

const community = { id: "community", slug: "main", name: "Learning Community", project_id: "project", description: "A place to learn" };
const member = { id: "alice", community_id: community.id, handle: "alice", display_name: "Alice", bio: "Hello", status: "active" };
const course = { id: "course", community_id: community.id, slug: "course", name: "Getting Started", kind: "course", visibility: "members" };
const lesson = { id: "lesson", community_id: community.id, section_id: "section", title: "Welcome lesson", body: "# Welcome\n\nLearn **safely**.\n\n<script>window.compromised=true</script>\n\n[Unsafe](javascript:alert(1))", video_storage_key: "42" };
const quiz = { id: "quiz", lesson_id: lesson.id, title: "Knowledge check", passing_score: 70, questions: [{ prompt: "Two plus two?", options: ["4", "5"] }] };
const assignment = { id: "assignment", title: "Introduce yourself", instructions: "Write **a short introduction**." };

async function fixture(page: Page, options: { empty?: boolean; lessonFailure?: boolean; members?: number } = {}) {
  const calls: Array<{ name: string; arguments: Record<string, any> }> = [];
  let profile = { ...member };
  let submissions: any[] = [], attempts: any[] = [];
  await page.addInitScript(() => {
    sessionStorage.setItem("apteva-community-auth:main", JSON.stringify({ user: { id: 1, email: "alice@example.test" }, apteva_access_token: "test-member-token", stored_at: Date.now(), apteva_expires_in: 3600 }));
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request(), url = new URL(request.url());
    const json = (value: unknown, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(value) });
    if (url.pathname.endsWith("/portal/bootstrap")) return json({ community, brand: { name: community.name, primary_color: "#059669", accent_color: "#059669" }, auth: { client_id: "client" }, signup: { enabled: true } });
    if (url.pathname.endsWith("/portal/products")) return json({ products: [], count: 0 });
    if (!url.pathname.endsWith("/mcp")) return json({});
    const { name, arguments: args = {} } = request.postDataJSON().params;
    calls.push({ name, arguments: args });
    let out: unknown;
    switch (name) {
      case "members_me": out = { communities: [community], memberships: [profile] }; break;
      case "members_ensure": out = { member: profile }; break;
      case "members_list": out = { members: options.members ? Array.from({ length: options.members }, (_, i) => ({ ...member, id: `member-${i}`, display_name: `Member ${i}` })).slice(args.offset || 0, (args.offset || 0) + args.limit) : [profile] }; break;
      case "members_update": profile = { ...profile, display_name: args.display_name, bio: args.bio }; out = profile; break;
      case "spaces_list": out = { spaces: options.empty ? [] : [course] }; break;
      case "dms_list_threads": out = { threads: [] }; break;
      case "membership_plans_list": out = { plans: [] }; break;
      case "membership_status": out = { subscription: null }; break;
      case "courses_get_details": out = { details: { summary: "Start learning" }, enrollment_rules: { access_mode: "free" } }; break;
      case "sections_list": out = { sections: [{ id: "section", title: "Basics" }] }; break;
      case "course_offer_get": out = { offer: null }; break;
      case "course_purchase_status": out = { purchase: null }; break;
      case "issued_certificate_get": out = { certificate: { id: "cert-1", title: "Community Graduate", body: "Awarded to Alice", issued_at: "2026-01-01" } }; break;
      case "course_tracks_list": out = { tracks: [], selected_track_id: null }; break;
      case "milestones_list": out = { milestones: [], next_action: "" }; break;
      case "assignment_reviews_list": out = { submissions: [] }; break;
      case "lessons_list":
        if (options.lessonFailure) return json({ jsonrpc: "2.0", error: { code: -32000, message: "Server temporarily unavailable" } });
        out = { lessons: [lesson] }; break;
      case "lesson_bundle_get": out = { lesson, quizzes: [quiz], assignments: [assignment], resources: [{ id: "resource", name: "Notes", storage_file_id: "43" }], comments: [] }; break;
      case "learning_status": out = { attempts, submissions }; break;
      case "lesson_file_url": out = { url: "https://files.example.test/video.mp4", expires_at: 4102444800 }; break;
      case "quiz_submit": attempts = [{ id: 1, quiz_id: "quiz", member_id: "alice", score: args.answers[0] === 0 ? 100 : 0, passed: args.answers[0] === 0 }]; out = attempts[0]; break;
      case "assignment_submit": submissions = [{ assignment_id: args.assignment_id, body: args.body, links: args.links || [], files: args.files || [], status: "submitted", feedback: "", version: 1, member_id: "alice" }]; out = submissions[0]; break;
      default: throw new Error(`Unmocked tool ${name}`);
    }
    return json({ jsonrpc: "2.0", id: 1, result: { content: [{ type: "text", text: JSON.stringify(out) }] } });
  });
  await page.route("https://files.example.test/**", (route) => route.fulfill({ status: 200, body: "", contentType: "video/mp4" }));
  await page.goto("/?community=main&project_id=project");
  await expect(page.locator('nav[aria-label="Community navigation"]')).toBeAttached();
  return calls;
}

async function navigate(page: Page, name: string) {
  if (await page.getByRole("button", { name: "Open navigation" }).isVisible()) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("navigation").getByRole("button", { name, exact: true }).click();
}

test("empty spaces, courses, and messages display their empty states", async ({ page }) => {
  await fixture(page, { empty: true });
  await navigate(page, "Spaces"); await expect(page.getByText("No discussion spaces yet.")).toBeVisible();
  await navigate(page, "Courses"); await expect(page.getByText("No courses available.")).toBeVisible();
  await navigate(page, "Messages"); await expect(page.getByText("No direct messages yet.")).toBeVisible();
});

test("profile saves, course content renders safely, quizzes and submissions survive reload", async ({ page }) => {
  const errors: string[] = []; page.on("pageerror", (e) => errors.push(e.message));
  const calls = await fixture(page);
  await navigate(page, "Profile");
  await page.getByLabel("Display name", { exact: true }).fill("Alice Updated");
  await page.getByLabel("Bio", { exact: true }).fill("Updated biography");
  await page.getByRole("button", { name: "Save profile" }).click();
  await expect(page.getByText("Profile saved.")).toBeVisible();
  expect(calls.find((c) => c.name === "members_update")?.arguments.display_name).toBe("Alice Updated");
  await navigate(page, "Courses");
  await expect(page.getByRole("heading", { name: "Welcome", exact: true })).toBeVisible();
  await expect(page.locator(".markdown strong").first()).toHaveText("safely");
  expect(await page.evaluate(() => (window as any).compromised)).toBeUndefined();
  expect(await page.getByRole("link", { name: "Unsafe" }).getAttribute("href")).not.toMatch(/^javascript:/);
  await page.getByRole("radio", { name: "4", exact: true }).check();
  await page.getByRole("button", { name: "Submit answers" }).click();
  await expect(page.getByText("Score: 100% · Passed")).toBeVisible();
  await page.getByLabel("Your submission").fill("My introduction");
  await page.getByRole("button", { name: "Save submission" }).click();
  await expect(page.getByText("Submission saved.")).toBeVisible();
  await page.getByRole("button", { name: "Open Notes", exact: true }).click();
  await expect(page.getByRole("link", { name: "Open Notes", exact: true })).toHaveAttribute("href", /files\.example/);
  await page.reload(); await navigate(page, "Courses");
  await expect(page.getByLabel("Your submission")).toHaveValue("My introduction");
  await expect(page.getByText("Score: 100% · Passed")).toBeVisible();
  await expect(page.getByRole("region", { name: "Earned certificate" })).toBeVisible();
  await page.screenshot({ path: test.info().outputPath("desktop.png"), fullPage: true });
  expect(errors).toEqual([]);
});

test("lesson service failure is visible and does not present an enrollment lock", async ({ page }) => {
  await fixture(page, { lessonFailure: true }); await navigate(page, "Courses");
  await expect(page.getByRole("alert")).toContainText("Server temporarily unavailable");
  await expect(page.getByRole("button", { name: "Enroll", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeEnabled();
});

test("member pagination reaches records beyond the first page", async ({ page }) => {
  const calls = await fixture(page, { members: 125 }); await navigate(page, "Members");
  await expect(page.getByRole("heading", { name: "Member 99", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Load more members" }).click();
  await expect(page.getByRole("heading", { name: "Member 124", exact: true })).toBeVisible();
  expect(calls.some((c) => c.name === "members_list" && c.arguments.offset === 100)).toBe(true);
  await expect(page.getByRole("button", { name: "Load more members" })).toHaveCount(0);
});

test("mobile course view fits its viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 }); await fixture(page); await navigate(page, "Courses");
  await expect(page.getByRole("heading", { name: "Welcome", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({ path: test.info().outputPath("mobile.png"), fullPage: true });
});

test("expired delegated sessions return to sign-in with an actionable message", async ({ page }) => {
  await fixture(page);
  await page.route(/\/api\/apps\/community\/mcp(?:\?|$)/, (route) => route.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ error: "token expired" }) }));
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Your session expired. Please sign in again.");
  await expect(page.getByRole("navigation")).toHaveCount(0);
  expect(await page.evaluate(() => sessionStorage.getItem("apteva-community-auth:main"))).toBeNull();
});
