# Trips

Plan trips and undated ideas, including destinations, transport, stays,
activities, budgets, todos and calendar mirroring.

## Destination stay favourites (v0.9.0)

In a trip's **Itinerary**, each destination has a **Stay options** shortlist.
Use **Save a stay** to keep a hotel, Airbnb or rental with a listing URL,
optional photo URL, address, notes and estimated price. Prices can be per night
or for the whole stay. One option per destination can be marked **Top choice**.
Nearby hotel search can prefill a name and address when Google Places is connected.
Airbnb listings can be saved by entering their details and link manually.

Shortlists belong to a destination **within this trip**; they are not a global
library. They are visible while planning scheduled trips and undated trip ideas.
Saved options do not create calendar events or contribute to budget totals.

**Use this stay** lets you confirm check-in/out dates and the total estimate,
including any fees. Destination dates prefill the stay when the stop spans at
least one night. Selecting adds an **unbooked** accommodation linked to that
destination. It contributes to the budget and mirrors to the calendar if dated
and calendar sync is enabled. Alternatives stay saved. Repeating selection opens
or returns the existing accommodation without changing it or creating duplicates.

Changing a favourite leaves its selected accommodation unchanged. Edit the
itinerary stay to adjust the booking. Removing a favourite preserves an already
selected stay; deleting a destination removes its shortlist but preserves the
itinerary stays, clearing their destination link. Deleting a trip removes both.

## MCP

- `stay_favorites_list`: pass `destination_id` or `trip_id` (or matching both).
- `stay_favorites_add`: requires `destination_id` and `name`. Optional fields:
  `kind`, `listing_url`, `photo_url`, `address`, `price_amount`, `price_basis`
  (`per_night` or `total`), `currency`, `notes`, `top_choice`.
- `stay_favorites_update`: requires `id`; supports the same editable fields.
  The destination cannot be changed. `price_amount: null` clears the estimate.
- `stay_favorites_delete`: requires `id`.
- `stay_favorites_select`: requires `id`; optional `check_in_at`, `check_out_at`,
  `cost_estimated`, `currency`. Dates default from the destination. A nightly
  estimate is multiplied by calendar nights. An explicit total overrides it.
  Clearing both dates creates an undated stay; pass `cost_estimated: null` if
  its total is unknown. Changing currency requires an explicit total estimate.

Money values are integer **minor units** (EUR 125 = `12500`; JPY 125 = `125`).
The list and dashboard include `accommodation_id` when an option was selected.
The dashboard includes all options in `stay_favorites`, so planning agents can
see and compare saved preferences before choosing. Setting `top_choice: true`
clears the previous top choice for that destination.

```json
{
  "destination_id": 12,
  "name": "Kyoto station apartment",
  "kind": "airbnb",
  "listing_url": "https://www.airbnb.com/rooms/example",
  "price_amount": 12500,
  "price_basis": "per_night",
  "currency": "EUR",
  "notes": "Two bedrooms, near the station",
  "top_choice": true
}
```

HTTP equivalents are `GET/POST /stay-favorites`, `GET/PATCH/DELETE
/stay-favorites/{id}`, and `POST /stay-favorites/{id}/select`.

## Validation

```sh
GOWORK=off go test -race ./...
GOWORK=off go build .
bun test ui/stay-favorite-prices.test.ts
# From the apps repository root:
bun run scripts/build-panels.ts --app trips
```
