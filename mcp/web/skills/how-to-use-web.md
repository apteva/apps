---
name: how-to-use-web
triggers:
  - web_search
  - web_extract
  - web_crawl
  - web_map
  - web_research
  - web_snapshot
  - search
  - research
  - crawl
---

# Web

Use this app when the user asks for browser-backed search, page extraction,
crawling, site mapping, research, or visual proof.

## Tool choice

- `web_search`: discover pages from a query. Returns normalized JSON results.
- `web_extract`: read one known URL. Use before summarizing a page.
- `web_crawl`: follow links and extract pages under bounded limits.
- `web_map`: discover a site's URL structure without full research synthesis.
- `web_research`: answer a research question with sources and citations.
- `web_snapshot`: capture visual evidence for a URL or active session.

## Defaults

The app opens computer browser sessions by default. v0.1 extracts readable text
through an HTTP retrieval adapter after browser open because the computer app
does not yet expose DOM/readability export. Check `extraction_backend` in tool
responses when precision matters.

Prefer `store: true` for durable research, crawls, and snapshots. Use
`snapshots: true` in `web_research` when the user needs visual proof.

## Attachments

After a high-value `web_search` or `web_research`, attach `web-result-card` for
the most relevant result when it improves the conversation. For screenshots,
use the returned signed storage URL directly or attach the existing computer /
screenshots component if the calling environment supports it.
