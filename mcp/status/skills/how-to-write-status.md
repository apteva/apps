---
name: how-to-write-status
triggers:
  - status_set
  - status_get
  - status_clear
---

# Status — how to keep your status line useful

The status line is the operator's at-a-glance signal for "what is
this agent on right now". One short sentence per phase. Not a
debug log — every line displaces the previous one, so make each
line worth replacing it for.

## When to call status_set

Call it at **phase transitions**, not every tool call:

- You start a new sub-task or new sequence of tool calls.
- The current phase changes character (begin → succeed → fail).
- You hit something that blocks progress and you need attention.

Skip it when:

- You're inside one logical phase taking many small steps. One
  status per phase, not per `read_file` / `bash` / `grep`.
- The message wouldn't differ meaningfully from the last one.
- You're mid-thinking and don't yet have a phase to announce.

## Tone discipline

Match the tone to the phase, not the mood:

- `working` — actively doing the thing. Use this most of the time.
- `success` — the thing succeeded. Set this *then* move on.
- `error` — the thing failed in a way the operator should see.
- `warn` — partial / degraded / needs attention but not blocked.
- `blocked` — waiting for human input or external system. **Status
  is the right place to surface this** — operators scan tone, not
  prose, and a `blocked` line is louder than any chat message.
- `info` — neutral. Use sparingly; usually you want one of above.
- `idle` — nothing in flight. Equivalent to "ready". Often pair
  with `status_clear` instead.

## Format

- One line. Roughly 5–12 words.
- Plain noun phrase or "verb-ing X". "Deploying staging.",
  "Reading the design doc.", "Waiting on Anna's approval."
- Skip subjects and pronouns; the operator knows it's you.
- Optional emoji adds glanceability for human readers; skip if
  it doesn't help.
- No timestamps in the message — `updated_at` is automatic.

Good:
- `working`: "Migrating users table to v3"
- `success`: "Smoke tests passing on staging"
- `blocked`: "Waiting on Anna's approval for the deploy"
- `error`: "Migration failed at step 4 — rolling back"

Bad:
- "Calling `read_file` on src/main.go" (per-tool noise)
- "Doing some work" (no signal)
- "[2026-05-18 14:32] Started deploy" (timestamp belongs in metadata)

## When to clear

Call `status_clear` only when you have *nothing* running and want
the slot empty. Otherwise update to a terminal tone (`success` /
`error` / `idle`) and leave the line up — operators learn faster
from "last did X, succeeded" than from an empty slot. A
permanently-empty status looks like an unhealthy agent.

## Common mistakes

- **Per-tool-call updates.** Status becomes noise; operator stops
  scanning it. One status per phase, not per step.
- **Mid-phase tone flapping** between `working` and `info` because
  you're unsure. Stay on `working` until the phase ends, then move
  to a terminal tone.
- **Forgetting to update after failure.** A stale `working` line
  on a failed agent is the worst possible state — the operator
  thinks you're still going. After any unrecoverable error, set
  tone to `error` with what failed.
- **Treating it like logs.** Status is the marquee, not the
  transcript. Use it for headlines; use the chat thread for
  details.
- **Setting `blocked` without saying who/what.** "Blocked" alone
  is uninspectable; "Waiting on Anna's approval" is actionable.

## Inter-agent reads

`status_get` is mostly for **other agents/apps** checking on you,
not self-reads — you already know what you set last. Common
callers: an orchestrator scanning a fleet, a workflow waiting for
a peer to reach a terminal tone.
