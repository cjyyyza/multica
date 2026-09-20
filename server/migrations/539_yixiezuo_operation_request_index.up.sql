CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_operation_request
    ON yixiezuo_operation (workspace_id, requested_by, request_key) WHERE request_key IS NOT NULL;
