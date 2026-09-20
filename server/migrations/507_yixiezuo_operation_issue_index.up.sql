CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_operation_issue
    ON yixiezuo_operation (workspace_id, issue_id, created_at DESC, id DESC)
    WHERE issue_id IS NOT NULL;
