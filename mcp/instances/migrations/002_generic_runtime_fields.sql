-- Generic runtime connection/resource metadata.
--
-- ssh_host/ssh_port let provider adapters represent hosts whose SSH
-- endpoint is not simply public_ipv4:22. RunPod Pods expose internal
-- 22/tcp through a provider-assigned public port, while normal VPS
-- providers keep the default 22.
--
-- resources_json stores normalized compute resources such as CPU,
-- memory, disk, and accelerators. ports_json stores provider port
-- mappings without forcing every provider into RunPod's shape.

ALTER TABLE instances ADD COLUMN ssh_host TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN ssh_port INTEGER NOT NULL DEFAULT 22;
ALTER TABLE instances ADD COLUMN resources_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE instances ADD COLUMN ports_json TEXT NOT NULL DEFAULT '{}';
