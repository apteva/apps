// bun run client/export-content.ts --digest <sha256> --out <new-directory>
// GAMES_APP_URL=https://host/api/apps/games GAMES_PROJECT_ID=... GAMES_GAME_ID=...
// GAMES_CONTENT_TOKEN is a server-side credential; never put it in a game build.
import { mkdir, lstat, rename, rm, writeFile } from "node:fs/promises";
import { resolve, dirname, join } from "node:path";
import {
  contentHash,
  validateManifest,
  verifyContent,
  type ContentManifest,
} from "./content";
export async function exportContent(opts: {
  baseURL: string;
  project: string;
  game: string;
  digest: string;
  out: string;
  token: string;
  fetcher?: typeof fetch;
}) {
  const { digest, project, game } = opts;
  if (!/^[a-f0-9]{64}$/.test(digest))
    throw new Error("An exact manifest digest is required");
  const url = new URL(opts.baseURL);
  if (
    url.protocol !== "https:" &&
    !(
      url.protocol === "http:" &&
      ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname)
    )
  )
    throw new Error("Use HTTPS for remote content");
  if (url.username || url.password || url.search || url.hash)
    throw new Error("Invalid app URL");
  const base = `${opts.baseURL.replace(/\/$/, "")}/admin/games/${encodeURIComponent(game)}/content/${digest}`;
  const fetcher = opts.fetcher || fetch;
  async function get(path: string, max: number): Promise<Uint8Array> {
    const response = await fetcher(path, {
      headers: { Authorization: `Bearer ${opts.token}` },
      redirect: "error",
    });
    if (!response.ok)
      throw new Error(`Content download failed (${response.status})`);
    const r = response.body?.getReader();
    if (!r) throw new Error("Empty response");
    const chunks: Uint8Array[] = [];
    let size = 0;
    try {
      while (true) {
        const { value, done } = await r.read();
        if (done) break;
        size += value.length;
        if (size > max) throw new Error("Oversized content response");
        chunks.push(value);
      }
    } finally {
      await r.cancel();
    }
    const b = new Uint8Array(size);
    let offset = 0;
    for (const c of chunks) {
      b.set(c, offset);
      offset += c.length;
    }
    return b;
  }
  const bytes = await get(
    `${base}?project_id=${encodeURIComponent(project)}&download=manifest`,
    2 * 1024 * 1024,
  );
  await verifyContent(bytes, digest);
  const manifest = JSON.parse(
    new TextDecoder().decode(bytes),
  ) as ContentManifest;
  validateManifest(manifest, game);
  const output = resolve(opts.out);
  await mkdir(dirname(output), { recursive: true });
  // Never overwrite a repository directory, symlink or a previous content export.
  try {
    await lstat(output);
    throw new Error(
      "Output already exists; use a new directory and review the content update in Code",
    );
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") throw e;
  }
  const stage = output + ".stage-" + crypto.randomUUID();
  await mkdir(stage);
  try {
    await writeFile(join(stage, "manifest.json"), bytes);
    await writeFile(join(stage, "bundle.gamescontent"), bytes);
    await writeFile(
      join(stage, "content.lock.json"),
      JSON.stringify({
        schema: manifest.schema,
        digest,
        manifest: "manifest.json",
      }),
    );
    let total = 0;
    const seen = new Set<string>();
    for (const asset of manifest.assets)
      for (const file of asset.files) {
        if (seen.has(file.path)) continue;
        seen.add(file.path);
        total += file.size;
        if (total > 128 * 1024 * 1024)
          throw new Error("Content release exceeds 128 MiB");
        const b = await get(
          `${base}/blobs/${file.sha256}?project_id=${encodeURIComponent(project)}`,
          file.size,
        );
        await verifyContent(b, file.sha256, file.size);
        const path = join(stage, file.path);
        await mkdir(dirname(path), { recursive: true });
        await writeFile(path, b);
      }
    await rename(stage, output);
  } catch (e) {
    await rm(stage, { recursive: true, force: true });
    throw e;
  }
  return { digest, output, manifest_sha256: await contentHash(bytes) };
}
if (import.meta.main) {
  const args = process.argv.slice(2);
  const arg = (name: string) => args[args.indexOf(name) + 1];
  const digest = args.includes("--digest") ? arg("--digest") : "";
  const out = args.includes("--out") ? arg("--out") : "";
  if (!out) throw new Error("--out is required");
  for (const key of [
    "GAMES_APP_URL",
    "GAMES_PROJECT_ID",
    "GAMES_GAME_ID",
    "GAMES_CONTENT_TOKEN",
  ])
    if (!process.env[key]) throw new Error(`${key} is required`);
  console.log(
    await exportContent({
      baseURL: process.env.GAMES_APP_URL!,
      project: process.env.GAMES_PROJECT_ID!,
      game: process.env.GAMES_GAME_ID!,
      token: process.env.GAMES_CONTENT_TOKEN!,
      digest,
      out,
    }),
  );
}
