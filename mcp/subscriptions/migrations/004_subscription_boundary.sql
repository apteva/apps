-- Remove commercial-collection state introduced by v0.3.0.
-- Subscriptions retains only its trial policy, lifecycle attempt, and cycle link.

DROP INDEX IF EXISTS ix_subscriptions_collection;

ALTER TABLE subscriptions DROP COLUMN billing_customer_id;
ALTER TABLE subscriptions DROP COLUMN collection_method;
ALTER TABLE subscriptions DROP COLUMN collection_status;
ALTER TABLE subscriptions DROP COLUMN last_collection_error;
ALTER TABLE subscriptions DROP COLUMN collection_invoice_id;

ALTER TABLE subscription_lifecycle_attempts DROP COLUMN billing_customer_id;
ALTER TABLE subscription_lifecycle_attempts DROP COLUMN invoice_id;
ALTER TABLE subscription_lifecycle_attempts DROP COLUMN collection_ref;
