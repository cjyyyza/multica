ALTER TABLE popo_bridge_command DROP COLUMN IF EXISTS attempt;

UPDATE requirement_outbox SET status = 'pending' WHERE status = 'accepted';

ALTER TABLE requirement_outbox DROP CONSTRAINT IF EXISTS requirement_outbox_status_check;
ALTER TABLE requirement_outbox
    ADD CONSTRAINT requirement_outbox_status_check
    CHECK (status IN ('pending', 'delivered', 'failed', 'conflict', 'unknown', 'superseded'));

ALTER TABLE requirement_outbox
    DROP COLUMN IF EXISTS recipient_user_id,
    DROP COLUMN IF EXISTS recipient_binding_id,
    DROP COLUMN IF EXISTS transport_command_id,
    DROP COLUMN IF EXISTS transport_attempt,
    DROP COLUMN IF EXISTS accepted_at;
