-- +goose Up

ALTER TABLE trips
    DROP CONSTRAINT trips_title_nonblank;

ALTER TABLE trips
    ADD CONSTRAINT trips_title_nonblank
    CHECK (length(btrim(title, E' \t\n\r\f\v')) > 0);

-- +goose Down

ALTER TABLE trips
    DROP CONSTRAINT trips_title_nonblank;

ALTER TABLE trips
    ADD CONSTRAINT trips_title_nonblank
    CHECK (length(btrim(title)) > 0);
