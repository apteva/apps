# Billing handoff: subscription collection primitives

Subscriptions v0.3.0 fixes paid no-card trial conversion without changing Billing. It now owns the durable, project-scoped lifecycle attempt and retry state, creates/links the Billing customer, invoice, subscription cycle, and payment link, and reconciles `invoice.paid` back to `subscription.active`.

The remaining crash-safety boundary is inside Billing. A process can still stop after Billing or Stripe creates an object but before Subscriptions persists its returned ID. Retrying that step can create a duplicate because the current Billing tools do not honor the deterministic `idempotency_key` already sent by Subscriptions.

## Required Billing work

1. `invoices_create_from_prepared_lines`
   - Accept `idempotency_key`.
   - Enforce uniqueness per `(project_id, idempotency_key)` in Billing's database.
   - Return the original invoice, including its current status, on replay.
   - Reject reuse of a key with materially different customer, currency, or lines.

2. `invoices_send_payment_link`
   - Accept `idempotency_key`.
   - Persist the key, provider session ID, URL, and expiry before returning.
   - Pass the key through to Stripe's `Idempotency-Key` header or the equivalent integration primitive.
   - Return the same still-valid session on replay.

3. `payment_method_setup_create`
   - Apply the same idempotency contract to setup sessions.

4. Add `invoices_charge_default_payment_method`
   - Inputs: `invoice_id`, `idempotency_key`.
   - Select the customer's active default reusable payment method.
   - Attempt off-session collection through the configured provider.
   - Return `{status, invoice, payment, requires_payment}` where status is `paid`, `processing`, `requires_payment`, or `failed`.
   - Replays must return the same payment/attempt rather than initiating another charge.
   - If no usable method exists, return `requires_payment` without treating it as an internal error.

5. Event correlation
   - Include invoice `metadata` in `invoice.paid` events, especially `source_app`, `subscription_id`, and `lifecycle_attempt_id`.
   - Continue including the invoice ID; Subscriptions v0.3.0 can already reconcile using that ID alone.

## Keys sent by Subscriptions

Subscriptions sends stable keys shaped like:

```text
subscriptions:<project_id>:<subscription_id>:trial_end:<effective_at>:invoice
subscriptions:<project_id>:<subscription_id>:trial_end:<effective_at>:charge
subscriptions:<project_id>:<subscription_id>:trial_end:<effective_at>:payment-link
```

Billing currently ignores these keys. Subscriptions' local unique attempt/cycle records prevent normal repeated worker runs from duplicating work, but only Billing can close the crash window around its own committed writes and provider calls.

## Billing acceptance tests

- Repeating invoice creation with the same key returns one invoice.
- Reusing an invoice key with different lines fails clearly.
- A simulated caller crash after invoice creation followed by retry returns the original invoice.
- Repeating payment-link creation with the same key returns one provider session.
- A default payment method produces one charge and a paid invoice.
- Missing or authentication-required payment methods return `requires_payment` without double charging.
- Every tool and emitted event remains project-scoped.
