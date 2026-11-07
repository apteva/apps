# Analytics availability and freshness

Shared reporting structures support requested metrics, reporting timezone,
provider observation time, a contiguous processed-through date, and explicit
pending/estimated/unavailable dates. Each available metric can carry its own
freshness. Values and freshness are separate from inventory coverage.

Normalized metrics and reports serialize unavailable metric values as null;
reported zero remains 0. The `available` list survives cache round trips and
compatibility click aliases are not counted twice. Reporting clients should
continue using `available` and `metric_info`, rather than interpreting every
numeric Go default as a provider measurement.

Daily points carry `data_status` (ready, estimated, pending, unavailable or unknown).
Pending/unavailable values serialize as null. Missing processing status remains unknown even when a numeric value was reported. Aggregation preserves pending
states and incomplete post coverage instead of plotting a partial sum as a
complete daily total. Missing calendar dates are gaps. Provider estimates are
retained and labeled independently of inventory coverage.

The UI shows exact dates, provider timezone when known, pending dates, estimates
and unavailable metrics. It supports a distinct 30-day range alongside 28 days
for comparisons with a provider's 30-day view. Provider API reports are not
presented as real-time UI totals. Report cache format 4 forces older cached
account and post reports to be refreshed without deleting historical metrics.

Pinterest's organic reporting dates are UTC. Board analytics retain the existing
bounded individual-Pin fallback (50 calls/report), including partial coverage
when the limit is reached. It now also recognizes the verified HTTP 401 response
with code 3 and exact restricted-feature message. Other 401s remain errors;
individual-Pin authentication failures stop further fallback requests.

The matching server and TypeScript executor changes enable optional CSV query
array encoding in Pinterest's catalog. Deploy the updated server/catalog before
refreshing Social reports. No SDK, OAuth scope, token or production state change
is part of this implementation.

Reference: https://github.com/pinterest/api-description/blob/main/v5/openapi.yaml
