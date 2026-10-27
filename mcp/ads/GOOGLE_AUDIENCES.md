# Structured Google audiences

The existing audience lifecycle tools and Audience workspace now include Google
custom segments, grouped audiences, first-party remarketing seeds and lookalikes.
Meta's customer lists, Page engagement and percentage-based lookalikes retain
their existing APIs. No raw provider mutation tool is added to Ads.

## Discovery and capability contract

Start with `audience_capabilities_get(ad_account_id)`, or read
`account_context_get.capabilities.audiences`. The existing `kinds`, `operations`,
`sync_modes`, `sources` and privacy fields remain. Additive fields include:

- `create_kinds`: types the provider can actually create.
- `by_kind`: supported operations and update fields, rather than implying that
  every discovered audience supports deletion or member uploads.
- `custom_segment`: member types, interpretation modes and required integration.
- `lookalike`: supported expansion levels, countries, eligible seed types,
  minimum matched membership and provider-evaluated eligibility.
- `engagement`: Google API creation restriction and an Audience Manager setup URL.
- `grouped`: typed composition and the local-resource targeting field.

`audience_list` and `audience_refresh` discover all Google user lists,
CustomAudience segments and Audience groups into the same project/account-scoped
registry. `audience_list(type=...)` filters normalized types. `audience_get` uses
its local numeric ID, as before. Provider types distinguish identical native IDs
across resource collections. Read-only lists do not advertise update/delete/sync.
Unknown provider list types are not mislabeled as writable customer lists.

`targeting_catalog_search(type=interest, query=...)` returns Google interest IDs,
names and native taxonomy metadata. Location and language searches remain.

## Custom segments

```json
{
  "ad_account_id": 6,
  "type": "custom_segment",
  "name": "Software buyers",
  "segment_mode": "interests",
  "keywords": ["workflow automation", "business software"],
  "urls": ["https://example.com"],
  "apps": ["com.example.app"],
  "idempotency_key": "software-buyers-1"
}
```

Pass this to `audience_create`; the `audience_create_custom` alias accepts
`audience_type=custom_segment`. `interests` maps to Google's `AUTO`, and
`search_terms` maps to `SEARCH`. Newly created segments do not use Google's
legacy `INTEREST`/`PURCHASE_INTENT` enum values. Keywords, URLs and Android package
names are distinct member signals, combined with OR semantics. URLs describe
interests; they do not restrict placements to those websites.

`audience_update` supports metadata and these member collections. Supplied
collections replace that collection; omitted collections and the interpretation
mode are merged from a fresh provider read. Provider-canonical URL domains
without a protocol remain editable. Unrecognized native member types block a
member edit rather than being silently dropped.

The UI provides creation and member editing. `audience_delete` uses the custom
segment service after checking direct targeting, grouped composition and seed
references. Member sync remains restricted to writable Customer Match lists.

## Lookalikes

```json
{
  "ad_account_id": 6,
  "type": "lookalike",
  "name": "Similar customers in Spain",
  "source_audience_ids": [42, 43],
  "country_codes": ["ES"],
  "expansion_level": "balanced",
  "idempotency_key": "similar-customers-es-1"
}
```

The seed IDs are local audience IDs in the same project/account. Eligible
first-party user lists include Customer Match, website/app and existing
remarketing lists. Custom segments, grouped audiences and other lookalikes are
not seeds. The single `source_audience_id` remains available; Meta's legacy
native string-ID calls remain compatible.

Google requires at least 100 active matched people across eligible seeds.
Display/search size estimates are not treated as proof of matched seed
membership. Google evaluates eligibility and account access; restrictions retain
the provider diagnostics and return `audience_ineligible` with guidance. Creation
success does not claim immediate readiness: Google can take 2–3 days to prepare a
segment. Narrow/balanced/broad expansion is independent of the ad group's
`optimized_targeting` setting. Google's percentage sizes are not modeled as
Meta's `ratio` field.

An identical existing seed/country/expansion definition is reused, because Google
rejects duplicate definitions. Google v23 rejects lookalike metadata updates and deletion with
`USER_LIST_MUTATE_NOT_SUPPORTED`, even when the list reports `readOnly=false`.
The app therefore advertises these lists as immutable; changes require a new
definition. This restriction was verified against the connected account.

## Groups and campaign targeting

Create `type=grouped` with `segment_audience_ids` containing local user-list or
custom-segment IDs, optionally plus discovered `user_interest_ids`. The result is
a normalized local audience resource, usable with:

```json
{
  "ad_account_id": 6,
  "campaign_id": "123456",
  "name": "Selected audience",
  "audience_resource_ids": [44],
  "optimized_targeting": false,
  "placements": ["youtube_in_feed"],
  "status": "PAUSED"
}
```

Use `adset_create` or `adset_update`. Demand Gen accepts one grouped Audience per
ad group. `audience_resource_ids` resolves local ownership and is mutually
exclusive with native `audience_ids`. Existing native `audience_ids` calls remain
valid. The Campaign creator and settings provide named group selection and an
explicit optimized-targeting control. New UI drafts default expansion off;
legacy tool calls that omit the setting retain their prior behavior.

Groups support metadata updates. An API caller can explicitly replace all
segments with `audience_update(replace_segments=true, segment_audience_ids=...,
user_interest_ids=...)`. Other dimensions are replaced as part of that explicit
operation. Groups cannot contain other groups. Google has no Audience remove
operation, so group deletion is not advertised. Existing
`audience_create_custom(audience_type=grouped)` and native group-list response
shapes remain compatible; creation also records the local audience resource.

## YouTube seeds: provider restriction

Google Ads supports viewer/subscriber data segments in its UI, but the Google
Ads API has no YouTube-list creation operation. Create those lists in
[Audience Manager](https://ads.google.com/aw/audiences/management), then refresh
here. Existing lists can be composed into groups or used as first-party
lookalike seeds when Google allows them. The API's `REMARKETING` type does not
reliably identify a specific channel/action, so the app reports the source as
unknown rather than inferring that every remarketing list is a subscriber list.

## Integration dependency and verification

Ads 0.1.52 requires integrations catalog 0.54.3 or newer, containing
`google-ads.custom_audience_mutate`, for structured custom segments. CustomAudienceService is not part of Google's
`bulk_mutate` operations. The companion integrations change adds only the typed
HTTP endpoint; existing Google Ads OAuth scopes cover it. Other Ads operations
continue using existing catalog endpoints. Publishing these releases does not
upgrade a running installation or activate delivery.

The opt-in `TestGoogleAudiencesLive` exercises the app's public handlers against
the connected real Google account. Existing tools use the server integration.
For the unpublished custom-segment endpoint,
`integrations/test/google-audiences-live-executor.ts` uses the new catalog's real
HTTP executor and the existing connection credentials in process memory. It
neither prints nor saves credentials. Its owner-only credential read is audited
by the server. This validates both repos without installing either in production.

```sh
ADS_GOOGLE_AUDIENCES_LIVE=1 \
ADS_LIVE_CUSTOM_EXECUTOR=/absolute/path/to/integrations/test/google-audiences-live-executor.ts \
GOWORK=off go test -run '^TestGoogleAudiencesLive$' -v .
```

Optional `ADS_LIVE_SERVER`, `ADS_LIVE_CONFIG`, `ADS_LIVE_CONNECTION`,
`ADS_LIVE_CUSTOMER`, `ADS_LIVE_EVIDENCE` and `ADS_LIVE_GROUP_ID` override defaults.
Reusing a test group avoids another non-removable fixture. The test removes its
custom segment and paused campaign/budget. Named reusable group and lookalike fixtures remain: Google provides no Audience
remove operation and v23 rejects lookalike removal. Existing identical lookalikes
are reused to avoid additional permanent fixtures. Sanitized results are
in [validation/google-audiences-live.json](validation/google-audiences-live.json).
The validation verifies configuration and requests, not paid reach or attribution.

## Provider references

- [Custom segments and AUTO/SEARCH modes](https://developers.google.com/google-ads/api/docs/remarketing/audience-segments/custom-audiences)
- [Lookalike seed requirements and preparation time](https://developers.google.com/google-ads/api/docs/remarketing/audience-segments/lookalike-audiences)
- [UserList v23 resource](https://developers.google.com/google-ads/api/reference/rpc/v23/UserList)
- [CustomAudience operations](https://developers.google.com/google-ads/api/reference/rpc/v23/CustomAudienceOperation)
- [Audience operations](https://developers.google.com/google-ads/api/reference/rpc/v23/AudienceOperation)
- [Google API team's YouTube-list creation restriction](https://groups.google.com/g/adwords-api/c/aw-cDKVzqJI)
