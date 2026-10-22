-- +goose Up

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    auth_subject TEXT NOT NULL UNIQUE,

    display_name TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT users_auth_subject_nonempty
        CHECK (length(auth_subject) > 0)
);

CREATE TABLE trips (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    owner_id UUID NOT NULL,

    title TEXT NOT NULL,

    notes TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT trips_owner_id_fkey
        FOREIGN KEY (owner_id)
        REFERENCES users(id)
        ON DELETE RESTRICT,

    CONSTRAINT trips_title_nonblank
        CHECK (length(btrim(title)) > 0)
);

CREATE INDEX trips_owner_created_idx
    ON trips (owner_id, created_at DESC, id DESC);

-- +goose Down

DROP TABLE trips;
DROP TABLE users;
