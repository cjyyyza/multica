CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_p4_changelist_identity
    ON p4_changelist (workspace_id, server_key, original_number);
