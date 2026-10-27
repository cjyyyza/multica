ALTER TABLE yixiezuo_operation
    ADD COLUMN requirement_id UUID,
    ADD COLUMN target_operation_id UUID,
    ADD COLUMN request_hash TEXT;

ALTER TABLE yixiezuo_operation DROP CONSTRAINT IF EXISTS yixiezuo_operation_kind_check;
ALTER TABLE yixiezuo_operation
    ADD CONSTRAINT yixiezuo_operation_kind_check
    CHECK (kind IN ('preview', 'refresh', 'publish', 'review', 'reconcile'));

ALTER TABLE yixiezuo_operation DROP CONSTRAINT IF EXISTS yixiezuo_operation_state_check;
ALTER TABLE yixiezuo_operation
    ADD CONSTRAINT yixiezuo_operation_state_check
    CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'conflict', 'unknown', 'needs_reconcile'));
