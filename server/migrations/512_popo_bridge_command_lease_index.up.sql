CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_command_lease
    ON popo_bridge_command (bridge_id, created_at)
    WHERE status IN ('pending', 'leased');
