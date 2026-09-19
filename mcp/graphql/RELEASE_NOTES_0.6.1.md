# GraphQL 0.6.1

- Defines safe null ordering for Resolver Module comparisons, so guards such as
  `total > 0` correctly skip numeric branches when source data is absent.
- Preserves standard GraphQL field-level null/error behavior for all other
  invalid calculations.
