-- Additive transport v2 persistence. Ordinary v1 commands remain in
-- popo_bridge_command. No primary/unique indexes are created in this file;
-- each identity index has its own concurrent migration. No FKs or cascades.
CREATE TABLE popo_v2_command (
    workspace_id       UUID NOT NULL,
    command_id         UUID NOT NULL,
    source_key         TEXT NOT NULL,
    kind               TEXT NOT NULL,
    command_type       TEXT NOT NULL DEFAULT 'send',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ NOT NULL,
    expires_at_wire    TEXT NOT NULL
);

CREATE TABLE popo_v2_delivery (
    workspace_id          UUID NOT NULL,
    delivery_id           UUID NOT NULL,
    command_id            UUID NOT NULL,
    recipient_binding_id  UUID NOT NULL,
    idempotency_key       UUID NOT NULL,
    payload_snapshot      JSONB NOT NULL,
    canonical_json        TEXT NOT NULL,
    payload_hash          TEXT NOT NULL,
    trace_id              UUID NOT NULL,
    status                TEXT NOT NULL DEFAULT 'pending',
    not_sent              BOOLEAN NOT NULL DEFAULT true,
    retryable             BOOLEAN NOT NULL DEFAULT false,
    partial_delivery      BOOLEAN NOT NULL DEFAULT false,
    remote_message_id     TEXT,
    evidence_finalized    BOOLEAN NOT NULL DEFAULT false,
    latest_attempt        INTEGER NOT NULL DEFAULT 0 CHECK (latest_attempt >= 0),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at            TIMESTAMPTZ NOT NULL,
    expires_at_wire       TEXT NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (idempotency_key = delivery_id),
    CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE popo_v2_lease (
    workspace_id       UUID NOT NULL,
    lease_id           UUID NOT NULL,
    delivery_id        UUID NOT NULL,
    attempt            INTEGER NOT NULL CHECK (attempt > 0),
    trace_id           UUID NOT NULL,
    state              TEXT NOT NULL DEFAULT 'pending',
    not_sent           BOOLEAN NOT NULL DEFAULT true,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE popo_v2_receipt (
    workspace_id          UUID NOT NULL,
    delivery_id           UUID NOT NULL,
    attempt               INTEGER NOT NULL CHECK (attempt > 0),
    lease_id              UUID NOT NULL,
    payload_hash          TEXT NOT NULL,
    trace_id              UUID NOT NULL,
    evidence_finalized    BOOLEAN NOT NULL,
    status                TEXT NOT NULL,
    remote_message_id     TEXT,
    error                 TEXT,
    error_code            TEXT,
    retryable             BOOLEAN NOT NULL,
    not_sent              BOOLEAN NOT NULL,
    partial_delivery      BOOLEAN NOT NULL,
    completion_kind       TEXT,
    completion_evidence   JSONB,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE popo_v2_receipt_component (
    workspace_id          UUID NOT NULL,
    delivery_id           UUID NOT NULL,
    attempt               INTEGER NOT NULL CHECK (attempt > 0),
    component_id          TEXT NOT NULL,
    lease_id              UUID NOT NULL,
    payload_hash          TEXT NOT NULL,
    trace_id              UUID NOT NULL,
    kind                  TEXT NOT NULL,
    required              BOOLEAN NOT NULL,
    evidence_finalized    BOOLEAN NOT NULL,
    status                TEXT NOT NULL,
    remote_message_id     TEXT,
    correlation_id        TEXT,
    error_code            TEXT,
    retryable             BOOLEAN NOT NULL,
    not_sent              BOOLEAN NOT NULL,
    completion_evidence   JSONB,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (component_id <> ''),
    CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$')
);
