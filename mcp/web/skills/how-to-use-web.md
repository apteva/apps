---
name: how-to-use-web
triggers:
  - web_search
  - web_extract
  - web_crawl
  - web_map
  - web_research
  - web_snapshot
  - web_extractor_run
  - web_extractor_schedule
  - search
  - research
  - crawl
---

# Web

Use this app when the user asks for browser-backed search, page extraction,
crawling, site mapping, research, visual proof, or a reusable scheduled web
extraction workflow.

## Tool choice

- `web_search`: discover pages from a query. Returns normalized JSON results.
- `web_extract`: read one known URL. Use before summarizing a page.
- `web_crawl`: follow links and extract pages under bounded limits.
- `web_map`: discover a site's URL structure without full research synthesis.
- `web_research`: answer a research question with sources and citations.
- `web_snapshot`: capture visual evidence for a URL or active session.
- `web_extractor_save/get/list/delete`: manage reusable, revisioned browser
  extractors. Save updates increment the revision.
- `web_extractor_run`: queue a run and return immediately. Poll with
  `web_run_get`; use `web_run_cancel` or `web_run_retry` for lifecycle control.
- `web_extractor_schedule/schedules/unschedule`: manage Jobs-owned extractor
  schedules without constructing Jobs targets manually.

## Extractors

Use an extractor when the same structured browser workflow will run more than
once, needs pagination or interaction, or should be scheduled. Definitions use
`schema_version: 1`, an explicit `allowed_hosts` list, bounded `limits`, and
steps such as `goto`, `click`, `extract`, `paginate`, `wait`, and `screenshot`.

Values resolve in this order: extractor defaults, named preset, schedule
overrides, explicit run input. A run snapshots the complete definition, so a
retry remains reproducible after later edits. Complete datasets are stored as
JSONL and CSV artifacts; `web_run_get` returns bounded preview items and signed
artifact links.

## Defaults

The app opens computer browser sessions by default. Extraction prefers rendered
browser DOM and returns text, markdown, metadata, structured data, links, and
images. It falls back to HTTP retrieval when the active Computer backend cannot
expose DOM content. Check `extraction_backend` in tool responses when precision
matters.

Prefer `store: true` for durable research, crawls, and snapshots. Use
`snapshots: true` in `web_research` when the user needs visual proof.

## Cache controls

Responses include a top-level `cache` object. Use the defaults for ordinary
web work. Pass `cache: "bypass"` or `max_age: 0` when the user asks for latest,
today, prices, status, news, or other freshness-sensitive data. Pass
`cache: "refresh"` to force a live browser visit and update the cache. Pass
`cache: "force"` only when cached data is required and a miss should fail.

Default freshness windows: search is 15 minutes, extract/crawl/map are 24 hours,
research is 1 hour. Snapshot caching is disabled unless `max_age` is set.

## Attachments

After a high-value `web_search` or `web_research`, attach `web-result-card` for
the most relevant result when it improves the conversation. For screenshots,
use the returned signed storage URL directly or attach the existing computer /
screenshots component if the calling environment supports it.
