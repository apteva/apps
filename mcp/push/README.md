# Push

Push is a small, self-hostable relay between Apteva instances and Apple Push
Notification service (APNs). It intentionally sends generic notification copy;
private inbox content remains on the user's Apteva instance.

## Setup

1. Create an `Apple Push Notifications` integration connection with an Apple
   Team ID, APNs Key ID, and `.p8` private key.
2. Install Push globally and bind its `ios_provider` dependency to that
   connection.

Apteva generates a relay encryption key inside the connection automatically.
It is hidden from the setup form and Push reads it through the bound-connection
credential gate. The operator never needs to generate or paste an app secret.

## Public relay domain

The Push panel can expose an operator-owned relay hostname such as
`push.example.com`. Push registers an `app://push` route with Apteva's
server-native ingress, which owns HTTPS routing and the managed certificate.

Bind the optional Domains dependency to publish the hostname's A, AAAA, or
CNAME record automatically. Push resolves the longest matching domain from the
selected project's Domains inventory. Without that binding, setup still creates
the ingress route and shows the exact DNS record to publish manually.

Detaching removes the ingress route and retains DNS by default. The admin API
accepts `?remove_dns=true` when the Domains-managed record should also be
removed. Managed certificate material remains cached by the platform.

## Device flow

The iOS app registers its APNs token with `POST /v1/devices/register`. Push
returns a `device.id` and a high-entropy `grant`. The app gives those values to
the user's Apteva instance. That instance uses the grant as a bearer token when
creating deliveries.

The grant is scoped to one device, expires after one year, and is stored only as
a hash. APNs device tokens are encrypted before they are written to SQLite.
Each device registration records its bundle ID and APNs environment, so one
team-scoped Apple connection can serve any number of iOS apps and both sandbox
and production tokens.

## Public API

- `POST /v1/devices/register`
- `DELETE /v1/grants/current`
- `DELETE /v1/devices/{id}`
- `POST /v1/devices/{id}/test`
- `POST /v1/deliveries`
- `GET /v1/deliveries/{id}`

Except for registration, these routes require `Authorization: Bearer <grant>`.
Delivery types are limited to `approval`, `alert`, `report`, and `test`.
The registration route is rate limited globally, by source, and by device token.
Revoking the current grant disconnects one Apteva instance without disabling
the same device for other instances. Re-registering that device with the same
instance rotates its previous grant.

## Operator API

The authenticated settings panel uses:

- `GET /stats`
- `GET /devices`
- `GET /deliveries`
- `GET|POST|DELETE /admin/relay-domain`
- `POST /admin/devices/{id}/test`
- `DELETE /admin/devices/{id}`

The current relay delivers synchronously and has no MCP tools. App Attest,
Android/FCM, and a durable retry worker can be added without changing the
device/grant API.
