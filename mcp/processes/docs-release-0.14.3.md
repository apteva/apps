# Processes 0.14.3

This patch release extends event-trigger `contains` filters to support JSON
array membership in addition to string substring matching. A filter such as
`data.list_ids contains 2` now matches an event whose `data.list_ids` array
contains the numeric value `2`, while preserving strict JSON type behavior.

The release changes Processes only; Conversations and the CRM event publisher
are unchanged.
