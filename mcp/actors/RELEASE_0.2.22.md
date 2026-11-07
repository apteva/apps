Actors 0.2.22 adds provider-neutral collection and saved-record verification:

- Direct authenticated collection URLs and encoded query parameters.
- Explicit empty-text evidence and bounded next-control traversal with stable end checks, semantic document scrolling, identity verification and read-only coverage reporting.
- Ordered array fields for attachments and form options.
- Case/whitespace-exact assertions and numeric post-action baseline comparisons.
- Editable AdultFolio search, inbox, message-history and guarded reply example. No real profile, recipient, context ID or message is embedded.

Validation: full Go suite and build, including stalled/limited pagination, wrong identity, unsafe controls, changing collections, exact composer checks, post-submit persistence failures, and durable duplicate-send protection. Live provider sends are independent of these fixture tests and require an approved recipient and content.
