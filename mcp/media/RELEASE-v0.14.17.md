# Media 0.14.17

`media_ask` now retries transient vision/chat failures automatically, including
the reported "Codex stream ended with error" response. It remains read-only,
using the same existing visual evidence and transcript throughout the request.

- At most three attempts share `describe_timeout_seconds` as one overall
  budget. Default backoff is 1 then 2 seconds. Retry/reset hints are honored;
  hints above 10 seconds or beyond the remaining request budget stop with
  explicit deferred/budget diagnostics instead of making an early retry.
- Visible authentication, quota/billing, invalid-input and permanent HTTP
  errors stop immediately, even when wrapped in a generic server error.
  Transient rate limits, supported 5xx/transport errors and incomplete Codex
  streams without completion can retry. A reported `response.incomplete`
  without a known transient cause is not retried blindly. Invalid/empty
  successful answers remain failures and are never treated as crop approval.
- `request_diagnostics` reports attempts, status, failure classification,
  available upstream error detail, safe retry/reset metadata, elapsed time
  and stop reason. Failed requests preserve diagnostics in the tool error.
  No request prompts or signed evidence URLs are added to diagnostics.
- The entire retry sequence owns a call slot during backoff. The SDK receives
  the remaining deadline where supported; a legacy timed-out call retains
  its in-flight slot until its actual completion, preventing duplicate work.
  Cancellation/shutdown stops waiting and retries. Background descriptions
  retain their separate call class and persisted backoff behavior.

The currently published platform Codex adapter can replace the detailed error
stream event with a generic string before Media receives it. Media labels
that case `stream_failure_unknown` and preserves the message it receives;
this app-only release cannot reconstruct details discarded upstream. Explicit
permanent error details supplied by the gateway always prevent retry.

All rendering, Smart Crop, audio validation and Storage safeguards from
0.14.16 remain included. No production app upgrade is performed by publication.

Validation: the full Go race suite passed (601 top-level tests, 25 skipped,
no failures), including retry recovery/exhaustion, permanent-error stops,
retry/reset budgets, cancellation and duplicate suppression, deadline propagation,
legacy/project-scoped compatibility and read-only evidence reuse. Targeted ask
and background-description race tests also passed. Six Media–Storage integration
checks passed, covering trim, PNG filename normalization, frame extraction,
missing-source failure, guarded trim/normalization and pending cancellation.
Go vet and Darwin arm64, Linux amd64 and Linux arm64 builds passed.
