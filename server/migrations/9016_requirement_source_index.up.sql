CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_source_identity
    ON requirement (workspace_id, source_host, external_id);
