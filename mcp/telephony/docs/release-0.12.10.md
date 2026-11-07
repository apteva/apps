# Telephony 0.12.10

## Bundled native Opus

Telephony provides pinned libopus 1.5.2 for Linux/macOS amd64/arm64. The verified
library is extracted into private app-owned storage and loaded once at mount;
no runtime download, package manager, administrator access or manual libopus
installation is needed. Fixed system-library and portable codec fallbacks remain.

WebRTC softphone, listening and private coaching share the codec factories.
FEC remains optional per attachment; disabling it now retains native Opus
encoding/decoding and ordinary PLC while disabling redundancy. Diagnostics
identify codec source, bundled version, fallback reason and actual FEC state.
PCM/WebSocket transport, live audio queue/age limits and carrier codec selection
are unchanged. Native Opus cannot restore quality already lost upstream.

GNU and musl libraries are included. Ordinary Linux release binaries use the
existing GNU loader; musl builds must select their loader at link time. Darwin
Intel runtime verification is provided by the new CI matrix.

## Long recording playback and storage

The project import/playback file limit is configurable through recording settings
as `max_file_bytes`, defaults to 1 GiB and accepts 1 MiB–1 GiB. Capture and its
separate duration policy remain unchanged. Downloads and negotiated Storage
upload parts stream from disk instead of allocating recording-sized buffers.

The reusable 256 MiB cache is bounded by entry count, expiry and reader ownership.
Large original/mixed/channel files use shared temporary files, removed after their
last reader closes. Two shared playback/import permits bound workspace; Range/HEAD,
project/agent authorization and private access are retained. App startup clears
its own transient directory after exclusive cutover, preserving durable originals.

Failed balanced conversion falls back to original playback. Explicit caller-only
or agent-only errors never substitute both sides. The player reports fallback and
channel errors clearly. Recording states distinguish Recorded locally, Stored
successfully and Storage import failed, with the failure reason.

HTTP 413 and configured file-limit failures pause automatic import retries until
the project limit changes or an operator explicitly retries. Temporary failures
retain bounded backoff. Original removal remains gated by stored size/SHA-256
verification; mismatch, failure and stale completion preserve the original.

## Verification

- Full local Go suite: 1,239 tests/subtests passed, eight optional skips.
- 251 frontend/worklet tests, recording and RTC/listening/coaching race checks,
  Go vet, typecheck and app/panel/headless builds passed.
- Six accelerated 30/60/120-minute SIP recording cases, inbound/outbound, cover
  verified HTTP Storage import, original/mix/caller/agent playback, start/middle/end
  seeking, private access and temporary cleanup. Two-hour originals are 691,200,044
  bytes; mono variants exceed the former cache limit. Playback allocated under
  1 MiB per case. Uses the actual recorder and HTTP Storage substitute, not real
  two-hour carrier calls.
- Three Chromium recording-player checks prove balanced fallback and preservation
  of explicit channel selection on failure.
- Bundled codec runtime checks passed locally on Darwin arm64 and Linux amd64/arm64,
  GNU and musl-loader variants, without system libopus. Four targets cross-built.
- Local Chromium PCM/WebSocket, WebRTC baseline and 128 kbit/s TURN/UDP profiles
  passed with zero missing test markers; these are modeled networks, not PSTN.
- Identical speech clean portable/native FEC on/off comparisons and loss repair
  checks are included. Better perceived audio on the reported production call
  remains unproven.
- At 50 paced codec-only streams on M1 Pro/GOMAXPROCS=2, native FEC used about
  1.39 CPU cores versus 1.00 portable, with zero skipped slots. Excludes sockets,
  SRTP, routing and database work; native encoding has measurable CPU overhead.

Retains previous Telephony changes, SDK v0.100.0, minimum Apteva 0.87.4 and
exclusive activation. Publishing does not install an app, restart an instance,
configure a carrier or make calls. Installing the update uses exclusive process
cutover; plan installation without active calls. See `docs/bundled-opus.md` and
`docs/long-recording-playback.md` for provenance, limits and reproducible tests.
