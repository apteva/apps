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
async function fixture(page: Page, dropped = () => 450) {
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
          : [{ ...base, metrics: {...base.metrics, playback_dropped_ms: dropped()}, call_id: second ? "next-call" : "audio-call" }],
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
test("widget highlights issues, filters and expands details without links", async ({ page }) => {
  const requests = await fixture(page);
  await page.goto("/?widget");
  await expect(page.getByRole("heading", { name: "Telephony audio health" })).toBeVisible();
  await expect(page.getByRole("link")).toHaveCount(0);
  await expect(page.getByLabel("Audio issue reports").getByText("Dropped audio", { exact: true })).toBeVisible();
  await expect(page.getByText("450 ms", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Show details" }).click();
  await expect(page.getByText("Call audio-call", { exact: false })).toBeVisible();
  await page.getByLabel("Audio problem").selectOption("dropped_audio");
  await expect.poll(() => new URL(requests.at(-1)!).searchParams.get("issue")).toBe("dropped_audio");
  await page.getByLabel("Audio time range").selectOption("1h");
  await page.getByText("More filters", {exact:true}).click();
  await page.getByLabel("Audio direction").selectOption("telephony_to_browser");
  await page.getByLabel("Audio adviser").selectOption(identity);
  await expect.poll(() => new URL(requests.at(-1)!).searchParams.get("adviser")).toBe(identity);
  await page.getByLabel("Audio provider").selectOption("telnyx");
  await expect(page.getByText("No matching issues.", {exact:false})).toBeVisible();
  await page.getByLabel("Audio provider").selectOption("twilio");
  await expect(page.getByLabel("Audio issue reports").getByText("Dropped audio", {exact:true})).toBeVisible();
  await page.getByText("More filters", {exact:true}).click();
  await page.screenshot({path:"/private/tmp/telephony-audio-health-widget.png"});
});

test("widget receives real SSE hints with project/install isolation and recovery", async ({ page, request }) => {
  let dropped=450;
  const requests=await fixture(page, () => dropped);
  await page.goto("/?widget");
  await expect(page.getByText("450 ms", {exact:true})).toBeVisible();
  await expect(page.getByText("SSE updates", {exact:true})).toBeVisible();
  await expect.poll(async () => (await (await request.get("/fixture/streams")).json()).count).toBe(1);
  // Let the connection's reconciliation fetch complete before checking unrelated events.
  await page.waitForTimeout(500);
  const before=requests.length;
  const event={app:"telephony",project_id:"monitor-project",install_id:42,topic:"telephony.audio.reports.changed",seq:1};
  await request.post("/fixture/audio-events",{data:{...event,project_id:"another-project"}});
  await request.post("/fixture/audio-events",{data:{...event,install_id:43}});
  await page.waitForTimeout(500);
  expect(requests.length).toBe(before);
  dropped=1900;
  for(let i=1;i<=4;i++) await request.post("/fixture/audio-events",{data:{...event,seq:i}});
  await expect(page.getByText("1.9 s",{exact:true})).toBeVisible();
  expect(requests.length).toBe(before+1);
  // Changing filters keeps the same SSE connection rather than allocating a new socket.
  await page.getByLabel("Audio problem").selectOption("reconnect");
  await expect.poll(() => new URL(requests.at(-1)!).searchParams.get("issue")).toBe("reconnect");
  expect((await (await request.get("/fixture/streams")).json()).count).toBe(1);
  await request.post("/fixture/audio-events",{data:{disconnect:true}});
  await expect(page.getByText("Live updates unavailable · recovery polling", {exact:true})).toBeVisible();
  await page.route("**/api/apps/telephony/audio-health?**",route=>route.fulfill({status:503,body:"temporary"}));
  await page.getByRole("button",{name:"Refresh audio health"}).click();
  await expect(page.getByRole("alert")).toContainText("HTTP 503");
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
