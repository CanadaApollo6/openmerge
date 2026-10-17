-- A chosen history remains canonical after its queue drains. Cache the complete
-- variant set so an imported legacy row is checked for conflicts while the
-- existing canonical choice is retained.
CREATE TABLE canonical_queues (
    installation text NOT NULL,
    repository text NOT NULL CHECK (repository = lower(repository)),
    base text NOT NULL,
    lane text NOT NULL,
    queue_id bigint NOT NULL REFERENCES queues(id),
    variant_ids bigint[] NOT NULL,
    PRIMARY KEY (installation, repository, base, lane)
);
