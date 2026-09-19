CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_channel_inbound_write_workspace
    ON channel_inbound_write (workspace_id);
