import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import GamesPanel from "./GamesPanel";

const games = [
  { id: "a", name: "Racer", slug: "racer", status: "active", legacy: false },
  { id: "b", name: "Puzzle", slug: "puzzle", status: "active", legacy: false },
];
const player = (name: string) => ({
  id: 1,
  auth_user_id: 10,
  display_name: name,
  status: "active",
  kind: "guest",
  login_count: 1,
  metadata: {},
});
let requests: URL[];
let event: (ev: unknown) => void;
let override: ((url: URL) => Promise<Response> | undefined) | undefined;
const originalFetch = globalThis.fetch;
const response = (value: unknown) =>
  new Response(JSON.stringify(value), {
    headers: { "Content-Type": "application/json" },
  });

beforeEach(() => {
  requests = [];
  override = undefined;
  (window as unknown as { __aptevaAppEvents: unknown }).__aptevaAppEvents = {
    subscribe: (
      _app: string,
      _project: string,
      callback: (ev: unknown) => void,
    ) => {
      event = callback;
      return () => {
        event = () => {};
      };
    },
  };
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    requests.push(url);
    const custom = override?.(url);
    if (custom) return custom;
    if (url.pathname === "/api/apps/games/admin/games")
      return response({ games });
    if (url.pathname.endsWith("/stats"))
      return response({
        players: { total: 60, active_24h: 2, banned: 0 },
        stat_defs: 0,
        leaderboards: 0,
        achievements: 0,
      });
    const name =
      url.searchParams.get("game_id") === "a" ? "Alpha User" : "Beta User";
    if (url.pathname.endsWith("/players"))
      return response({ players: [player(name)], total: 60 });
    if (url.pathname.endsWith("/players/1"))
      return response({
        player: player(name),
        active_ban: null,
        bans: [],
        stats: [],
        data: [],
        achievements: [],
        audit: [],
      });
    if (url.pathname.endsWith("/leaderboards"))
      return response({ leaderboards: [] });
    return response({});
  }) as typeof fetch;
});
afterEach(() => {
  cleanup();
  globalThis.fetch = originalFetch;
});
async function openGame(index = 0) {
  render(<GamesPanel projectId="project" appName="games" installId={1} />);
  await waitFor(() =>
    expect(screen.getAllByRole("button", { name: "Open game" })).toHaveLength(
      2,
    ),
  );
  fireEvent.click(screen.getAllByRole("button", { name: "Open game" })[index]);
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: /Alpha User|Beta User/ }),
    ).toBeTruthy(),
  );
}

describe("Games panel", () => {
  test("selects games explicitly and resets player state on game switching", async () => {
    await openGame();
    fireEvent.click(screen.getByRole("button", { name: /Alpha User/ }));
    await waitFor(() =>
      expect(screen.getByDisplayValue("Alpha User")).toBeTruthy(),
    );
    fireEvent.click(screen.getByRole("button", { name: "All games" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Open game" })[1]);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Beta User/ })).toBeTruthy(),
    );
    expect(screen.queryByDisplayValue("Alpha User")).toBeNull();
    expect(
      requests
        .filter((u) => u.pathname.endsWith("/players"))
        .map((u) => u.searchParams.get("game_id")),
    ).toEqual(["a", "b"]);
  });

  test("loads the next page rather than trapping users in the first 50 players", async () => {
    await openGame();
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() =>
      expect(
        requests.some(
          (u) =>
            u.pathname.endsWith("/players") &&
            u.searchParams.get("offset") === "50",
        ),
      ).toBe(true),
    );
  });

  test("ignores a slow old search response", async () => {
    await openGame();
    let resolveOld: (response: Response) => void = () => {};
    override = (url) => {
      if (url.searchParams.get("q") === "old")
        return new Promise((resolve) => {
          resolveOld = resolve;
        });
      if (url.searchParams.get("q") === "new")
        return Promise.resolve(
          response({ players: [player("New result")], total: 1 }),
        );
    };
    fireEvent.change(screen.getByPlaceholderText("Search name or id"), {
      target: { value: "old" },
    });
    await waitFor(() =>
      expect(requests.some((u) => u.searchParams.get("q") === "old")).toBe(
        true,
      ),
    );
    fireEvent.change(screen.getByPlaceholderText("Search name or id"), {
      target: { value: "new" },
    });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /New result/ })).toBeTruthy(),
    );
    await act(async () => {
      resolveOld(response({ players: [player("Stale result")], total: 1 }));
    });
    expect(screen.queryByRole("button", { name: /Stale result/ })).toBeNull();
  });

  test("preserves an edited name during an event refresh", async () => {
    await openGame();
    fireEvent.click(screen.getByRole("button", { name: /Alpha User/ }));
    await waitFor(() =>
      expect(screen.getByDisplayValue("Alpha User")).toBeTruthy(),
    );
    fireEvent.change(screen.getByDisplayValue("Alpha User"), {
      target: { value: "Unsaved name" },
    });
    const before = requests.filter((u) =>
      u.pathname.endsWith("/players/1"),
    ).length;
    act(() => event({ seq: 1, topic: "stat.updated", data: { game_id: "a" } }));
    await waitFor(() =>
      expect(
        requests.filter((u) => u.pathname.endsWith("/players/1")).length,
      ).toBeGreaterThan(before),
    );
    expect(screen.getByDisplayValue("Unsaved name")).toBeTruthy();
  });

  test("refreshes during sustained events and ignores another game's events", async () => {
    await openGame();
    const before = requests.filter((u) => u.pathname.endsWith("/stats")).length;
    act(() => event({ seq: 1, topic: "stat.updated", data: { game_id: "b" } }));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 450));
    });
    expect(requests.filter((u) => u.pathname.endsWith("/stats")).length).toBe(
      before,
    );
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        event({ seq: i + 2, topic: "stat.updated", data: { game_id: "a" } });
        await new Promise((resolve) => setTimeout(resolve, 100));
      });
    }
    expect(
      requests.filter((u) => u.pathname.endsWith("/stats")).length,
    ).toBeGreaterThan(before);
  });
});

test("edits game title and description together", async () => {
  let saved: Record<string, unknown> | undefined;
  const baseFetch = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "PATCH") {
      saved = JSON.parse(String(init.body));
      return response({ game: { ...games[0], ...saved } });
    }
    return baseFetch(input, init);
  }) as typeof fetch;
  render(<GamesPanel projectId="project" appName="games" installId={1} />);
  await waitFor(() =>
    expect(screen.getAllByRole("button", { name: "Edit game" })).toHaveLength(
      2,
    ),
  );
  fireEvent.click(screen.getAllByRole("button", { name: "Edit game" })[0]);
  fireEvent.change(screen.getByLabelText("Game title"), {
    target: { value: "Racer Deluxe" },
  });
  fireEvent.change(screen.getByLabelText("Game description"), {
    target: { value: "Fast multiplayer racing." },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save game" }));
  await waitFor(() =>
    expect(saved).toEqual({
      name: "Racer Deluxe",
      slug: "racer",
      description: "Fast multiplayer racing.",
    }),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Create game" })).toBeTruthy(),
  );
});
