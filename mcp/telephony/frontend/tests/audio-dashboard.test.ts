import { expect, test } from "bun:test";
import {
  audioDashboardURL,
  audioDashboardLink,
  initialAudioFilters,
  defaultAudioFilters,
  audioWidgetPreferences,
  audioStateLabel,
} from "../../ui/audio-dashboard";
const host = { appName: "telephony", projectId: "project a", installId: 42 };
test("monitor scopes project/install and preserves exact identity and stage filters", () => {
  const identity = JSON.stringify({
    issuer_app: "auth",
    issuer_install_id: "11",
    subject_id: "1",
  });
  const url = new URL(
    audioDashboardURL(
      host,
      {
        ...defaultAudioFilters,
        adviser: identity,
        stage: "browser_to_telephony",
        issue: "reconnect",
        provider: "twilio",
      },
      "",
      100,
      Date.parse("2026-10-06T12:00:00Z"),
    ),
    "https://example.test",
  );
  expect(url.pathname).toBe("/api/apps/telephony/audio-health");
  expect(url.searchParams.get("project_id")).toBe("project a");
  expect(url.searchParams.get("install_id")).toBe("42");
  expect(url.searchParams.get("adviser")).toBe(identity);
  expect(url.searchParams.get("from")).toBe("2026-10-05T12:00:00.000Z");
  expect(url.searchParams.get("stage")).toBe("browser_to_telephony");
});
test("widget links directly to matching monitor filters and expands selected report", () => {
  const href = audioDashboardLink(
    host,
    { range: "7d", provider: "telnyx", state: "issues", search: "call1" },
    "call1",
  );
  const q = new URL(href, "https://example.test").searchParams;
  expect(q.get("tab")).toBe("audio-health");
  expect(q.get("audio_call_id")).toBe("call1");
  expect(initialAudioFilters(q.toString())).toEqual({
    ...defaultAudioFilters,
    range: "7d",
    provider: "telnyx",
    state: "issues",
    search: "call1",
  });
});
test("invalid windows and unsupported deep-link values cannot create broad queries", () => {
  expect(() =>
    audioDashboardURL(host, {
      ...defaultAudioFilters,
      range: "custom",
      from: "invalid",
      until: "invalid",
    }),
  ).toThrow();
  expect(() =>
    audioDashboardURL(host, {
      ...defaultAudioFilters,
      range: "custom",
      from: "2026-01-01",
      until: "2026-03-01",
    }),
  ).toThrow();
  expect(
    initialAudioFilters("range=all&stage=wrong&issue=wrong&state=wrong"),
  ).toEqual(defaultAudioFilters);
});
test("widget settings bounded and no-telemetry labels do not claim healthy audio", () => {
  expect(audioWidgetPreferences({ time_range: "bad", max_calls: 999 })).toEqual(
    { range: "24h", maxCalls: 12, provider: "" },
  );
  expect(audioWidgetPreferences({ max_calls: 1 }).maxCalls).toBe(3);
  expect(audioStateLabel("unobserved")).toBe("No telemetry");
  expect(audioStateLabel("stale")).toBe("Telemetry stale");
});
