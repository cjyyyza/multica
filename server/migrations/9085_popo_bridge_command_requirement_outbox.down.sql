-- This down is safe only before the single-ledger runtime creates or updates
-- Requirement commands. requirement_outbox is not maintained after 9085.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM popo_bridge_command
        WHERE requirement_id IS NOT NULL
          AND updated_at > COALESCE((
              SELECT applied_at FROM schema_migrations
              WHERE version = '9085_popo_bridge_command_requirement_outbox'
          ), 'infinity'::timestamptz)
    ) THEN
        RAISE EXCEPTION '9085 rollback is unavailable after single-ledger writes; use a forward migration';
    END IF;
END
$$;

DELETE FROM popo_bridge_command WHERE bridge_id IS NULL;

ALTER TABLE popo_bridge_command
    ALTER COLUMN bridge_id SET NOT NULL;

UPDATE popo_bridge_command SET attempt = 1 WHERE attempt < 1;
ALTER TABLE popo_bridge_command DROP CONSTRAINT IF EXISTS popo_bridge_command_attempt_check;
ALTER TABLE popo_bridge_command
    ADD CONSTRAINT popo_bridge_command_attempt_check CHECK (attempt > 0);

ALTER TABLE popo_bridge_command
    DROP COLUMN IF EXISTS requirement_id,
    DROP COLUMN IF EXISTS requirement_kind,
    DROP COLUMN IF EXISTS occurrence_id,
    DROP COLUMN IF EXISTS operation_id,
    DROP COLUMN IF EXISTS recipient_user_id,
    DROP COLUMN IF EXISTS recipient_binding_id,
    DROP COLUMN IF EXISTS requirement_revision,
    DROP COLUMN IF EXISTS issue_revision,
    DROP COLUMN IF EXISTS snapshot_payload,
    DROP COLUMN IF EXISTS request_hash,
    DROP COLUMN IF EXISTS payload_hash,
    DROP COLUMN IF EXISTS lease_id,
    DROP COLUMN IF EXISTS dispatched_at,
    DROP COLUMN IF EXISTS not_sent,
    DROP COLUMN IF EXISTS retryable,
    DROP COLUMN IF EXISTS partial_delivery,
    DROP COLUMN IF EXISTS evidence_finalized,
    DROP COLUMN IF EXISTS needs_reconcile,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS accepted_at,
    DROP COLUMN IF EXISTS delivered_at,
    DROP COLUMN IF EXISTS superseded_at,
    DROP COLUMN IF EXISTS unknown_at;
