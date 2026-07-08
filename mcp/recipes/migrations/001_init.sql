-- recipes v0.1: recipe library, nutrition estimates, meal planning, and
-- shopping-list generation. Pantry remains the inventory source of truth; this
-- app stores recipe ingredients and can optionally compare them with pantry
-- stock by normalized names.

CREATE TABLE IF NOT EXISTS recipes (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    title           TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    cuisine         TEXT    NOT NULL DEFAULT '',
    meal_type       TEXT    NOT NULL DEFAULT '',
    servings        REAL    NOT NULL DEFAULT 1 CHECK(servings > 0),
    prep_minutes    INTEGER NOT NULL DEFAULT 0,
    cook_minutes    INTEGER NOT NULL DEFAULT 0,
    source_url      TEXT    NOT NULL DEFAULT '',
    notes           TEXT    NOT NULL DEFAULT '',
    archived        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, title)
);

CREATE INDEX IF NOT EXISTS idx_recipes_project_title
    ON recipes(project_id, archived, title);

CREATE TABLE IF NOT EXISTS recipe_ingredients (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    recipe_id       INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    position        INTEGER NOT NULL DEFAULT 0,
    name            TEXT    NOT NULL,
    normalized_name TEXT    NOT NULL,
    quantity        REAL    NOT NULL DEFAULT 0,
    unit            TEXT    NOT NULL DEFAULT '',
    preparation     TEXT    NOT NULL DEFAULT '',
    optional        INTEGER NOT NULL DEFAULT 0,
    pantry_item_id  INTEGER,
    calories        REAL,
    protein_g       REAL,
    carbs_g         REAL,
    fat_g           REAL,
    fiber_g         REAL,
    sugar_g         REAL,
    sodium_mg       REAL,
    source          TEXT    NOT NULL DEFAULT 'estimate'
                    CHECK(source IN ('estimate','manual','import')),
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_recipe_ingredients_recipe
    ON recipe_ingredients(recipe_id, position);

CREATE INDEX IF NOT EXISTS idx_recipe_ingredients_name
    ON recipe_ingredients(project_id, normalized_name);

CREATE TABLE IF NOT EXISTS recipe_steps (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    recipe_id   INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL DEFAULT 0,
    instruction TEXT    NOT NULL,
    timer_min   INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_recipe_steps_recipe
    ON recipe_steps(recipe_id, position);

CREATE TABLE IF NOT EXISTS recipe_tags (
    recipe_id INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    tag       TEXT    NOT NULL,
    PRIMARY KEY(recipe_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_recipe_tags_tag
    ON recipe_tags(tag);

CREATE TABLE IF NOT EXISTS nutrition_overrides (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    normalized_name TEXT    NOT NULL,
    label           TEXT    NOT NULL,
    grams           REAL    NOT NULL DEFAULT 100 CHECK(grams > 0),
    calories        REAL    NOT NULL DEFAULT 0,
    protein_g       REAL    NOT NULL DEFAULT 0,
    carbs_g         REAL    NOT NULL DEFAULT 0,
    fat_g           REAL    NOT NULL DEFAULT 0,
    fiber_g         REAL    NOT NULL DEFAULT 0,
    sugar_g         REAL    NOT NULL DEFAULT 0,
    sodium_mg       REAL    NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, normalized_name)
);

CREATE TABLE IF NOT EXISTS meal_plan_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    plan_date   TEXT    NOT NULL, -- YYYY-MM-DD
    slot        TEXT    NOT NULL DEFAULT 'dinner'
                CHECK(slot IN ('breakfast','lunch','dinner','snack','other')),
    recipe_id   INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    servings    REAL    NOT NULL DEFAULT 1 CHECK(servings > 0),
    notes       TEXT    NOT NULL DEFAULT '',
    cooked_at   TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_meal_plan_date
    ON meal_plan_entries(project_id, plan_date, slot);
