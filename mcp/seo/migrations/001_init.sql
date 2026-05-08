-- seo v0.1 — generic SEO research workbench.
--
-- Schema is grounded in the convergent shape across DataForSEO,
-- Ahrefs, and Moz. The pattern is: stable identity tables (domains,
-- pages, keywords) + per-(entity, provider, ts) snapshot rows for
-- metrics + dedicated time-series for things providers always hand
-- back in bulk (keyword volume history) + event-style rows for
-- rankings and backlinks.
--
-- Every snapshot table has a raw_json column that captures the
-- unflattened provider response, so provider-specific fields
-- (Ahrefs distribution buckets, DataForSEO pos_* counts, Moz
-- link-count forest) survive without schema churn. Promote a field
-- to a typed column once it proves load-bearing.
--
-- Times: ts columns are unix seconds (INTEGER). created_at is a
-- TIMESTAMP for cheap human inspection in the panel.
--
-- Scope: every top-level entity has project_id matching the apteva
-- project the install belongs to (or '' for global scope). Children
-- (pages, *_metrics, rankings, backlinks) inherit scope via FK.

CREATE TABLE IF NOT EXISTS domains (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    host        TEXT    NOT NULL,                       -- normalised: lowercase, no scheme, no trailing slash
    label       TEXT    NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, host)
);

CREATE INDEX IF NOT EXISTS idx_domains_scope ON domains(project_id);

CREATE TABLE IF NOT EXISTS pages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    domain_id   INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    path        TEXT    NOT NULL,                       -- relative, leading slash, e.g. '/blog'
    label       TEXT    NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(domain_id, path)
);

CREATE INDEX IF NOT EXISTS idx_pages_domain ON pages(domain_id);

CREATE TABLE IF NOT EXISTS domain_metrics (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    domain_id                INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    provider                 TEXT    NOT NULL,          -- 'ahrefs' | 'dataforseo' | 'moz' | 'manual' | 'stub'
    ts                       INTEGER NOT NULL,          -- unix seconds
    country_iso              TEXT,                      -- 2-letter ISO; NULL = global (Moz)
    authority_score          INTEGER,                   -- 0-100; provider's flagship (DR / DA / rank)
    spam_score               REAL,
    organic_traffic          INTEGER,                   -- estimated monthly visits
    organic_keywords         INTEGER,
    paid_traffic             INTEGER,
    paid_keywords            INTEGER,
    backlinks_count          INTEGER,
    referring_domains_count  INTEGER,
    raw_json                 TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_domain_metrics_lookup ON domain_metrics(domain_id, provider, ts DESC);

CREATE TABLE IF NOT EXISTS page_metrics (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    page_id                  INTEGER NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    provider                 TEXT    NOT NULL,
    ts                       INTEGER NOT NULL,
    country_iso              TEXT,
    authority_score          INTEGER,
    organic_traffic          INTEGER,
    organic_keywords         INTEGER,
    backlinks_count          INTEGER,
    referring_domains_count  INTEGER,
    raw_json                 TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_page_metrics_lookup ON page_metrics(page_id, provider, ts DESC);

CREATE TABLE IF NOT EXISTS keywords (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    text          TEXT    NOT NULL,                     -- normalised: trimmed, lowercased
    country_iso   TEXT    NOT NULL DEFAULT 'US',        -- 2-letter ISO
    language_iso  TEXT    NOT NULL DEFAULT 'en',        -- ISO 639-1
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, text, country_iso, language_iso)
);

CREATE INDEX IF NOT EXISTS idx_keywords_scope ON keywords(project_id);

CREATE TABLE IF NOT EXISTS keyword_metrics (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    keyword_id        INTEGER NOT NULL REFERENCES keywords(id) ON DELETE CASCADE,
    provider          TEXT    NOT NULL,
    ts                INTEGER NOT NULL,
    volume            INTEGER,                          -- monthly searches
    difficulty        INTEGER,                          -- 0-100
    cpc_usd           REAL,
    clicks            INTEGER,                          -- per-month estimated clicks (Ahrefs)
    organic_ctr       REAL,                             -- 0..1 (Moz)
    intent_json       TEXT    NOT NULL DEFAULT '[]',    -- e.g. ["informational","commercial"]
    serp_features_json TEXT   NOT NULL DEFAULT '[]',    -- e.g. ["featured_snippet","video"]
    raw_json          TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_keyword_metrics_lookup ON keyword_metrics(keyword_id, provider, ts DESC);

-- Monthly volume series. All three providers expose ~24 months
-- inline on every keyword fetch — denormalising into snapshot rows
-- would waste space, so it gets its own table.
CREATE TABLE IF NOT EXISTS keyword_volume_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    keyword_id  INTEGER NOT NULL REFERENCES keywords(id) ON DELETE CASCADE,
    provider    TEXT    NOT NULL,
    year        INTEGER NOT NULL,
    month       INTEGER NOT NULL CHECK(month BETWEEN 1 AND 12),
    volume      INTEGER NOT NULL,
    UNIQUE(keyword_id, provider, year, month)
);

CREATE INDEX IF NOT EXISTS idx_keyword_volume_history_lookup
    ON keyword_volume_history(keyword_id, year DESC, month DESC);

CREATE TABLE IF NOT EXISTS rankings (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    domain_id           INTEGER NOT NULL REFERENCES domains(id)  ON DELETE CASCADE,
    keyword_id          INTEGER NOT NULL REFERENCES keywords(id) ON DELETE CASCADE,
    provider            TEXT    NOT NULL,
    ts                  INTEGER NOT NULL,
    rank                INTEGER,                        -- 1..100; NULL if outside tracked range
    rank_url            TEXT    NOT NULL DEFAULT '',    -- which page on the domain ranked
    device              TEXT    NOT NULL DEFAULT 'desktop'
                        CHECK(device IN ('desktop','mobile')),
    serp_features_json  TEXT    NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_rankings_domain_kw_ts ON rankings(domain_id, keyword_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_rankings_kw_ts        ON rankings(keyword_id, ts DESC);

CREATE TABLE IF NOT EXISTS backlinks (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    domain_id         INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    provider          TEXT    NOT NULL,
    source_url        TEXT    NOT NULL,                 -- the linking page
    dest_url          TEXT    NOT NULL,                 -- the page on `domain` being linked to
    anchor            TEXT    NOT NULL DEFAULT '',
    is_dofollow       INTEGER,                          -- 0/1, NULL when provider can't tell (Moz)
    is_nofollow       INTEGER,
    is_ugc            INTEGER,
    is_sponsored      INTEGER,
    source_authority  INTEGER,                          -- DR / DA of the source domain
    first_seen        INTEGER,                          -- unix seconds
    last_seen         INTEGER,
    is_lost           INTEGER NOT NULL DEFAULT 0,
    raw_json          TEXT    NOT NULL DEFAULT '{}',
    UNIQUE(domain_id, provider, source_url, dest_url, anchor)
);

CREATE INDEX IF NOT EXISTS idx_backlinks_domain_lastseen ON backlinks(domain_id, last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_backlinks_domain_lost     ON backlinks(domain_id, is_lost);
