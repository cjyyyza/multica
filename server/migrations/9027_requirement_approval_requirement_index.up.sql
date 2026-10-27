CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_approval_requirement
    ON requirement_approval (requirement_id);
