CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_channel_issue_source_workspace
    ON channel_issue_source (workspace_id);
