# Voice interruption ordering and PCM continuity — 2026-10-23

## Captured local failure

Voice Agent 1194, Product Studio, conversation
`conv-951bc04c6f41efbec0f1f2ae`, voice session
`voice-e05fa55a1d3fab49d1a25fe10e88f4d5`.

At 11:55 UTC, the saved assistant segment spanning 30600–35000 ms contained:

> Tu mélanges de la farine, des œufs, du lait, Oui, je t'écoute.

The caller interruption, “Non attends, attends, arrête”, started at 33800 ms.
The recipe checkpoint ended at 33200 ms; the acknowledgment extended that same
segment to 35000 ms. Core grouped by punctuation, arrival gap and caption gap,
without treating a caller onset as an assistant display boundary. Consequently,
Conversations could not separate the acknowledgment using the saved text alone.

The subsequent “Ok, je m'arrête” case was separate because its caption started
1600 ms after the recipe's last caption, exceeding the old 1500 ms grouping
threshold. The first acknowledgment's gap was only 1200 ms. This explains why
one interruption appeared correct and the other did not.

No realtime errors, reconnects or overflows were recorded in the first session.
The four renderer speech interruptions corresponded to caller onsets, rather
than repeated word-level content suppression. Server browser writes peaked at
20.8 ms. No raw output recording or browser underrun history was captured, so
these logs cannot prove the acoustic source of every reported crackle.

## Changes

Core now starts a separate assistant segment when a caller onset divides its
caption timeline. It also retains immutable, timestamped caption fragments in
checkpoints and saved history. This evidence supports delayed caller captions
that arrive after an assistant checkpoint. Caller speech can continue across a
brief assistant backchannel without splitting its durable utterance.

Conversations persists the additive fragment metadata and presents assistant
fragments in provider timeline order within each uninterrupted voice session
and generation. It groups readable adjacent segments without merging across a
caller, typed message, card, session or generation. Durable message IDs, content,
and timestamps remain intact; virtual display pieces have distinct keys. Legacy
rows without fragment evidence are retained without guessing a text split.

Playback now uses one AudioWorklet queue and a persistent fractional resampling
cursor across PCM packet boundaries. It replaces separately resampled and
scheduled AudioBufferSource nodes, whose repeated minimum lead-in could create
holes under arrival jitter. A 120 ms startup/rebuffer reserve absorbs brief
jitter. Ordinary packet boundaries receive no fade or inserted delay. Real
starts, interruption transitions and starvation receive brief smoothing.

Partial progress and targeted clear acknowledgments originate on the audio
thread. Canceled IDs reject late audio, other IDs survive a scoped clear, old
connection epochs cannot acknowledge a replacement socket, and microphone
streaming remains continuous. Silent PCM preserves elapsed time without keeping
the speaking indicator active.

## Validation

- Captured French interruption replay, including a caller caption arriving after
  the already checkpointed acknowledgment; content/identities remain intact.
- Dashboard, external widget and packaged-client browser tests verify the recipe,
  caller interruption and acknowledgment appear in that order, before and after
  reload.
- Sample-level tests compare continuous 440 Hz PCM against uninterrupted linear
  resampling at 48 kHz and 44.1 kHz with jittered 20 ms packets. The same arrival
  sequence reproduces a 25 ms hole with the previous scheduling formula.
- A test-only browser AudioWorklet tap checks actual rendered samples for inserted
  gaps and abrupt packet-boundary jumps. Three repeated browser runs passed after
  increasing the initial reserve from 60 ms to 120 ms; the smaller reserve had
  an intermittent underrun under test load.
- Paid native Tier 3 passed through isolated Server, rebuilt Core, Conversations
  and the production Chromium audio client/worklet, using real `gpt-live-1`
  (Brise) and French caller audio. The main agent used `gpt-6.1-sol`.
  The caller interrupted voiced output; one scoped clear was acknowledged;
  426 microphone frames continued during recovery; 110 consumption receipts were
  observed; the next reply correctly answered “mardi”, played, and was persisted
  after the caller transcript. No disconnect/error/missing-caption stop occurred.
- A second paid native run against the final Core build also passed: correct
  interruption/reply ordering, one scoped clear, 278 microphone frames during
  recovery, 116 playback receipts, and no disconnect/error/missing-caption stop.
- 128 Bun tests, all 122 browser tests, the full Conversations Go race/integration
  suite, and Core realtime race tests passed. The final Core binary also built.

The native run used a temporary test instance; it did not restart or install
anything in the user's local instance or production. No credentials were saved
in source or test fixtures. The source changes have not been released.

The sample and browser tests establish continuity for supplied PCM. A follow-up
listening check on the user's Mac speakers is still needed to confirm that all
perceived noise is resolved; physical echo, the device path and provider audio
are not reproduced by a synthetic browser microphone. Existing historical rows
that never stored fragment evidence are not rewritten.
