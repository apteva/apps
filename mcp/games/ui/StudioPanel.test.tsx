import { afterEach, expect, test } from "bun:test";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { StudioPanel, Portfolio, formatMicros } from "./StudioViews";
import { GameTelemetry, type PlayEvent } from "../client/telemetry";
const oldFetch = globalThis.fetch;
afterEach(() => {
  cleanup();
  globalThis.fetch = oldFetch;
});
const reply = (data: unknown, status = 200) =>
  new Response(JSON.stringify(status === 200 ? { data } : { error: data }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
test("source discovery failure is actionable and cannot create a build", async () => {
  const calls: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://local").pathname;
    calls.push(path);
    if (path.endsWith("discovery"))
      return reply("connect the code app in Games settings", 400);
    return reply([]);
  }) as typeof fetch;
  render(<StudioPanel projectId="p" gameId="a" view="source" />);
  fireEvent.click(screen.getByText("Discover repositories and deployments"));
  await waitFor(() =>
    expect(screen.getByRole("alert").textContent).toContain("connect the code"),
  );
  expect(calls.some((x) => x.endsWith("build"))).toBe(false);
});
test("uncertain build dispatch surfaces reconciliation and runs once", async () => {
  let builds = 0;
  const target = {
    id: "2:production",
    name: "Moon",
    platform: "android",
    environment: "production",
  };
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://local").pathname;
    if (path.endsWith("/sources")) return reply([]);
    if (path.endsWith("/targets")) return reply([target]);
    if (path.endsWith("/release_status"))
      return reply({
        builds: [],
        releases: [
          { id: 13, status: "pending", availability_status: "unconfirmed" },
        ],
      });
    if (path.endsWith("/history"))
      return reply(
        builds
          ? [
              {
                target_id: target.id,
                request_key: "one",
                action: "build",
                status: "unknown",
                error: "Lost connection",
              },
            ]
          : [],
      );
    if (path.endsWith("/build")) {
      builds++;
      expect(JSON.parse(String(init?.body)).target_id).toBe(target.id);
      return reply({ status: "unknown" });
    }
    return reply({});
  }) as typeof fetch;
  render(<StudioPanel projectId="p" gameId="a" view="releases" />);
  await waitFor(() => expect(screen.getByText("Build game")).toBeTruthy());
  fireEvent.click(screen.getByText("Build game"));
  await waitFor(() =>
    expect(screen.getByRole("status").textContent).toContain(
      "Outcome uncertain",
    ),
  );
  expect(builds).toBe(1);
  expect(screen.getByText("Reconcile request")).toBeTruthy();
  expect(screen.getByText(/unconfirmed/)).toBeTruthy();
});
test("portfolio is paginated and does not call reporting providers", async () => {
  const paths: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    paths.push(String(input));
    return reply({
      games: [
        {
          game: { id: "a", name: "Moon", status: "active" },
          sources: [],
          targets: [],
          metric_sources: [],
          deliveries: [],
        },
      ],
      total: 40,
    });
  }) as typeof fetch;
  render(<Portfolio projectId="p" />);
  await waitFor(() => expect(screen.getByText("Moon")).toBeTruthy());
  fireEvent.click(screen.getByText("Next games"));
  await waitFor(() => expect(paths.length).toBe(2));
  expect(paths.every((p) => p.includes("/portfolio"))).toBe(true);
});
test("monetary micros retain precision beyond JavaScript safe integers", () => {
  expect(formatMicros("9007199254740993")).toBe("9007199254.740993");
  expect(formatMicros("-1250000")).toBe("-1.250000");
  expect(formatMicros("bad")).toBe("Unavailable");
});
const event = (id: string): PlayEvent => ({
  id,
  name: "run_completed",
  session_id: "s",
  release: "1.0",
  environment: "test",
  time: new Date().toISOString(),
  props: { score: 10 },
});
test("offline client retries unchanged event IDs and bounds its queue", async () => {
  const batches: PlayEvent[][] = [];
  let fail = true;
  const client = new GameTelemetry({
    endpoint: "/events",
    token: async () => "guest-token",
    enabled: true,
    request: (async (_input: RequestInfo | URL, init?: RequestInit) => {
      batches.push(JSON.parse(String(init?.body)).events);
      if (fail) throw new Error("offline");
      return new Response(JSON.stringify({ accepted: 50 }));
    }) as typeof fetch,
  });
  for (let i = 0; i < 501; i++) client.track(event(String(i)));
  expect(client.pending).toBe(500);
  await expect(client.flush()).rejects.toThrow("offline");
  expect(client.pending).toBe(500);
  fail = false;
  await client.flush();
  expect(client.pending).toBe(450);
  expect(batches[0]).toEqual(batches[1]);
});
test("disabled telemetry sends no request and stores no new events", async () => {
  const client = new GameTelemetry({
    endpoint: "/events",
    token: async () => {
      throw new Error("must not ask for a token");
    },
    enabled: false,
  });
  client.track(event("x"));
  await client.flush();
  expect(client.pending).toBe(0);
});
