CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_candidate_revision
    ON requirement_candidate (requirement_id, revision);
