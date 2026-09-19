CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_media_staging_workspace
    ON popo_media_staging (workspace_id, created_at);
