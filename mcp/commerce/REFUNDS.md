# Executing authorized refunds

Available in Commerce v0.10.0. Requires Billing v0.13.0 or newer.

Commerce exposes `commerce_sale_refund` and `POST /admin/sales/{sale_id}/refunds`.
The Commerce sale panel exposes the same action. Billing is unchanged and remains
the payment executor; only original Stripe payments are supported for execution.
Manual refund accounting is still available through the financial ledger, but
recording an adjustment does not send money.

Pass `sale_id`, a stable `idempotency_key`, and optionally `amount_cents` and
`reason` (`requested_by_customer`, `duplicate`, or `fraudulent`). Omit the amount
to refund the remaining paid sale balance. Explicit amounts must be positive
whole cents. Commerce validates the sale's project, invoice identity and project,
currency, and paid balance; Billing performs the authoritative reservation and
original-payment refundable-balance checks.

The first request durably freezes the invoice, amount, reason, and a namespaced
Billing key. Always reuse the original key and parameters after a timeout or
lost response, including for a full refund. Never create a fresh key merely
because the request outcome is unknown. Conflicting reuse is rejected.

Responses contain `request.status`, the actual Billing `request.refund` when
available, current `sale` and `invoice`, and `reconciled`. `pending`, `submitted`,
and `unknown` do not mean the refund completed. An `execution_error` after a
Billing call means the outcome may be unknown. A succeeded request with
`reconciled: false` means refund execution was confirmed but Commerce could not
refresh its state; retry the same request to reconcile without issuing another
refund. Processor-confirmed negative Billing payments drive refund totals and
payment status, and the existing financial reconciler continues updating them.

`commerce_sale_refunds_list` and `GET /admin/sales/{sale_id}/refunds` recover the
stored requests and original caller keys after a restart. Replaying a submitted
or unknown request asks Billing for the same idempotent operation. Replaying a
terminal request refreshes the invoice and Commerce state without executing a
new refund. Approval and enquiry tracking remain in Processes and CRM.
