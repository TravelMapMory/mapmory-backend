-- +goose Up

-- Results of /complete processing (#12, #21). Unknown metadata stays NULL.
ALTER TABLE photos
    ADD COLUMN error_code TEXT,
    ADD COLUMN uploaded_at TIMESTAMPTZ,
    ADD COLUMN processing_started_at TIMESTAMPTZ,
    ADD COLUMN capture_time_local TIMESTAMP,
    ADD COLUMN capture_utc_offset TEXT,
    ADD COLUMN exif_lat DOUBLE PRECISION,
    ADD COLUMN exif_lng DOUBLE PRECISION,
    ADD COLUMN lat DOUBLE PRECISION,
    ADD COLUMN lng DOUBLE PRECISION,
    ADD COLUMN display_object_name TEXT;

ALTER TABLE photos
    ADD CONSTRAINT photos_error_code_valid
        CHECK (error_code IN ('unsupported_type', 'file_too_large', 'image_too_large', 'invalid_image')),

    -- A failed photo always says why; no other state carries an error.
    ADD CONSTRAINT photos_error_code_matches_state
        CHECK ((state = 'failed') = (error_code IS NOT NULL)),

    -- Only processed photos have a display image.
    ADD CONSTRAINT photos_display_matches_state
        CHECK ((state IN ('ready', 'needs-location')) = (display_object_name IS NOT NULL)),

    ADD CONSTRAINT photos_display_object_name_unique
        UNIQUE (display_object_name),

    ADD CONSTRAINT photos_capture_utc_offset_format
        CHECK (capture_utc_offset ~ '^[+-][0-9]{2}:[0-9]{2}$'),

    -- Both coordinates of a pair are set together or not at all.
    ADD CONSTRAINT photos_exif_coordinates_paired
        CHECK ((exif_lat IS NULL) = (exif_lng IS NULL)),
    ADD CONSTRAINT photos_coordinates_paired
        CHECK ((lat IS NULL) = (lng IS NULL)),

    ADD CONSTRAINT photos_exif_coordinates_range
        CHECK (exif_lat BETWEEN -90 AND 90 AND exif_lng BETWEEN -180 AND 180),
    ADD CONSTRAINT photos_coordinates_range
        CHECK (lat BETWEEN -90 AND 90 AND lng BETWEEN -180 AND 180);

-- The photo list: newest upload first, not-yet-uploaded photos last.
DROP INDEX photos_trip_created_idx;

CREATE INDEX photos_trip_uploaded_idx
    ON photos (trip_id, (uploaded_at IS NULL), uploaded_at DESC, id DESC);

-- +goose Down

DROP INDEX photos_trip_uploaded_idx;

CREATE INDEX photos_trip_created_idx
    ON photos (trip_id, created_at DESC, id DESC);

ALTER TABLE photos
    DROP CONSTRAINT photos_coordinates_range,
    DROP CONSTRAINT photos_exif_coordinates_range,
    DROP CONSTRAINT photos_coordinates_paired,
    DROP CONSTRAINT photos_exif_coordinates_paired,
    DROP CONSTRAINT photos_capture_utc_offset_format,
    DROP CONSTRAINT photos_display_object_name_unique,
    DROP CONSTRAINT photos_display_matches_state,
    DROP CONSTRAINT photos_error_code_matches_state,
    DROP CONSTRAINT photos_error_code_valid;

ALTER TABLE photos
    DROP COLUMN display_object_name,
    DROP COLUMN lng,
    DROP COLUMN lat,
    DROP COLUMN exif_lng,
    DROP COLUMN exif_lat,
    DROP COLUMN capture_utc_offset,
    DROP COLUMN capture_time_local,
    DROP COLUMN processing_started_at,
    DROP COLUMN uploaded_at,
    DROP COLUMN error_code;
