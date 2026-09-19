# Processes v0.13.0

Process organization metadata is now available across the API and UI. Procedures
can carry one optional category and normalized tags without changing procedure
execution, assignment snapshots, or run history.

The Processes list supports category filtering and shows category/tag badges. The
procedure editor exposes organization fields. The project map keeps all process
boundaries visible, adds category labels, searches tags, and supports category
filtering alongside status and live-run filters.

Existing procedures remain compatible and appear as **Uncategorized** until edited.
No database migration is required because metadata is stored in the immutable
procedure definition JSON. MCP `list` accepts `category` and `tag` filters.

## Validation

- `GOWORK=off go test -race ./...`
- 11 Bun UI and project-map model tests
- Panel build and host React import verification
