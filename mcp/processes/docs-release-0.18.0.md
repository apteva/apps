# Processes 0.18.0

Assignments can choose which provider their workers run on, so one procedure can
run differently per assignment. Nothing changes for assignments that don't set one:
workers keep using their agent's default.

- `provider` sets the provider for every worker in the assignment's runs.
- `step_providers` overrides it for named steps.
- The dashboard has a provider dropdown on the assignment, per-step overrides, and
  shows the provider on each run step.

The choice is frozen per run. It selects a provider, not a model: Core picks the
model from that provider's settings. It's a preference, not a guarantee: if a request
fails, Core may retry that call on another provider.

A thread can't change provider. With `worker_continuity` `auto`, steps with different
providers get separate workers and don't share context. `per_executor` refuses them.

If the agent can't use the provider, the step stays undelivered with the reason shown
and keeps retrying with that provider, never without it. It starts once the provider
is available.

## Requirements

Setting a provider needs an Apteva Server and Core with thread provider selection,
which aren't on their main branches at this release. On an older Server, assignments
that set a provider wait with a warning; assignments that don't are unaffected.

Choose models on any provider you pin. Without a choice, Server gives every tier the
first model upstream lists, which may be unavailable, and each call then falls back.

## Other changes

- app-sdk v0.115.0 (from v0.104.0).
- Migration 023 adds `process_worker_providers`.
- Tier 3 scenario 19 verifies each worker's provider from Core telemetry.
