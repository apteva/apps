-- certs 002: remember per-cert ACME challenge type.
--
-- App-level challenge_type remains the default. A caller can force
-- http-01 for client-managed DNS hostnames, and renewals must keep
-- using that path even when the app default is dns-01.

ALTER TABLE certs ADD COLUMN challenge_type TEXT
    CHECK (challenge_type IS NULL OR challenge_type IN ('dns-01','http-01'));
