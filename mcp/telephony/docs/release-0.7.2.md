# Telephony 0.7.2

Fix repeated inbound AI startups after handoff failure, preserving all 0.7.1
changes. This is a source release; no production installation is updated.

- Shared, durable AI startup budget: three total attempts within 15 seconds by
  default, with backoff and configurable bounded limits.
- Stop configuration/access and uncertain failures immediately; retry explicit
  temporary failures only after successful cleanup. No provider-message matching.
- Execute a pinned `ai_startup_failed` branch once, or terminate cleanly when
  none is configured. Preserve existing adviser offers and newer call ownership.
- Fence late startup results on caller hangup or startup deadline. Atomically
  attach the prepared thread and mark startup ready.
- Distinguish startup failure from AI-handled calls and retain a deduplicated
  callback opportunity when the call remains unhandled.
- Add administrative per-call startup diagnostics, without adding work to the
  adviser call-list query.
- Migration 034 adds startup state and attempt history. Existing routes inherit
  safe defaults; no Telnyx integration JSON change is needed.

See [AI handoff policy](ai-handoff.md) for configuration and error semantics.
Unknown/ambiguous failures intentionally stop instead of being treated as transient.
The underlying AI configuration problem must still be corrected separately.

Validation: full Go unit and local sidecar integration suites, focused race
regressions, frontend tests/typecheck/build, and a standalone Go build. Live
carrier and AI-provider calls are excluded; production remains untouched.
