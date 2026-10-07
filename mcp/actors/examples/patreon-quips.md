# Patreon Quips actor example

Import `patreon-quips.json` as a user-defined actor, or merge its operations and defaults into a Patreon actor. Requires Actors 0.2.9+ and Computer 0.7.93+. It contains no account, context, post ID, asset, or credentials. Supply an authenticated creator `context_id` at runtime. Quips are public short updates; the reply permission controls who can reply, not who can view.

| Operation | Runtime inputs beyond `context_id` | Behavior |
| --- | --- | --- |
| `fetch_quips` | Optional `quip_list_pages` (default 5) | Ordered library rows: `content_summary`, `published_date`, and `status` for scheduled items. Summaries can include Patreon's image count. Does not invent post IDs or URLs. |
| `fetch_quip_stats` | `quip_url` | Impressions, seen, revenue, membership conversions, likes, comments, reshares. Metric values remain strings; `--` means unavailable. |
| `inspect_quips_composer` | None | Opens and closes the empty composer without posting. |
| `quip_preflight` | `quip_body`, optional `quip_reply_permission` | Verifies text/replies and discards the Quip. |
| `quip_post` | Above, plus `quip_request_id` | Publishes text and verifies the saved text/replies after reload. Canonical URL is in run `output.current_url`. |
| `quip_image_post` | Above, plus `quip_media_base64` | Uploads one image, requires a loaded preview, publishes, then verifies the saved image/text/replies. |
| `quip_image_url_post` | Same, with `quip_media_source_url` instead of base64 | Downloads an image URL through Computer's normal file-source handling. |
| `quip_image_preflight` | Body/replies and `quip_media_base64` | Verifies the loaded image and discards the Quip. |
| `quip_video_post` | Body/replies/request ID and `quip_media_base64` | Requires the native video player and “Video uploaded” before Share, then verifies saved video/text/replies. |
| `quip_video_url_post` | Same, with `quip_media_source_url` instead of base64 | Source must be a downloadable video file, such as MP4, rather than an HTML player page. |
| `verify_quip` | `quip_url`, expected `quip_body`/`quip_reply_permission` | Read-only saved-text/reply reconciliation. |
| `update_quip` | Above, `quip_expected_body`, `quip_request_id` | Checks the original saved body, edits the caption/replies, and verifies persistence. Leaves attachments in place. |
| `schedule_quip_preflight` | Body/replies, `quip_publish_date`, `quip_publish_time`, `quip_timezone_label` | Sets the schedule, reopens and checks every field, then discards. |
| `schedule_quip` | Above, `quip_publish_date_label`, `quip_request_id` | Schedules text, reloads the library, checks the saved row, reopens the saved editor and verifies date/time/timezone/text/replies. |
| `verify_scheduled_quip` | Same schedule expectations, no request ID | Read-only scheduling reconciliation. |

Use ISO dates (`2027-10-07`) and 24-hour times (`12:00`). `quip_timezone_label` must match Patreon's displayed timezone (for example PDT); it is required, never inferred from the machine timezone. `quip_publish_date_label` is the library's English date label for that same date (`Oct 7, 2027`). Supply the correct label for your date.

Saved schedule verification defaults to the first library row (`quip_scheduled_row_selector`). If other scheduled Quips sort ahead of the target, set this selector to its actual row. The actor requires the exact expected body, date label, and Scheduled status before opening that row; a mismatch fails verification rather than inspecting another post. The row selector remains a runtime input, not a permanent post identifier.

`quip_reply_permission` defaults to `No one`; accepted UI choices are `Anyone`, `All members`, `Paid members`, and `No one`. `quip_media_filename` and `quip_media_mime_type` are optional runtime inputs. URL operations suit larger assets; Actors' input-size limit applies to base64. Native video testing used a short MP4; larger uploads must finish inside the actor's configured run limit.

Keep the same operation and `quip_request_id` on retry. A durable once-key blocks another consequential click, including after an uncertain result. Never replace a failed request with a new ID, another publish operation, or a generic `run`; reconcile the existing post using the verification operations. A verification failure after Share/Schedule/Update may mean the action committed. The production actor retains its existing 240-second limit and zero step retries.

Exact text readback was tested with plain, single-paragraph text. Rich formatting or multiple paragraphs can change HTML whitespace and will fail closed if exact readback differs. Media operations publish one attachment per run; updating attachments and scheduling media are outside this example.

This is an editable site workflow example. Patreon selectors and English labels belong to its definition, not the Actors or Computer engines.

## Validation (2026-10-07)

Local creator checks completed for real text/image/native-MP4 publication, reload and saved-media/body/reply verification, caption editing, full scheduling with saved date/time/timezone verification, scheduled-test cleanup, populated library rows, and structured stats. Incorrect expected text failed before editing; reusing an image-post request ID failed before a second Share. An Anyone-replies preflight passed at the production 1600×800 viewport. Production smoke checks opened/closed the empty composer and correctly read the empty Quips library; no production content was posted. Existing Patreon operations, presets, browser settings and run limits were preserved. The generic Computer media-picker change passed browser tests for dynamic/native inputs and unsafe-target rejection plus the Computer Go test suite.
