# Media v0.15.3 — preserve video presentation during audio normalization

Audio normalization could pass loudness checks while a MOV/MP4 stream-copy remux
made hidden HEVC dependency pictures visible. Timestamp-copy flags alone do not
preserve the source edit-list behavior. The final picture validator correctly
rejected the resulting output.

The runtime now verifies every copied video packet's size and SHA-256 before
restoring the original video track's timing, edit, codec and dependency tables.
Only the remuxed chunk layout is retained; offsets for all tracks are rebased
when metadata size changes, preserving fast-start layout. Source files remain
untouched. Unsupported layouts and offset overflow fail explicitly.

Every decoded picture and presentation timestamp still must match. Loudness,
true-peak, sample-rate, A/V timing, color, decode and upload checks remain enforced.
Packet validation signatures now include discard flags, preventing proof reuse
when hidden-picture presentation changes. Normalization diagnostics use
`media-two-pass-loudnorm-7` and report whether presentation was restored.

Validation includes a synthetic reproduction (timestamp-copy flags still leak
pictures), local and remote-script regressions, real executor-component tests,
metadata-growth/fast-start and 32/64-bit offset guards, stale discard-proof
rejection, the complete short suite, focused race tests and Linux amd64/arm64
builds. Full-file tests also use the two reported production sources privately;
no production app upgrade or session-run mutation is part of this release.

Media uses App SDK v0.110.0. The optional executor remains v0.15.0 at its existing
exact source commit; processing plans come from the parent release. Server owns
its placement and lifecycle, and Media has no Instances dependency.
