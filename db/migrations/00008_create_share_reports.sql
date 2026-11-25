-- +goose Up

-- Reports on public shares (design doc 3.5, 5.3 SHARE_REPORT). A share with an
-- unresolved report is hidden from the public map and its public page until a
-- team member reviews it and sets resolved_at (or unpublishes the share).
CREATE TABLE share_reports (
    id UUID PRIMARY KEY,

    share_id UUID NOT NULL,

    reason TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    resolved_at TIMESTAMPTZ,

    CONSTRAINT share_reports_share_fkey
        FOREIGN KEY (share_id)
        REFERENCES shares(id)
        ON DELETE CASCADE,

    CONSTRAINT share_reports_reason_length
        CHECK (char_length(btrim(reason)) BETWEEN 1 AND 500)
);

CREATE INDEX share_reports_open_idx
    ON share_reports (share_id)
    WHERE resolved_at IS NULL;

-- +goose Down

DROP TABLE share_reports;
