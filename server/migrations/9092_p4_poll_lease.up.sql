ALTER TABLE p4_changelist
    ADD COLUMN poll_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN poll_lease_until TIMESTAMPTZ;
