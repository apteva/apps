# Telephony 0.12.40

Recording Storage uploads now default to MP3 at 128 kbps. SIP still captures
24 kHz PCM WAV during the call; conversion happens afterward in the bounded
background import worker. An embedded LAME 3.100 WASM encoder requires no host
FFmpeg, runtime downloads, Docker, or codec installation. Its source, LGPL
license and rebuild script are included under `native/lame/`.

- New `storage_format: mp3|wav` project recording setting, available through
  the settings tool, HTTP API and Calls panel. Defaults to MP3 for both existing
  and new project settings; applies to pending/future imports. Existing stored
  files remain unchanged. Existing provider MP3 downloads pass through without
  re-encoding; WAV is the lossless capture-storage alternative.
- Stereo caller/agent channels remain separate. Mono stays mono. Narrowband
  8 kHz input uses 16 kHz MP3 output for decoder compatibility, without adding
  speech bandwidth. MP3 has normal codec delay/padding; original WAV is the
  choice for exact sample preservation.
- Source and stored formats/sizes are tracked separately, so remote state
  migration continues to verify the retained WAV against its own size. Private original, balanced,
  caller-only and agent-only playback remain available for converted WAV/SIP
  recordings. Legacy/provider MP3 keeps original-only playback because its MPEG
  profile may not support channel decoding. Telephony MP3 variants decode
  incrementally to bounded disk files and preserve seeking and access checks.
- Source WAV remains until upload size/SHA-256 verification succeeds. Existing
  copy-versus-delete retention policy is unchanged. Conversion failures, size/
  hash mismatches and upload errors retain the original and report failure.
  Storage 413 responses continue to block automatic retries.
- Imports/playback share two disk-work permits. Each encoder has an isolated
  32 MiB memory limit and cancellation. Input/output transfer is streamed. MP3
  decoding is capped at 2 GiB PCM; generated playback remains subject to the
  configured 1 MiB–1 GiB limit and large-file cleanup. Worst-case concurrent
  temporary disk is 6 GiB plus the 256 MiB cache; durable originals are separate.
- Storage and source-download ceilings remain unchanged. MP3 does not implicitly
  enable recording, bind Storage, or delete retained originals.

Verification includes channel-separation and mono/stereo codec tests, migration
and defaults, private uploads/MIME, copy and move retention, conversion and
verification failures, cancellation, concurrency, Chromium MP3 playback/seek,
and repeatable 30/60/120-minute inbound/outbound recording matrices. The existing
WAV matrix continues to cover lossless storage. CI runs MP3 codec tests on all
supported native platforms and the long MP3 matrix in a separate job.

Publishing does not install or restart any instance.
