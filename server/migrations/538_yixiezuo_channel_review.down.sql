DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM yixiezuo_operation WHERE kind = 'review') THEN
        RAISE EXCEPTION 'Cannot remove channel review support while review records exist';
    END IF;
END $$;
ALTER TABLE yixiezuo_operation DROP CONSTRAINT IF EXISTS yixiezuo_operation_kind_check;
ALTER TABLE yixiezuo_operation ADD CONSTRAINT yixiezuo_operation_kind_check
    CHECK (kind IN ('preview', 'refresh', 'publish'));
ALTER TABLE yixiezuo_operation DROP COLUMN IF EXISTS request_key;
