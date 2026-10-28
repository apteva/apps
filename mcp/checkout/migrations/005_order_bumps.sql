CREATE TABLE order_bumps (
 id INTEGER PRIMARY KEY, project_id TEXT NOT NULL,
 trigger_price_id INTEGER NOT NULL DEFAULT 0, trigger_product_id INTEGER NOT NULL DEFAULT 0,
 source_app TEXT NOT NULL DEFAULT '', context_ref TEXT NOT NULL DEFAULT '',
 price_id INTEGER NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
 image_url TEXT NOT NULL DEFAULT '', sort_order INTEGER NOT NULL DEFAULT 0,
 active INTEGER NOT NULL DEFAULT 1, starts_at TEXT NOT NULL DEFAULT '', ends_at TEXT NOT NULL DEFAULT '',
 fulfillment_app TEXT NOT NULL, fulfillment_ref TEXT NOT NULL,
 CHECK(trigger_price_id>0 OR trigger_product_id>0)
);
CREATE INDEX order_bumps_project ON order_bumps(project_id,active,sort_order);
CREATE TABLE checkout_bump_views (
 session_id INTEGER NOT NULL REFERENCES checkout_sessions(id),
 bump_id INTEGER NOT NULL REFERENCES order_bumps(id),
 selected_once INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(session_id,bump_id)
);
CREATE TABLE checkout_item_refunds (
 project_id TEXT NOT NULL, session_id INTEGER NOT NULL REFERENCES checkout_sessions(id),
 item_id INTEGER NOT NULL REFERENCES cart_items(id),
 idempotency_key TEXT NOT NULL, amount_cents INTEGER NOT NULL CHECK(amount_cents>0),
 status TEXT NOT NULL DEFAULT 'pending',
 PRIMARY KEY(project_id,idempotency_key)
);
