-- Durable routing, recovery, cancellation, and phased strategy execution.
CREATE TABLE broker_bindings (
 portfolio_id INTEGER PRIMARY KEY REFERENCES portfolios(id) ON DELETE CASCADE,
 connection_id INTEGER NOT NULL,
 execution_environment TEXT NOT NULL,
 UNIQUE(connection_id)
);
-- Recover only unambiguous ownership; never choose an arbitrary old account.
WITH candidates AS (
 SELECT p.id, MIN(CAST(json_extract(j.metadata,'$.broker_connection_id') AS INTEGER)) AS connection_id, p.execution_environment
 FROM portfolios p JOIN journal j ON j.portfolio_id=p.id
 WHERE p.mode='live' AND CAST(json_extract(j.metadata,'$.broker_connection_id') AS INTEGER)>0
 GROUP BY p.id HAVING COUNT(DISTINCT CAST(json_extract(j.metadata,'$.broker_connection_id') AS INTEGER))=1
)
INSERT INTO broker_bindings
SELECT c.id,c.connection_id,c.execution_environment FROM candidates c
WHERE NOT EXISTS (
 SELECT 1 FROM journal j JOIN portfolios p ON p.id=j.portfolio_id
 WHERE p.mode='live' AND p.id<>c.id AND CAST(json_extract(j.metadata,'$.broker_connection_id') AS INTEGER)=c.connection_id
);
ALTER TABLE orders ADD COLUMN broker_order_id TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN broker_connection_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN reconciliation_required INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN strategy_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN strategy_version INTEGER NOT NULL DEFAULT 0;
UPDATE orders SET strategy_id=COALESCE((SELECT json_extract(metadata,'$.strategy_id') FROM journal WHERE json_extract(metadata,'$.order_id')=orders.id AND json_extract(metadata,'$.strategy_id')>0 ORDER BY id DESC LIMIT 1),0);
-- Unknown historical strategy versions remain zero and fail revalidation.
UPDATE orders SET broker_order_id=COALESCE((SELECT json_extract(metadata,'$.broker_order_id') FROM journal WHERE json_extract(metadata,'$.order_id')=orders.id AND json_extract(metadata,'$.broker_order_id') IS NOT NULL ORDER BY id DESC LIMIT 1),'');
UPDATE orders SET broker_connection_id=COALESCE((SELECT connection_id FROM broker_bindings WHERE portfolio_id=orders.portfolio_id),0);
CREATE INDEX ix_orders_broker_id ON orders(broker_connection_id,broker_order_id);
CREATE TABLE order_requests (
 project_id TEXT NOT NULL, portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL, intent_hash TEXT NOT NULL, order_id TEXT NOT NULL UNIQUE,
 PRIMARY KEY(project_id,portfolio_id,request_key)
);
CREATE TABLE strategy_rebalances (
 assignment_id INTEGER PRIMARY KEY, portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
 strategy_id INTEGER NOT NULL, strategy_version INTEGER NOT NULL, targets_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', updated_at TEXT DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE objective_results (
 objective_id INTEGER PRIMARY KEY REFERENCES portfolio_objectives(id) ON DELETE CASCADE,
 actual_pct REAL NOT NULL, achieved INTEGER NOT NULL, finalized_at TEXT NOT NULL
);
CREATE TABLE portfolio_revisions (portfolio_id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 0);
CREATE TABLE position_history (id INTEGER PRIMARY KEY, portfolio_id INTEGER NOT NULL, symbol TEXT NOT NULL, outcome TEXT NOT NULL, qty REAL NOT NULL, avg_cost REAL NOT NULL, observed_at TEXT NOT NULL);
CREATE INDEX ix_position_history_at ON position_history(portfolio_id,symbol,outcome,observed_at,id);
INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at) SELECT portfolio_id,symbol,COALESCE(outcome,''),qty,avg_cost,strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM positions;
CREATE TRIGGER history_position_insert AFTER INSERT ON positions BEGIN
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at) VALUES(NEW.portfolio_id,NEW.symbol,COALESCE(NEW.outcome,''),NEW.qty,NEW.avg_cost,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER history_position_update AFTER UPDATE OF qty,avg_cost,symbol,outcome ON positions
WHEN OLD.qty IS NOT NEW.qty OR OLD.avg_cost IS NOT NEW.avg_cost OR OLD.symbol IS NOT NEW.symbol OR OLD.outcome IS NOT NEW.outcome BEGIN
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at)
 SELECT OLD.portfolio_id,OLD.symbol,COALESCE(OLD.outcome,''),0,0,strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE OLD.symbol IS NOT NEW.symbol OR COALESCE(OLD.outcome,'') IS NOT COALESCE(NEW.outcome,'');
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at) VALUES(NEW.portfolio_id,NEW.symbol,COALESCE(NEW.outcome,''),NEW.qty,NEW.avg_cost,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER history_position_delete AFTER DELETE ON positions BEGIN
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at) VALUES(OLD.portfolio_id,OLD.symbol,COALESCE(OLD.outcome,''),0,0,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 INSERT INTO portfolio_revisions VALUES(OLD.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER revision_cash AFTER UPDATE OF cash ON portfolios WHEN OLD.cash IS NOT NEW.cash BEGIN
 INSERT INTO portfolio_revisions VALUES(NEW.id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TABLE objective_observations (objective_id INTEGER NOT NULL REFERENCES portfolio_objectives(id) ON DELETE CASCADE, observed_at TEXT NOT NULL, actual_pct REAL NOT NULL, PRIMARY KEY(objective_id,observed_at));
CREATE TABLE order_commissions (order_id TEXT NOT NULL REFERENCES orders(id), currency TEXT NOT NULL, amount REAL NOT NULL, PRIMARY KEY(order_id,currency));
-- Historical notifications contain cumulative commissions. Seed the watermark
-- without debiting balances again; do not sum repeated cumulative snapshots.
INSERT INTO order_commissions(order_id,currency,amount)
SELECT order_id,currency,MAX(amount) FROM (
 SELECT o.id AS order_id,UPPER(json_extract(c.value,'$.currency')) AS currency,SUM(CAST(json_extract(c.value,'$.amount') AS REAL)) AS amount
 FROM journal j JOIN orders o ON o.id=json_extract(j.metadata,'$.order_id'), json_each(j.metadata,'$.raw_commissions') c
 WHERE j.kind='fill' AND json_extract(c.value,'$.amount')>0 AND COALESCE(json_extract(c.value,'$.currency'),'')<>''
 GROUP BY j.id,o.id,UPPER(json_extract(c.value,'$.currency'))
) GROUP BY order_id,currency;
CREATE TABLE replay_steps (portfolio_id INTEGER PRIMARY KEY REFERENCES portfolios(id), run_id INTEGER NOT NULL, step INTEGER NOT NULL, digest TEXT NOT NULL, replay_at TEXT NOT NULL);
ALTER TABLE portfolios ADD COLUMN broker_binding_required INTEGER NOT NULL DEFAULT 0;
UPDATE portfolios SET broker_binding_required=1,live_armed=0 WHERE mode='live' AND id NOT IN (SELECT portfolio_id FROM broker_bindings);
