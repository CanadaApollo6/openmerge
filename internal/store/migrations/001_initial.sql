CREATE TABLE queues (
    id bigserial PRIMARY KEY,
    installation text NOT NULL CHECK (installation <> ''),
    repository text NOT NULL CHECK (repository <> ''),
    base text NOT NULL CHECK (base <> ''),
    lane text NOT NULL DEFAULT '',
    initial_fence text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (installation, repository, base, lane)
);

CREATE TABLE events (
    id bigserial PRIMARY KEY,
    queue_id bigint NOT NULL REFERENCES queues(id),
    type text NOT NULL,
    payload jsonb NOT NULL CHECK (payload->>'type' = type),
    dedupe_key text,
    recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_queue_watermark ON events(queue_id, id);
-- A delivery's last event carries its key, so a duplicate returns the batch's
-- final watermark. All of a delivery's events commit in the same transaction.
CREATE UNIQUE INDEX events_queue_dedupe ON events(queue_id, dedupe_key)
    WHERE dedupe_key IS NOT NULL;

CREATE TABLE actions (
    id bigserial PRIMARY KEY,
    queue_id bigint NOT NULL REFERENCES queues(id),
    key text NOT NULL,
    type text NOT NULL,
    payload jsonb NOT NULL CHECK (payload->>'type' = type),
    watermark bigint NOT NULL CHECK (watermark >= 0),
    status text NOT NULL DEFAULT 'planned'
        CHECK (status IN ('planned', 'claimed', 'in_flight', 'done', 'cancelled', 'poisoned')),
    error text,
    attempt_id text,
    lease_duration_us bigint CHECK (lease_duration_us > 0),
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (queue_id, key),
    CHECK (status <> 'poisoned' OR error IS NOT NULL),
    CHECK (status NOT IN ('claimed', 'in_flight') OR lease_duration_us IS NOT NULL),
    CHECK ((status IN ('claimed', 'in_flight')) = (attempt_id IS NOT NULL)),
    CHECK ((status IN ('claimed', 'in_flight')) = (lease_until IS NOT NULL))
);
CREATE INDEX actions_claimable ON actions(id)
    WHERE status IN ('planned', 'claimed', 'in_flight');
