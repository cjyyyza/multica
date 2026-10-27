-- Extended inbound keeps bridge-provided pre-download hash evidence. NULL is
-- intentionally not comparable: legacy rows are not backfilled with fake hashes.
-- execution_* is the POPO-owned engine fence, keyed by bridge/robot/event.
ALTER TABLE popo_inbound_event
    ADD COLUMN payload_hash TEXT,
    ADD COLUMN payload_evidence JSONB,
    ADD COLUMN dedupe_enabled BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN execution_claim_token UUID,
    ADD COLUMN execution_received_at TIMESTAMPTZ,
    ADD COLUMN execution_processed_at TIMESTAMPTZ,
    ADD CONSTRAINT popo_inbound_event_payload_hash_shape
        CHECK (payload_hash IS NULL OR payload_hash ~ '^sha256:[0-9a-f]{64}$');

-- Preserve historical evidence even if an old installation/event index allowed
-- duplicate bridge/robot/event identities. Only one row participates in dedupe.
WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY bridge_id, robot_id, event_id
               ORDER BY created_at, id
           ) AS position
    FROM popo_inbound_event
)
UPDATE popo_inbound_event AS event
SET dedupe_enabled = false
FROM ranked
WHERE event.id = ranked.id AND ranked.position > 1;
