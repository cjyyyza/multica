CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_candidate_id
    ON requirement_candidate (id);
