# GraphQL v0.21.2

A stability release on top of 0.21.1. No GraphQL behaviour changes, and existing
plans keep working as they are.

## App SDK v0.115.0

GraphQL moves from app SDK v0.99.0 to v0.115.0. The change that matters most:
calls to other apps, including every Database request, now share a pool of
keep-alive connections to the platform gateway. Before, only two idle connections
per host were kept, so concurrent requests closed most connections after one use
and could run out of local ports under load.

## Literal search

This release includes everything in 0.21.1: `contains_ascii` search on a query's
own fields and on linked records inside an `exists`, and up to 24 relationships in
a filter on Database 0.11.1. See the 0.21.1 notes for how it works and its limits.

Version 0.21.0 was withdrawn: it was published on the wrong commit and removed
before anything installed it.
