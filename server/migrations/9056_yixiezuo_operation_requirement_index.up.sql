CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_operation_requirement
    ON yixiezuo_operation (workspace_id, requirement_id, created_at, id) WHERE requirement_id IS NOT NULL;
