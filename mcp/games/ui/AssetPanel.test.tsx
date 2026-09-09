import { afterEach, expect, test } from "bun:test";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { AssetPanel, SpritePreview } from "./AssetViews";
import {
  contentHash,
  fetchLockedContent,
  validateManifest,
} from "../client/content";
import { exportContent } from "../client/export-content";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
const original = globalThis.fetch;
afterEach(() => {
  cleanup();
  globalThis.fetch = original;
});
const reply = (data: any) => new Response(JSON.stringify({ data }));
test("asset editor exposes all thirteen kinds without generating on save", async () => {
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const action = String(input).split("/").pop()!.split("?")[0];
    return reply(
      action === "assets"
        ? { assets: [], has_more: false }
        : action === "content"
          ? { manifests: [], heads: {} }
          : [],
    );
  }) as typeof fetch;
  render(<AssetPanel projectId="p" gameId="g" />);
  await waitFor(() => expect(screen.getByText("Create an asset")).toBeTruthy());
  const kind = screen.getByLabelText("Kind") as HTMLSelectElement;
  expect(Array.from(kind.options).map((o) => o.value)).toEqual([
    "sprite",
    "spriteset",
    "rig",
    "tileset",
    "style",
    "material",
    "font",
    "sfx",
    "music",
    "stream",
    "locale",
    "table",
    "blob",
  ]);
  fireEvent.change(kind, { target: { value: "locale" } });
  expect(
    JSON.parse(
      (screen.getByLabelText("Asset specification") as HTMLTextAreaElement)
        .value,
    ).locale.language,
  ).toBe("en");
  expect(screen.queryByText("Generate candidate with Media Studio")).toBeNull();
});
test("stale asset selection cannot replace the current editor", async () => {
  let resolveSlow: (v: Response) => void = () => {};
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const action = String(input).split("/").pop()!.split("?")[0];
    if (action === "assets")
      return reply({
        assets: [
          { id: "a", name: "A", kind: "sprite", head: "a" },
          { id: "b", name: "B", kind: "sprite", head: "b" },
        ],
        has_more: false,
      });
    if (action === "content") return reply({ manifests: [], heads: {} });
    if (action === "version") {
      const id = JSON.parse(String(init?.body)).version_id;
      if (id === "a")
        return new Promise<Response>((r) => {
          resolveSlow = r;
        });
      return reply({
        id: "b",
        asset_id: "b",
        name: "B",
        kind: "sprite",
        spec: { license: "owned" },
      });
    }
    return reply([]);
  }) as typeof fetch;
  render(<AssetPanel projectId="p" gameId="g" />);
  await waitFor(() => expect(screen.getByText("A")).toBeTruthy());
  fireEvent.click(screen.getByText("A"));
  fireEvent.click(screen.getByText("B"));
  await waitFor(() =>
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("B"),
  );
  resolveSlow(
    reply({
      id: "a",
      asset_id: "a",
      name: "A",
      kind: "sprite",
      spec: { license: "owned" },
    }),
  );
  await new Promise((r) => setTimeout(r, 20));
  expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("B");
});
test("sprite preview uses frame geometry", () => {
  render(
    <SpritePreview
      url="https://example.test/atlas.png"
      frames={[
        {
          name: "idle",
          x: 2,
          y: 3,
          width: 16,
          height: 24,
          pivot_x: 0.5,
          pivot_y: 1,
          duration_ms: 120,
        },
      ]}
      animations={[]}
    />,
  );
  const frame = screen.getByRole("img", { name: "Frame idle" });
  expect(frame.style.width).toBe("16px");
  expect(frame.style.height).toBe("24px");
  expect(screen.getByText(/120 ms/)).toBeTruthy();
});
async function fixture() {
  const file = new TextEncoder().encode("retained bytes");
  const sha = await contentHash(file);
  const manifest = {
    schema: "apteva.games.content/v1",
    game: "g",
    assets: [
      {
        asset: "art",
        version: "v",
        target: "generic",
        engine_version: "1",
        platform: "web",
        files: [
          {
            path: `art/${sha}.bin`,
            sha256: sha,
            size: file.length,
            mime: "application/octet-stream",
          },
        ],
      },
    ],
    dependencies: [],
  };
  const bytes = new TextEncoder().encode(JSON.stringify(manifest));
  return { file, manifest, bytes, digest: await contentHash(bytes) };
}
test("runtime verifies cached content and works offline with exact versions", async () => {
  const f = await fixture();
  const store = new Map<string, Uint8Array>();
  let calls = 0;
  const opts = {
    baseURL: "https://example.test/api/apps/games",
    game: "g",
    digest: f.digest,
    token: async () => "player-token",
    cache: {
      get: async (k: string) => store.get(k),
      put: async (k: string, b: Uint8Array) => {
        store.set(k, b);
      },
    },
    fetcher: (async (input: RequestInfo | URL) => {
      calls++;
      return new Response(String(input).includes("/blobs/") ? f.file : f.bytes);
    }) as typeof fetch,
  };
  const session = await fetchLockedContent(opts);
  expect(await session.file(f.manifest.assets[0].files[0])).toEqual(f.file);
  const offline = await fetchLockedContent({ ...opts, offline: true });
  expect(await offline.file(f.manifest.assets[0].files[0])).toEqual(f.file);
  expect(calls).toBe(2);
  for (const k of store.keys())
    if (k.endsWith(f.digest)) store.set(k, new Uint8Array([1]));
  await expect(fetchLockedContent({ ...opts, offline: true })).rejects.toThrow(
    "integrity",
  );
});
test("exporter verifies files and refuses to overwrite", async () => {
  const f = await fixture();
  const dir = await mkdtemp(join(tmpdir(), "games-export-test-"));
  try {
    const opts = {
      baseURL: "https://example.test/api/apps/games",
      project: "p",
      game: "g",
      digest: f.digest,
      out: join(dir, "content"),
      token: "test",
      fetcher: (async (input: RequestInfo | URL) =>
        new Response(
          String(input).includes("/blobs/") ? f.file : f.bytes,
        )) as typeof fetch,
    };
    await exportContent(opts);
    expect(
      JSON.parse(await readFile(join(opts.out, "content.lock.json"), "utf8"))
        .digest,
    ).toBe(f.digest);
    await expect(exportContent(opts)).rejects.toThrow("already exists");
    await expect(
      exportContent({
        ...opts,
        out: join(dir, "bad"),
        fetcher: (async (input: RequestInfo | URL) =>
          new Response(
            String(input).includes("/blobs/") ? new Uint8Array([1]) : f.bytes,
          )) as typeof fetch,
      }),
    ).rejects.toThrow("integrity");
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("content manifests require the exact logo for each build target", () => {
  const version = "a".repeat(64);
  const asset = {
    asset: "logo",
    version,
    target: "generic",
    engine_version: "1",
    platform: "desktop",
    files: [],
  };
  const manifest = {
    schema: "apteva.games.content/v1",
    game: "g",
    logo: { asset: "logo", version },
    assets: [asset],
  };
  expect(() => validateManifest(manifest, "g")).not.toThrow();
  expect(() =>
    validateManifest(
      { ...manifest, assets: [{ ...asset, version: "b".repeat(64) }] },
      "g",
    ),
  ).toThrow("logo");
  expect(() =>
    validateManifest(
      {
        ...manifest,
        assets: [asset, { ...asset, asset: "hero", target: "godot" }],
      },
      "g",
    ),
  ).toThrow("logo");
  expect(() =>
    validateManifest({ ...manifest, logo: undefined, assets: [] }, "g"),
  ).not.toThrow();
});

test("a stale logo change reports conflict without changing the displayed logo", async () => {
  let changed = false;
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const action = String(input).split("/").pop()!.split("?")[0];
    if (action === "logo")
      return new Response(
        JSON.stringify({ error: "Game logo changed; reload before saving" }),
        { status: 400 },
      );
    return reply(
      action === "assets"
        ? { assets: [], has_more: false }
        : action === "content"
          ? { manifests: [], heads: {} }
          : [],
    );
  }) as typeof fetch;
  render(
    <AssetPanel
      projectId="p"
      gameId="g"
      logoVersionId="current"
      onLogoChanged={() => {
        changed = true;
      }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Clear game logo" }));
  await waitFor(() =>
    expect(screen.getByRole("alert").textContent).toContain("reload"),
  );
  expect(changed).toBe(false);
  expect(screen.getByRole("button", { name: "Clear game logo" })).toBeTruthy();
});
