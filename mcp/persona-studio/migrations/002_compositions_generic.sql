ALTER TABLE persona_compositions ADD COLUMN source_plan_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE persona_compositions ADD COLUMN resolved_plan_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE persona_compositions ADD COLUMN output_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE persona_compositions ADD COLUMN latest_render_id INTEGER;
ALTER TABLE persona_compositions ADD COLUMN render_status TEXT NOT NULL DEFAULT '';
ALTER TABLE persona_compositions ADD COLUMN render_error TEXT NOT NULL DEFAULT '';
ALTER TABLE persona_compositions ADD COLUMN source_composition_id INTEGER;
ALTER TABLE persona_compositions ADD COLUMN variant_group_id TEXT NOT NULL DEFAULT '';
