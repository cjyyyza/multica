CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_command_source
    ON popo_v2_command (workspace_id, source_key);
