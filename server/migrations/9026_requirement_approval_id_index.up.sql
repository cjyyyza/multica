CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_approval_id
    ON requirement_approval (id);
