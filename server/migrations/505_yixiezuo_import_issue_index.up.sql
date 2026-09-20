CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_import_issue
    ON yixiezuo_import (workspace_id, issue_id);
