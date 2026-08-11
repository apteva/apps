CREATE TABLE IF NOT EXISTS storefront_product_profiles (
    community_id         TEXT      NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    catalog_product_id   INTEGER   NOT NULL,
    testimonial_ids_json TEXT      NOT NULL DEFAULT '[]',
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (community_id, catalog_product_id)
);

CREATE INDEX IF NOT EXISTS idx_storefront_product_profiles_product
ON storefront_product_profiles(catalog_product_id, community_id);
