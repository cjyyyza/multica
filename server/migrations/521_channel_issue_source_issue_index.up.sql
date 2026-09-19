CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_channel_issue_source_issue
    ON channel_issue_source (issue_id);
