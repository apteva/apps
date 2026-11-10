# Database 0.18.0

AptevaDB aggregate-only batches can opt in to request coalescing and
a bounded, in-process result cache. Identical authorized reads share
one execution before bulk admission. Successful results in cache mode
are keyed to the revision of the snapshot that produced them. Each
caller keeps its own deadline.

Use aggregate_reuse_mode with off (the default), coalesce, or cache.
Batch diagnostics distinguish executed, joined, and cached requests.
The release updates the existing AptevaDB dependency to alpha.22 and
adds no third-party libraries or external services.
