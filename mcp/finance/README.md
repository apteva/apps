# Finance

Unified personal finance, portfolio and bank-account tracking. Version **0.2.1** adds Enable Banking imports and reviewed payments through five payment providers, alongside the existing ledger.

## One connection selection for all financial operations

The manifest declares one optional integration dependency: **Financial connections**, with `mode: multiple`. Select bank and brokerage connections here once, and choose a default. Account discovery, bank sync, brokerage imports, payment preparation, authorization and status checks all resolve through this same binding. Connections not selected in this field cannot be used by Finance.

The internal role key remains `open_banking`, so existing account-data selections continue working, including legacy single-ID bindings. When upgrading from v0.2.0, add any connections previously selected only under **Bank payments** or **Brokerage import** to this field. Reuse the saved connections; new credentials are not needed. TrueLayer and Salt Edge's distinct upstream data/payment products are selectable together in the same field.

## Banking providers

| Integration | Import account data | Real payments in Finance |
|---|---|---|
| Plaid | Yes | UK/EU payment initiation; US ACH and same-day ACH through MCP |
| Teller | Yes | Beta Zelle payments, where supported by the institution |
| Enable Banking | Yes, using an authorized session | SEPA / instant SEPA; optional deferred execution |
| Nordigen / GoCardless Bank Account Data | Yes | Data-only product |
| TrueLayer | Yes | Installed connector is the Data API |
| Salt Edge | Yes | Account Information only |
| TrueLayer Payments | Separate Data connection | EUR/GBP payments through its hosted page |
| Salt Edge Payments | Separate Account Information connection | SEPA payments through Widget |

Connectors, bank support and enabled products determine actual availability. The capabilities shown in Finance describe the supported implementation, not a guarantee that an individual connection has payment-product access. No live payments are part of development validation.

## Enable Banking

1. Configure the `enable-banking` integration with its application ID and RSA signing key. These credentials stay in the platform integration.
2. Use its `list_banks` and `start_authorization` tools to choose a bank and request consent with a registered redirect URL and unpredictable state.
3. Complete bank consent, verify callback state, then exchange the returned code with `authorize_session`.
4. Select the connection in Finance's Banking tab and enter the resulting `session_id`. Discover and link the accounts, then sync. The MCP `banking_discover` / `banking_link_account` tools accept the same `session_id`.
5. After consent renewal, discover/link using the new session ID. Finance matches `identification_hash` within the same connection to preserve the ledger when the account uid changes.

Sync verifies consent and account membership, follows continuation keys even after empty pages, detects repeated cursors, deduplicates by `entry_reference`, and imports only booked transactions. Debit/credit signs and decimal amounts are parsed explicitly. Current booked balances (`CLBD`/`ITBD`) can reconcile the ledger; available balances are not substituted. If a bank only returns an available balance, transactions still import but there is no balance reconciliation. Background requests do not fabricate PSU headers.

The existing Finance currency model uses hundredths. Imports reject known currencies with different minor-unit scales rather than display wrong amounts. Transactions lacking a stable entry reference are rejected for manual review rather than risking duplicates.

## Real payments

`txns_transfer` remains ledger-only. Use **Bank payments & transfers** in the Banking tab or these MCP tools:

- `banking_payment_prepare`: prepare an immutable request using a stable `request_key`. Money is integer minor units (`1234` means 12.34). No provider payment is submitted.
- Review the returned amount, currency, source/direction, recipient identifiers and reference.
- `banking_payment_submit`: submit the exact request ID with `confirmed=true`, after authorization for those details. Drafts expire after 30 minutes.
- `banking_payment_authorize`: obtain the provider-hosted authorization link or Teller Connect MFA handoff when needed.
- `banking_payment_get` with `refresh=true`: fetch provider status. For a submission with an unknown provider ID, reconcile using a provider ID from bank records; Finance verifies matching details. Teller can list matching candidates, whose dates and references must be checked before selection.
- `banking_payments_list`: list the latest 100 project requests and provider capability notes.
- `banking_payment_cancel`: cancel a local draft, or request eligible Plaid ACH cancellation with `confirmed=true`; the bank rechecks eligibility.

- `banking_payment_options`: list PIS banks, Teller schemes or existing Plaid recipients; supports recipient/bank pagination.
- `banking_payment_callback`: verify the complete return URL against the prepared return URL and persisted random state.
- `banking_payment_continue`: execute after bank authorization, with `confirmed=true`, for Enable Banking deferred payments or Plaid ACH challenges.
- `banking_payment_events`: synchronize Plaid Transfer events from a durable connection/project cursor.

### Enable Banking payments

Enable PIS on the application and bind its connection to Finance. Load payment banks, choose a bank and supported SEPA/instant SEPA type, then enter the recipient name/IBAN, EUR amount, reference, registered HTTPS return URL and payer IBAN if required. Finance checks the bank's current capabilities before preparation and submission. Banks requiring extra fields beyond the simple SEPA form are rejected before creation with the missing field identified.

For banks advertising deferred submission, select **Authorize first, then confirm execution**. After opening the authorization link and completing consent, paste the complete return URL into Finance. It verifies the return URL and random state; a separate confirmation invokes `submit_payment`. The provider enforces authorization and execution eligibility. Neither a return URL nor an authorization status is treated as settlement. Unknown execution outcomes are reconciled by status reads and never replayed.

### TrueLayer Payments

Bind `truelayer-payments` using Payments credentials, signing key and the matching environment. Provide the payer name/email and a registered HTTPS return URL. EUR payments use the recipient IBAN; GBP uses sort code/account number. TrueLayer's hosted page handles bank selection, bank-specific forms and consent. Refresh provider status afterward. This release supports external-account payments, not merchant-account payouts, refunds or mandates.

### Salt Edge Payment Initiation

Bind `saltedge-payments` using PIS credentials/signing. Load a payment bank and supply a PIS customer ID, the actual initiating user's IP address, debtor and creditor IBANs, recipient name, EUR amount/reference and registered HTTPS return URL. Finance reads the SEPA template before preparation and opens the returned Widget URL for consent. Other templates and additional bank-specific attributes are outside this form. A provider validation failure is shown for reconciliation rather than blindly retried.

### Plaid payment initiation

Enable the Payment Initiation product. Register the payer with the integration's `create_user`, and the recipient with `create_payment_recipient`. Finance lists existing recipients and verifies the selected recipient ID/name with Plaid before review. It accepts `user_id` and `recipient_id`, a recipient name, EUR or GBP amount, payer bank country and reference. Bank authorization uses Hosted Link and the payer's user ID. The payer selects/authorizes the source bank through that flow; a Finance source-account selection is not imposed on Plaid Link.

### Plaid ACH

Enable the US Transfer product and link a USD bank account to Finance. Supply `legal_name`, `ach_class`, `direction` (`credit` pays the linked bank account from the Plaid funding account; `debit` pulls from it), and network (`ach` or `same-day-ach`). The panel currently selects standard ACH; MCP also exposes same-day ACH.

Finance creates and persists an authorization before creating the transfer. Declined authorizations never proceed. A `user_action_required` decision opens Hosted Link with `transfer.authorization_id`. Complete Link, then explicitly continue the same reviewed request. Finance rechecks authorization using the same idempotency key before transfer creation. Declines and risk-decision overrides never create a transfer.

### Teller

Enable beta payment access at a supported bank and link its USD account. Provide the recipient's Zelle email/phone and name. MCP also allows `recipient_type=business`; the panel defaults to a person. Finance verifies `account.links.payments` and the OPTIONS Zelle capability before submission. Payments use the JSON payee payload and stable `Idempotency-Key`.

If MFA is required, provide the Teller application ID and matching environment in the panel and complete Teller Connect. A Connect callback is not treated as proof of settlement. Refresh and, where Teller initially returned only a Connect token, reconcile the provider payment ID. A Teller payment record without a status is shown as `recorded`, not as settled.

## Duplicate protection and recovery

Requests and submission state are stored in SQLite. A compare-and-set claims each draft before any bank write; duplicate clicks/concurrent submit calls do not replay it. Provider idempotency keys are derived from the persisted request ID. A transport failure or process interruption leaves an uncertain/submitting request for reconciliation, never automatic re-submission. The same request key cannot be reused with different details, and a provider payment cannot be attached to multiple requests on the same connection.

Payment submission does not create a booked ledger entry. Import the actual bank transaction through sync to avoid double-counting. Provider status remains visible in its own terminology.

## Status updates and runtime prerequisites

The `payment-status` worker runs every five minutes. It rotates through up to 100 known payments from the last 90 days, including settled payments, to catch later status changes. Plaid Transfer event sync also catches older returns/reversals using a durable cursor, refreshed from authoritative transfer records. A failed status read does not advance the event cursor. The worker never initiates, authorizes or cancels payments.

Install/update the integration runtime and catalog containing the payment additions and signing implementations described in `integrations/docs/payment-products.md`. Publishing Finance does not deploy that server update or activate payment products. Verified webhook ingress remains in the integration platform; Finance currently uses polling/event sync rather than a direct webhook subscription. No live bank account or transfer was used for release validation.

## Validation

Go tests cover the existing audit fixes plus concurrent submissions, unknown outcomes, project isolation, changed/archived sources, expired drafts, invalid amounts, Plaid authorization/ACH decisions, Teller MFA/reconciliation, and Enable Banking pagination/renewal. Bun tests cover exact amount entry/display. The payment review/submission UI was exercised against a local mock with no real bank calls.

Use `go test -race ./...`, `go vet ./...`, `go build .`, and `bun test ui/BankPayments.test.ts` from this directory. Build the panel with `bun run scripts/build-panels.ts --app finance` from the apps repo. The shared panel checker also reports existing failures in unrelated Instances, 3D Studio and SEO bundles.
