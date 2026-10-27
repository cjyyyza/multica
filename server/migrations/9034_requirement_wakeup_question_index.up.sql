CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_wakeup_question
    ON requirement_wakeup (requirement_id, question_comment_id);
