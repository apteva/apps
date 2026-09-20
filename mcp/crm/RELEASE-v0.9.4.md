# CRM v0.9.4

Makes the saved-segment MCP contract self-describing so agents can build valid
audiences without guessing predicate names or argument shapes.

## Changes

- `segments_create` now documents all supported synthetic predicates and the
  core-field filter shape directly in its tool description and input schema.
- `segments_update` exposes the same shared definition contract inside `patch`.
- The schemas include full copyable tool-input examples and definition examples
  for `tag_in`, `tag_not_in`, `attribute`, `last_activity_within`,
  `channel_present`, `in_list`, `not_in_list`, `not_in_segment`, and core fields.
- Unknown or missing predicate validation errors now return the complete list of
  supported predicates plus a core-field example.
- Agent guidance and the native manifest describe the same segment contract.

## Compatibility

The runtime behavior and stored segment format are unchanged. Definitions remain
flat AND-ed arrays, existing definitions continue to compile, and the richer JSON
schema adds guidance without rejecting previously accepted objects.

## Validation

Regression tests verify that create and update share one contract, every
predicate is discoverable, every published definition example compiles, the
documented `tag_in` call succeeds through the real tool handler, and invalid
predicate errors enumerate all supported alternatives.
