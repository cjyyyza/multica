ALTER TABLE yixiezuo_operation DROP CONSTRAINT IF EXISTS yixiezuo_operation_kind_check;
ALTER TABLE yixiezuo_operation
    ADD CONSTRAINT yixiezuo_operation_kind_check
    CHECK (kind IN ('preview', 'refresh', 'publish', 'review', 'reconcile', 'draft'));
