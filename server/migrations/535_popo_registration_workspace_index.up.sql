CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_registration_workspace
    ON popo_registration (workspace_id, created_at);
