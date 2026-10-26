# Media v0.15.2

Component cancellation now relies on the worker's durable job identity and
verified process-group stop. A second legacy shell cleanup could race the
worker's workdir trap and incorrectly turn a confirmed stop into a failure.
Cancellation preserves receipt identity checks, durable receipt writes and
explicit failures when stopping cannot be confirmed.

Includes v0.15.1's base64 component upload chunks for public gateways. Storage
receives original bytes, with existing scope, transfer limits and checksums.
The executor remains pinned at v0.15.0; no worker redeploy is needed.

Validation includes the Media suite, race tests of real worker rendering,
indexing, guarded audio, cancellation immediately after submit, scoped transfer
encoding and parallel chunk integrity. Local installation and DigitalOcean tests
cover video/audio indexing, resized output, verified audio normalization, sampled
analysis and confirmed cancellation through Server's component route.
