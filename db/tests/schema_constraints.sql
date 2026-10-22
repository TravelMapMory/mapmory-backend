\set ON_ERROR_STOP on

BEGIN;

DO $$
DECLARE
    user_id UUID;
    trip_id UUID;
    other_user_id UUID;
    actual_notes TEXT;
    actual_created_at TIMESTAMPTZ;
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

    RAISE NOTICE 'All schema constraint tests passed';
END
$$;

ROLLBACK;