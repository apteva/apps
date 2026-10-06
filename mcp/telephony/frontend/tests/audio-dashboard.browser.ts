import { test, expect, type Page } from "@playwright/test";
const identity = JSON.stringify({
  issuer_app: "auth",
  issuer_install_id: "11",
  subject_type: "user",
  subject_id: "19",
});
const base = {
  call_id: "audio-call",
  provider: "twilio",
  status: "in-progress",
  from: "+33123456789",
  to: "+33187654321",
  adviser: identity,
  adviser_label: "Paris sales · user 19",
  destination: "sales",
  active: true,
  state: "audio_degraded",
  observed_at: new Date().toISOString(),
  telemetry: true,
  issues: ["dropped_audio", "reconnect"],
  stages: ["telephony_to_browser"],
  health: {
    state: "audio_degraded",
    stages: {
      telephony_to_browser: {
        state: "audio_degraded",
        signal: "playback_drop",
        last_bad_at: new Date().toISOString(),
      },
    },
  },
  metrics: {
    playback_dropped_ms: 450,
    max_rtt_ms: 85,
    reconnects: 2,
    context_suspensions: 1,
    worker_pauses: 2,
    browser_max_write_ms: 31,
  },
};
async function fixture(page: Page) {
  const requests: string[] = [];
  await page.route("**/api/apps/telephony/audio-health?**", async (route) => {
    const url = new URL(route.request().url());
    requests.push(url.href);
    if (url.searchParams.has("call_id")) {
      await route.fulfill({
        json: {
          browser: {
            server: {
              browser_socket: {
                peer_address_hash: "hash-only",
                events: [{ close_code: 1006, reason: "transport" }],
              },
            },
            session_events: [{ action: "audio_context", outcome: "suspended" }],
          },
          carrier: { provider: "twilio" },
        },
      });
      return;
    }
    const empty = url.searchParams.get("provider") === "telnyx";
    const second = url.searchParams.has("cursor");
    await route.fulfill({
      json: {
        calls: empty
          ? []
          : [{ ...base, call_id: second ? "next-call" : "audio-call" }],
        totals: {
          calls: empty ? 0 : 120,
          active: empty ? 0 : 2,
          affected: empty ? 0 : 110,
          degraded: empty ? 0 : 1,
          unobserved: 0,
        },
        facets: {
          provider: [
            { value: "twilio", label: "twilio" },
            { value: "telnyx", label: "telnyx" },
          ],
          adviser: [{ value: identity, label: "Paris sales · user 19" }],
          destination: [{ value: "sales", label: "Paris sales" }],
        },
        alerts: [
          {
            provider: "twilio",
            stage: "telephony_to_browser",
            state: "audio_degraded",
            call_count: 3,
            call_ids: ["audio-call"],
            occurred_at: new Date().toISOString(),
          },
        ],
        next_cursor: empty || second ? "" : "next-page",
        pending_reports: 0,
        generated_at: new Date().toISOString(),
      },
    });
  });
  return requests;
}
test("monitor filters, pages, expands full diagnostics and reports request failures", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const requests = await fixture(page);
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Audio health", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("120 match", { exact: false })).toBeVisible();
  await page.getByLabel("Adviser", { exact: true }).selectOption(identity);
  await expect
    .poll(() => new URL(requests.at(-1)!).searchParams.get("adviser"))
    .toBe(identity);
  await page.getByLabel("Audio direction").selectOption("telephony_to_browser");
  await page.getByLabel("Issue / observation").selectOption("dropped_audio");
  await expect
    .poll(() => new URL(requests.at(-1)!).searchParams.get("issue"))
    .toBe("dropped_audio");
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await expect
    .poll(() => new URL(requests.at(-1)!).searchParams.get("cursor"))
    .toBe("next-page");
  await page.getByRole("button", { name: "Previous", exact: true }).click();
  await page.getByRole("button", { name: /Paris sales/ }).click();
  await page.getByText("Full recorded diagnostics:", { exact: false }).click();
  await expect(
    page.getByText('"peer_address_hash": "hash-only"', { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByText("AudioContext suspensions", { exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "/private/tmp/telephony-audio-health-view.png",
    fullPage: true,
  });
  await page.getByLabel("Provider", { exact: true }).selectOption("telnyx");
  await expect(
    page.getByText("No calls match these filters.", { exact: false }),
  ).toBeVisible();
  await page.route("**/api/apps/telephony/audio-health?**", (route) =>
    route.fulfill({ status: 503, body: "temporary" }),
  );
  await page.getByRole("button", { name: "Refresh audio health" }).click();
  await expect(page.getByRole("alert")).toContainText("HTTP 503");
  expect(errors).toEqual([]);
});
test("dashboard widget deep-links to adviser call and preserves project/install filters", async ({
  page,
}) => {
  const requests = await fixture(page);
  await page.goto("/?widget");
  await expect(
    page.getByRole("heading", { name: "Telephony audio health" }),
  ).toBeVisible();
  const link = page.getByRole("link", { name: "View audio health →" });
  const url = new URL(
    (await link.getAttribute("href")) || "",
    "http://fixture",
  );
  expect(url.searchParams.get("project_id")).toBe("monitor-project");
  expect(url.searchParams.get("install_id")).toBe("42");
  expect(url.searchParams.get("tab")).toBe("audio-health");
  await expect(page.getByRole("link", { name: /Paris sales/ })).toHaveAttribute(
    "href",
    /audio_call_id=audio-call/,
  );
  expect(new URL(requests.at(-1)!).searchParams.get("state")).toBe("issues");
  await page.screenshot({
    path: "/private/tmp/telephony-audio-health-widget.png",
  });
});
test("widget link opens monitor with filters and selected diagnostics expanded", async ({
  page,
}) => {
  await fixture(page);
  await page.goto(
    "/?tab=audio-health&provider=twilio&search=audio-call&audio_call_id=audio-call",
  );
  await expect(page.getByLabel("Provider", { exact: true })).toHaveValue(
    "twilio",
  );
  await expect(page.getByLabel("Call, number or team")).toHaveValue(
    "audio-call",
  );
  await expect(
    page.getByText("Call audio-call", { exact: false }),
  ).toBeVisible();
});
