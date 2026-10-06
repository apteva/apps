# GraphQL v0.8.1

Publishes the tested v0.8.0 runtime under a new patch version with manifest and
marketplace URLs pinned to `graphql/v0.8.1`. There are no runtime or UI changes.

Includes authorization-safe in-flight read sharing, resolver batching, generic
upstream snapshots and metadata, bounded scheduling and cancellation, runtime
controls, filtered request diagnostics, and the exported dashboard widget.

Tables supports batch snapshots; request-wide snapshots require a backend with
reusable handles. See RUNTIME.md for configuration and adapter contracts.

Validation: runtime and UI files match v0.8.0, whose full Go race suite, vet,
source build, real Tables integration, telemetry tests, and browser checks passed.

Source release and marketplace metadata only. No production deployments,
installation upgrades, or restarts.
