import { test, expect, type Page } from "@playwright/test";
async function fixture(page: Page) {
  const requests: string[] = [],
    mutations: string[] = [],
    now = Date.now(),
    date = (offset = 0) => new Date(now + offset).toISOString();
  await page.addInitScript(() => {
    navigator.mediaDevices.getUserMedia = () => {
      throw new Error("Inspector must not open microphone");
    };
  });
  await page.route("**/api/**", async (route) => {
    const u = new URL(route.request().url());
    if (route.request().method() !== "GET") mutations.push(u.pathname);
    requests.push(u.href);
    if (u.pathname.endsWith("/events"))
      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": fixture\n\n",
      });
    if (u.pathname.endsWith("/calls"))
      return route.fulfill({
        json: {
          calls: [
            {
              id: "live-call",
              status: "in-progress",
              to_number: "+1 202 555 0101",
              from_number: "+1 202 555 0111",
              direction: "outbound",
              peer_kind: "human",
              placed_at: date(-120000),
              recording_mode: "off",
            },
            {
              id: "finished-call",
              status: "completed",
              to_number: "+1 202 555 0102",
              direction: "outbound",
              peer_kind: "realtime",
              placed_at: date(-180000),
              ended_at: date(-60000),
              recording_mode: "off",
            },
          ],
        },
      });
    if (!u.pathname.endsWith("/audio-health"))
      return route.fulfill({ json: { recordings: [], numbers: [] } });
    const id = u.searchParams.get("call_id"),
      active = id !== "finished-call";
    const sip = (i: number) => ({
      id: String(i),
      at: date(-30000 + i * 3000),
      sip: {
        signaling: { codec: "G.722", bye_failures: 0 },
        rtp: {
          packets_received: 1900 + i * 140,
          packets_sent: 1800 + i * 145,
          sequence_loss: 0,
          out_of_order: 1,
          jitter_ms: 8 + (i % 4),
          max_gap_ms: 81,
          max_send_gap_ms: 24,
          socket_receive_drops: 0,
          socket_receive_drops_available: true,
          read_errors: 0,
          send_errors: 0,
          decode_errors: 0,
        },
        jitter_buffer: {
          queue_ms: 60 + (i % 3) * 10,
          late_discarded_ms: 0,
          expired_discarded_ms: 20,
          overflow_discarded_ms: 0,
          concealed_ms: 40,
          clock_drift_ppm: 25,
          fresh_delivery_rebuffers: 1,
          events: [
            {
              occurred_at: date(-12000),
              reason: "fresh_delivery_rebuffer",
              duration_ms: 20,
            },
          ],
        },
      },
    });
    const browser = active
      ? {
          received_at: date(),
          media_transport: "webrtc",
          rtt_ms: 38,
          webrtc: {
            candidateType: "relay",
            protocol: "udp",
            jitterMs: 9,
            packetsLost: 2,
            concealedMs: 60,
            sendBitrateBps: 32000,
            receiveBitrateBps: 34000,
            jitterBufferIntervalMs: 42,
          },
          session_events: [
            {
              timestamp: date(-16000),
              action: "ice_restart",
              outcome: "media resumed",
            },
          ],
          transport_samples: Array.from({ length: 11 }, (_, i) => ({
            id: String(i),
            timestamp: date(-30000 + i * 3000),
            metrics: {
              rtt_ms: 32 + (i % 4) * 8,
              receiver_jitter_buffer_interval_ms: 40 + (i % 3) * 5,
            },
          })),
        }
      : {};
    return route.fulfill({
      json: {
        call: {
          call_id: id,
          status: active ? "in-progress" : "completed",
          active,
          peer_kind: active ? "human" : "realtime",
          provider: "telnyx",
          ended_at: active ? "" : date(-60000),
        },
        generated_at: date(),
        browser,
        carrier: {
          updated_at: date(),
          agent_audio: active
            ? {}
            : {
                reply_timing: {
                  recent: [
                    {
                      speech_to_core_audio_ms: 870,
                      core_audio_to_carrier_write_ms: 12,
                      first_core_audio_received_at: date(-90000),
                      first_carrier_write_at: date(-89988),
                    },
                  ],
                },
              },
        },
        carrier_live: sip(10),
        ...(u.searchParams.get("history") === "incremental"
          ? {
              carrier_history: {
                records: Array.from({ length: 11 }, (_, i) => sip(i)),
                next_cursor: "end",
              },
            }
          : {}),
      },
    });
  });
  return { requests, mutations };
}
test("select live and finished calls, expand, inspect and export without touching audio", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const { requests, mutations } = await fixture(page);
  await page.goto("/");
  await page.getByRole("button", { name: /in-progress.*202 555 0101/ }).click();
  const inspector = page.getByRole("region", { name: "Call diagnostics" });
  await expect(
    inspector.getByRole("heading", { name: "Call diagnostics" }),
  ).toBeVisible();
  await expect(inspector.getByText("38 ms", { exact: true })).toBeVisible();
  expect(
    new URL(requests.find((r) => r.includes("audio-health"))!).searchParams.get(
      "history",
    ),
  ).toBe("none");
  await inspector.getByRole("button", { name: "Expand", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog
    .getByRole("button", { name: "SIP / carrier", exact: true })
    .click();
  await expect(dialog.getByText("81 ms", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Timeline", exact: true }).click();
  await expect(
    dialog.getByText("fresh delivery rebuffer", { exact: true }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "Overview", exact: true }).click();
  await page.screenshot({
    path: "/private/tmp/telephony-call-inspector-expanded.png",
  });
  const downloaded = page.waitForEvent("download");
  await dialog.getByRole("button", { name: "Export", exact: true }).click();
  expect((await downloaded).suggestedFilename()).toBe(
    "call-live-call-diagnostics.json",
  );
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await page.getByRole("button", { name: /completed.*202 555 0102/ }).click();
  await expect(
    inspector.getByText("Finished call · completed", { exact: true }),
  ).toBeVisible();
  await inspector.getByRole("button", { name: "Browser", exact: true }).click();
  await expect(
    inspector.getByText("This AI call has no adviser browser leg.", {
      exact: false,
    }),
  ).toBeVisible();
  await inspector
    .getByRole("button", { name: "AI / Core", exact: true })
    .click();
  await expect(inspector.getByText("870 ms", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
  expect(mutations).toEqual([]);
});
test("narrow dark/light views remain readable and expanded dialog fits", async ({
  page,
}) => {
  await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?standalone");
  await expect(page.getByText("38 ms", { exact: true })).toBeVisible();
  for (const mode of ["dark", "light"]) {
    await page.evaluate(
      (mode) => (document.documentElement.dataset.mode = mode),
      mode,
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `/private/tmp/telephony-call-inspector-${mode}.png`,
      fullPage: true,
    });
  }
  await page.getByRole("button", { name: "Expand", exact: true }).click();
  const box = await page.getByRole("dialog").boundingBox();
  expect(box!.width).toBeLessThanOrEqual(390);
  expect(box!.height).toBeLessThanOrEqual(844);
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Expand", exact: true }),
  ).toBeFocused();
});
test("access revocation clears raw evidence, shows error and does not loop", async ({
  page,
}) => {
  const { requests } = await fixture(page);
  await page.goto("/?standalone");
  await expect(page.getByText("38 ms", { exact: true })).toBeVisible();
  await expect
    .poll(() => requests.some((r) => r.includes("history=incremental")))
    .toBe(true);
  let denied = 0;
  await page.route("**/audio-health?**", (route) => {
    denied++;
    return route.fulfill({ status: 403, json: {} });
  });
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Operator access");
  await expect(page.getByText("38 ms", { exact: true })).toHaveCount(0);
  await page.waitForTimeout(3500);
  expect(denied).toBe(1);
});
