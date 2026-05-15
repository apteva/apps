-- Issuer settings — the entity that emits invoices ("BILL FROM").
--
-- Singleton: one row per install. The CHECK constraint plus
-- "INSERT OR REPLACE ... VALUES (1, ...)" upsert pattern means there
-- can only ever be one row. Multi-issuer (FK invoices.issuer_id) is a
-- forward-compat move and would replace this with a regular table.

CREATE TABLE issuer_settings (
  id              INTEGER PRIMARY KEY CHECK (id = 1),

  -- Display + legal. display_name is the prominent header on the PDF;
  -- legal_name is the registered entity name compliance bodies want
  -- on the invoice (e.g. trading "Acme Co" vs registered "Acme Holdings Ltd").
  display_name    TEXT    NOT NULL DEFAULT '',
  legal_name      TEXT,

  email           TEXT,
  phone           TEXT,
  website         TEXT,
  brand_color     TEXT,                          -- "#0066ff" — optional accent

  -- Same JSON shape as customers.billing_address.
  address         TEXT    NOT NULL DEFAULT '{}', -- {line1,line2,postal_code,city,state,country}

  -- Same JSON shape as customers.tax_ids.
  tax_ids         TEXT    NOT NULL DEFAULT '[]', -- [{type,value}]

  -- Bank coordinates for non-Stripe payment methods (wire/SEPA/etc).
  -- Rendered on the PDF under "PAY BY BANK TRANSFER".
  bank            TEXT    NOT NULL DEFAULT '{}', -- {iban,bic,bank_name,bank_code,beneficiary}

  footer_text     TEXT,                          -- shown below totals
  default_terms   TEXT,                          -- "Payment due within 30 days"

  metadata        TEXT    NOT NULL DEFAULT '{}',

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
