# GraphQL 0.7.1

GraphQL 0.7.1 caches the immutable resolver/source/module execution plan attached to each atomic release. This removes per-request map reconstruction introduced by 0.7.0 while retaining release isolation, limits, telemetry, authenticated subscriptions, and the existing Tables batching/projection fast paths.
