# Observation replacement

An upsert policy with `operation: replace` replaces the entire observation's
properties. The operation defaults to `replace` when omitted. Send a complete
observation on each write; replacement is not a partial update.

- Omitted optional properties are absent from the stored replacement, including
  nested objects and provenance such as `source_event_id`.
- Explicit JSON `null` is stored as `null` for optional properties. Required
  properties reject omission, `null`, and an empty string when validation mode
  is `reject`. Other validation modes retain their existing violation behavior.
- Policy-generated bucket metadata, dimensions, and an explicitly configured
  output value are derived from the incoming observation. They are not copied
  from the previous observation. The resulting properties are validated before
  storage; a rejected replacement leaves the previous observation unchanged.
- Row ID and upsert identity are maintained separately from the properties.
  Replacement preserves the row ID when the identity is unchanged. Changing a
  configured dimension or time bucket identifies a different row.
- An identical transport replay with the same `delivery_id` remains a no-op;
  that delivery ID cannot be reused for different data.

There is no general-purpose partial-merge operation. `sum`, `increment`, `min`,
and `max` are distinct numeric aggregation operations and retain their existing
accumulation behavior. Do not use `replace` to accumulate properties across
observations.

## Upgrading to 0.15.1

Earlier versions inadvertently retained omitted properties on policy-based
replacement upserts. Existing stored rows are not rewritten by this release:
the original incoming payload cannot reliably be reconstructed. Repair affected
rows by submitting a complete observation from a trusted source with a new
delivery ID (if using delivery IDs). Do not infer missing counts or provenance
from stale fields. Callers that previously relied on implicit merging must send
all required properties on replacement, including required provenance.
