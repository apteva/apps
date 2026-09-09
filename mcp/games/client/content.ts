// Shared runtime contract. A head is resolved once, then exact manifest/file
// digests are verified. Supply an engine-owned persistent cache for offline use.
export type ContentFile = {
  path: string;
  sha256: string;
  size: number;
  mime: string;
};
export type ContentManifest = {
  schema: string;
  game: string;
  logo?: { asset: string; version: string };
  assets: Array<{
    asset: string;
    version: string;
    target: string;
    engine_version: string;
    platform: string;
    files: ContentFile[];
  }>;
};
export interface ContentCache {
  get(key: string): Promise<Uint8Array | undefined>;
  put(key: string, bytes: Uint8Array): Promise<void>;
}
export async function contentHash(bytes: Uint8Array): Promise<string> {
  return Array.from(
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", new Uint8Array(bytes)),
    ),
  )
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}
export async function verifyContent(
  bytes: Uint8Array,
  digest: string,
  size?: number,
): Promise<void> {
  if (
    (size !== undefined && bytes.length !== size) ||
    (await contentHash(bytes)) !== digest
  )
    throw new Error("Content integrity check failed");
}
export function validateManifest(
  manifest: ContentManifest,
  game: string,
): void {
  if (
    manifest.schema !== "apteva.games.content/v1" ||
    manifest.game !== game ||
    !Array.isArray(manifest.assets) ||
    manifest.assets.length > 256
  )
    throw new Error("Invalid game content manifest");
  if (manifest.logo) {
    const logo = manifest.logo;
    if (
      !/^[a-z][a-z0-9_-]{0,63}$/.test(logo.asset) ||
      !/^[a-f0-9]{64}$/.test(logo.version) ||
      !manifest.assets.some(
        (a) => a.asset === logo.asset && a.version === logo.version,
      ) ||
      manifest.assets.some(
        (a) =>
          !manifest.assets.some(
            (l) =>
              l.asset === logo.asset &&
              l.version === logo.version &&
              l.target === a.target &&
              l.engine_version === a.engine_version &&
              l.platform === a.platform,
          ),
      )
    )
      throw new Error("Missing or invalid game logo rendition");
  }
  for (const a of manifest.assets) {
    if (!Array.isArray(a.files)) throw new Error("Missing files");
    for (const f of a.files) {
      if (
        !/^[a-z][a-z0-9_-]{0,63}\/[a-f0-9]{64}\.[a-z0-9]+$/.test(f.path) ||
        !/^([a-f0-9]{64})$/.test(f.sha256) ||
        !Number.isSafeInteger(f.size) ||
        f.size <= 0 ||
        f.size > 25 * 1024 * 1024
      )
        throw new Error("Invalid content file");
    }
  }
}
export async function fetchLockedContent(options: {
  baseURL: string;
  game: string;
  digest: string;
  token: () => Promise<string>;
  cache: ContentCache;
  offline?: boolean;
  fetcher?: typeof fetch;
}): Promise<{
  manifest: ContentManifest;
  file: (file: ContentFile) => Promise<Uint8Array>;
}> {
  const { baseURL, game, digest, token, cache, offline } = options;
  if (!/^[a-f0-9]{64}$/.test(digest))
    throw new Error("An exact manifest digest is required");
  const url = new URL(baseURL);
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
  const fetcher = options.fetcher || fetch;
  const endpoint = `${baseURL.replace(/\/$/, "")}/v2/games/${encodeURIComponent(game)}/content/${digest}`;
  async function read(
    key: string,
    url: string,
    hash: string,
    size?: number,
  ): Promise<Uint8Array> {
    let b = await cache.get(key);
    if (b) {
      await verifyContent(b, hash, size);
      return b;
    }
    if (offline) throw new Error("Content is not cached for offline use");
    const response = await fetcher(url, {
      headers: { Authorization: `Bearer ${await token()}` },
      redirect: "error",
    });
    if (!response.ok)
      throw new Error(`Content request failed (${response.status})`);
    const limit = size ?? 2 * 1024 * 1024;
    const reader = response.body?.getReader();
    if (!reader) throw new Error("Missing content body");
    const chunks: Uint8Array[] = [];
    let total = 0;
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        total += value.length;
        if (total > limit) throw new Error("Content exceeds declared size");
        chunks.push(value);
      }
    } finally {
      await reader.cancel();
    }
    b = new Uint8Array(total);
    let offset = 0;
    for (const c of chunks) {
      b.set(c, offset);
      offset += c.length;
    }
    await verifyContent(b, hash, size);
    await cache.put(key, b);
    return b;
  }
  const key = `games:${baseURL}:${game}:${digest}`;
  const manifestBytes = await read(
    key,
    endpoint + "?download=manifest",
    digest,
  );
  const manifest = JSON.parse(
    new TextDecoder().decode(manifestBytes),
  ) as ContentManifest;
  validateManifest(manifest, game);
  return {
    manifest,
    file: async (f) => {
      if (
        !manifest.assets.some((a) =>
          a.files.some(
            (allowed) =>
              allowed.path === f.path &&
              allowed.sha256 === f.sha256 &&
              allowed.size === f.size,
          ),
        )
      )
        throw new Error("File is not in the locked manifest");
      return read(
        `${key}:${f.sha256}`,
        `${endpoint}/blobs/${f.sha256}`,
        f.sha256,
        f.size,
      );
    },
  };
}
