CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_pm_notification_target
    ON yixiezuo_pm_notification (workspace_id, issue_id, agent_id);
