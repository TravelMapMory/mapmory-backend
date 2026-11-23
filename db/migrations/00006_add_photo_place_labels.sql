-- +goose Up

-- Place labels resolved from a photo's effective coordinates (backend #20, #43;
-- design doc 5.3 and 7.2). label_status moves independently of the photo
-- state: pending (no labels yet), suggested (from the resolver), confirmed
-- (accepted or set by the owner). The provider and its attribution are kept
-- with the labels because the provider's terms require showing them.
ALTER TABLE photos
    ADD COLUMN country TEXT,
    ADD COLUMN city TEXT,
    ADD COLUMN place_name TEXT,
    ADD COLUMN label_status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN label_provider TEXT,
    ADD COLUMN label_attribution TEXT;

ALTER TABLE photos
    ADD CONSTRAINT photos_label_status_valid
        CHECK (label_status IN ('pending', 'suggested', 'confirmed')),

    -- Labels only exist for a photo that has a position.
    ADD CONSTRAINT photos_labels_need_coordinates
        CHECK ((country IS NULL AND city IS NULL AND place_name IS NULL) OR lat IS NOT NULL);

-- +goose Down

ALTER TABLE photos
    DROP CONSTRAINT photos_labels_need_coordinates,
    DROP CONSTRAINT photos_label_status_valid;

ALTER TABLE photos
    DROP COLUMN label_attribution,
    DROP COLUMN label_provider,
    DROP COLUMN label_status,
    DROP COLUMN place_name,
    DROP COLUMN city,
    DROP COLUMN country;
