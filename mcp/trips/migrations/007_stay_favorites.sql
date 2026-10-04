-- Shortlists are separate from itinerary stays: they carry no budget/calendar cost.
CREATE TABLE stay_favorites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    destination_id INTEGER NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'hotel' CHECK(kind IN ('hotel','airbnb','hostel','rental','friend','other')),
    listing_url TEXT NOT NULL DEFAULT '',
    photo_url TEXT NOT NULL DEFAULT '',
    address TEXT NOT NULL DEFAULT '',
    price_amount INTEGER CHECK(price_amount IS NULL OR price_amount >= 0),
    price_basis TEXT NOT NULL DEFAULT 'total' CHECK(price_basis IN ('total','per_night')),
    currency TEXT NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    top_choice INTEGER NOT NULL DEFAULT 0 CHECK(top_choice IN (0,1)),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_stay_favorites_destination ON stay_favorites(destination_id, top_choice);
CREATE INDEX idx_stay_favorites_trip ON stay_favorites(trip_id);
CREATE UNIQUE INDEX idx_stay_favorites_top ON stay_favorites(destination_id) WHERE top_choice=1;
ALTER TABLE accommodations ADD COLUMN stay_favorite_id INTEGER REFERENCES stay_favorites(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX idx_accommodations_favorite ON accommodations(stay_favorite_id) WHERE stay_favorite_id IS NOT NULL;
