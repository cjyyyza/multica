CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_wakeup_current
    ON requirement_wakeup (current_wakeup_id);
