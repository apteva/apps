# Media Executor v0.15.1

Expose the authenticated managed-runtime status endpoint required by current
Server deployments. Report the exact deployment ID and generation injected by
Server, together with passive, active and drained admission state. Validate the
same identity on activation and drain so stale lifecycle requests cannot affect
another generation. Invalid managed identity fails startup.

The worker remains optional and private. Media owns durable jobs; Server owns
placement, generation, authentication and deployment lifecycle. No Instances
integration is introduced into Media. This release uses App SDK v0.110.0.

Validated with worker race tests and the real parent/worker SDK integration,
including authenticated status, passive start, generation mismatch rejection,
drain, durable job receipts, file transfer and cancellation.
