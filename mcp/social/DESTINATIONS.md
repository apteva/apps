# Publishing destinations

Connect the identity that publishes **as** an account. Select where it publishes
with an optional, capability-driven destination API. Facebook Pages remain
publishing identities. Pinterest boards are destinations under a Pinterest account.

## Shared contract

`account_list` returns `destination_capabilities`, `default_destination_id`,
`default_destination_name`, and `account_kind`. Accounts without these capabilities
keep their existing publishing flow.

- `destination_list(social_account_id, cursor?, limit?)`: normalized `items` with
  IDs, names, descriptions and privacy; optional cursor and saved default.
- `destination_create(social_account_id, name, description?, privacy?)`: creates a
  destination without publishing or changing the default.
- `account_set_default_destination(social_account_id, destination_id)`: validates
  account access and sets the default for future posts.
- Authenticated HTTP equivalents: GET/POST/PUT `/destinations/accounts/{id}`.
  The path owns the account ID and the authenticated/query project owns scope.

A post target can use `destination_id`. `board_id` and `boardId` are accepted
compatibility aliases; conflicting values are rejected. Selected overrides are
validated through the exact account's provider connection. The resolved default
is stored in target options on create/update, before review or scheduling.
Changing a default does not change existing target options or approval revisions.
Drafts can exist without a board; choose one before submitting or publishing.

## Pinterest adapters

| Operation | Native | Zernio |
|---|---|---|
| List | `list_boards`, with bookmark pagination | `list_account_pinterest_boards` |
| Create | `create_board` | `create_pinterest_board` |
| Set default | Stored in Social | `set_default_pinterest_board` and stored in Social |
| Publish | `create_pin.board_id` | `platforms[].platformSpecificData.boardId` |
| Privacy | PUBLIC, SECRET | PUBLIC, PROTECTED, SECRET |

The Zernio operations extend the **existing** `zernio.json` catalog. The installed
integration catalog must contain them; an older catalog returns an actionable
error. No server or SDK implementation change is required. OAuth's existing
Pinterest default-board selection continues to work.

New native connections do not require an existing board and are deduplicated by
connection. Old native board accounts retain their IDs, history and scheduled
posts as `destination_preset` rows. Migration freezes their original boards into
existing targets. Known Zernio defaults are migrated too. If an old Zernio target
still relies on an unknown implicit default, changing the provider default is
refused until its destination is resolved explicitly, including mirrored schedules.

## Reporting

Native board analytics accept `scope=destination, destination_id=...`. Account
analytics use `scope=provider_account`; Zernio board analytics remain explicitly
unsupported rather than presenting account totals as board totals. Cache keys and
metric ledger scopes distinguish destinations. Home excludes board presets from
aggregate totals when their parent account is selected, while retaining their
individual rows. Import preserves each Pin's board; new provider imports capture
their board in their initial draft revision.

## Verification

`destinations_test.go` covers both adapters, direct/provider publication payloads,
pagination, inaccessible boards, project isolation, missing catalogs, default
freezing, legacy migrations, draft imports and distinct metric scopes. UI tests
cover create/select/default actions, pagination and late responses across project
or account switches. Existing Social workflow, OAuth, delivery and reporting tests
remain release gates. All provider calls in these tests are mocked.
