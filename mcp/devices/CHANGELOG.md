# Changelog

## 0.2.0 — 2026-11-08

- Integrate MQTT 0.4 scoped topics, qualified credentials, and authenticated event identities.
- Enforce the gateway-authenticated project across HTTP/MCP resources and queries.
- Journal provisioning, rotation, enable/disable, and deletion with recoverable intent and idempotency conflicts.
- Encrypt short-lived credential recovery results with an independent mode-0600 recovery key.
- Add generic v2 structured data, protocol adapters, schema-described operations, application receipts, deduplication, ordering, and paginated history.
- Persist asynchronous commands for recoverable dispatch, enforce absolute deadlines and concurrency, and retain late responses.
- Publish mutation events through an acknowledged transactional outbox with replayable history and durable usage measurements.
- Accept validated operational limits and independent history/usage/secret retention settings.
- Extend the panel with generic operations, recoverable provisioning, scoped install routing, and paginated history.
- Preserve legacy data on migration and require explicit credential/client review before re-enabling old devices.
- Add real MQTT sidecar integration, restart/migration/failure/concurrency tests, and durable Python/Arduino command examples.

Migration: read README before updating an existing installation. Requires MQTT >=0.4.0; back up the database and the new recovery key. Firmware usernames/topics change. Native v1 telemetry retains its legacy best-effort delivery contract.
