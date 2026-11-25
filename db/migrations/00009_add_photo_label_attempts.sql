-- +goose Up

-- Place-label retries (design doc 7.3): when the provider was unavailable at
-- upload, labels stay pending and are retried up to 3 times the next time the
-- owner opens the trip. The counter stops retries for a position the provider
-- cannot label, so a trip listing never keeps spending quota on it.
ALTER TABLE photos
    ADD COLUMN label_attempts SMALLINT NOT NULL DEFAULT 0;

ALTER TABLE photos
    ADD CONSTRAINT photos_label_attempts_range
        CHECK (label_attempts BETWEEN 0 AND 10);

-- +goose Down

ALTER TABLE photos
    DROP CONSTRAINT photos_label_attempts_range;

ALTER TABLE photos
    DROP COLUMN label_attempts;
