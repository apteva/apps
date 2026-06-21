# Orders

Orders is the operational ledger for physical commerce. Use it after a cart has converted or after a marketplace order has been imported.

Use Catalog for product/price truth, Checkout for buyer session state, Billing for invoices/payments, and Orders for shipment/fulfillment state.

Default flow:

1. Create or import the order.
2. Verify payment status and shipping address.
3. Mark the order ready to fulfill.
4. Create a fulfillment against the bound provider.
5. Sync tracking until shipped/delivered.
6. Record returns when needed.

Do not auto-submit fulfillment for imported supplier/MOQ products until address, stock, customs/compliance, and provider SKU mapping are verified.
