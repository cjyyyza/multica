CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_command_id
    ON popo_v2_command (command_id);
