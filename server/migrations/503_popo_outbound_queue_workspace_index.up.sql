CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_outbound_queue_workspace_pending
    ON popo_outbound_queue (workspace_id, created_at)
    WHERE delivered_at IS NULL;
