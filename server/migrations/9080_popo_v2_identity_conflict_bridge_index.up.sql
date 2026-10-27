CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_identity_conflict_bridge
    ON popo_v2_identity_conflict (workspace_id, bridge_id);
