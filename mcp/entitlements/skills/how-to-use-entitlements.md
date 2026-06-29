# Entitlements

Use Entitlements to grant, revoke, check, and meter access.

Do not encode billing rules here. Billing, Checkout, Subscriptions, or admin workflows create grants. Apps such as courses, SaaS features, communities, and downloads call `entitlements_check`.

Good feature keys are explicit and stable:

- `course:123:view`
- `course:123:certificate`
- `plan:pro`
- `api:requests`
