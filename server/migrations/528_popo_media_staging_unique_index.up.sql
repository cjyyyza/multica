CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_media_staging_bridge_event_index
    ON popo_media_staging (bridge_id, event_id, media_index);
