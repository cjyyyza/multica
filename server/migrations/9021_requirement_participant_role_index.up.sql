CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_participant_role
    ON requirement_participant (requirement_id, user_id, role);
