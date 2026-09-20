CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_command_workspace
    ON popo_bridge_command (workspace_id, created_at);
