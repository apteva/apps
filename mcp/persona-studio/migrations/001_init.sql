CREATE TABLE IF NOT EXISTS personas (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  handle TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  audience TEXT NOT NULL DEFAULT '',
  personality TEXT NOT NULL DEFAULT '',
  tone TEXT NOT NULL DEFAULT '',
  visual_style TEXT NOT NULL DEFAULT '',
  negative_style TEXT NOT NULL DEFAULT '',
  brand_rules_json TEXT NOT NULL DEFAULT '{}',
  default_voice_id TEXT NOT NULL DEFAULT '',
  default_avatar_id TEXT NOT NULL DEFAULT '',
  default_image_provider TEXT NOT NULL DEFAULT '',
  default_video_provider TEXT NOT NULL DEFAULT '',
  default_audio_provider TEXT NOT NULL DEFAULT '',
  default_music_provider TEXT NOT NULL DEFAULT '',
  default_avatar_provider TEXT NOT NULL DEFAULT '',
  archived_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_personas_project ON personas(project_id, archived_at, updated_at);

CREATE TABLE IF NOT EXISTS persona_style_profiles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  persona_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  asset_type TEXT NOT NULL,
  prompt_prefix TEXT NOT NULL DEFAULT '',
  prompt_suffix TEXT NOT NULL DEFAULT '',
  negative_prompt TEXT NOT NULL DEFAULT '',
  provider_settings_json TEXT NOT NULL DEFAULT '{}',
  composition_settings_json TEXT NOT NULL DEFAULT '{}',
  is_default INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(persona_id) REFERENCES personas(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_style_profiles_persona ON persona_style_profiles(project_id, persona_id, asset_type, is_default);

CREATE TABLE IF NOT EXISTS persona_references (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  persona_id INTEGER NOT NULL,
  storage_file_id INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'style',
  label TEXT NOT NULL DEFAULT '',
  weight REAL NOT NULL DEFAULT 1.0,
  notes TEXT NOT NULL DEFAULT '',
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(persona_id) REFERENCES personas(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_references_persona ON persona_references(project_id, persona_id, kind, active);

CREATE TABLE IF NOT EXISTS persona_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  persona_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'product',
  description TEXT NOT NULL DEFAULT '',
  usage_rules TEXT NOT NULL DEFAULT '',
  visual_rules TEXT NOT NULL DEFAULT '',
  storage_file_ids_json TEXT NOT NULL DEFAULT '[]',
  active INTEGER NOT NULL DEFAULT 1,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(persona_id) REFERENCES personas(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_items_persona ON persona_items(project_id, persona_id, kind, active);

CREATE TABLE IF NOT EXISTS persona_campaigns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  persona_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  brief TEXT NOT NULL DEFAULT '',
  platforms_json TEXT NOT NULL DEFAULT '[]',
  content_pillars_json TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'draft',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(persona_id) REFERENCES personas(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_campaigns_persona ON persona_campaigns(project_id, persona_id, status);

CREATE TABLE IF NOT EXISTS persona_assets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  persona_id INTEGER NOT NULL,
  campaign_id INTEGER,
  storage_file_id INTEGER,
  media_generation_id INTEGER,
  asset_type TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'ready',
  prompt TEXT NOT NULL DEFAULT '',
  resolved_prompt TEXT NOT NULL DEFAULT '',
  provider_slug TEXT NOT NULL DEFAULT '',
  provider_model TEXT NOT NULL DEFAULT '',
  settings_json TEXT NOT NULL DEFAULT '{}',
  reference_ids_json TEXT NOT NULL DEFAULT '[]',
  item_ids_json TEXT NOT NULL DEFAULT '[]',
  cache_key TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(persona_id) REFERENCES personas(id) ON DELETE CASCADE,
  FOREIGN KEY(campaign_id) REFERENCES persona_campaigns(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_assets_persona ON persona_assets(project_id, persona_id, asset_type, created_at);
CREATE INDEX IF NOT EXISTS idx_assets_cache ON persona_assets(project_id, cache_key);

CREATE TABLE IF NOT EXISTS persona_compositions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  persona_id INTEGER NOT NULL,
  campaign_id INTEGER,
  composer_composition_id INTEGER,
  storage_file_id INTEGER,
  title TEXT NOT NULL,
  aspect TEXT NOT NULL DEFAULT '9:16',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  plan_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'draft',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(persona_id) REFERENCES personas(id) ON DELETE CASCADE,
  FOREIGN KEY(campaign_id) REFERENCES persona_campaigns(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_compositions_persona ON persona_compositions(project_id, persona_id, status);

CREATE TABLE IF NOT EXISTS persona_generation_cache (
  cache_key TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  persona_id INTEGER NOT NULL,
  asset_type TEXT NOT NULL,
  storage_file_id INTEGER NOT NULL,
  asset_id INTEGER NOT NULL,
  generation_id INTEGER,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at TEXT NOT NULL DEFAULT ''
);
