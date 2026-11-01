-- +goose Up

CREATE TABLE upload_batches (
    id UUID PRIMARY KEY,

    trip_id UUID NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT upload_batches_trip_id_fkey
        FOREIGN KEY (trip_id)
        REFERENCES trips(id)
        ON DELETE RESTRICT
);

CREATE INDEX upload_batches_trip_idx
    ON upload_batches (trip_id);

-- One row per accepted upload. Processing results (#12) add columns later.
CREATE TABLE photos (
    id UUID PRIMARY KEY,

    trip_id UUID NOT NULL,

    batch_id UUID NOT NULL,

    client_file_id TEXT NOT NULL,

    state TEXT NOT NULL DEFAULT 'pending',

    object_name TEXT NOT NULL,

    content_type TEXT NOT NULL,

    size_bytes BIGINT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT photos_trip_id_fkey
        FOREIGN KEY (trip_id)
        REFERENCES trips(id)
        ON DELETE RESTRICT,

    CONSTRAINT photos_batch_id_fkey
        FOREIGN KEY (batch_id)
        REFERENCES upload_batches(id)
        ON DELETE RESTRICT,

    CONSTRAINT photos_batch_client_file_unique
        UNIQUE (batch_id, client_file_id),

    CONSTRAINT photos_object_name_unique
        UNIQUE (object_name),

    CONSTRAINT photos_client_file_id_length
        CHECK (char_length(client_file_id) BETWEEN 1 AND 100),

    CONSTRAINT photos_state_valid
        CHECK (state IN ('pending', 'uploading', 'processing', 'ready', 'needs-location', 'failed')),

    CONSTRAINT photos_content_type_valid
        CHECK (content_type IN ('image/jpeg', 'image/png')),

    CONSTRAINT photos_size_bytes_range
        CHECK (size_bytes BETWEEN 1 AND 25000000)
);

CREATE INDEX photos_trip_created_idx
    ON photos (trip_id, created_at DESC, id DESC);

-- +goose Down

DROP TABLE photos;
DROP TABLE upload_batches;
