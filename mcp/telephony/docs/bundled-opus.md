# App-owned native Opus

Telephony now ships libopus 1.5.2 as compressed, platform-specific resources.
It extracts/loads the bundle at mount before accepting calls. This is local
source work; it does not install or modify a running instance.

## Scope and failure behavior

- WebRTC callers, listeners and private coaching use the same codec factories.
- FEC remains an independent per-attachment option. Disabling it retains native
  encoding/decoding and ordinary packet-loss concealment.
- PCM/WebSocket framing, audio queues, stale-frame limits and carrier codec
  selection are unchanged. Native Opus cannot restore narrowband/upstream loss.
- No runtime network access, package manager or elevated permissions are needed.
- One library handle lives for the process lifetime; codecs belong to a single
  media goroutine and are destroyed on cleanup. Mount-time work adds no media timer.
- Library loading failure retains fixed system-library fallback, then portable
  encoding/decoding. Calls do not fail solely because native Opus is unavailable.
- Encoder diagnostics include source, bundled version, capability, requested and
  actual FEC, and a fixed fallback reason. No cache path or credential is logged.

## Packaging

`native/opus/manifest.json` pins upstream URL, archive SHA-256 and each embedded
compressed/uncompressed library's hash and size. `native/opus/COPYING` retains
upstream's BSD license. Each Go target embeds only its own libraries:

| Target | Runtime baseline |
| --- | --- |
| Linux amd64/arm64 GNU | glibc 2.17 or newer |
| Linux amd64/arm64 musl | musl host |
| Darwin amd64 | macOS 10.15 or newer |
| Darwin arm64 | macOS 11 or newer |

The default Linux executable still uses the existing glibc ELF loader. Musl
builds must select their loader at link time (for example
`go build -ldflags '-I /lib/ld-musl-aarch64.so.1'` for Alpine arm64). The bundle
supplies libopus, not a replacement host C runtime. The Telephony CI matrix
checks the native library on both architectures and both Linux libc variants,
plus Darwin arm64/amd64.

The checked-in resources allow normal CGO-disabled builds without a compiler or
system libopus. Developers regenerate them with `bun native/opus/build.ts` on
macOS using Xcode tools, Zig 0.16.0 and make. That build-time download is pinned
and verified. Runtime never executes this builder. DRED, deep PLC and OSCE
are disabled; this is regular libopus voice encoding, not a neural codec.

Extraction verifies both hashes, bounds decompression at the declared size
(maximum 8 MiB), and atomically installs an executable/read-only library beneath
`DataDir()/native/opus-1.5.2/<hash>/`. The native root is private (0700), the
library is 0500. The embedded upstream notice is also written as `COPYING`. Valid cached content is reused; corruption is repaired.
Managed symlinks are rejected. Trusted host ancestors are canonicalized to
support normal macOS /var aliases. Cache/download failures are observational.

## Verification

The usual Go suite covers integrity, binary architecture, corruption repair,
symlink rejection, concurrent extraction, FEC-control readback, actual redundant
packets, native FEC opt-out, cross-codec compatibility and portable fallback.
Local Linux container checks must exercise libraries without installing libopus.
Cross-compilation alone does not prove the dynamic ABI on a different host.

`TestRTCVoiceSpeechDemo` emits input, portable clean, native FEC-off clean and
native FEC-on clean WAVs from identical speech using the existing resampler.
These are an audible comparison, not a MOS score or proof of better sound on a
particular PSTN call. Existing loss replays separately check PLC/FEC behavior.

`BenchmarkRTCVoiceEncoding` and `TestRTCCodecCPUStress` compare portable, native
without FEC and native with FEC. The paced test includes resampling and decoding
at 50 frames/sec and reports completed and skipped slots, measured CPU and
projected full-rate CPU. It excludes sockets, SRTP, routing and database work;
capacity must be measured on the deployment host.

## Local results — 11 October 2026

Darwin arm64 native runtime tests pass, as do isolated Linux amd64/arm64
containers without system libopus, for both GNU and musl. Musl tests use an
executable with the musl ELF interpreter; an ordinary GNU executable is not
an Alpine release artifact. All four release targets cross-build with CGO
disabled. Darwin amd64 cannot execute on this Mac without Rosetta; its runtime
check is in the added CI matrix, which had not been run remotely at the time of this local verification.

The full Go suite passed 1,228 tests/subtests (seven optional skips), and the
frontend suites passed 251 tests. Focused RTC/listening/coaching race tests,
`go vet`, typecheck, headless/panel builds and the Chromium desktop/mobile
example passed. The final cache/notice checks also pass.

Identical 8.62-second synthesized French speech produced clean input/portable/
native FEC-off/native FEC-on clips. Native FEC-on generated 380 redundant
packets out of 431; opt-out control tests verified zero redundancy. At the
selected recoverable 20 ms speech gap, FEC reduced waveform error relative to
the same native clean decode by 8.16 dB. This is an ablation of loss repair,
not a clean-audio perceptual score or evidence for the production complaint.

On this M1 Pro, three encoder microbenchmark repetitions at 32 kbit/s measured
83.6–84.4 µs/frame for portable, 157.6–160.6 µs without FEC, and 159.1–160.8 µs
with FEC. These sine-input numbers do not represent speech FEC overhead alone.
Native calls through purego allocate 200 Go bytes in seven allocations/frame;
portable encoding allocates zero in this microbenchmark.

The paced bidirectional speech test used GOMAXPROCS=2, 50 frames/sec and three
seconds per scenario, including resampling and decoding. All 1/10/25/50-call
variants, including modeled 5% ingress loss, had zero skipped slots:

| Codec | 25 streams: CPU % of one core | 50 streams: CPU % of one core | 50 streams: work p99 |
| --- | ---: | ---: | ---: |
| Portable | 66.4% | 99.6% | 0.56 ms |
| Native, FEC off | 80.1% | 127.7% | 0.63 ms |
| Native, FEC on | 85.1% | 139.3% | 0.66 ms |
| Native, FEC on, 5% ingress loss | 86.7% | 130.3% | 0.67 ms |

There is measurable native-codec overhead. These short codec-only workloads
are not a server admission limit and do not cover prolonged full-call load.
No bitrate, encoder complexity or production queue cap was reduced.

Raw local artifacts: `/Users/marcoschwartz/.codex/artifacts/telephony-bundled-opus-20261011/`.

The 20-second local Chromium/network profiles retained their existing gates:

| Profile | Adviser → carrier p95 | Carrier → adviser p95 | Missing markers, both directions |
| --- | ---: | ---: | ---: |
| PCM/WebSocket baseline | 50 ms | 134 ms | 0% |
| WebRTC baseline | 135 ms | 253 ms | 0% |
| WebRTC, TURN/UDP, 128 kbit/s/direction | 124 ms | 192 ms | 0% |

Server diagnostics on both WebRTC profiles confirm `source: bundled`,
`library_version: 1.5.2`, `fec_enabled: true` and 32 kbit/s. These are simulated
networks, identifiable tone markers and a local Telnyx substitute, not a real
carrier/PSTN call or a speech-quality certification. Prior poor results at more
constrained bandwidth are not superseded by this small baseline check.
