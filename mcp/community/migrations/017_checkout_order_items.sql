CREATE TABLE community_checkout_orders (
 session_id INTEGER PRIMARY KEY, invoice_id INTEGER NOT NULL UNIQUE,
 project_id TEXT NOT NULL, community_id TEXT NOT NULL REFERENCES communities(id),
 member_id TEXT NOT NULL REFERENCES members(id), primary_price_id INTEGER NOT NULL
);
CREATE TABLE community_checkout_items (
 session_id INTEGER NOT NULL REFERENCES community_checkout_orders(session_id),
 item_id INTEGER NOT NULL, space_id TEXT NOT NULL REFERENCES spaces(id),
 price_id INTEGER NOT NULL, PRIMARY KEY(session_id,item_id)
);
