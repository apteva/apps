-- Community v0.7: recurring course memberships.
--
-- Community is the product orchestrator. Catalog owns recurring prices,
-- Subscriptions owns lifecycle/cycles, and Billing owns money movement.

CREATE TABLE IF NOT EXISTS membership_plans (
    id                    TEXT PRIMARY KEY,
    community_id          TEXT NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    catalog_product_id    INTEGER NOT NULL,
    catalog_price_id      INTEGER NOT NULL,
    product_name          TEXT NOT NULL,
    price_nickname        TEXT NOT NULL DEFAULT '',
    unit_amount_cents     INTEGER NOT NULL CHECK(unit_amount_cents > 0),
    currency              TEXT NOT NULL,
    interval              TEXT NOT NULL CHECK(interval IN ('day','week','month','year')),
    interval_count        INTEGER NOT NULL DEFAULT 1 CHECK(interval_count > 0),
    scope_type            TEXT NOT NULL DEFAULT 'all_courses'
                          CHECK(scope_type IN ('all_courses','selected_courses','course_tags')),
    collection_method     TEXT NOT NULL DEFAULT 'automatic'
                          CHECK(collection_method IN ('automatic','send_invoice')),
    trial_days            INTEGER NOT NULL DEFAULT 0 CHECK(trial_days >= 0),
    grace_days            INTEGER NOT NULL DEFAULT 7 CHECK(grace_days >= 0),
    active                INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at           TIMESTAMP,
    UNIQUE(community_id, catalog_price_id)
);
CREATE INDEX IF NOT EXISTS idx_membership_plans_community
    ON membership_plans(community_id, active, updated_at DESC);

CREATE TABLE IF NOT EXISTS membership_plan_courses (
    plan_id               TEXT NOT NULL REFERENCES membership_plans(id) ON DELETE CASCADE,
    space_id              TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(plan_id, space_id)
);

CREATE TABLE IF NOT EXISTS membership_plan_tags (
    plan_id               TEXT NOT NULL REFERENCES membership_plans(id) ON DELETE CASCADE,
    tag                   TEXT NOT NULL COLLATE NOCASE,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(plan_id, tag)
);

CREATE TABLE IF NOT EXISTS member_subscriptions (
    id                    TEXT PRIMARY KEY,
    community_id          TEXT NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    member_id             TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    plan_id               TEXT NOT NULL REFERENCES membership_plans(id),
    billing_customer_id   INTEGER,
    subscription_id       INTEGER UNIQUE,
    status                TEXT NOT NULL DEFAULT 'creating'
                          CHECK(status IN (
                            'creating','trialing','past_due','active','paused',
                            'cancelled','ended','failed'
                          )),
    current_period_start  TIMESTAMP,
    current_period_end    TIMESTAMP,
    next_renewal_at       TIMESTAMP,
    cancel_at             TIMESTAMP,
    checkout_url          TEXT,
    payment_success_url   TEXT NOT NULL DEFAULT '',
    payment_cancel_url    TEXT NOT NULL DEFAULT '',
    access_started_at     TIMESTAMP,
    past_due_at           TIMESTAMP,
    last_error            TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at              TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_member_subscriptions_member
    ON member_subscriptions(member_id, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ux_member_subscriptions_live
    ON member_subscriptions(community_id, member_id)
    WHERE status IN ('creating','trialing','past_due','active','paused');

CREATE TABLE IF NOT EXISTS membership_checkouts (
    id                    TEXT PRIMARY KEY,
    member_subscription_id TEXT NOT NULL REFERENCES member_subscriptions(id) ON DELETE CASCADE,
    cycle_id              INTEGER,
    billing_invoice_id    INTEGER UNIQUE,
    billing_session_id    TEXT,
    checkout_url          TEXT,
    status                TEXT NOT NULL DEFAULT 'creating'
                          CHECK(status IN ('creating','awaiting_payment','paid','cancelled','failed')),
    idempotency_key       TEXT NOT NULL UNIQUE,
    last_error            TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    paid_at               TIMESTAMP
);

CREATE TABLE IF NOT EXISTS membership_cycle_operations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    member_subscription_id TEXT NOT NULL REFERENCES member_subscriptions(id) ON DELETE CASCADE,
    subscription_id       INTEGER NOT NULL,
    cycle_id              INTEGER NOT NULL,
    period_start          TIMESTAMP NOT NULL,
    period_end            TIMESTAMP NOT NULL,
    billing_invoice_id    INTEGER UNIQUE,
    status                TEXT NOT NULL DEFAULT 'pending'
                          CHECK(status IN (
                            'pending','invoiced','collecting','action_required',
                            'payment_failed','paid','cancelled','failed'
                          )),
    checkout_url          TEXT,
    idempotency_key       TEXT NOT NULL UNIQUE,
    last_error            TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    paid_at               TIMESTAMP,
    UNIQUE(subscription_id, cycle_id)
);
CREATE INDEX IF NOT EXISTS idx_membership_cycle_operations_status
    ON membership_cycle_operations(status, updated_at);
