ALTER TABLE object_storages ADD COLUMN setup_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE object_storages ADD COLUMN request_key TEXT;
CREATE UNIQUE INDEX object_storage_request_key ON object_storages(request_key) WHERE request_key IS NOT NULL;
