CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_identity_conflict_attempt
    ON popo_v2_identity_conflict (workspace_id, delivery_id, attempt);
