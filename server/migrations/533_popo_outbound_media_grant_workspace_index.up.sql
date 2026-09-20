CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_outbound_media_grant_workspace
    ON popo_outbound_media_grant (workspace_id, created_at);
