-- Community v0.6: one-time paid course sales orchestrated by Community.
--
-- Catalog remains the source of truth for products/prices and Billing owns
-- customers, invoices, payment sessions, refunds, and payment webhooks.
-- Community stores only the offer binding, the purchase workflow, and the
-- resulting enrollment provenance.

CREATE TABLE IF NOT EXISTS course_offers (
    space_id              TEXT    PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
    catalog_product_id    INTEGER NOT NULL,
    catalog_price_id      INTEGER NOT NULL UNIQUE,
    product_name          TEXT    NOT NULL,
    price_nickname        TEXT    NOT NULL DEFAULT '',
    unit_amount_cents     INTEGER NOT NULL CHECK(unit_amount_cents >= 0),
    currency              TEXT    NOT NULL,
    active                INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at           TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_course_offers_active
    ON course_offers(active, updated_at DESC);

CREATE TABLE IF NOT EXISTS course_purchases (
    id                    TEXT    PRIMARY KEY,
    community_id          TEXT    NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    space_id              TEXT    NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    member_id             TEXT    NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    catalog_product_id    INTEGER NOT NULL,
    catalog_price_id      INTEGER NOT NULL,
    product_name          TEXT    NOT NULL,
    unit_amount_cents     INTEGER NOT NULL CHECK(unit_amount_cents >= 0),
    currency              TEXT    NOT NULL,
    customer_email        TEXT    NOT NULL,
    customer_name         TEXT    NOT NULL DEFAULT '',
    auth_subject_id       TEXT    NOT NULL DEFAULT '',
    billing_customer_id   INTEGER,
    billing_invoice_id    INTEGER UNIQUE,
    billing_session_id    TEXT,
    checkout_url          TEXT,
    status                TEXT    NOT NULL DEFAULT 'creating'
                          CHECK(status IN (
                            'creating','awaiting_payment','payment_failed',
                            'paid','fulfilled','cancelled','failed',
                            'refund_pending','partially_refunded','refunded'
                          )),
    refunded_cents        INTEGER NOT NULL DEFAULT 0 CHECK(refunded_cents >= 0),
    last_error            TEXT    NOT NULL DEFAULT '',
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    paid_at               TIMESTAMP,
    fulfilled_at          TIMESTAMP,
    cancelled_at          TIMESTAMP,
    refunded_at           TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_course_purchases_course
    ON course_purchases(space_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_course_purchases_member
    ON course_purchases(member_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ux_course_purchases_active
    ON course_purchases(space_id, member_id)
    WHERE status IN (
      'creating','awaiting_payment','payment_failed','paid','fulfilled',
      'refund_pending','partially_refunded'
    );

CREATE TABLE IF NOT EXISTS course_purchase_events (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    purchase_id    TEXT    NOT NULL REFERENCES course_purchases(id) ON DELETE CASCADE,
    event_key      TEXT    NOT NULL,
    event_name     TEXT    NOT NULL,
    source_app     TEXT    NOT NULL DEFAULT '',
    payload_json   TEXT    NOT NULL DEFAULT '{}',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(purchase_id, event_key)
);
CREATE INDEX IF NOT EXISTS idx_course_purchase_events_purchase
    ON course_purchase_events(purchase_id, created_at DESC);

ALTER TABLE course_enrollments ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE course_enrollments ADD COLUMN source_ref TEXT;
ALTER TABLE course_enrollments ADD COLUMN access_expires_at TIMESTAMP;
ALTER TABLE course_enrollments ADD COLUMN access_revoked_at TIMESTAMP;
