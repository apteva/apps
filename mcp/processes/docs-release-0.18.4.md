# Processes 0.18.4

An unavailable agent no longer aborts legacy wait revalidation. Each failed or
stopped owner is deferred with durable per-step/revision backoff; healthy owners
continue, including beyond a page containing only unavailable agents. Stopped
agents are not woken for revalidation. Lost replies retry the same tracked event
identity. Checkpoint writes can proceed while another delivery is pending.

Cleanup now waits for a live owner acknowledgement. Processes also consumes
read-only thread-start metadata to detect workers restored after cleanup of an
already terminal run, then requeues only their exact owned cleanup records.
Pending retry order, immutable run outcomes, approvals, callbacks and native
clock owners are preserved. The metadata repair has bounded pages, durable
restart cursors and retry backoff; it never loads full worker contexts.

Migration 027 adds revalidation retries and metadata scan cursors. This release
adds the narrowly scoped `platform.telemetry.observe` permission using APIs
already present in production; SDK remains pinned to v0.115.2, the latest tag by
ancestry. No Core/Server source or binary changes, new projects, or synthetic
production workflows are included.

Validation: full Go unit and integration suites; focused race tests for batch,
checkpoint, approval/timer and restored-worker cleanup; Go vet; Linux amd64
build; 206 Bun unit tests; 44 Playwright browser tests; three existing live Core
tier 3 tests with unchanged Core source. Regression cases include unavailable
first/middle owners, a full failed first page, restart/backoff and event identity,
concurrent checkpoint access, metadata replay and scoped terminal-worker repair.
