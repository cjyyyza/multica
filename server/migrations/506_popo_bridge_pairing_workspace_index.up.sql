CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_pairing_workspace
    ON popo_bridge_pairing (workspace_id, created_at);
