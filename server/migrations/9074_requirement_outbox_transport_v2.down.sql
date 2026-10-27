ALTER TABLE requirement_outbox
    DROP COLUMN IF EXISTS transport_protocol,
    DROP COLUMN IF EXISTS transport_delivery_id;
