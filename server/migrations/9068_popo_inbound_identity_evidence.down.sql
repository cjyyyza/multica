ALTER TABLE popo_inbound_event
    DROP CONSTRAINT IF EXISTS popo_inbound_event_payload_hash_shape,
    DROP COLUMN IF EXISTS execution_processed_at,
    DROP COLUMN IF EXISTS execution_received_at,
    DROP COLUMN IF EXISTS execution_claim_token,
    DROP COLUMN IF EXISTS dedupe_enabled,
    DROP COLUMN IF EXISTS payload_evidence,
    DROP COLUMN IF EXISTS payload_hash;
