\set ON_ERROR_STOP on

BEGIN;

DO $$
DECLARE
    user_id UUID;
    trip_id UUID;
    other_user_id UUID;
    actual_notes TEXT;
    actual_created_at TIMESTAMPTZ;
    batch_id UUID := gen_random_uuid();
    actual_state TEXT;
BEGIN
    -- Valid user creation with database-generated defaults.
    INSERT INTO users (auth_subject, display_name)
    VALUES ('test-uid-1', 'Test User')
    RETURNING id, created_at INTO user_id, actual_created_at;

    IF user_id IS NULL OR actual_created_at IS NULL THEN
        RAISE EXCEPTION 'User defaults were not generated';
    END IF;

    -- Duplicate Firebase subjects must be rejected.
    BEGIN
        INSERT INTO users (auth_subject)
        VALUES ('test-uid-1');

        RAISE EXCEPTION 'Duplicate auth_subject was accepted';
    EXCEPTION
        WHEN unique_violation THEN NULL;
    END;

    -- Missing Firebase subjects must be rejected.
    BEGIN
        INSERT INTO users DEFAULT VALUES;

        RAISE EXCEPTION 'Missing auth_subject was accepted';
    EXCEPTION
        WHEN not_null_violation THEN NULL;
    END;

    -- Empty Firebase subjects must be rejected.
    BEGIN
        INSERT INTO users (auth_subject)
        VALUES ('');

        RAISE EXCEPTION 'Empty auth_subject was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    -- Create a second user to test ownership.
    INSERT INTO users (auth_subject)
    VALUES ('test-uid-2')
    RETURNING id INTO other_user_id;

    -- Valid trip creation with default notes.
    INSERT INTO trips (owner_id, title)
    VALUES (user_id, 'Japan 2026')
    RETURNING id, notes, created_at
    INTO trip_id, actual_notes, actual_created_at;

    IF trip_id IS NULL OR actual_created_at IS NULL THEN
        RAISE EXCEPTION 'Trip defaults were not generated';
    END IF;

    IF actual_notes <> '' THEN
        RAISE EXCEPTION 'Unexpected default notes: %', actual_notes;
    END IF;

    -- Missing owners must be rejected.
    BEGIN
        INSERT INTO trips (title)
        VALUES ('Missing owner');

        RAISE EXCEPTION 'Missing owner_id was accepted';
    EXCEPTION
        WHEN not_null_violation THEN NULL;
    END;

    -- Nonexistent owners must be rejected.
    BEGIN
        INSERT INTO trips (owner_id, title)
        VALUES (
            '00000000-0000-0000-0000-000000000001',
            'Invalid owner'
        );

        RAISE EXCEPTION 'Nonexistent owner_id was accepted';
    EXCEPTION
        WHEN foreign_key_violation THEN NULL;
    END;

    -- Blank titles must be rejected.
    BEGIN
        INSERT INTO trips (owner_id, title)
        VALUES (user_id, '   ');

        RAISE EXCEPTION 'Blank title was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    -- Trip queries must be scoped by owner.
    IF (
        SELECT count(*)
        FROM trips
        WHERE owner_id = other_user_id
    ) <> 0 THEN
        RAISE EXCEPTION 'Trip unexpectedly belongs to another user';
    END IF;

    -- Prevent deleting a user who still owns trips.
    BEGIN
        DELETE FROM users WHERE id = user_id;

        RAISE EXCEPTION 'Owner deletion was accepted';
    EXCEPTION
        WHEN foreign_key_violation THEN NULL;
    END;

    -- Upload batches must reference an existing trip.
    BEGIN
        INSERT INTO upload_batches (id, trip_id)
        VALUES (gen_random_uuid(), gen_random_uuid());

        RAISE EXCEPTION 'Batch for a nonexistent trip was accepted';
    EXCEPTION
        WHEN foreign_key_violation THEN NULL;
    END;

    INSERT INTO upload_batches (id, trip_id)
    VALUES (batch_id, trip_id);

    -- A valid photo starts pending.
    INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
    VALUES (gen_random_uuid(), trip_id, batch_id, 'file-1', 'originals/test/1', 'image/jpeg', 25000000)
    RETURNING state INTO actual_state;

    IF actual_state <> 'pending' THEN
        RAISE EXCEPTION 'Unexpected default photo state: %', actual_state;
    END IF;

    -- client_file_id is unique within a batch.
    BEGIN
        INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
        VALUES (gen_random_uuid(), trip_id, batch_id, 'file-1', 'originals/test/2', 'image/jpeg', 1);

        RAISE EXCEPTION 'Duplicate client_file_id in a batch was accepted';
    EXCEPTION
        WHEN unique_violation THEN NULL;
    END;

    -- Two photos must never share a storage object.
    BEGIN
        INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
        VALUES (gen_random_uuid(), trip_id, batch_id, 'file-2', 'originals/test/1', 'image/jpeg', 1);

        RAISE EXCEPTION 'Duplicate object_name was accepted';
    EXCEPTION
        WHEN unique_violation THEN NULL;
    END;

    -- Out-of-range values are rejected.
    BEGIN
        INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
        VALUES (gen_random_uuid(), trip_id, batch_id, 'file-3', 'originals/test/3', 'image/heic', 1);

        RAISE EXCEPTION 'Unsupported content_type was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    BEGIN
        INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
        VALUES (gen_random_uuid(), trip_id, batch_id, 'file-4', 'originals/test/4', 'image/png', 25000001);

        RAISE EXCEPTION 'Oversized photo was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    BEGIN
        INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes, state)
        VALUES (gen_random_uuid(), trip_id, batch_id, 'file-5', 'originals/test/5', 'image/png', 1, 'done');

        RAISE EXCEPTION 'Unknown photo state was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    BEGIN
        INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
        VALUES (gen_random_uuid(), trip_id, batch_id, '', 'originals/test/6', 'image/png', 1);

        RAISE EXCEPTION 'Empty client_file_id was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    -- A failed photo must carry an error code, and only a failed one may.
    BEGIN
        UPDATE photos SET state = 'failed' WHERE client_file_id = 'file-1';

        RAISE EXCEPTION 'Failed photo without error_code was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    BEGIN
        UPDATE photos SET error_code = 'invalid_image' WHERE client_file_id = 'file-1';

        RAISE EXCEPTION 'Pending photo with error_code was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    -- A processed photo must have a display image.
    BEGIN
        UPDATE photos SET state = 'ready' WHERE client_file_id = 'file-1';

        RAISE EXCEPTION 'Ready photo without display image was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    -- Coordinates are set in pairs and within range.
    BEGIN
        UPDATE photos SET exif_lat = 60.0 WHERE client_file_id = 'file-1';

        RAISE EXCEPTION 'Unpaired EXIF coordinate was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    BEGIN
        UPDATE photos SET lat = 91, lng = 0 WHERE client_file_id = 'file-1';

        RAISE EXCEPTION 'Out-of-range latitude was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    BEGIN
        UPDATE photos SET capture_utc_offset = '3:00' WHERE client_file_id = 'file-1';

        RAISE EXCEPTION 'Malformed UTC offset was accepted';
    EXCEPTION
        WHEN check_violation THEN NULL;
    END;

    -- A complete processed row is accepted.
    UPDATE photos SET
        state = 'ready', display_object_name = 'display/test/1.jpg',
        capture_time_local = '2025-07-14T09:30:00', capture_utc_offset = '+03:00',
        exif_lat = 60.18, exif_lng = 24.82, lat = 60.18, lng = 24.82
    WHERE client_file_id = 'file-1';

    -- Prevent deleting a trip that still has photos.
    BEGIN
        DELETE FROM trips WHERE id = trip_id;

        RAISE EXCEPTION 'Trip deletion with photos was accepted';
    EXCEPTION
        WHEN foreign_key_violation THEN NULL;
    END;

    RAISE NOTICE 'All schema constraint tests passed';
END
$$;

ROLLBACK;