ALTER TABLE p4_changelist
    DROP COLUMN IF EXISTS poll_generation,
    DROP COLUMN IF EXISTS poll_lease_until;
