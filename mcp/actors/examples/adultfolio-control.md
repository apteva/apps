# Editable AdultFolio actor

Import `adultfolio-control.json`, then set `defaults.context_id` to the saved Computer login context and `defaults.account_url` to the authenticated account's canonical profile URL. All provider selectors and workflow policy are in this editable example. No credentials or real account IDs are shipped.

Operations:

- `inspect_page` / `inspect_controls`: verified account, current URL and visible semantic controls/navigation. Optional `page_url`.
- `get_search_options`: current profile/distance/order/gender/country filter options, with ordered value and label arrays.
- `search_models`: `model_query`. Searches the authenticated AdultFolio site and returns genuine model cards with numeric model ID, canonical profile URL, message URL, role, references and response rate.
- `list_conversations` / `list_threads`: inbox conversations, deduplicated by numeric model ID. AdultFolio has one conversation per correspondent.
- `list_messages` / `get_thread`: `model_id`, `model_url`. Verifies the requested correspondent and reads older messages. Returns server message IDs, sender URL, incoming/outgoing direction, timestamps, body and attachment/image URL arrays. Messages are emitted in discovery order; sort by `sent_at` and numeric ID for a chronological display.
- `prepare_reply`: `model_id`, `model_url`, `message`. Fills and verifies the real visible composer; never clicks Send.
- `reply`: the same inputs plus a stable `request_id`. Revalidates account, thread, recipient and exact composer text. Sends once through a fresh semantic target, reloads the conversation, and verifies a new server message ID, the outgoing account and exact saved text. A reserved/uncertain request ID must never be automatically replaced with another ID to retry.
- `verify_reply`: `model_id`, `model_url`, `message`, `baseline_message_id`. Read-only recovery check after an uncertain send. The baseline must be the last server message ID seen before sending.

Read coverage is explicit. `inspection_complete=false` or `more_results_remaining=true` means absence was not proved. Consumers must read all dataset cursors and inspect coverage even if the run failed. Bounded inbox/history reads return successful partial datasets with `inspection_complete=false` and `more_results_remaining=true`. Identity, inaccessible-view, changed-collection, timeout or truncated-HTML failures remain failures; login/identity changes, unverified empty views, ambiguous controls and stalled pagination stop safely. Large name queries may exceed the rendered-HTML limit; narrow the query instead of treating failure as no results. `read_pages` (default 5), `max_pages`, `max_items` and `max_duration_seconds` can be supplied as inputs for bounded previews or full inspections.

This example intentionally has no sample recipient or message. Live sends require an explicitly selected recipient and approved message. Reply confirmation checks are independent of the effect guard: a click alone is never reported as a saved reply.

`search_filtered_models` accepts an explicitly constructed `search_results_url`, using values returned by `get_search_options` (the profile value for models is `model`, not `models`). The default bounded reads are suitable for recent conversations/messages; a complete historical crawl may reach provider or HTML limits and must never be described as complete.

Keyword and filtered search readiness waits for result cards or explicit empty-state text, rather than the initially empty AJAX container. Message pagination waits five seconds after navigation and checks loading text. Some site histories leave their older-message control present after returning no new records; stalled reads retain the retrieved dataset but fail with incomplete coverage. Consumers must not interpret that failure as a complete conversation.
