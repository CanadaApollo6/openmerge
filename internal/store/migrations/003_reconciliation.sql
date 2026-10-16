CREATE TABLE observations (
    queue_id bigint NOT NULL REFERENCES queues(id),
    key text NOT NULL,
    value bytea NOT NULL,
    PRIMARY KEY (queue_id, key)
);
CREATE TABLE webhook_deliveries (
    queue_id bigint NOT NULL REFERENCES queues(id),
    delivery_id text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (queue_id, delivery_id)
);
ALTER TABLE queues ADD COLUMN reconcile_requested boolean NOT NULL DEFAULT false;
