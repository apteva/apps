# SaaS

Catalog products define independently sold SaaS products. Multiple SaaS plans
may point to prices under the same Catalog product and can be changed within
that product. Moving an account to a different Catalog product requires a
separate purchase or an explicit migration workflow. The SaaS panel exposes
this boundary with separate product and plan selectors and product-level
account filtering.

Shared SaaS access control for Apteva apps.

New paid plans default to `metadata.collection_method=charge_automatically`.
Set it explicitly to `send_invoice` for manual collection. Automatic plans save
the payment method during the first hosted checkout, then ask Billing to collect
later renewal invoices. The v0.8.1 migration pins older paid plans that omitted
this setting to `send_invoice`, so upgrading does not change their behavior.

SaaS generalizes the access, lifecycle, and live-usage parts that worked
well in Hosting without assuming every sold product is a container. Apps
such as CRM and Storage stay installed once; SaaS sells access to them
through Auth organizations, Entitlement grants, plan limits, and live
usage gauges.

## Boundaries

- Catalog owns product and price records.
- Billing owns invoices, customers, and payment links.
- Subscriptions owns recurring lifecycle state.
- Auth owns organizations, users, clients, and sessions.
- Entitlements owns grants and limits.
- SaaS owns plan composition, account state, and live usage gauges.
- Target apps own their own domain data and expose ordinary tools.

CRM is an optional companion. When installed, SaaS upserts each customer by
email through `contacts_upsert_by_channel`, stores the resulting contact ID,
and retries failed synchronization without blocking customer creation or
checkout. This sync follows the SaaS customer boundary: Auth identities such
as invited members, administrators, and service accounts are not implicitly
treated as CRM contacts.

SaaS does not install apps, run containers, or expose domains. Hosting can
become a target later through its existing tools, but SaaS does not modify
or depend on Hosting.

SaaS declares dynamic app calls so operators can meter whichever installed
apps they configure in usage sources. Target apps are still ordinary apps;
they do not need SaaS-specific hooks.

## Checkout

`saas_checkout_create` is the paid-sale entrypoint. For a paid plan it:

- creates or links the Billing customer;
- resolves the Catalog price;
- reserves an optional Catalog discount from the plan or `discount_code`;
- creates an idempotent Subscription and durable checkout record;
- passes Catalog's immutable discount application to Subscriptions and redeems
  the reservation after the Subscription is durable;
- creates the first subscription cycle and prepares its lines through
  Subscriptions before asking Billing to create the invoice;
- returns a payment link and leaves the account `past_due`, or returns a
  setup link for a card-required trial without creating an invoice;
- grants Entitlements during provisioning, while access checks only allow
  `active` accounts.

This keeps the sales flow inside SaaS without moving invoices, payments,
subscriptions, auth, or product data out of their owner apps.

Plans may set `catalog_discount_id` for one automatic promotion. Public checkout
may instead provide one `discount_code`; automatic and code discounts do not
stack. SaaS stores reservation and pricing state for recovery, while Catalog
owns eligibility and redemption limits and Subscriptions owns application by
billing cycle. Billing receives only the final prepared line items from
Subscriptions. Checkout responses include `discount` and `pricing` summaries
when a promotion applies.

If Subscriptions prepares a zero-total cycle, SaaS marks that cycle paid and
activates it without creating a Billing invoice or payment link. This covers
100% promotions without inventing a zero-value payment.

Paid plans with `trial_days > 0` and
`trial_requires_payment_method: false` in plan or Catalog price metadata
start as no-card trials. SaaS creates a `trialing` Subscription, activates
the account, runs fulfillment immediately, and does not call Billing until
payment is required at trial end.

When Subscriptions publishes `subscription.cycle_due`, SaaS prepares the
period lines through Subscriptions, creates and finalizes the Billing invoice,
links the invoice to the subscription cycle, and creates its payment link. The
operation is checkpointed by subscription cycle so delivery retries resume at
the last completed step. When Billing publishes `invoice.paid`, SaaS marks the
cycle paid and asks Subscriptions to transition to `active`; the resulting
`subscription.active` event activates and fulfills the SaaS account.

`saas_checkout_get` returns the durable status and current payment/setup URL.
Administrative manual payments use the separately permission-gated
`saas_checkout_mark_paid` tool. Public checkout cannot override plan pricing,
trial policy, provider, billing/auth IDs, lifecycle dates, or payment state.
When Billing publishes `invoice.paid` during an administrative payment, the
tool and event handler share the same idempotent operation: the loser waits for
the winner and returns the completed checkout instead of a false duplicate
payment error. `method` is checked before anything is recorded: `wire`
(default), `cash`, `check`, `other` or `stripe`, with `manual` accepted as
`other`.

### Conversion event

When a checkout's first invoice is paid, SaaS emits `saas.checkout.paid` once
with `checkout_id`, `account_id`, `plan_key`, `invoice_id`, `subscription_id`,
`amount_cents`, `currency`, `paid_at` and `idempotency_key`
(`saas:checkout_paid:<checkout_id>`). A `checkout.paid` row in the account
history is the durable guard, so a replayed `invoice.paid` or a payment
recovery never emits twice; consumers should still dedupe on the key. Free,
zero-total and trial activations do not emit it. Subscribe a Function or
webhook to it to send one server-side Purchase event per paid checkout.

### Auth org per account

Provisioning creates one Auth org per account by default. Products whose users
sign in to a shared org of their own set plan metadata `auth_org: "none"`: no
org is created and `auth_org_id` stays empty (reconcile does not create one).
On such a plan, `create_owner_user: true` needs the shared org passed as
`auth_org_id`, and fulfillment action args cannot reference `auth_org`.

## Plan Changes

`saas_account_change_plan` upgrades or downgrades an active subscription-backed
account between plans attached to the same Catalog product. A move to another
product is intentionally rejected because it may require a separate data and
fulfillment migration.

SaaS validates the product boundary, asks Subscriptions to create a durable
item replacement, coordinates any Billing invoice and payment link, replaces
Entitlements, and runs the target plan's `plan_upgraded`, `plan_downgraded`, or
`plan_changed` fulfillment action. The account plan is committed only after
those steps succeed. Subscriptions remains authoritative for item history,
cycle-effective changes, generic proration, and discount continuation.

Changes can be `immediate` or `next_cycle`. Immediate upgrades support
`prorate`, `charge_full`, and `none`; any positive adjustment is held until
Billing confirms payment. Next-cycle changes use `none` and are applied by the
Subscriptions due worker. Existing discounts can be `preserve`d or `drop`ped.
Immediate prorated downgrades are rejected until Billing provides a generic
credit-note workflow; operators can schedule them for the next cycle instead.

Every request requires an `idempotency_key`. `saas_plan_change_get` returns the
durable state and payment URL, and the recovery worker resumes interrupted
Billing, Subscriptions, Entitlements, and fulfillment steps without creating a
second invoice or subscription transition. Completion emits
`saas.account.plan_changed`.

SaaS exposes `saas.read`, `saas.checkout`, and `saas.admin` permissions. Plan,
fulfillment, account-management, usage-write, and manual-payment tools require
`saas.admin`; customer checkout requires `saas.checkout`.

## Cancelling accounts

`saas_account_cancel` cancels the account and then asks Subscriptions to cancel
the linked subscription (`subscriptions_cancel`, with the caller's `actor` and
an optional `reason`, default `account_cancelled`).

When the plan metadata sets `cancel_at_period_end: true` and the account has a
subscription, the cancel is scheduled instead (v0.13.0): the subscription is
cancelled at period end, the account stays `active` with access, and its
metadata records `cancel_scheduled` (`requested_at`, `reason`, `actor`,
`cancel_at`). When Subscriptions reports `subscription.ended`, the account
moves to `cancelled` and the plan's `account_cancelled` actions run (for
example releasing a hosted machine). Pass `immediate: true` to cut access at
once, including on an account whose cancel is already scheduled.

The request is recorded in the account metadata under `subscription_cancel`
(`requested`, `done` or `failed`, with attempts and the last error). A failed
call never rolls the account back: it is retried by a repeated
`saas_account_cancel`, by `saas_account_reconcile`, and by the
`subscription-cancel-sync` worker every 10 minutes. The worker also cleans up
accounts cancelled before v0.12.1 whose subscriptions are still active.
Reconciling a cancelled account only ensures the subscription is cancelled; it
does not restore plan access.

A cancel reported by Subscriptions (`subscription.cancelled` / `.ended`) is
marked as done and never sent back. A `subscription.cycle_due` renewal for a
cancelled account creates no invoice: SaaS records `renewal.skipped` and cancels
the stray subscription immediately.

## Recovery and account browsing

The checkout recovery worker resumes failed billing preparation and paid-invoice
activation, including expired processing leases. Billing inputs are saved before
external calls so retries retain the original cycle dates, collection policy,
and checkout redirect URLs. Existing operations without saved inputs recover
their dates from prepared lines or the original Subscription cycle. Recovery
does not start another collection attempt for an invoice simply awaiting payment;
Billing owns collection retries. Active leases are left alone, and polling is
paced so unavailable operations do not prevent other accounts from recovering.

Delayed collection-failure events are checked against the current Billing
invoice before changing access. An invoice that is now paid retains paid access;
actual refunds and voids still follow the configured policy.

Plan-change recovery polls all pending changes fairly, including when more than
100 changes are scheduled or awaiting payment. The panel pages accounts in groups
of 50 and shows account/status totals for the entire filtered result. Changing a
filter returns to the first page.

## Fulfillment Actions

Plans can define generic lifecycle actions. SaaS calls the configured
app/tool, expands `{{...}}` templates from the account/customer/plan
context, and stores selected output fields in account metadata. Actions
default to `once_per_transition`; set `execution_policy` to `always` only
for operations that are explicitly safe to repeat. SaaS reserves each run
before calling the target and builds a deterministic `idempotency_key`.
For app actions the key is passed only when the target tool lists
`idempotency_key` in its input schema (read once from the app's MCP
`tools/list` and cached for 10 minutes); tools that reject unknown fields,
such as `containers_run`, never receive it. If the tool list cannot be read,
the key is left out (v0.13.2). `integration_execute` actions always receive
it.

`account_past_due` actions do not run while the account is still in its
initial paid checkout (`checkout_status` is `processing` or
`awaiting_payment` and there was no trial): such an account never had
access, so there is nothing to suspend. SaaS records a
`fulfillment.skipped` event instead. Once the first invoice is paid, later
renewal failures run them as usual, and so does a failed first charge after
a trial (v0.13.1).

```json
{
  "plan_key": "container-pro",
  "event": "account_active",
  "app_name": "containers",
  "tool_name": "containers_create",
  "args": {
    "name": "saas-{{account.slug}}",
    "image": "nginx:alpine",
    "env": {
      "SAAS_ACCOUNT_ID": "{{account.id}}"
    }
  },
  "store": {
    "metadata.workload_id": "workload.id"
  }
}
```

Later lifecycle actions can use stored metadata:

```json
{
  "plan_key": "container-pro",
  "event": "account_past_due",
  "app_name": "containers",
  "tool_name": "containers_stop",
  "args": {
    "workload_id": "{{account.metadata.workload_id}}"
  }
}
```

Supported lifecycle events are `account_active`, `account_past_due`,
`account_suspended`, `account_resumed`, `account_cancelled`, `plan_upgraded`,
`plan_downgraded`, and `plan_changed`.

Fulfillment inputs and outputs are persisted as `redacted` by default. Common
credential fields are removed recursively, and actions can name additional
sensitive paths:

```json
{
  "persist_input": "redacted",
  "persist_output": "none",
  "sensitive_input_paths": ["provider.credentials"],
  "sensitive_output_paths": ["session.exchange_code"]
}
```

Persistence modes are `redacted`, `none`, and `full`. `full` must be selected
explicitly. Actions using `persist_output: none` cannot configure `store`
mappings because there would be no durable response available to resume the
mapping after an interrupted call. Sensitive output values cannot be mapped
into account metadata.

Historical cleanup runs in transactions of at most 128 records, checkpointing
each batch. Sanitized records are indexed by persistence version and skipped on
subsequent mounts. Changing an action's persistence mode or sensitive paths
invalidates that action's history so the next mount applies the new policy.

### Integration-backed fulfillment

`saas_connection_ensure` is the one-time credential-ingestion boundary. It
sends supplied fields directly to Server's encrypted, app-owned connection
store. With `account_id`, SaaS writes only the resulting numeric connection ID
to account metadata. Without `account_id`, it creates a project-wide provider
connection and returns its ID. Raw credentials must never be placed in plan
action arguments.

An action with `execution_kind: integration_execute` reads that numeric ID and
calls the configured integration tool. An optional `managed` block describes
restricted provider grants and a remote app bundle. Grant delivery tokens are
assembled only in memory and are excluded from persisted fulfillment input and
output.

Generic integration actions receive the fulfillment `idempotency_key` in their
outgoing input, including actions using an explicit `input` object. Configure a
target tool that honors this key for operations that must not be repeated.
Managed provisioning continues to use its stable `request_id` instead.
Revoked managed grants are disabled on the controller before the remote
provisioning request is sent, so the delegated token is revoked even if the
customer's instance is unreachable. A controller revocation failure leaves the
fulfillment action retryable and prevents remote delivery.

```json
{
  "plan_key": "phone",
  "event": "account_active",
  "execution_kind": "integration_execute",
  "tool_name": "provision_apply",
  "args": {
    "connection_id": "{{account.metadata.instance_connection_id}}",
    "managed": {
      "tenant_id": "{{account.id}}",
      "grants": [{
        "grant_id": "phone",
        "provider_connection_id": 72,
        "app_slug": "twilio",
        "allowed_tools": ["calls_create"]
      }],
      "bundle": {
        "bundle_id": "phone",
        "apps": [{
          "key": "telephony",
          "manifest_url": "https://example.com/telephony/apteva.yaml"
        }]
      }
    }
  }
}
```

The fulfillment run supplies the remote request ID and bundle revision. A
retry reuses the same desired-state operation, while a later lifecycle
transition receives a newer revision.

## Live Usage

Plan usage sources point at app tools and tell SaaS where to read the
quantity. For CRM contact count, SaaS can call the existing
`contacts_search` tool and read `total`:

```json
{
  "plan_key": "crm-pro",
  "app_name": "crm",
  "tool_name": "contacts_search",
  "feature_key": "crm:contacts",
  "read_path": "total",
  "call_args": { "limit": 1, "offset": 0 }
}
```

For apps that already expose gauges, the compatibility format is still:

```json
{
  "usage": [
    { "feature_key": "crm:contacts", "quantity": 812 },
    { "feature_key": "crm:seats", "quantity": 4 }
  ]
}
```

`saas_usage_sync` stores those values as current snapshots. It does not
write them to Entitlements' additive `usage_record` stream, because live
gauges such as contact count or storage size can go down.

Each source is synchronized independently with bounded concurrency and a
timeout. Successful complete responses replace the source's prior
generation; failed responses preserve the last good gauges and update the
source failure state. Access checks return `usage_unknown` when a configured
source has never succeeded or is older than its freshness threshold.

SaaS persists quota state per account and feature. The default warning
threshold is 80%; set `metadata.warning_threshold_percent` on a plan limit
to use another value from 1 through 99. Successful pulled or manually
recorded usage updates emit transition-only events:

- `saas.quota.approaching` when usage enters the warning range.
- `saas.quota.reached` when usage equals the limit.
- `saas.quota.exceeded` when usage is above the limit.
- `saas.quota.recovered` when usage moves down to a less severe state.

Repeated usage updates in the same state do not emit duplicate events. Event
payloads include the account, customer, plan, feature, quantity, limit,
percentage, threshold, and previous/current state. Quotas remain scoped to a
SaaS account (tenant); `auth_user_id` identifies its owner but does not make
the quota user-specific.

## Account and Billing Reporting

`saas_account_get` and `saas_account_list` return the account's nested SaaS
customer and a generic Billing summary. The account and customer retain their
own `created_at` dates. Billing summaries include first and last positive
payment dates, payment and paid-invoice counts, net totals grouped by currency,
the latest linked invoice, and projection freshness/completeness.

Account listing supports `customer_email`, `plan_key`, `status`,
`created_before`, `created_after`, `has_paid`, `paid_since`, `paid_until`,
`last_paid_before`, and `last_paid_after`, plus `limit` and `offset`. Date
filters are RFC3339. For example, callers identify accounts older than two
calendar months by calculating that boundary and passing it as
`created_before`.

Billing remains authoritative. SaaS already consumes Billing invoice lifecycle
events; on a linked invoice event it calls `billing.invoices_get` and replaces
only its local query projection for that invoice and its payments. Listing is
therefore a bounded local query rather than an N+1 set of Billing calls.
`saas_billing_sync` repairs or backfills linked invoices, and the checkout
recovery worker incrementally backfills projections missing after an upgrade.
`billing_sync_pending` and each summary's `data_complete` flag expose unfinished
backfill explicitly.

### Prepaid credit packs

Catalog defines one-time metered prices (`meter_key` + `unit_size`). Use
`saas_credit_pack_checkout_create` to send an account through the generic
Checkout/Billing flow. SaaS stores only the durable linkage and snapshots the
credit quantity; it grants Entitlements credits after a confirmed
`invoice.paid` using `billing:invoice:{invoice_id}:credit_grant`. Refunds append
negative compensating transactions keyed by payment/refund identity.
`saas_credit_balance` and `saas_credit_transactions` are facades over
Entitlements, and `saas_account_get` includes best-effort credits and
entitlement reporting alongside live gauges.

An `invoice.paid` event activates access only after Billing confirms that the
invoice status is actually `paid`. Partial payments update the reporting
projection but leave the subscription and account payment state unchanged.

## Prepaid Plans

`billing_mode: prepaid` accounts are active without a subscription; access
depends on the balance of a Credits wallet. Plan metadata:

| Key | Meaning |
|---|---|
| `credits_unit` | Required. Wallet unit, e.g. `usd` (Credits keeps currencies in micro-units: 1 usd = 1,000,000). |
| `signup_credit` | Optional promo credit granted once at activation (idempotent by `saas:signup:<account>`). |
| `signup_credit_expires_days` | Optional expiry for the signup credit. |
| `low_balance_threshold` | Wallet `low_threshold`; Credits emits `credits.balance.low` below it. |
| `max_output_tokens_estimate` | Output tokens assumed when a request sets no limit (default 4096). |
| `input_estimate_margin_pct` | Percent added to the estimated input tokens when pricing a hold (default 20; 0 turns it off; must not be negative). |
| `credential_provider` | `{app, create_tool, list_tool, revoke_tool}` for account API keys. Defaults to llm's `llm_tokens_*`. |

Activation adds a `credits_wallet` provisioning step (wallet ensure plus the
signup grant). `saas_access_check` also reads the wallet and returns
`allowed: false, reason: no_credit` when nothing is available.

### Meters

`saas_plan_meter_set` links `(plan_key, source_app, meter)` to Catalog
metered prices for `input`, `output`, `cached_input` and `per_request`. SaaS
only links; Catalog owns the prices, and SaaS caches them for the process
because they never change once created. `meter` is usually `model:<name>`;
`model:*` is the plan's default when no exact meter matches. Prices must be
metered, active and in the plan's `credits_unit`.

Catalog stores whole cents, so token prices use a large `unit_size` (for
$0.075 per million tokens: 75 cents per 10,000,000). A price of C cents per
U units costs `C * 10000 / U` micro-units each; every request is rounded up
once to whole micro-units. Cached input tokens are part of the input count
and use the cached price when set, otherwise the input price.

### Admission hook

Account keys point llm's admission hook at SaaS:

- `saas_usage_authorize` — reads `token_metadata.account_id` from a 5-second
  account cache, prices the worst case (estimated input plus the plan's
  `input_estimate_margin_pct`, default 20%, rounded up once; max output; one
  request) and calls `credits_reserve` with reference
  `<source_app>/request/<request_id>` and scope `project:<token_metadata.project>`.
  Returns `{allow: true, admission_ref: <hold_id>, max_output_tokens}` or a
  refusal. `max_output_tokens` is the output limit the hold was priced on: the
  request's own limit, else the plan's `max_output_tokens_estimate` (default
  4096). llm caps the provider request at it, so output never overruns the hold.

  | Reason | HTTP | error_type |
  |---|---|---|
  | No credit (`no_credit`) or cap reached (`cap_reached`) | 429 | `insufficient_quota` |
  | Account not active, wallet frozen, key not linked | 403 | `permission_error` |
  | Model has no price (`no_price`) | 400 | `invalid_request_error` |

- `saas_usage_settle` — prices the actual counts and calls `credits_commit`,
  or `credits_release` when the request failed. Retries are safe: Credits
  deduplicates by hold. If the meter disappeared since authorize, the hold is
  committed in full.

Each call is one cached lookup plus one Credits call: no Entitlements check,
usage sync or CRM. Both are generic: another app with the same hook passes
its own `source_app` (and optionally `meter`) and adds meters for it.

### Account API keys

`saas_account_keys_create` (label, optional `project` such as Production,
optional `prefix`/`scopes`) calls the credential provider with
`metadata: {account_id, project}` and the SaaS admission hook, stores only
the token id, and returns the secret once. `saas_account_keys_list` and
`saas_account_keys_revoke` are scoped to the account. Suspended accounts are
refused at authorize; cancelling an account revokes its keys.

Account spaces (Production / Staging / Development) are not modelled in SaaS:
`project` is a free-text label carried in token metadata and used as the
Credits cap scope, so caps per space work through `credits_caps_set`.

`saas_account_get` and `saas_account_list` include the Credits wallet for
prepaid accounts. Top-ups, caps and the statement are called on Credits
directly (`credits_topup_create`, `credits_caps_set`, `credits_entries_list`).

## Included Usage

A plan can include a recurring allowance per meter, such as 60 free minutes
a month (v0.14.0). Set it in the plan metadata:

```json
{
  "included_usage": [
    {"source_app": "telephony", "meter": "call:minutes", "amount": 60, "unit": "minutes", "period": "month"}
  ]
}
```

- `source_app` defaults to `llm`. `meter` matches exactly, or `<kind>:*` as
  a fallback, like plan meters.
- `unit` names the allowance's own Credits wallet for the account. It must
  differ from the plan's `credits_unit` and be unique within the plan
  (use `call_minutes` / `video_minutes` for two minute allowances).
- `period` is `month` (UTC calendar month, the default) or `billing_cycle`
  (the subscription's current period; paid subscription plans only).
  Allowances always expire at the period end (`expires: period_end`).

**Grants.** Each period SaaS calls `credits_grant` into the allowance wallet
with `expires_at` = period end, priority 1 and
`source_ref = saas:included:acct:<account>:meter:<app>/<meter>:period:<start>:plan:<plan>`,
so a retry never grants twice. Grants run when an account is activated or
resumed, after a plan change, after a paid renewal, and from the
`included-usage` worker (every 10 minutes; each account is checked again at
its period end, or after 10 minutes if a grant failed). Free plans get
calendar-month grants from the worker; no payment event is needed.

**Plan changes.** Only the difference over what was already granted this
period is added (60 to 100 adds 40). A downgrade adds nothing and never
takes back credit.

**Spending.** Quantity meters report `estimated_quantity` to
`saas_usage_authorize` and `quantity` to `saas_usage_settle`. Authorize
holds what the allowance can cover and prices only the rest from the paid
wallet through the meter's `unit_price_id` (set with `saas_plan_meter_set`).
When the allowance is used up and the plan has no paid price for the meter
(free plans), the request is refused with reason `included_exhausted`.
Accounts on free or paid subscription plans can use metered usage up to
their allowance; only prepaid plans spend paid credit. Settle takes the
actual quantity from the allowance first; anything beyond it goes to the
paid hold. The admission ref is `inc:<allowance hold>:<units held>:<paid
hold or 0>`; plain hold ids from earlier versions still settle.

**Reporting.** `saas_account_included_usage` returns, per allowance,
`included`, `used`, `remaining`, `period_start` and `resets_at`, plus the
paid balance for prepaid plans ("42 of 60 free minutes left, resets 1 Dec").
`saas_access_check` adds `included_usage`, and a prepaid account with no
paid credit stays allowed while an allowance has units left.
`saas_usage_get` with `account_id` adds `included_usage` and `paid_balance`.

Plan limits' `reset_interval` is deprecated: Entitlements stores it but never
resets usage. Use `included_usage` for recurring allowances.

## Transactional Email

When the optional messaging app is installed, SaaS emails the account owner
at four points: the hosted payment link when a manual-collection cycle (or an
automatic-collection fallback without a reusable payment method) creates one,
a receipt when an invoice is paid, a failed-payment notice on
`invoice.payment_failed` and `invoice.payment_action_required` (voided and
refunded invoices are operator actions and stay silent), and a trial-ending
reminder swept every 10 minutes for active accounts whose `trial_ends_at`
falls within the next 48 hours.

Prepaid accounts also get a low-balance notice on `credits.balance.low` (at
most one per account per day) and a top-up receipt on `credits.topup.paid`.

Every send is best-effort and never blocks payment collection or activation.

Since v0.13.1 every email is first stored in an outbox (`saas_email_outbox`),
then sent through `messaging.send_message` with `channel: "email"`, the
owner's plain address and an idempotency key:

- **Sender.** Set `email_from` (and optionally `email_from_name`) in the
  plan metadata. Without it, messaging uses the project's default email
  sender. Either way the address must be a verified, sending-enabled sender
  in messaging for that project, or the send fails.
- **Outcome.** A messaging status of `failed` (or an error) is recorded as
  `notification.failed`; anything else, including `pending`, is recorded as
  `notification.sent`. A `pending` email is never retried, so the customer
  never gets it twice.
- **Retries.** The `email-outbox` worker (every 2 minutes) retries failed
  emails with backoff from 2 minutes up to 6 hours, and gives up after 10
  attempts (outbox status `abandoned`). After an error it reuses the same
  idempotency key; after a stored `failed` message it uses `<key>:retry:N`,
  because messaging returns the stored failed row for a repeated key.

The trial reminder marks `trial_reminder_sent_at` in account metadata once
delivered; failed attempts are throttled to one per six hours until the
window closes, on top of the outbox backoff.
