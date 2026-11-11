# Editable YouTube channel actor

Import `youtube.json` as a general channel-management actor for work the YouTube Data API does not cover: community posts (including Members only), members-only video visibility, and a Studio content list that shows the visibility of every upload. No account, channel, handle or credentials are built in.

Keep a saved Computer login context and the channel identity together in a preset: `context_id`, `channel_url` (`https://www.youtube.com/@handle`), `channel_handle` (`@handle`) and `channel_id` (`UC…`). Every operation proves the context owns `channel_id` before acting: on youtube.com through the owner-only YouTube Studio link on the channel page, and in Studio through its channel navigation link. A different, signed-out or consent-gated session fails closed. `allowed_hosts` covers only `www.youtube.com`, `youtube.com` and `studio.youtube.com`, so a redirect to `accounts.google.com` or `consent.youtube.com` stops the run: reconnect the context through Computer and retry.

Selectors target the English UI. URLs carry `hl=en`, but set the account's own language to English as well.

## Operations

Read-only:

- `observe_posts_page`, `observe_studio_video` (`video_id`): calibration snapshots of the Posts tab composer and a Studio video's visibility control. Run these first on the real account and compare the semantic controls they report with the selectors below.
- `fetch_posts`: every community post on the channel (`id`, `post_url`, `text`, `published`, `members_badge`, `likes`). This is the duplicate check for post recovery.
- `fetch_videos`: public uploads from the Videos tab (`id`, `url`, `title`, `metadata`, `duration`, `badges`).
- `fetch_studio_content`: Studio's full upload list (`id`, `title`, `visibility`, `restrictions`, `date`, `views`), including Members only, unlisted, private and draft videos.

Community posts. Each operation has a `_preflight` that fills and verifies the composer, then stops before Post. Check on a test channel whether the composer keeps unsent text before relying on a preflight as side-effect free.

- `text_post`: `post_body`.
- `image_post`: `post_body`, `image_url`, `image_filename`, `image_mime_type`.
- `poll_post`: `post_body`, `poll_option_1`, `poll_option_2`.

All post operations accept `visibility_label`, the composer audience's exact live label: `Public` (the default), `Members only`, or a membership level as the composer shows it. The actor asserts the chosen label before posting. After posting, it reloads the Posts tab and asserts that the newest unpinned post has exactly `post_body` and that its feed badge equals `expected_feed_badge` (`""` for public posts, `Members only` for members posts).

Supply one stable `request_id` per logical post and reuse the same `actors_run.idempotency_key`. The Post click is guarded by a durable once-key shared by all three post types (`once_scope: community_post`), so the same `request_id` cannot post twice, even through an explicit retry. Recovery uses `fetch_posts` and matches on `text` = `post_body`. Read every dataset cursor before treating absence as proof.

Videos:

- `set_video_visibility`: `video_id`, `visibility_label` (the exact radio label in Studio's visibility dialog, such as `Public`, `Unlisted`, `Private` or `Members only`), `request_id`. Opens the video in Studio, selects the option, checks the pending value, saves through a guarded commit, reloads, and asserts the saved visibility. Uploading videos is out of scope; use the API for that, then use this operation to make the upload members-only.

## Calibration status

These selectors were checked against live public YouTube on 2026-10-31: the Posts feed (`ytd-backstage-post-thread-renderer`, `a[href*="/post/"]`, `#content-text`, `#published-time-text`, `#vote-count-middle`, the continuation sentinel) and the Videos grid (lockup view-model markup). The following were **not** verified, because they appear only to a signed-in owner:

- the owner-only Studio link on the channel page;
- the post composer (`ytd-backstage-post-dialog-renderer`, `#placeholder-area`, `#contenteditable-root`, `#visibility-button`, Image/Poll controls);
- the members-only feed badge (`ytd-sponsors-only-badge-renderer`);
- every Studio selector (`ytcp-*`, the visibility radios, the content table and its Next page control).

Before the first real post, run `observe_posts_page` and `text_post_preflight` with a test channel and correct any selector that fails. Publishing operations stop rather than guess when markup changes.
