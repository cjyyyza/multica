ALTER TABLE requirement_outbox
    ADD COLUMN recipient_user_id UUID,
    ADD COLUMN recipient_binding_id UUID,
    ADD COLUMN transport_command_id UUID,
    ADD COLUMN transport_attempt INTEGER NOT NULL DEFAULT 0 CHECK (transport_attempt >= 0),
    ADD COLUMN accepted_at TIMESTAMPTZ;

ALTER TABLE requirement_outbox DROP CONSTRAINT IF EXISTS requirement_outbox_status_check;
ALTER TABLE requirement_outbox
    ADD CONSTRAINT requirement_outbox_status_check
    CHECK (status IN ('pending', 'accepted', 'delivered', 'failed', 'conflict', 'unknown', 'superseded'));

UPDATE requirement_outbox
SET recipient_user_id = CASE
    WHEN recipient_binding->>'recipient_user_id' IS NOT NULL
        THEN (recipient_binding->>'recipient_user_id')::uuid
    WHEN recipient_binding->>'id' IS NOT NULL
        THEN (recipient_binding->>'id')::uuid
    ELSE NULL
END
WHERE recipient_user_id IS NULL;

ALTER TABLE popo_bridge_command
    ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0);
