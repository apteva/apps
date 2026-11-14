# Processes 0.18.6

Legacy steps sometimes retained a running label after an app-authored provider
failure suspended their delivery. Processes now restores their recoverable
runtime hold only when the exact original runtime audit event, current suspended
delivery, empty wait metadata and no retained dependency agree. Output, original
failure, evidence and frozen policy are preserved. The parent status is repaired,
the worker can retire, and recovery still requires an explicit operator action.
Arbitrary executor-supplied error text cannot authorize this repair; retained
approvals, callbacks, native timers and resumed delivery are excluded.

An accepted legacy ownership inspection without a saved wait checkpoint can be
requested again after fifteen minutes, at most three accepted inspections per
step revision. A new inspection receives a fresh tracked identity; ambiguous
HTTP retries reuse the same identity. Silence never authorizes worker cleanup.

Migration 029 adds the bounded inspection counter and an index for suspended
legacy holds. The independent owner retries and bounded per-agent metadata repair
from v0.18.4/v0.18.5 remain in place. No additional permissions, Core/Server changes,
new projects or synthetic production workflows are included.

Validated with complete Go unit/integration suites, focused lifecycle race tests,
Go vet and Linux amd64 build. The unchanged UI passed 206 Bun and 44 browser tests,
and three existing live Core tier 3 checks passed during this release session.
