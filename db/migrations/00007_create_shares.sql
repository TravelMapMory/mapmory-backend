-- +goose Up

-- Link and public shares (backend #19, #24, #25; design doc 4.2, 5.3). A share
-- is a fixed selection of photos from one trip; editing the trip never adds
-- photos to it. Revoking sets revoked_at and the link stops working at once.
CREATE TABLE shares (
    id UUID PRIMARY KEY,

    trip_id UUID NOT NULL,

    mode TEXT NOT NULL,

    title TEXT NOT NULL,

    description TEXT,

    -- 128 random bits, base64url. The owner can copy the link again later, so
    -- the token is kept; it is the only key to the share.
    token TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    revoked_at TIMESTAMPTZ,

    CONSTRAINT shares_trip_id_fkey
        FOREIGN KEY (trip_id)
        REFERENCES trips(id)
        ON DELETE CASCADE,

    CONSTRAINT shares_token_unique
        UNIQUE (token),

    -- Lets share_photos prove that each photo belongs to the share's trip.
    CONSTRAINT shares_id_trip_unique
        UNIQUE (id, trip_id),

    CONSTRAINT shares_mode_valid
        CHECK (mode IN ('link', 'public')),

    CONSTRAINT shares_title_length
        CHECK (char_length(btrim(title)) BETWEEN 1 AND 120),

    CONSTRAINT shares_description_length
        CHECK (description IS NULL OR char_length(description) BETWEEN 1 AND 600),

    CONSTRAINT shares_token_length
        CHECK (char_length(token) >= 22)
);

CREATE INDEX shares_trip_created_idx
    ON shares (trip_id, created_at DESC, id DESC);

-- Public map lookups only touch live public shares.
CREATE INDEX shares_public_live_idx
    ON shares (id)
    WHERE mode = 'public' AND revoked_at IS NULL;

ALTER TABLE photos
    ADD CONSTRAINT photos_id_trip_unique
        UNIQUE (id, trip_id);

-- The selected photos, each with the coarse position recipients see: the
-- centre of its city, or its position rounded to 0.01 degrees (doc 5.4).
-- Exact coordinates never leave the photos table through a share.
CREATE TABLE share_photos (
    share_id UUID NOT NULL,

    photo_id UUID NOT NULL,

    trip_id UUID NOT NULL,

    display_lat DOUBLE PRECISION,

    display_lng DOUBLE PRECISION,

    PRIMARY KEY (share_id, photo_id),

    CONSTRAINT share_photos_share_fkey
        FOREIGN KEY (share_id, trip_id)
        REFERENCES shares(id, trip_id)
        ON DELETE CASCADE,

    -- A shared photo must belong to the share's trip (design doc 3.3).
    CONSTRAINT share_photos_photo_fkey
        FOREIGN KEY (photo_id, trip_id)
        REFERENCES photos(id, trip_id)
        ON DELETE CASCADE,

    CONSTRAINT share_photos_display_paired
        CHECK ((display_lat IS NULL) = (display_lng IS NULL)),

    CONSTRAINT share_photos_display_range
        CHECK (display_lat BETWEEN -90 AND 90 AND display_lng BETWEEN -180 AND 180)
);

CREATE INDEX share_photos_photo_idx
    ON share_photos (photo_id);

CREATE INDEX share_photos_display_idx
    ON share_photos (display_lat, display_lng)
    WHERE display_lat IS NOT NULL;

-- +goose Down

DROP TABLE share_photos;

ALTER TABLE photos
    DROP CONSTRAINT photos_id_trip_unique;

DROP TABLE shares;
