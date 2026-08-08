-- Provider-neutral host classification plus private adapter metadata.
--
-- platform/resource_class are part of the public instance contract and let
-- callers distinguish Linux VMs from macOS bare-metal hosts. Provider metadata
-- holds lifecycle IDs that adapters need but API consumers should not receive.

ALTER TABLE instances ADD COLUMN platform TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN resource_class TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN deletable_at TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN provider_metadata_json TEXT NOT NULL DEFAULT '{}';
