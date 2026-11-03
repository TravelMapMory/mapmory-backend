-- +goose Up
-- The photo list follows the agreed capture order (#45): camera-local capture
-- time oldest first, then upload time, then id. infinity puts undated and
-- never-uploaded photos last, matching ListByTrip's keyset query.
DROP INDEX photos_trip_uploaded_idx;
CREATE INDEX photos_trip_capture_idx
    ON photos (
        trip_id,
        COALESCE(capture_time_local, 'infinity'::timestamp),
        COALESCE(uploaded_at, 'infinity'::timestamptz),
        id
    );
-- +goose Down
DROP INDEX photos_trip_capture_idx;
CREATE INDEX photos_trip_uploaded_idx
    ON photos (trip_id, (uploaded_at IS NULL), uploaded_at DESC, id DESC);
