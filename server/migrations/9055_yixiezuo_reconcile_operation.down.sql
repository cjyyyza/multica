DELETE FROM yixiezuo_operation WHERE kind = 'reconcile';
UPDATE yixiezuo_operation SET state = 'unknown' WHERE state = 'needs_reconcile';

ALTER TABLE yixiezuo_operation DROP CONSTRAINT IF EXISTS yixiezuo_operation_kind_check;
ALTER TABLE yixiezuo_operation
    ADD CONSTRAINT yixiezuo_operation_kind_check
    CHECK (kind IN ('preview', 'refresh', 'publish', 'review'));

ALTER TABLE yixiezuo_operation DROP CONSTRAINT IF EXISTS yixiezuo_operation_state_check;
ALTER TABLE yixiezuo_operation
    ADD CONSTRAINT yixiezuo_operation_state_check
    CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'conflict', 'unknown'));

ALTER TABLE yixiezuo_operation
    DROP COLUMN IF EXISTS requirement_id,
    DROP COLUMN IF EXISTS target_operation_id,
    DROP COLUMN IF EXISTS request_hash;
