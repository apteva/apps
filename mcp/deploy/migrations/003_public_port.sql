-- Opt-in direct public access for a deployment's runtime port.
--
-- Default remains loopback-only. When public_port=1, the runtime binds
-- the release process to 0.0.0.0 and exposes the IP:port URL in status.

ALTER TABLE deployments ADD COLUMN public_port INTEGER NOT NULL DEFAULT 0;
