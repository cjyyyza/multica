CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_import_source
    ON yixiezuo_import (workspace_id, source_host, external_id);
