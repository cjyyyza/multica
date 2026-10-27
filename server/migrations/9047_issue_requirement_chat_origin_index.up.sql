CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_requirement_chat_origin
    ON issue (origin_id) WHERE origin_type = 'requirement_chat';
