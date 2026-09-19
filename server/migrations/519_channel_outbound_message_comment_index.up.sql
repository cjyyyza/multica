CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_channel_outbound_message_comment
    ON channel_outbound_message (comment_id)
    WHERE comment_id IS NOT NULL;
