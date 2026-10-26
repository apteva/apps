# Structured Demand Gen

Demand Gen extends the existing Ads campaign, ad group, creative, ad, activation,
resource and performance APIs. No provider query/mutation proxy is required.
Existing Search, website, mobile and provider-native options remain available.

## Create a campaign

Call `campaign_create` with `channel_type=demand_gen`, a name, an objective,
`optimization_goal`, `status=PAUSED`, and an `idempotency_key`.

Supported goals:

| Goal | Typical objective | Bidding |
| --- | --- | --- |
| `link_clicks` | `traffic` | Maximize clicks |
| `conversions` | `leads` | Maximize conversions / target CPA |
| `conversion_value` | `sales` | Maximize conversion value / target ROAS |
| `channel_subscriptions` | `engagement` | Maximize conversions / target CPA |
| `follow_on_views` | `engagement` | Maximize conversions / target CPA |

Choose exactly one positive `daily_budget_cents` or `lifetime_budget_cents`.
Total budgets require start and end dates. Budgets are not shared. Campaign
budget and goal configuration are created atomically. Dates use the account's
calendar day, represented by the v23 `startDateTime` and `endDateTime` fields.

`conversion_event_list` without a mobile-app selection discovers existing Google
conversion actions, reusing the project/account resource registry. Explicit
`conversion_action_resource_ids` select those local resource IDs. YouTube goals
can automatically select a single matching enabled action. YouTube-hosted
channel subscriptions remain distinct from paid app subscriptions.

Selection uses a reusable custom conversion goal and campaign-level configuration.
All standard campaign conversion goals are made non-biddable, so the selected
custom goal is exclusive. Account-wide goals and conversion-action primary flags
are not changed. Readiness verifies the effective provider goal selection and
that standard goals remain disabled. An external goal change must be reconciled
before activation.

## Add an ad group

`adset_create` keeps the existing parent campaign ID and adds:

- `placements`: `youtube_in_stream`, `youtube_in_feed`, `youtube_shorts`,
  `discover`, `gmail`, `display`; `youtube` is a shortcut for the three YouTube
  surfaces.
- `locations`, `excluded_locations`, `languages`: native Google constant IDs.
- `audience_ids`: one Google grouped Audience ID; these are different from
  customer-list IDs and local normalized audience resource IDs.
- `optimized_targeting`: optional boolean.

Campaign location/language selections are saved as defaults and inherited by new
ad groups. Google receives these criteria at the ad-group level. YouTube
engagement groups default to YouTube-only placements. Group creation and criteria
are atomic. Updates replace only the supplied targeting categories, preserving
unrelated criteria and exclusions.

`audience_list` with `audience_type=grouped` discovers suitable Google Audience
IDs. `audience_create_custom` with that type builds grouped audiences from typed
`user_list_ids`, `custom_audience_ids` or `user_interest_ids`. Interest IDs must
identify targetable segments (for example IN_MARKET or AFFINITY), not taxonomy
headings. Google API v23 does not offer deletion of these Audience resources.

## Creative assets and ads

Use existing creative tools to create/reference YouTube video and image assets.
`creative_create(format=image,image_url=...)` uses the existing image upload
pipeline. A Google `carousel` creative creates one reusable card from a headline,
a destination URL and existing landscape/square/portrait image asset IDs. Create
2–10 cards, with distinct image assets between cards.

`ad_create` accepts these structured formats:

| `ad_format` | Required assets/text |
| --- | --- |
| `responsive_video` | Video and logo asset IDs, business name, headlines, long headlines, descriptions, final URLs |
| `responsive_image` | Landscape and/or square image asset IDs, logo IDs, business name, headlines, descriptions, final URLs |
| `carousel` | 2–10 card asset IDs, one logo, business name, one headline and description, final URLs |

Use `video_asset_ids`, `logo_asset_ids`, `image_asset_ids`,
`square_image_asset_ids`, `portrait_image_asset_ids`, or `card_asset_ids` as
appropriate. The app validates customer ownership, asset types, image dimensions,
parent campaign type and format-specific text requirements. Existing Search-ad
requirements are still checked by the Search builder.

Google v23 rejects direct edits to Demand Gen creative fields. To change a
creative, call `ad_update` with the complete structured creative,
`replace_creative=true`, `status=PAUSED` and an `idempotency_key`. The app
atomically creates a paused successor and pauses the old ad, preserves its
history, records the successor, and returns `replacement_ad_id`. Activate that
new ID separately. Ordinary status-only updates continue to update the same ad.

## Readiness and reporting

- `account_context_get` advertises Demand Gen formats, goals and placements.
- `campaign_configuration_get` reads budget, schedule, bidding and native goal
  configuration plus saved structured settings.
- `adset_configuration_get` reads placements, audiences and targeting.
- `conversion_readiness_get(campaign_id=...)` checks the selected goals.
- Existing `delivery_activate` and status APIs enforce applicable checks.
- `performance_get` preserves its normal cached reports; `breakdown=placement`
  or `creative` returns separate live provider breakdowns. Asset impressions and
  clicks can overlap, and unavailable asset spend is null rather than fabricated.
- `conversion_performance_get` reports each action/source separately, including
  subscriptions and follow-on views with source `youtube`. Event rows never
  multiply delivery spend.

Google v23 exposes YouTube-hosted conversion actions but no direct link check for
an arbitrary selected channel. The app reports that channel link as unknown.
After verifying it in Google Ads, callers may explicitly provide
`acknowledge_unverified_measurement` at activation. This cannot bypass disabled,
mismatched or externally changed conversion goals. Creating paused campaigns
requires no such acknowledgement.

The existing Campaigns panel includes a structured creator, resumable drafts,
settings/targeting controls and conversion results. Requests use the same tools as
agents. Drafts and creation keys are isolated by install, project and account.

## Validation

`go test ./...`, `go test -race -p 1 ./...`, `go vet ./...`, Go build, Bun UI
tests, targeted TypeScript checks and the panel build cover the implementation.
The catalog suite is also run in an isolated synchronized catalog/embed layout;
the user's separate server checkout is not overwritten.

`TestDemandGenLive` is opt-in. It calls the app's actual structured handlers,
forwarding integration execution through an existing authenticated Apteva server
to Google Ads. It tests real paused video/image/carousel campaigns, daily/total
budgets, targeting, grouped audiences, configuration readback, campaign and group
updates, paused creative replacement, campaign-specific goal switching, conversion
reports and placement/asset breakdown queries. It removes its campaigns and
budgets, and never enables paid delivery. Immutable reusable assets and Audience
resources remain in the provider account where v23 has no deletion operation.

Example (use your own account/connection IDs and existing asset IDs):

```sh
env GOWORK=off ADS_DEMAND_GEN_LIVE=1 \
  ADS_LIVE_SERVER=https://your-apteva-server \
  ADS_LIVE_CONNECTION=123 ADS_LIVE_CUSTOMER=1234567890 \
  ADS_LIVE_VIDEO_ASSET=100 ADS_LIVE_LOGO_ASSET=101 \
  ADS_LIVE_IMAGE_ASSET=102 ADS_LIVE_SECOND_CARD_IMAGE=103 \
  ADS_LIVE_FINAL_URL=https://www.youtube.com/@yourchannel \
  go test -run '^TestDemandGenLive$' -v .
```

Read the API key at runtime from `ADS_LIVE_CONFIG` (default
`~/.apteva-prod/apteva.json`). Optionally set `ADS_LIVE_AUDIENCE_ID` to reuse an
existing grouped audience. Never commit credentials or evidence containing them.
Provider approval, actual serving and paid attribution are separate from this
paused-resource test.

Provider references: [campaigns](https://developers.google.com/google-ads/api/docs/demand-gen/create-campaign),
[channel controls](https://developers.google.com/google-ads/api/docs/demand-gen/channel-controls),
[campaign goals](https://developers.google.com/google-ads/api/docs/conversions/goals/campaign-goals),
[engagement prerequisites](https://support.google.com/google-ads/answer/12301500?hl=en),
[reporting](https://developers.google.com/google-ads/api/docs/demand-gen/reporting).
