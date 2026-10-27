CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_chat_session
    ON requirement (chat_session_id);
