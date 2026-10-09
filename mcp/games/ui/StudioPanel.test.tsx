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
test("Google Play reporting maps a package and selects an exact report month", async () => {
  const requests: Record<string, unknown>[] = [];
  let connected = false;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://local").pathname;
    if (path.endsWith("/sources") || path.endsWith("/targets"))
      return reply([]);
    if (path.endsWith("/metric_sources"))
      return reply(
        connected
          ? [
              {
                id: "play-sales",
                provider: "google-play-developer",
                family: "earnings",
                external_id: "com.example.game",
              },
            ]
          : [],
      );
    if (path.endsWith("/discovery"))
      return reply([{ connection_id: 10, provider: "google-play-developer" }]);
    if (path.endsWith("/metric_source_set")) {
      requests.push(JSON.parse(String(init?.body)));
      connected = true;
      return reply({ id: "play-sales" });
    }
    if (path.endsWith("/metrics_sync")) {
      requests.push(JSON.parse(String(init?.body)));
      return reply({ months: 1 });
    }
    return reply({});
  }) as typeof fetch;
  render(<StudioPanel projectId="p" gameId="a" view="metrics" />);
  fireEvent.click(screen.getByText("Discover reporting connections"));
  await waitFor(() =>
    expect(screen.getByText("google-play-developer · #10")).toBeTruthy(),
  );
  fireEvent.change(screen.getByLabelText("Reporting connection"), {
    target: { value: "10" },
  });
  fireEvent.change(screen.getByLabelText("Android package ID"), {
    target: { value: "com.example.game" },
  });
  fireEvent.change(screen.getByLabelText("Google Play report family"), {
    target: { value: "earnings" },
  });
  fireEvent.click(screen.getByText("Connect reporting source"));
  await waitFor(() => expect(requests.length).toBe(1));
  expect(requests[0]).toMatchObject({
    provider: "google-play-developer",
    external_id: "com.example.game",
    family: "earnings",
  });
  await waitFor(() =>
    expect(
      screen.getByLabelText("Exact report month (optional, YYYYMM)"),
    ).toBeTruthy(),
  );
  fireEvent.change(
    screen.getByLabelText("Exact report month (optional, YYYYMM)"),
    { target: { value: "202608" } },
  );
  fireEvent.click(screen.getByText("Refresh reports"));
  await waitFor(() => expect(requests.length).toBe(2));
  expect(requests[1]).toMatchObject({
    source_id: "play-sales",
    month: "202608",
  });
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

test("guided setup creates a reviewed association without pipeline JSON", async () => {
  let created: Record<string, any> | undefined;
  const options = {
    recipes: [
      {
        id: "kiln-ios",
        version: "1",
        name: "Kiln iOS",
        platform: "ios",
        os: "darwin",
        prepare: [],
        tests: [{ name: "ios-artifact" }],
        outputs: ["Moonhorde.ipa"],
      },
    ],
    repositories: [
      { id: 1, slug: "moonhorde", name: "Moonhorde" },
      { id: 2, slug: "kiln", name: "Kiln" },
    ],
    deployments: [],
    runners: [],
    issues: [],
  };
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://local").pathname;
    if (path.endsWith("setup_options")) return reply(options);
    if (path.endsWith("source_pin"))
      return reply({
        snapshot_id: "engine-pin",
        source_revision: "engine-revision",
        expires_at: "2026-10-10T10:00:00Z",
      });
    if (path.endsWith("/setup")) {
      created = JSON.parse(String(init?.body));
      return reply({
        status: "complete",
        stage: "association",
        results: {
          source: { id: 1 },
          target: { name: "moonhorde-ios", environment: "production" },
        },
      });
    }
    return reply([]);
  }) as typeof fetch;
  render(<StudioPanel projectId="p" gameId="moon" view="source" />);
  fireEvent.click(screen.getByText("Set up deployment"));
  await waitFor(() =>
    expect(screen.getByText("Moonhorde (moonhorde)")).toBeTruthy(),
  );
  fireEvent.change(screen.getByLabelText("Setup repository"), {
    target: { value: "moonhorde" },
  });
  fireEvent.change(screen.getByLabelText("Deployment name"), {
    target: { value: "moonhorde-ios" },
  });
  fireEvent.change(screen.getByLabelText("Capsule runner URL"), {
    target: { value: "https://runner.example" },
  });
  fireEvent.change(screen.getByLabelText("Bundle ID"), {
    target: { value: "com.moonhorde.game" },
  });
  fireEvent.change(screen.getByLabelText("Xcode scheme"), {
    target: { value: "Moonhorde" },
  });
  // The setup and existing-target forms both expose dependencies; select the setup form.
  fireEvent.change(screen.getAllByLabelText("Dependency repository")[0], {
    target: { value: "kiln" },
  });
  fireEvent.click(screen.getAllByText("Capture dependency pin")[0]);
  await waitFor(() =>
    expect(screen.getByText("Use pinned dependency")).toBeTruthy(),
  );
  fireEvent.click(screen.getByText("Use pinned dependency"));
  fireEvent.click(screen.getByText("Review association"));
  expect(created).toBeUndefined();
  fireEvent.click(screen.getByText("Confirm setup"));
  await waitFor(() => expect(created).toBeTruthy());
  expect(created!.setup).toMatchObject({
    repo_slug: "moonhorde",
    platform: "ios",
    recipe_id: "kiln-ios",
    runner_backend: "runner",
    dependencies: [{ slug: "kiln", path: "engine", snapshot_id: "engine-pin" }],
  });
  expect(created!.setup.target_config_json).toBeUndefined();
  expect(
    screen.getByText("Target linked: moonhorde-ios · production"),
  ).toBeTruthy();
});

test("readiness sends the selected build and channel and separates evidence", async () => {
  const requests: Record<string, any>[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://local").pathname;
    if (path.endsWith("targets"))
      return reply([
        {
          id: "20:production",
          name: "Moonhorde",
          platform: "ios",
          environment: "production",
        },
      ]);
    if (path.endsWith("release_status"))
      return reply({
        builds: [
          {
            id: 42,
            status: "succeeded",
            artifact_download_url: "/artifacts/42",
            artifact_manifest_json: JSON.stringify({
              pipeline: {
                tests: ["ios-artifact"],
                completed_at: "2026-10-09T10:00:00Z",
                artifact_sha256: "digest",
              },
            }),
          },
        ],
        releases: [],
      });
    if (path.endsWith("release_plan")) {
      const a = JSON.parse(String(init?.body));
      requests.push(a);
      return reply({
        readiness: {
          build_id: a.build_id,
          channel: a.channel,
          checked_at: "2026-10-09T10:30:00Z",
          checks: [
            {
              id: "tests",
              name: "Artifact tests",
              kind: "evidence",
              status: "ready",
              reason: "Selected build passed",
              action: "Inspect logs",
              evidence_at: "2026-10-09T10:00:00Z",
            },
          ],
        },
      });
    }
    return reply([]);
  }) as typeof fetch;
  render(<StudioPanel projectId="p" gameId="moon" view="releases" />);
  await waitFor(() => expect(screen.getByText("#42 · succeeded")).toBeTruthy());
  fireEvent.change(screen.getByLabelText("Build"), { target: { value: "42" } });
  fireEvent.click(screen.getByText("Check readiness"));
  await waitFor(() =>
    expect(screen.getByLabelText("Deployment readiness")).toBeTruthy(),
  );
  expect(requests[0]).toMatchObject({
    target_id: "20:production",
    build_id: 42,
    channel: "internal",
  });
  expect(
    screen.getByText(/Build evidence · Selected build passed/),
  ).toBeTruthy();
  expect(
    screen.getByText("Download build #42 artifacts").getAttribute("href"),
  ).toBe("/artifacts/42");
  fireEvent.change(screen.getByLabelText("Release channel"), {
    target: { value: "production" },
  });
  expect(screen.queryByLabelText("Deployment readiness")).toBeNull();
});

test("partial setup edits reuse the completed repository and deployment", async () => {
  let submitted: Record<string, any> | undefined;
  const input = {
    repo_slug: "moonhorde",
    create_repo: true,
    deployment_name: "moonhorde-ios",
    deployment_id: 0,
    platform: "ios",
    environment: "production",
  };
  globalThis.fetch = (async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(url), "http://local").pathname;
    if (path.endsWith("setup_options"))
      return reply({
        repositories: [{ slug: "moonhorde", name: "Moonhorde" }],
        deployments: [
          {
            id: 7,
            name: "moonhorde-ios",
            target_kind: "ios",
            environments: [{ name: "production" }],
          },
        ],
        recipes: [],
        runners: [],
        issues: [],
      });
    if (path.endsWith("setup_history"))
      return reply([
        {
          request_key: "original",
          input,
          status: "blocked",
          stage: "association",
          results: {
            repository: { repository: { slug: "moonhorde" } },
            deployment: { deployment: { id: 7 } },
          },
        },
      ]);
    if (path.endsWith("/setup")) {
      submitted = JSON.parse(String(init?.body));
      return reply({ status: "complete", results: {} });
    }
    return reply([]);
  }) as typeof fetch;
  render(<StudioPanel projectId="p" gameId="moon" view="source" />);
  await waitFor(() =>
    expect(screen.getByText("Open saved setup")).toBeTruthy(),
  );
  fireEvent.click(screen.getByText("Open saved setup"));
  fireEvent.click(
    screen.getByText("Edit remaining setup using completed resources"),
  );
  fireEvent.click(screen.getByText("Review association"));
  fireEvent.click(screen.getByText("Confirm setup"));
  await waitFor(() => expect(submitted).toBeTruthy());
  expect(submitted!.request_key).not.toBe("original");
  expect(submitted!.setup).toMatchObject({
    repo_slug: "moonhorde",
    create_repo: false,
    deployment_id: 7,
  });
});
