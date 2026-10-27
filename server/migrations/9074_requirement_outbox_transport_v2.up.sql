ALTER TABLE requirement_outbox
    ADD COLUMN transport_delivery_id UUID,
    ADD COLUMN transport_protocol INTEGER NOT NULL DEFAULT 1 CHECK (transport_protocol IN (1, 2));
