CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_command_requirement ON popo_bridge_command (workspace_id, requirement_id, created_at, id) WHERE requirement_id IS NOT NULL;
