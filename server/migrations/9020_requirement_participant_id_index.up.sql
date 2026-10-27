CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_participant_id
    ON requirement_participant (id);
