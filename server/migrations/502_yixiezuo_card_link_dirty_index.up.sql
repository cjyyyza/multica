CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_yixiezuo_card_link_dirty
    ON yixiezuo_card_link (connection_id, dirty)
    WHERE dirty;
