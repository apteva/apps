-- pantry v0.2: persisted manual shopping list.
--
-- Manual shopping intent is deliberately separate from pantry stock.
-- A row here means "buy this", not "we own this". The item_id link is
-- optional so one-off or not-yet-tracked products can live on the list
-- without polluting inventory definitions.

CREATE TABLE IF NOT EXISTS shopping_list_items (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT    NOT NULL,
    item_id    INTEGER REFERENCES items(id) ON DELETE SET NULL,
    name       TEXT    NOT NULL,
    quantity   REAL    NOT NULL DEFAULT 1 CHECK(quantity > 0),
    unit       TEXT    NOT NULL DEFAULT 'each',
    category   TEXT    NOT NULL DEFAULT '',
    store      TEXT    NOT NULL DEFAULT '',
    source     TEXT    NOT NULL DEFAULT 'manual'
               CHECK(source IN ('manual','low_stock','agent','recipe')),
    status     TEXT    NOT NULL DEFAULT 'open'
               CHECK(status IN ('open','checked','dismissed','purchased')),
    notes      TEXT    NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_shopping_list_project_status
    ON shopping_list_items(project_id, status, created_at);

CREATE INDEX IF NOT EXISTS idx_shopping_list_project_item
    ON shopping_list_items(project_id, item_id);
