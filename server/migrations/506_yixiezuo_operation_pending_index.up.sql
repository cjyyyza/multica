CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_operation_pending
    ON yixiezuo_operation (workspace_id, requested_by, created_at)
    WHERE state IN ('pending', 'running');
