-- Stripe credentials and webhook signing secrets belong to the platform
-- connection layer. Billing no longer stores a local signing secret.
DROP TABLE IF EXISTS billing_stripe_settings;
