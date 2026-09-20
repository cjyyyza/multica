CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_channel_outbound_message_issue
    ON channel_outbound_message (issue_id)
    WHERE issue_id IS NOT NULL;
