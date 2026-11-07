// Rebuild the app-owned libraries from verified upstream source. Run on macOS
// with Xcode command-line tools, Zig, make and Bun; never invoked on install.
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

const version = "1.5.2";
const sourceURL = `https://downloads.xiph.org/releases/opus/opus-${version}.tar.gz`;
const sourceSHA256 = "65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1";
const root = new URL(".", import.meta.url).pathname;
const work = await mkdtemp(join(tmpdir(), "telephony-opus-build-"));
const hash = (data: Uint8Array) => createHash("sha256").update(data).digest("hex");
const response = await fetch(sourceURL, { signal: AbortSignal.timeout(60000) });
if (!response.ok) throw new Error(`Source HTTP ${response.status}`);
const archive = new Uint8Array(await response.arrayBuffer());
if (hash(archive) !== sourceSHA256) throw new Error("Upstream source checksum mismatch");
await writeFile(join(work, "source.tar.gz"), archive);
async function run(args: string[], cwd: string, env?: Record<string, string>) {
  const p = Bun.spawn(args, { cwd, env: { ...process.env, ...env }, stdout: "pipe", stderr: "pipe" });
  const output = Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text()]);
  const code = await p.exited;
  const [out, err] = await output;
  if (code !== 0) throw new Error(`${args[0]} failed (${code})\n${out.slice(-5000)}\n${err.slice(-5000)}`);
}
await run(["tar", "-xzf", "source.tar.gz"], work);
const platforms = [
  { key: "linux-amd64-gnu", target: "x86_64-linux-gnu.2.17", host: "x86_64-linux-gnu" },
  { key: "linux-arm64-gnu", target: "aarch64-linux-gnu.2.17", host: "aarch64-linux-gnu" },
  { key: "linux-amd64-musl", target: "x86_64-linux-musl", host: "x86_64-linux-musl" },
  { key: "linux-arm64-musl", target: "aarch64-linux-musl", host: "aarch64-linux-musl" },
  { key: "darwin-amd64", arch: "x86_64", host: "x86_64-apple-darwin" },
  { key: "darwin-arm64", arch: "arm64", host: "aarch64-apple-darwin" },
];
const entries: Record<string, unknown> = {};
for (const platform of platforms) {
  const dir = join(work, platform.key);
  await mkdir(dir);
  const env = platform.target
    ? { CC: `zig cc -target ${platform.target}`, AR: "zig ar", RANLIB: "zig ranlib", CFLAGS: "-O2 -g0" }
    : { CC: "cc", CFLAGS: `-O2 -g0 -arch ${platform.arch}`, LDFLAGS: `-arch ${platform.arch}`, MACOSX_DEPLOYMENT_TARGET: platform.arch === "arm64" ? "11.0" : "10.15" };
  await run([join(work, `opus-${version}`, "configure"), `--host=${platform.host}`, ...(platform.target ? ["--disable-shared", "--enable-static", "--with-pic"] : ["--enable-shared", "--disable-static"]), "--disable-extra-programs", "--disable-doc", "--disable-dred", "--disable-deep-plc", "--disable-osce"], dir, env);
  await run(["make", "-j2"], dir, env);
  const file = platform.target ? "libopus.so.0.10.1" : "libopus.0.dylib";
  if (platform.target) {
    await run(["zig", "cc", "-target", platform.target, "-shared", "-Wl,-soname,libopus.so.0", "-Wl,--whole-archive", ".libs/libopus.a", "-Wl,--no-whole-archive", "-lm", "-o", join(dir, ".libs", file)], dir);
  }
  const library = await readFile(join(dir, ".libs", file));
  const compressed = gzipSync(library, { level: 9 });
  const asset = `${platform.key}.gz`;
  await writeFile(join(root, "assets", asset), compressed);
  entries[platform.key] = { asset, sha256: hash(library), compressed_sha256: hash(compressed), bytes: library.length };
  console.log(`${platform.key}: ${library.length} bytes → ${compressed.length} bytes`);
}
await writeFile(join(root, "COPYING"), await readFile(join(work, `opus-${version}`, "COPYING")));
await writeFile(join(root, "manifest.json"), JSON.stringify({ schema: "telephony-opus/v1", version, source_url: sourceURL, source_sha256: sourceSHA256, libraries: entries }, null, 2) + "\n");
console.log(`Build directory retained for inspection: ${work}`);
