CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_outbound_media_grant_command_attachment
    ON popo_outbound_media_grant (command_id, attachment_id);
