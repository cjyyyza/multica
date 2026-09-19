CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_channel_inbound_write_message
    ON channel_inbound_write (installation_id, message_id, kind);
