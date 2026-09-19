CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_channel_inbound_write_comment
    ON channel_inbound_write (comment_id)
    WHERE comment_id IS NOT NULL;
