CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_outbound_media_grant_bridge_attachment
    ON popo_outbound_media_grant (bridge_id, attachment_id);
