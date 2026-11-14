# Database 0.18.2

Deterministic version-1 pipelines in find and aggregate now participate
in the existing snapshot-aware batch coalescing and result cache.
Batches can mix ordinary reads with scan, compute, join and intermediate
aggregate pipeline stages.

The reuse key includes the full ordered plan, eligibility filters,
expressions, join rules, projections, pagination and execution limits.
Explicitly empty pipeline containers bypass reuse to preserve validation
semantics. Authorization, snapshot revision invalidation, cancellation,
memory limits and diagnostics retain the existing behavior.

Tests cover eight overlapping requests, raw-result parity, exact distinct
counts, pagination, cursor validation, source and join dependency writes,
writes during admission, independent caller cancellation, scope and
database isolation, response mutation, and oversized results.

The app pins SDK v0.115.2. The AptevaDB dependency remains alpha.22.
