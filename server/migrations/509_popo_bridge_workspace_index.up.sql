CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_workspace
    ON popo_bridge (workspace_id, created_at);
