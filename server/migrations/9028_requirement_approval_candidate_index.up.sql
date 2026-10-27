CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_approval_candidate
    ON requirement_approval (candidate_id);
