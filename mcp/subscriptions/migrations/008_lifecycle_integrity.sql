ALTER TABLE subscriptions ADD COLUMN billing_anchor_day INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscription_cycles ADD COLUMN total_override INTEGER NOT NULL DEFAULT 0;
UPDATE subscriptions SET billing_anchor_day=COALESCE(CAST(strftime('%d',CASE WHEN status='trialing' THEN COALESCE(trial_end,current_period_end) ELSE current_period_start END) AS INTEGER),0);

-- Preserve historical duplicate cycles; retries resolve to the first cycle.
CREATE TABLE subscription_cycle_keys (
  subscription_id INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  period_start TEXT NOT NULL,
  cycle_id INTEGER NOT NULL REFERENCES subscription_cycles(id) ON DELETE CASCADE,
  PRIMARY KEY(subscription_id, period_start)
);
INSERT INTO subscription_cycle_keys(subscription_id,period_start,cycle_id)
SELECT subscription_id,strftime('%Y-%m-%dT%H:%M:%SZ',period_start),MIN(id)
FROM subscription_cycles WHERE julianday(period_start) IS NOT NULL
GROUP BY subscription_id,strftime('%Y-%m-%dT%H:%M:%SZ',period_start);

CREATE INDEX ix_subscriptions_search ON subscriptions(project_id,updated_at DESC,id DESC);
CREATE INDEX ix_subscriptions_due ON subscriptions(julianday(next_renewal_at),id)
  WHERE billing_provider='local' AND status IN ('active','trialing');
CREATE INDEX ix_subscriptions_cancel_due ON subscriptions(julianday(cancel_at),id)
  WHERE cancel_at IS NOT NULL AND status NOT IN ('cancelled','ended');

-- Event intent commits with the business mutation. Delivery is at least once.
CREATE TABLE subscription_outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  subscription_id INTEGER NOT NULL,
  topic TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  delivered_at TEXT
);
CREATE INDEX ix_subscription_outbox_pending ON subscription_outbox(id) WHERE delivered_at IS NULL;
