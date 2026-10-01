# CRM v0.9.3

Adds stable list context to the CRM event contract without changing event topic
names or removing existing payload fields.

## Changes

- Contact, deliverability, conversation and opportunity events now carry sorted
  active `list_ids` membership snapshots.
- `contact.activity.added` and `conversation.message.received` also carry
  `attributed_list_ids` for outbound list selection and inbound routing matches.
- `contact.deleted` preserves `list_ids_before_delete`.
- `contact.merged` preserves `loser_list_ids_before_merge` and reports the
  post-merge `winner_list_ids` union.
- Segment lifecycle events carry their optional `list_id` scope.
- Inbound list context is calculated inside the existing state-and-outbox
  transaction. Other events are enriched before entering the durable outbox.
- Empty list fields are emitted as arrays, not omitted or encoded as null.

## Compatibility

The payload changes are additive. Existing consumers can continue reading the
previous fields. New consumers should still tolerate replayed pre-v0.9.3 events
that lack list context and must continue deduplicating by `event_id`.

No database migration is required.

## Validation

The Go suite covers active/archived membership snapshots, empty arrays, outbound
and inbound attribution, merge/delete before-and-after snapshots, segment scope,
opportunity enrichment, manifest declarations and project-scoped outbox delivery.
