# Orders (v0.1.0)

Physical commerce order ledger for Apteva.

Orders sits after Checkout/Billing and before fulfillment providers. It records the durable operational state for physical products: order lines, shipment addresses, fulfillment submissions, tracking, returns, and an append-only event log.

## Boundaries

- Catalog owns products and prices.
- Checkout owns carts and checkout sessions.
- Billing owns invoices and payments.
- Orders owns physical delivery operations.

## v0.1 Scope

- Orders and order line items.
- Create from manual payloads, channel imports, or checkout/invoice snapshots.
- Manual status transitions.
- Fulfillment attempts for Huboo, Hive Fulfillment, and byrd bindings.
- Shipments, tracking sync records, returns, and event history.

Automatic fulfillment submission is intentionally off by default. Review shipping address, stock, compliance, and customs details before creating a warehouse order.

## Local Development

```bash
cd mcp/orders
go build .
APTEVA_PROJECT_ID=test ./orders
curl http://localhost:8080/health
```

## Tests

```bash
go test ./...
```
