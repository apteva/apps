# Push

Push is a small, self-hostable relay between Apteva instances and Apple Push
Notification service (APNs). It intentionally sends generic notification copy;
private inbox content remains on the user's Apteva instance.

## Setup

1. Create an `Apple Push Notifications` integration connection with an Apple
   Team ID, APNs Key ID, `.p8` private key, app bundle ID, and environment.
2. Install Push globally and bind its `ios_provider` dependency to that
   connection.

Apteva generates a relay encryption key inside the connection automatically.
It is hidden from the setup form and Push reads it through the bound-connection
credential gate. The operator never needs to generate or paste an app secret.

## Device flow

The iOS app registers its APNs token with `POST /v1/devices/register`. Push
returns a `device.id` and a high-entropy `grant`. The app gives those values to
the user's Apteva instance. That instance uses the grant as a bearer token when
creating deliveries.

The grant is scoped to one device, expires after one year, and is stored only as
a hash. APNs device tokens are encrypted before they are written to SQLite.

## Public API

- `POST /v1/devices/register`
- `DELETE /v1/devices/{id}`
- `POST /v1/devices/{id}/test`
- `POST /v1/deliveries`
- `GET /v1/deliveries/{id}`

Except for registration, these routes require `Authorization: Bearer <grant>`.
Delivery types are limited to `approval`, `alert`, `report`, and `test`.

## Operator API

The authenticated settings panel uses:

- `GET /stats`
- `GET /devices`
- `GET /deliveries`
- `POST /admin/devices/{id}/test`
- `DELETE /admin/devices/{id}`

The MVP delivers synchronously and has no MCP tools. Rate limiting, App Attest,
Android/FCM, and a durable retry worker can be added without changing the
device/grant API.
